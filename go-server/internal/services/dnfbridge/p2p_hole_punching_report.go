package dnfbridge

import (
	"encoding/hex"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// 本文件是 op966（生成表标签 `CmdPacketP2pHolePunchingSuccessRate`，
// `dnfenum/cmdpacket.go:977`）在 dnfbridge 侧的**唯一归属点**：只读观测，
// **一个字节都不回**。
//
// 现场（2026-09-26 08:55 的 changlog 条目）：两成员队伍成队期间，队长在副本门口
// 交互的两个窗口（07:05:xx / 07:15:xx）里，客户端自己的全包体 trace
// （`90CN_protocol.log`）记到 6 条 C2S `msg=966 body_len=23`，而同一窗口
// **一条 `msg=15`（普通选图请求）都没有** —— 服务端因此 0 条
// `game-enter-select` / `game-dungeon-select-request-decoded`。
// 改动前全仓 `P2pHolePunching|HolePunch|hole_punch` 在 dnfbridge 下**零命中**
// ⇒ 这条上报没有路由、没有 handler，一路落进匿名 "unhandled latest game command"
// 桶被静默丢弃。
//
// 已观测到的事实（**只有这些**，不要再往外读）：
//   - 6 条样本（07:05:28.832 / :41.000 / :53.045 / 07:15:09.080 / :21.186 / :33.283）
//     的 body **逐字节完全相同**：
//     `FF 00 00 00 00 E0 2E 00 00 01 00 00 00 05 00 00 00 28 00 00 00 04 00`
//   - 同一窗口内间隔 ≈ 12 s ⇒ 它是**周期性上报**，不是"按一次键发一次"的请求；
//     数值恒定也说明它**不像是**一个逐次变化的"测量值"。
//   - 整段 trace（06:40:46–07:16:12）里它**只**出现在两个组队窗口，单人副本那次
//     （06:58）一次都没有 ⇒ 与"队伍"状态相关，最可能是队伍期的 P2P
//     保活/监控路径。
//
// 因此 **body 布局未证实**：本处理器不做任何解码（不猜 u32、不猜"成功率"、不猜
// 人数），body 一律按 hex 原样记录，等第二次不同结果的样本再对齐字段。这与
// AGENTS.md「Current EXE behavior is authoritative for uncertain packet layouts」
// 与「Never invent ... to hide a missing dependency」一致。日志事件名也刻意用中性的
// `game-p2p-hole-punching-report-observed`，**不断言**它是一次测量结果。
//
// 回包：**零字节**。全仓没有 op966 的已证 S2C 形状，参考实现（86JP / A21 / 厂商 9.16）
// 也没有这一族的服务端侧——错回一个包比不回更糟，所以这里**连受控实验档都不开**
// （对比 `upgrade_room.go` 的 `DNFBRIDGE_UPGRADE_ROOM_REPLY`：那一族有"创建房间"
// 语义、需要一个可回退的可选拒绝档；本处理器没有任何可切换的线缆行为，
// 因此**不需要 env 开关**，"回退"就是 revert 本文件与 game_aligned.go 里的那一处调用）。
// 与 `game-party-peer-connect-result-observed`（op97，`game_dispatch_upper.go:420`）
// 和 `game-quest-clear-coupon-debug-command-observed`（op176，`quest_coupon_clear.go:473`）
// 同款：无条件、只记一行、无开关。
//
// 到达路径：966 不在 `protocol.isKnownClientUpperMsg`（`protocol/stream.go:255`）的
// 表里，所以 class1/op966 帧会被 `splitLegacyGamePacket` 切成 **legacy** 包
// （`cmd=1 type=966`，与现场 `cmd=1 type=576` 同形），经 `handleGameCommand` 的
// default 分支落到 `handleAlignedGameCommand`。本函数的调用点就在那里、与
// `handleOnlinePartyCommand` 同一层（队伍族）。该处对未登记 opcode 不做 body
// 归一化（`normalizeLegacyGameBody` 只对已登记模块改字节），所以记录的就是原始 body。
//
// 音量：现场节奏 ≈ 12 s/人（两成员队伍 ≈ 10 行/分钟）。包日志**本来就**为每个收到的
// 帧写一行 `RECV kind=game-legacy ... type=966`（无速率限制），本事件只是在同一节奏上
// **多加一行**，不改变任何 flood 特性 ⇒ 不需要引入 `avatar_appearance_coalesce.go`
// 那种"出站合并"（约束的是高频出站包）或 `party_udp_relay.go` 那种"每腿一次"
// （约束的是逐数据报路径）。为了保住样本，这里**每条都记**：宁可 12 s 一行，
// 也不要把样本合并掉。

// currentP2pHolePunchingReportOpcode 是客户端周期性上报的 opcode。
// 名字沿用生成表，但本文件**不据此断言** body 里就是"成功率"。
const currentP2pHolePunchingReportOpcode = dnfenum.CmdPacketP2pHolePunchingSuccessRate

// observeCurrentP2pHolePunchingReport 是 op966 的唯一入口；返回 handled=true 表示
// 这条命令已具名处理（调用方不要再落进 unhandled 桶）。它不做任何写操作、不排队、
// 不发包，永远返回 nil error；body 长度不合法也照样记录（不 panic、不静默）。
func (s *Service) observeCurrentP2pHolePunchingReport(session *gameSession, typ uint16, body []byte) (bool, error) {
	if s == nil || session == nil {
		return false, nil
	}
	if dnfenum.CmdPacket(typ) != currentP2pHolePunchingReportOpcode {
		return false, nil
	}
	// "这条上报是否发生在队伍里"正是这次实测最关心的相关性（966 只在组队窗口出现）。
	// 会话自带的 runtime party 状态在分发点就是现成的（`session.party`，纯内存、
	// 无 DB、无锁竞争面），所以直接读，不额外往下穿线。
	// 注意 `runtimePartyMembers` 按 `MaxMembers` 截断且跳过 UserID==0，
	// party_member_count 就是真实成员数（含自己）。
	party := runtimePartyStateSnapshot(session)
	s.logGameEvent(session, "game-p2p-hole-punching-report-observed",
		"char_id", session.selectedCharacterID,
		"type", typ,
		"runtime_cmd_name", dnfenum.CmdPacketName(typ),
		"body_len", len(body),
		"body_hex", hex.EncodeToString(body),
		"in_party", party.PartyID > 0,
		"party_id", party.PartyID,
		"party_member_count", len(runtimePartyMembers(party)),
		"layout", "unproven_hex_only_no_field_decoding",
		"reply", "none",
		"behavior", "read_only_observed_zero_bytes_sent")
	return true, nil
}
