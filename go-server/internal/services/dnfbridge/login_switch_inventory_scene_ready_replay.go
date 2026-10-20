package dnfbridge

// 本文件是**缺陷②（重登后「Buff 强化」换装栏显示错误/空）**的修法：给 **list 1**
// （换装栏物品容器）补上和 list 3 同一条"场景提交之后再交付一次"的下行。
//
// ── 一、为什么是 list 1、为什么是"提交之后"
//
// 第 3/4 轮已证：换装栏的**绑定关系**由 op1428 的 `u8 count + count×(u32,u32)` 帧
// 写进客户端 manager 329 的 `std::map<u32,u32>`（`map[KEY]=VALUE`，解析器
// DNF.exe RVA `0x565660`），而该 op1428 handler 注册在 **class-0 注册表**
// （注册点 `0x565756 push 0 / push 0x965660 / push 0x594` → 包装 `0x1E5C940` →
// 注册表访问器 `0x1E62A80` = `kClass0RegistryRva`；class-0 派发入口 `0x1E5CA00`
// 用的是同一个访问器）⇒ 我们发的 `msg_id=1428 classification=0` 帧**确实被派发并写表**。
//
// 本轮**新的现场证据**（只读 SQL，char 6）给出了绑定值的语义：pairs 的 **value 就是
// list 1 的槽位号** —— `dnf_inventory_items` 里 `1:<slot>` 恰好 11 行、槽位
// **54..64**，与客户端登记的 11 个 value（54,62,61,57,59,63,58,64,60,56,55）一一对应：
//
//	1:54 101520658  1:55 101520658  1:56 101520658  1:57 101520658  1:58 101520671
//	1:59 101530518  1:60 101530603  1:61 101530603  1:62 101530607  1:63 101530612  1:64 101530612
//
// ⇒ 换装栏面板 = op1428 的 map **×** list 1 容器。而登录路径上：
//   · op1428 有 `_skill_switch_inventory_after_scene_ready` 这条**提交之后**的重投影；
//   · list 3 有 `game-login-equipment-scene-ready-replay-sent` 这条**提交之后**的补发；
//   · **list 1 只有"提交之前"的那一次七容器引导**
//     （`game-upper-select-inventory-bootstrap-send packet_index=1 list_type=1`，
//     source 里没有 `_scene_ready_`）。
//
// 而 actor 世代那条机制（`login_mode1_last_before_commit.go` 的现场读数）说明客户端是在
// **场景最终提交那一刻**重建它手里那一代容器的 ⇒ 提交前落地的 list 1 落在被销毁的那一代上，
// 于是 map 与容器**在客户端那一刻对不上**：面板空/错。修法就是让 list 1 和 list 3 一样，
// 在**同一个边界**（客户端本次进场的第一条 legacy type1345）用**同一个载体**
// （`buildCurrentItemListBodyForSession` + `sendCurrentSceneItemList(op13)`）再交付一次。
//
// ── 二、开关（消费点自读 env，不进 options.go；默认即修复，回退必须显式）
//
//	DNFBRIDGE_LOGIN_SWITCH_INVENTORY_SCENE_READY_REPLAY
//	  默认（含未设置/写错的值）= after_scene_ready：补发一次 list 1；
//	  before_transition_only（别名 off/0/false/no/legacy/early_only/disable/disabled/none）
//	    = 一键回退到今天：本文件**一个字节都不发**（list 3 那条修复不受影响），
//	      事件 `game-login-switch-inventory-scene-ready-replay-suppressed`。
//
// 收口范围与 list 3 完全一致：只有"选角/登录进城"这条路由、只有本次进场的第一条
// type1345；fail-closed（仓储/记录缺失就只记日志，绝不发一个编出来的空容器）。

import (
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"strings"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
)

const (
	// currentSwitchInventoryListType 是「Buff 强化」换装栏物品容器的 wire list type。
	// 现场依据：char 6 的 `1:<slot>` 行在 54..64 与客户端 op1428 的 11 个 value 一一对应。
	currentSwitchInventoryListType byte = 1

	// loginSwitchInventorySceneReadyReplayEnv 是"换装栏容器要不要在场景就绪提交点补发"的开关名。
	loginSwitchInventorySceneReadyReplayEnv = "DNFBRIDGE_LOGIN_SWITCH_INVENTORY_SCENE_READY_REPLAY"

	loginSwitchInventorySceneReadyReplayAfterSceneReady      = "after_scene_ready"
	loginSwitchInventorySceneReadyReplayBeforeTransitionOnly = "before_transition_only"

	// loginSwitchInventorySceneReadyReplayPosition 是这条下行在登录序列里的位置字符串。
	loginSwitchInventorySceneReadyReplayPosition = "after_typed_op24_final_transition_commit_after_client_type1345_scene_ready_ack_same_boundary_and_carrier_as_list_3"
	// loginSwitchInventorySceneReadyReplayEvent/Sent/Suppressed 是本条的事件名。
	loginSwitchInventorySceneReadyReplaySentEvent       = "game-login-switch-inventory-scene-ready-replay-sent"
	loginSwitchInventorySceneReadyReplaySuppressedEvent = "game-login-switch-inventory-scene-ready-replay-suppressed"
	loginSwitchInventorySceneReadyReplayDeferredEvent   = "game-login-switch-inventory-scene-ready-replay-deferred"
	// loginPostCommitContainerListsEvent 把"提交之后到底交付了哪几个 list"汇总成一条。
	loginPostCommitContainerListsEvent = "game-login-post-commit-container-lists-sent"
)

// loginSwitchInventorySceneReadyReplayEnabled 回答"要不要在场景就绪提交点补发换装栏容器"。
// 只有**显式**写回退字面量才关闭；其余取值（含未设置、写错的串）都是修复档。
func loginSwitchInventorySceneReadyReplayEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(loginSwitchInventorySceneReadyReplayEnv))) {
	case loginSwitchInventorySceneReadyReplayBeforeTransitionOnly, "early_only",
		"off", "0", "false", "no", "legacy", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

// loginSwitchInventorySceneReadyReplaySwitch 只用于事件/诊断。
func loginSwitchInventorySceneReadyReplaySwitch() string {
	if loginSwitchInventorySceneReadyReplayEnabled() {
		return loginSwitchInventorySceneReadyReplayAfterSceneReady
	}
	return loginSwitchInventorySceneReadyReplayBeforeTransitionOnly
}

// loginPostCommitContainerLists 返回"提交之后会交付的 list 清单"（供两个事件原样回显）。
func loginPostCommitContainerLists() string {
	if loginSwitchInventorySceneReadyReplayEnabled() {
		return "3,1"
	}
	return "3"
}

// loginSwitchInventoryContainerWireRowAudit 从**已经发出的 list-1 容器包体**里读回逐行
// 槽位 / 物品，供事件原样回显（与 list 3 的 `loginEquipmentContainerWireRowAudit` 同口径，
// 但信封不同）。
//
// list 1 的信封 = `u8 listType | u16 avatarExpansion | u16 count | count × entrySize`
// （`current_item_list_protocol.go:13-14`；list 1 每行 127 = 0x77 + 两段 4 字节长度前缀，
// 见 `currentItemListEntrySizeForType`）。行内布局仍是 `槽 u16@0 / itemID u32@2`。
func loginSwitchInventoryContainerWireRowAudit(body []byte, entrySize int) (slots []string, items []string, rows int, ok bool) {
	if body == nil || len(body) < 5 || entrySize <= 0x06 {
		return nil, nil, 0, false
	}
	count := int(binary.LittleEndian.Uint16(body[3:5]))
	if count < 0 || 5+count*entrySize > len(body) {
		return nil, nil, 0, false
	}
	for index := 0; index < count; index++ {
		row := body[5+index*entrySize : 5+(index+1)*entrySize]
		slot := int16(binary.LittleEndian.Uint16(row[0:2]))
		itemID := binary.LittleEndian.Uint32(row[2:6])
		slots = append(slots, strconv.Itoa(int(slot)))
		items = append(items, strconv.FormatUint(uint64(itemID), 10))
	}
	return slots, items, count, true
}

// replayLoginSwitchInventorySceneReadyContainer 在客户端声明"场景就绪"之后，按场景路径
// 同一个载体补发一次 **list 1**（换装栏物品容器）——与 list 3 那条修复同边界、同发送器、
// 同包格式，只是换了一个 list type。
//
// 调用点：`game_select_scene_deferred_tail.go`，紧跟 list 3 那条补发。
func (s *Service) replayLoginSwitchInventorySceneReadyContainer(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	// 与 list 3 同一套归属判据：只有"选角/登录进城"这条路由。
	session.townMu.Lock()
	owned := session.initialTownRouteCharacterID != 0 &&
		session.initialTownRouteCharacterID == session.selectedCharacterID &&
		session.initialTownRouteStage >= currentInitialTownRouteTransitionSent
	session.townMu.Unlock()
	if !owned {
		return nil
	}
	if !loginSwitchInventorySceneReadyReplayEnabled() {
		s.logPacketEvent(loginSwitchInventorySceneReadyReplaySuppressedEvent,
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"list", currentSwitchInventoryListType,
			"rows", 0,
			"boundary", loginEquipmentSceneReadyReplayBoundary,
			"ordering_position", loginSwitchInventorySceneReadyReplayPosition,
			"post_commit_lists", loginPostCommitContainerLists(),
			"switch", loginSwitchInventorySceneReadyReplaySwitch(),
			"switch_env", loginSwitchInventorySceneReadyReplayEnv,
			"reason", "rollback_literal_keeps_the_pre_transition_only_switch_inventory_delivery")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	body, bodySource, entryCount, ok := s.buildCurrentItemListBodyForSession(ctx, session, currentSwitchInventoryListType)
	if !ok || len(body) == 0 {
		// 仓储不可用/记录缺失：只记日志，**绝不**发一个编出来的空容器
		// （与 list 3 的 fail-closed 口径一致）。
		s.logPacketEvent(loginSwitchInventorySceneReadyReplayDeferredEvent,
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"list", currentSwitchInventoryListType,
			"boundary", loginEquipmentSceneReadyReplayBoundary,
			"ordering_position", loginSwitchInventorySceneReadyReplayPosition,
			"post_commit_lists", loginPostCommitContainerLists(),
			"switch", loginSwitchInventorySceneReadyReplaySwitch(),
			"switch_env", loginSwitchInventorySceneReadyReplayEnv,
			"reason", "switch_inventory_snapshot_unavailable_no_invented_container")
		return nil
	}
	entrySize := currentItemListEntrySizeForType(currentSwitchInventoryListType)
	slots, items, rows, auditOK := loginSwitchInventoryContainerWireRowAudit(body, entrySize)
	s.logPacketEvent(loginSwitchInventorySceneReadyReplaySentEvent,
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"list", currentSwitchInventoryListType,
		"rows", rows,
		"entry_count", entryCount,
		"msg_id", uint16(dnfenum.CmdPacketLeaveParty),
		"classification", 0,
		"list_type", currentSwitchInventoryListType,
		"body_len", len(body),
		"body_source", bodySource,
		"entry_size", entrySize,
		"slots_sent", strings.Join(slots, ","),
		"items_sent", strings.Join(items, ","),
		"row_audit_ok", boolDigit(auditOK),
		"boundary", loginEquipmentSceneReadyReplayBoundary,
		"ordering_position", loginSwitchInventorySceneReadyReplayPosition,
		"post_commit_lists", loginPostCommitContainerLists(),
		"pair_semantics_evidence", "op1428_pair_values_are_list_1_slots_char_6_has_exactly_11_rows_at_54_64_matching_the_11_registered_values",
		"switch", loginSwitchInventorySceneReadyReplaySwitch(),
		"switch_env", loginSwitchInventorySceneReadyReplayEnv)
	s.logPacketEvent(loginPostCommitContainerListsEvent,
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"source", source,
		"lists", loginPostCommitContainerLists(),
		"list_3_rows", -1,
		"list_1_rows", rows,
		"boundary", loginEquipmentSceneReadyReplayBoundary,
		"ordering_position", loginSwitchInventorySceneReadyReplayPosition,
		"switch", loginSwitchInventorySceneReadyReplaySwitch(),
		"switch_env", loginSwitchInventorySceneReadyReplayEnv)
	return s.sendCurrentSceneItemList(session, uint16(dnfenum.CmdPacketLeaveParty), body)
}

// noteLoginPostCommitEquipmentRows 由 list 3 那条修复在发送成功后调用，把"提交之后
// 交付了哪几个 list、list 3 有多少行"补进同一族日志（`list_1_rows` 由 list 1 那条填）。
func (s *Service) noteLoginPostCommitEquipmentRows(session *gameSession, source string, rows int) {
	if s == nil || session == nil {
		return
	}
	s.logPacketEvent(loginPostCommitContainerListsEvent,
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"source", source,
		"lists", loginPostCommitContainerLists(),
		"list_3_rows", rows,
		"list_1_rows", -1,
		"boundary", loginEquipmentSceneReadyReplayBoundary,
		"ordering_position", loginSwitchInventorySceneReadyReplayPosition,
		"switch", loginSwitchInventorySceneReadyReplaySwitch(),
		"switch_env", loginSwitchInventorySceneReadyReplayEnv)
}
