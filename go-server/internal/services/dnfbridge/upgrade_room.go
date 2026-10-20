package dnfbridge

import (
	"encoding/hex"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 强化直播（UPGRADE ROOM 家族）在 dnfbridge 侧的唯一归属点。
//
// 现场（2026-09-25 19:40:06.761 / 19:40:08.879，香港 packet_log.txt:2202-2205，
// conn_id=game-000016，角色 3）：
//
//	RECV kind=game-legacy cmd=1 type=576 runtime_cmd_name=ENUM_CMDPACKET_CREATE_UPGRADE_ROOM
//	     body_len=4 len=17 hex=01400211000000d234ded26700790bb000
//	RECV ... type=576 ... header_seq=105 body_len=4 hex=01400211000000a4b9c1a36900790bb000
//
// 帧格式（与同一日志的 op19 逐字节同构，故可判定）：`01 | 40 02 | 11 00 00 00 |
// <u32 checksum> | <u16 seq> | <payload>` ⇒ **seq=103/105（u16，与 header_seq 相等），
// payload 恒为 `79 0b`（= 0x0B79 = 2937）**；`body_len=4` 是 seq+payload。
// 两次点击 payload 相同 ⇒ 它是"请求创建强化直播房间"的固定上下文 id，不是随机数。
//
// 「强化直播」这个入口本身在**当前客户端自己的字符串表**里：
// `patch-artifacts/antiaera/readback/*/text/etc_etc.chn.str.txt` 的 `popup_menu_103`
// 与 `popup_menu_105` = 「强化直播」，邻居是 `popup_menu_100 进入决斗场`、
// `popup_menu_101 决斗排行榜` ⇒ 它是角色弹窗菜单里的一条，属于"观战/竞技"那一族。
//
// 参考实现（全部逐条查过）**都没有这一族**：
//   - 86JP C# `a21zigai/86JP/Server/DfoServer/Network/Protocol/GameProtocolHandler.cs:391-394`：
//     `_cmdDispatch.TryGetValue` 未命中就只写 `[GameProtocol] Unhandled CMD type=0x0240`
//     并返回 —— **一个字节都不回**；`PacketTypes.cs:1625` 只有 `CREATE_UPGRADE_ROOM = 0x0241`
//     这一条枚举（A21 的编号比当前 EXE 大 1）。
//   - A21 各分支（`ServerS4A21-master`、`(1)`、`9.3基线副本`、`s4a21_master_unzip`、
//     `s4a21_9272b7f_unzip`、`S4A21-完整包-20260923`、`一键配置开服`、`Temp/*`）同样
//     只有 `PacketTypes*.cs` 的枚举声明，全树 `d[0x0240]/d[0x0241]` 注册为 0 处。
//   - 厂商 9.16 二进制（`D:\DNF90-test\server\runtime\logs\packet_log.txt`，同一
//     `LongHeng Go dnfbridge` 血统）全日志 `type=576|577|580|588|589`、`UpgradeRoom`、
//     `upgrade-room` 命中 **0** ⇒ 参考行为同样是"收下即丢"。
//
// 所以本文件的判据是：**当前客户端的正确包形没有证据**。按 AGENTS.md「Current EXE
// behavior is authoritative for uncertain packet layouts」与「Never invent ... to hide
// a missing dependency」，默认档必须与参考逐字节相同（不回包），但**不许再静默**：
//   - `reference_drop`（默认）：只回一条具名、可 grep 的事件（含 body_hex），
//     client 方向与 86JP/A21/厂商 9.16 完全一致；与"unhandled latest game command"
//     匿名桶的区别是这条事件自带 opcode 名、域分类、证据与 body。
//   - `echo_failure`：回一条 class1 失败 ACK（同 opcode、body `04`）。
//     **形状未经当前客户端验证**（全仓没有 576 的已证回包体，也没有当前 EXE 的
//     强化直播 S2C noti 编号——当前 EXE 的 512..527 已经是 SeriaroomAnimationDecoEvent/
//     Bingo 那一族，A21 的 UPGRADE_ROOM_INFO=512 编号在这份 EXE 上不成立），
//     因此它只是**受控实验档**，默认关闭，改 env 重启即可 A/B 或回退。
//
// 真正的功能缺口（不在本次改动范围内，也无人可抄）：整族需要"房间"子系统 ——
// 建房/房间列表/观众加入与离开/房主上架装备/开始强化/结果广播（86JP 的 S2C 侧
// 名字是 UPGRADE_ROOM_INFO / UPGRADE_ROOM_LIST_INFO / CREATE_UPGRADE_ROOM_INFO /
// UPGRADE_ROOM_PUT_UP_ITEM / UPGRADE_ROOM_UPGRADE_START ...）。参考实现连服务端
// 侧都没有，当前 EXE 的对应 S2C 编号亦无证据，故本批只做"注册 + 分类 + 取证 + 可控拒绝"。
const upgradeRoomReplyModeEnv = "DNFBRIDGE_UPGRADE_ROOM_REPLY"

const (
	// upgradeRoomReplyReferenceDrop 与 86JP/A21/厂商 9.16 逐字节相同：只记日志，不回包。
	upgradeRoomReplyReferenceDrop = "reference_drop"
	// upgradeRoomReplyEchoFailure 回一条 class1 失败 ACK（受控实验档，形状未证实）。
	upgradeRoomReplyEchoFailure = "echo_failure"
)

// currentUpgradeRoomFailureCode 与 86JP `InventoryHandler.Move.cs:137` 的
// `MapMoveErrorCode` 兜底码一致（0x04 = 该操作当前不可执行）。它只用于
// `echo_failure` 档；不代表 576 的官方失败码。
const currentUpgradeRoomFailureCode byte = 0x04

// upgradeRoomFamilyCommands 是 ENUM_CMDPACKET_*_UPGRADE_ROOM 全族（dnfenum/cmdpacket.go:584-599）。
// 客户端"强化直播"入口实测只发 576；其余成员一起登记，是为了让整族不再落进匿名
// "unhandled latest game command" 桶（同族任何一条到达都能被具名判读）。
var upgradeRoomFamilyCommands = []dnfenum.CmdPacket{
	dnfenum.CmdPacketCreateUpgradeRoom,
	dnfenum.CmdPacketDestroyUpgradeRoom,
	dnfenum.CmdPacketJoinSpectatorUpgradeRoom,
	dnfenum.CmdPacketLeaveSpectatorUpgradeRoom,
	dnfenum.CmdPacketUpgradeRoomGiveItemInfo,
	dnfenum.CmdPacketUpgradeRoomPutUpItem,
	dnfenum.CmdPacketUpgradeRoomUpgradeStart,
	dnfenum.CmdPacketUpgradeRoomUpgradeCancel,
	dnfenum.CmdPacketUpgradeRoomMasterMessage,
}

func isUpgradeRoomCommand(typ uint16) bool {
	for _, opcode := range upgradeRoomFamilyCommands {
		if uint16(opcode) == typ {
			return true
		}
	}
	return false
}

// upgradeRoomReplyMode 返回 `DNFBRIDGE_UPGRADE_ROOM_REPLY` 的归一化取值。
func (s *Service) upgradeRoomReplyMode() string {
	if s == nil {
		return upgradeRoomReplyReferenceDrop
	}
	return s.options.upgradeRoomReplyMode
}

// handleUpgradeRoomCommand 是 576 家族的唯一入口；返回 handled=true 表示
// 这条命令已经由本函数具名处理（调用方不要再落到 unhandled 桶）。
func (s *Service) handleUpgradeRoomCommand(session *gameSession, typ uint16, body []byte) (bool, error) {
	if s == nil || session == nil {
		return false, nil
	}
	if !isUpgradeRoomCommand(typ) {
		return false, nil
	}
	reply := s.upgradeRoomReplyMode()
	s.logPacketEvent("game-upgrade-room-command-observed",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"type", typ,
		"runtime_cmd_name", runtimeCmdPacketName(byte(dnfenum.GameCmdCommand), typ),
		"body_len", len(body),
		"body_hex", hex.EncodeToString(body),
		"reply", reply,
		"feature", "reinforce_broadcast_upgrade_room",
		"behavior", "registered_no_reference_implementation_and_no_current_exe_body_evidence")
	if reply != upgradeRoomReplyEchoFailure {
		// 参考档：与 86JP GameProtocolHandler:393 / 厂商 9.16 一样不发明回包。
		return true, nil
	}
	ack := []byte{currentUpgradeRoomFailureCode}
	if err := s.sendGameUpperRawClass(
		session,
		typ,
		ack,
		dnfproto.DefaultChannelClassification,
	); err != nil {
		return true, err
	}
	s.logPacketEvent("game-upgrade-room-command-refusal-sent",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"type", typ,
		"classification", dnfproto.DefaultChannelClassification,
		"body_hex", hex.EncodeToString(ack),
		"shape_evidence", "unproven_on_current_client_experimental_switch",
		"reason", "no_reference_implementation_and_no_s2c_body_evidence")
	return true, nil
}
