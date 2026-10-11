package dnfbridge

// 本文件修的是**登录路径**上"装备栏容器（list 3）交付时机"这一处缺口 ——
// 与 login_equipment_container.go 是同一个症状的**第二步**（那一步只在
// typed op24 **之前**补了容器，实测不够）。
//
// ── 一、症状与一句话结论
//
// 操作者原话：「光剑登陆后是被卸下的。进出副本能看到光剑属性变成正常的，
// 没有进入之前是 0 耐久的、和我本来属性不一样。」
// ⇒ 登录后**客户端的本地槽位状态是错的**（武器按"没穿上"渲染：0 耐久、属性不是
// 权威那一份），**换一次场景就被重建正确**。这是**一个**症状、**一个**原因。
//
// 结论：**同一批字节，交付时机不同**。装备（list 3）容器在登录路径上只出现在
// "typed op24 之前"这一个位置；而两条场景路径（进副本 / 回城）都把装备载体放在
// **客户端提交场景之后**。客户端在场景提交时重建它那份 list 3，于是"提交前"
// 那一次容器下发落在被销毁的那一代对象上 —— 表现为"没穿上 + 0 耐久"。
//
// ── 二、逐包对照（现场 packet_log.txt，conn game-000011，角色 6，只读取证）
//
//	登录（14:25:08）：
//	  .436 SEND class0/op2 498   mode0 actor create（含武器外观行）
//	  .436 SEND class0/op2 163   mode1 binding-only
//	  .564-.579 SEND class0/op13 ×7  {0,1,2,7,12,38,**3**}   <= 装备容器在这里
//	  .602 SEND class0/op2 2161  mode1 完整装备 create 行（31 行，槽 12 耐久 48）
//	  .718 SEND class0/op24 12   **typed op24 场景最终提交**
//	  .746 RECV legacy type1345 4  客户端声明"场景就绪"
//	  .746-.773 场景尾包（op985/op99/op1239/op1340/op1346/op1371/op413）
//	  ⇒ 提交**之后**没有任何装备载体下发。
//
//	进副本（14:27:22，`party_dungeon_shared_loading_barrier`，客户端先 op37 完成读条、
//	房间已由 op29 提交）：
//	  22.181 SEND class0/op13 ×7  同七个容器（list 3 body_len 3692、31 行）
//	  22.191 SEND class0/op2 2161 mode1 完整装备 create 行
//	  ⇒ 装备载体在**客户端提交场景之后**。
//
//	回城（14:31:00.724 op24 → .846 mode0 498 → .872 mode1 2161 → op105/op102/op37/op30/op19/op120，
//	`game-town-post-transition-player-state-sent`）：
//	  ⇒ 登录路径**从不**跑这一串（全日志 `game-town-post-transition-player-state-sent`
//	    只出现在 `..._pending_town_return_after_confirmed_typed_op24` /
//	    `..._without_retained_runtime` 两种 source，即场景路径）。
//
// 容器**内容**两条路径逐字节同形（body_len 3692 = 3 + 119 × 31，槽 12 的 0x77 行
// `0c00 6dc10506 fec99a3b 0f 3000` ⇒ +0x0B 耐久 48），差别只在**交付位置**。
//
// ── 三、为什么选"容器"而不是重放 mode0/mode1（这条边界是被现场钉死的）
//
// 在 type1345 边界重放 mode0/mode1/op105/op102/op37/op30/op19/op120 会**打崩客户端**
// （changlog.md:9943-9944：`C0000005 @ 0x0262EBCB` 那条 3013 场景对象空 owner
// `owner+0x4C8`；`game_select_scene_deferred_tail.go:98-103` 与
// `game_dispatch_legacy.go:218-222` 两处注释同样写着）。因此本条**只**补
// 物品栏载体（class0/op13 list 3 容器），它是场景路径在"提交之后"用的**同一个**
// 载体、同一台发送机（`buildCurrentItemListBodyForSession(3)` +
// `sendCurrentSceneItemList(op13)`），不新增任何包格式，也不碰 actor 世代。
//
// ── 四、开关（消费点自读 env，不进 options.go；默认即修复，回退必须显式）
//
//	DNFBRIDGE_LOGIN_EQUIPMENT_SCENE_READY_REPLAY=after_scene_ready（默认，含未设置/写错的值）
//	    客户端本次进场的**第一条** legacy type1345（= 场景就绪提交点）之后，
//	    补发一次 list 3 容器；事件
//	    `game-login-equipment-scene-ready-replay-sent`（含 char_id / rows /
//	    slots_sent / durability_carried / ordering_position / switch）。
//	before_transition_only（别名 off/0/false/no/legacy/early_only/disable/disabled/none）
//	    一键回退到今天：本文件一个字节都不发，事件
//	    `game-login-equipment-scene-ready-replay-suppressed` 说明档位取值。
//
// 收口范围（本文件只在下面这一种情形执行，其余路径一行代码都不走）：
//   · 只有"选角/登录进城"这条路由（`initialTownRouteCharacterID == selectedCharacterID`
//     且 `routeStage >= currentInitialTownRouteTransitionSent`）；
//   · 只有本次进场的**第一条** type1345（复用既有 `initialTownLegacySceneReadyAccepted`
//     false→true 边沿，与 skill_switch_inventory 的第二条边界同一处去重口径）；
//   · 失败只记日志，不把已经成功的场景尾包变成错误。

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
)

const (
	// loginEquipmentSceneReadyReplayEnv 是"装备栏容器要不要在场景就绪提交点补发"的开关名。
	loginEquipmentSceneReadyReplayEnv = "DNFBRIDGE_LOGIN_EQUIPMENT_SCENE_READY_REPLAY"

	loginEquipmentSceneReadyReplayAfterSceneReady      = "after_scene_ready"
	loginEquipmentSceneReadyReplayBeforeTransitionOnly = "before_transition_only"

	// loginEquipmentSceneReadyReplayPosition 是这条下行在登录序列里的**位置**字符串：
	// 事件里原样回显，下一次登录一眼可判"补齐的那一次装备容器落在哪一段之后"。
	loginEquipmentSceneReadyReplayPosition = "after_typed_op24_final_transition_commit_after_client_type1345_scene_ready_ack"
	// loginEquipmentSceneReadyReplayBoundary 是触发边界的名字。
	loginEquipmentSceneReadyReplayBoundary = "client_legacy_type1345_scene_ready_ack_first_of_this_scene_entry"
)

// loginEquipmentSceneReadyReplayEnabled 回答"要不要在场景就绪提交点补发装备容器"。
// 只有**显式**写回退字面量才关闭；其余取值（含未设置、after_scene_ready、写错的串）
// 都是修复档 —— 默认即修复，回退必须显式（与 loginEquipmentContainerAuthoritativeEnabled
// 同一取舍）。
func loginEquipmentSceneReadyReplayEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(loginEquipmentSceneReadyReplayEnv))) {
	case loginEquipmentSceneReadyReplayBeforeTransitionOnly, "early_only",
		"off", "0", "false", "no", "legacy", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

// loginEquipmentSceneReadyReplaySwitch 只用于事件/诊断。
func loginEquipmentSceneReadyReplaySwitch() string {
	if loginEquipmentSceneReadyReplayEnabled() {
		return loginEquipmentSceneReadyReplayAfterSceneReady
	}
	return loginEquipmentSceneReadyReplayBeforeTransitionOnly
}

// loginEquipmentContainerWireRowAudit 从**已经发出的包体**里读回逐行事实
// （槽位 / 物品 / 耐久），供事件原样回显 —— 报的是"这一包真正带出去的字节"，
// 不是任何中间标志位。
//
// 行布局与 `current_item_list_equipment_projection.go` 同一份 0x77 行：
// 槽 u16@0 / itemID u32@2 / 耐久 u16@0x0B（厂商
// `ItemListProtocolWriter.cs:157-161` 的 `slot, itemId, value, attr, durability`）。
func loginEquipmentContainerWireRowAudit(body []byte, entrySize int) (slots []string, durability []string, nonzero []string, rows int, ok bool) {
	if body == nil || len(body) < 3 || entrySize <= 0x0D {
		return nil, nil, nil, 0, false
	}
	count := int(binary.LittleEndian.Uint16(body[1:3]))
	if count < 0 || 3+count*entrySize > len(body) {
		return nil, nil, nil, 0, false
	}
	for index := 0; index < count; index++ {
		row := body[3+index*entrySize : 3+(index+1)*entrySize]
		slot := int16(binary.LittleEndian.Uint16(row[0:2]))
		word := binary.LittleEndian.Uint16(row[0x0B:0x0D])
		slots = append(slots, strconv.Itoa(int(slot)))
		durability = append(durability, fmt.Sprintf("%d:%d", slot, word))
		if word != 0 {
			nonzero = append(nonzero, fmt.Sprintf("%d:%d", slot, word))
		}
	}
	return slots, durability, nonzero, count, true
}

// replayLoginEquipmentSceneReadyContainer 是这条修复的正文：在客户端本次进场声明
// "场景就绪"之后，按场景路径同一个载体补发一次 list 3（已穿戴）容器。
//
// 调用点：`game_select_scene_deferred_tail.go` 的
// `sendCurrentLegacyTownSceneReadyBoundary`（与"Buff 强化换装栏"的第二条边界同一处，
// 同一 false→true 边沿去重）。
func (s *Service) replayLoginEquipmentSceneReadyContainer(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	// 只在"选角/登录进城"这条路由上补：路由归属与提交阶段都要成立。
	session.townMu.Lock()
	owned := session.initialTownRouteCharacterID != 0 &&
		session.initialTownRouteCharacterID == session.selectedCharacterID &&
		session.initialTownRouteStage >= currentInitialTownRouteTransitionSent
	session.townMu.Unlock()
	if !owned {
		return nil
	}
	if !loginEquipmentSceneReadyReplayEnabled() {
		// 回退档也要留下"开关取的是哪个值"的证据：这条日志是操作者在 packet_log.txt 里
		// 区分"修复档没生效"和"我写了回退字面量"的唯一判据。
		s.logPacketEvent("game-login-equipment-scene-ready-replay-suppressed",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"list", currentSocketListEquipment,
			"rows", 0,
			"boundary", loginEquipmentSceneReadyReplayBoundary,
			"ordering_position", loginEquipmentSceneReadyReplayPosition,
			"switch", loginEquipmentSceneReadyReplaySwitch(),
			"switch_env", loginEquipmentSceneReadyReplayEnv,
			"reason", "rollback_literal_keeps_the_pre_transition_only_equipment_container_delivery")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	body, bodySource, entryCount, ok := s.buildCurrentEquippedItemListBodyForSession(ctx, session)
	if !ok {
		// 仓储不可用/记录缺失：只记日志，绝不用一个编出来的容器体顶替
		// （与 `buildCurrentSelectInventoryBootstrapBody` 的 fail-closed 口径一致）。
		s.logPacketEvent("game-login-equipment-scene-ready-replay-deferred",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"list", currentSocketListEquipment,
			"boundary", loginEquipmentSceneReadyReplayBoundary,
			"ordering_position", loginEquipmentSceneReadyReplayPosition,
			"switch", loginEquipmentSceneReadyReplaySwitch(),
			"switch_env", loginEquipmentSceneReadyReplayEnv,
			"reason", "equipment_snapshot_unavailable_no_invented_container")
		return nil
	}
	entrySize := currentItemListEntrySizeForType(currentSocketListEquipment)
	slots, durability, nonzero, rows, auditOK := loginEquipmentContainerWireRowAudit(body, entrySize)
	if !auditOK {
		// 审计解析失败不阻断下发：包体来自既有构建器，事件里标出来即可。
		s.logPacketEvent("game-login-equipment-scene-ready-replay-audit-failed",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"list", currentSocketListEquipment,
			"body_len", len(body),
			"entry_size", entrySize,
			"switch", loginEquipmentSceneReadyReplaySwitch(),
			"switch_env", loginEquipmentSceneReadyReplayEnv,
			"reason", "emitted_container_body_did_not_match_the_0x77_row_layout")
	}
	s.logPacketEvent("game-login-equipment-scene-ready-replay-sent",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"list", currentSocketListEquipment,
		"rows", rows,
		"entry_count", entryCount,
		"msg_id", uint16(dnfenum.CmdPacketLeaveParty),
		"classification", 0,
		"list_type", currentSocketListEquipment,
		"body_len", len(body),
		"body_source", bodySource,
		"entry_size", entrySize,
		"slots_sent", strings.Join(slots, ","),
		"durability_carried", strings.Join(durability, ","),
		"nonzero_durability_carried", strings.Join(nonzero, ","),
		"boundary", loginEquipmentSceneReadyReplayBoundary,
		"ordering_position", loginEquipmentSceneReadyReplayPosition,
		// FIX ②（`login_switch_inventory_scene_ready_replay.go`）：提交之后到底交付了
		// 哪几个 list。list 3 由本条发，list 1（换装栏物品容器）由同一处边界的下一条发。
		"post_commit_lists", loginPostCommitContainerLists(),
		"switch", loginEquipmentSceneReadyReplaySwitch(),
		"switch_env", loginEquipmentSceneReadyReplayEnv,
		"scene_path_evidence", "party_dungeon_loading_barrier_delivers_the_same_seven_containers_after_the_client_finish_loading_and_room_commit_login_delivered_them_before_typed_op24",
		"actor_generation_replay_excluded", "type1345_replaying_mode0_mode1_op105_op102_op37_op30_op19_op120_crashes_the_current_client_changlog_9943_9944")
	if err := s.sendCurrentSceneItemList(session, uint16(dnfenum.CmdPacketLeaveParty), body); err != nil {
		return err
	}
	// 汇总"提交之后交付了哪几个 list"：list 3 的行数在这里回填，list 1 由
	// `login_switch_inventory_scene_ready_replay.go` 在紧随其后的同一处边界回填。
	s.noteLoginPostCommitEquipmentRows(session, source, rows)
	// The scene commit and the list-3 create replay replace the creature object
	// that received the earlier op14. Reapply only the authoritative worn pet's
	// dynamic state after that create; actor and creature initialization packets
	// must not be replayed at this boundary.
	creature, err := s.currentEquippedCreatureForCharacterWithError(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil {
		return fmt.Errorf("read scene-ready equipped creature: %w", err)
	}
	if creature.valid() {
		if err := s.sendSelectedEquippedCreatureItemRefresh(session, source+"_after_equipment_container_equipped_item"); err != nil {
			return err
		}
	}
	return nil
}
