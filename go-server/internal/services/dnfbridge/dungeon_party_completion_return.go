package dnfbridge

import (
	"os"
	"strings"
)

// 本文件把上一轮"队长退出副本 ⇒ 解散队伍"的行为**收窄到放弃路径**，并给
// **通关回城**这条路径补上操作者口径："和队员一起退出且保留队伍"。
//
// # 操作者报障（原文）
//
//	「组队完成副本后点击队长右上角返回城镇会直接退出队伍。
//	  正常情况下应该和队员一起退出且保留队伍。」
//
// # 两条出图路径在线上是**可区分**的（不是同一条路径）
//
//  1. **通关回城（本文件）**：`sendCurrentCompletedDungeonReturnToTown`
//     （`dungeon_return_town.go:480`）只在 `Run.Status == DungeonRunCompleted`
//     时才会走到（同文件 `:493` 的闸门），提交点是 `:597-608`，钩子在 `:613`，
//     `source` 字面量 `completed_town_route`。当前客户端那条请求是 op72
//     （`request_msg_id=72`，`source=current_exe_op72_card_exit_return_to_town`）。
//  2. **中途放弃**：`commitPendingDungeonReturnForSceneRequest`
//     （`dungeon_return_town.go:684`）提交一条"待决回城"，提交点上
//     `snapshot.Run.Status == DungeonRunActive` ⇒ `runtime.Session.Abandon()`
//     （同文件 `:699-705`），钩子在 `:729`，`source` 字面量
//     `back_to_village_committed`，事件字段 `run_status=abandoned`。
//
// 现场逐行（packet_log.txt，队伍 1 = 队长角色 3 + 队员角色 6，副本 310）：
//
//	通关回城（= 操作者报障的那次）：
//	:109090 EVENT kind=game-dungeon-completed-town-route-send conn_id=game-000008 char_id=3
//	        request_msg_id=72 dungeon_id=310 town_id=31 area_id=2
//	        source=current_exe_op72_card_exit_return_to_town t=2026-09-28T14:30:10.304294263Z
//	:109132 EVENT kind=game-party-dungeon-leader-exit-disband conn_id=game-000008
//	        source=completed_town_route party_id=1 instance_id=1 leader_char_id=3
//	        leaving_char_id=3 member_count_before=2 removed_char_ids=[6 3] disbanded=true
//	        members_still_in_dungeon=[6] peer_actor_removal_sent_to=[6]
//	        roster_cleared_sent_to=[3 6] mode=disband t=2026-09-28T14:30:10.386130981Z
//	:109133 EVENT kind=game-dungeon-completed-town-route-committed conn_id=game-000008
//	        char_id=3 request_msg_id=72 dungeon_id=310 town_id=31 area_id=2
//	        runtime_detached=true t=2026-09-28T14:30:10.386146981Z
//	:109195 EVENT kind=game-dungeon-completed-town-route-committed conn_id=game-000011
//	        char_id=6 request_msg_id=72 dungeon_id=310 town_id=31 area_id=2
//	        runtime_detached=true t=2026-09-28T14:30:11.646211567Z   ← 队员由**自己的**客户端送回城镇
//
//	中途放弃（上一轮修好的那条）：
//	:96603 EVENT kind=game-dungeon-party-instance-member-left instance_id=1 party_id=1
//	        dungeon_id=310 char_id=3 member_count=1 source=back_to_village_committed
//	:96604 EVENT kind=game-dungeon-back-to-village-committed conn_id=game-000021 char_id=3
//	        dungeon_id=310 room=3:0 run_status=abandoned source=current_exe_op35_position_after_pending_town_return
//
// 现场全量统计（同一份 packet_log.txt，见报告 §1）：
//   - `game-party-dungeon-leader-exit-disband` 一共 **2** 条，`source` 全是
//     `completed_town_route`（14:30:10.386 / 15:32:35.000）——**误伤的全是通关回城**；
//   - `game-dungeon-back-to-village-committed` 一共 27 条，`run_status` **27/27 全是
//     abandoned**，其中**没有**任何一条伴随解散事件——放弃路径没有被这条误伤。
//
// # 为什么通关回城不该解散
//
// `dungeon_return_town.go:493` 已经把"这次回城是通关回城"钉死成运行时事实，
// `party_town_return.go:9-16,30-39` 的既有同屏重建也要求"同一队伍的每个成员都回到城镇"，
// 前提正是队伍还在——解散之后 `state.PartyID <= 0` ⇒ 重建直接放弃，两个人回到城镇
// 也再不会互相投影（现场 `game-party-town-return-copresence-rebuilt` 计数 = 0）。
//
// 队员**不需要**服务端替它移动：它自己的客户端会走同一条 `completed_town_route`
// （上面 `:109195` / `:138768`，落后 1.3~2.0 秒）。所以这里**一个字节都不发**，
// 只做两件事：保留队伍 + 在最后一个成员也回到城镇时用既有收口点重建同屏。
// 刻意**不**照搬厂商 `TownHandler.cs:1619-1673 TryFanOutLeaderReturnToTownAsync`
// 那种"服务端替队友发 op24"的扇出：本客户端自己会发，服务端再推一次会与
// 队友自己的 op72 路线撞成两条城镇切换（`runtime.townReturnPending` 只挡得住重复，
// 挡不住"服务端先发、客户端后发"这一对）；而厂商那一份自己还标着
// `⚠️协议/渲染, 待真机`。取证与取舍见报告 §2。
//
// # 本开关
//
//	`DNFBRIDGE_PARTY_DUNGEON_COMPLETION_RETURN`
//	  - `keep`（默认：空/未设置/非法值）= 修好后的行为：通关回城**保留队伍**，
//	    不发明册销毁帧，队员由自己的客户端送回城镇，最后一个成员到位后重建城镇同屏；
//	  - `disband` = **逐字节回到改动前**：通关回城与放弃回城一样调用
//	    `disbandRuntimePartyOnDungeonLeaderExit`（同一函数、同一 source ⇒ 同一组包、
//	    同一组事件、同一份 run-key 清理）。
//
// 本开关**不**接受 `legacy`：那是兄弟开关 `DNFBRIDGE_PARTY_DUNGEON_LEADER_EXIT` 的
// 回退字面量（含义是"一个字节都不做"），在这里当成非法值落回修复档 `keep`，
// 避免"设了 legacy 以为回退了、其实没有"。
const dungeonPartyCompletionReturnEnv = "DNFBRIDGE_PARTY_DUNGEON_COMPLETION_RETURN"

const (
	dungeonPartyCompletionReturnValueKeep    = "keep"
	dungeonPartyCompletionReturnValueDisband = "disband"
)

// dungeonPartyCompletionReturnMode 是 dungeonPartyCompletionReturnEnv 的归一化取值。
type dungeonPartyCompletionReturnMode int

const (
	// dungeonPartyCompletionReturnKeep 是默认档（零值）= 修好后的行为。
	dungeonPartyCompletionReturnKeep dungeonPartyCompletionReturnMode = iota
	// dungeonPartyCompletionReturnDisband 是改动前行为（= 通关回城也解散），一键回退档。
	dungeonPartyCompletionReturnDisband
)

func (mode dungeonPartyCompletionReturnMode) String() string {
	if mode == dungeonPartyCompletionReturnDisband {
		return dungeonPartyCompletionReturnValueDisband
	}
	return dungeonPartyCompletionReturnValueKeep
}

// readDungeonPartyCompletionReturnMode 解析 DNFBRIDGE_PARTY_DUNGEON_COMPLETION_RETURN。
// **默认 `keep`（= 修好后的行为）**：空/未设置/非法值都是 keep；
// 只有显式 `disband` 回到改动前那条"通关回城也解散"的路径。
func readDungeonPartyCompletionReturnMode(value string) dungeonPartyCompletionReturnMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case dungeonPartyCompletionReturnValueDisband:
		return dungeonPartyCompletionReturnDisband
	default:
		return dungeonPartyCompletionReturnKeep
	}
}

// dungeonPartyCompletionReturnModeValue 自读开关（不改 options.go）。
func (s *Service) dungeonPartyCompletionReturnModeValue() dungeonPartyCompletionReturnMode {
	return readDungeonPartyCompletionReturnMode(os.Getenv(dungeonPartyCompletionReturnEnv))
}

// settleRuntimePartyOnDungeonCompletionReturn 在**通关回城**提交之后调用
// （`dungeon_return_town.go` 两处：提交通关城镇路线的 `:613`，以及从结算页
// 走 op132/op42 那条"待决回城"的提交点 `:729`——后者只在 run_status 是
// completed 时才会走到这里）。
//
// 默认档（`keep`）触发条件：本会话的权威队伍名单 ≥ 2 人（单人/无队伍时与改动前
// 逐字节相同：不改状态、不发包、不记本文件的事件）。命中后：
//  1. **不动中央管理器**：PartyId / 槽位 / 队长全部原样保留；
//  2. **不发任何帧**：不发空名册（op153 + op9 kind3），也不发"队长 actor 移除"
//     （op9 kind3）——队友还在图内是**正常**的，它自己的客户端马上会走
//     自己的 completed_town_route 回城；
//  3. 记一条 `game-party-dungeon-completion-return-party-kept`，把"这次通关回城
//     保留了哪支队伍、还有谁在图内"写清；
//  4. 调**既有**的 `rebuildRuntimePartyTownCoPresenceAfterReturn`
//     （`party_town_return.go:17`）：队友还在图内 / 还没消费完城镇场景收尾时它是
//     no-op，等**每个**成员都到位之后才把队友角色与实时名册重新投影出来——
//     这就是"和队员一起退出"里"一起"的那一环。
//
// 返回值 = **仍留在本场实例内、被刻意留着不管的成员数**（keep 档一个字节都没发，
// 这个数字只用于日志判读与断言；回退档返回被解散函数发出的 peer 移除帧数，与改动前同义）。
func (s *Service) settleRuntimePartyOnDungeonCompletionReturn(
	session *gameSession,
	instance *dungeonPartyInstance,
	leavingCharacterID uint16,
	source string,
) int {
	if s == nil || session == nil || leavingCharacterID == 0 {
		return 0
	}
	if s.dungeonPartyCompletionReturnModeValue() == dungeonPartyCompletionReturnDisband {
		// 一键回退档：逐字节回到改动前 —— 通关回城仍然按"队长退出副本"解散。
		// 调的是同一个函数、同一个 source：包、事件、run-key 清理都逐字节相同；
		// 兄弟开关 `DNFBRIDGE_PARTY_DUNGEON_LEADER_EXIT=legacy` 也照旧在里面生效。
		return s.disbandRuntimePartyOnDungeonLeaderExit(session, instance, leavingCharacterID, source)
	}

	// 权威名单按**角色**查中央管理器（会话本地缓存可能落后，见 party_manager_runtime.go）。
	state := s.runtimePartyStateForSession(session)
	members := runtimePartyMembers(state)
	if state.PartyID <= 0 || len(members) < 2 {
		// 单人 / 没有活队伍：与改动前逐字节相同。
		return 0
	}
	leaderID := state.UserID
	inDungeon := dungeonPartyInstanceMemberSessions(instance, leavingCharacterID)
	remainingInDungeon := make([]uint16, 0, len(inDungeon))
	for _, peer := range inDungeon {
		if peer.CharacterID != 0 {
			remainingInDungeon = append(remainingInDungeon, peer.CharacterID)
		}
	}

	s.logGameEvent(session, "game-party-dungeon-completion-return-party-kept",
		"source", source,
		"party_id", state.PartyID,
		"instance_id", instanceIDOfPartyDungeonInstance(instance),
		"leader_char_id", leaderID,
		"leaving_char_id", leavingCharacterID,
		"leaving_is_leader", leaderID == leavingCharacterID,
		"member_count", len(members),
		"members_still_in_dungeon", remainingInDungeon,
		"party_preserved", true,
		"roster_frames_sent", 0,
		"member_instance_retained", true,
		"switch", dungeonPartyCompletionReturnEnv,
		"mode", s.dungeonPartyCompletionReturnModeValue().String(),
		"reason", "completed_dungeon_return_keeps_the_party_and_every_member_returns_to_town_with_the_leader")

	// 4. "一起回城"的最后一环。既有收口点，不新增机制：
	//    `currentPartyTownReturnSceneReady` 会对每个成员要求"没有待决回城、没有副本
	//    运行时、城镇场景就绪"，任何一个没到位就整条放弃（`party_town_return.go:28-32`）。
	if err := s.rebuildRuntimePartyTownCoPresenceAfterReturn(
		session,
		source+"_party_town_copresence_after_completed_return",
	); err != nil {
		s.logGameEvent(session, "game-party-dungeon-completion-return-copresence-failed",
			"source", source,
			"party_id", state.PartyID,
			"leader_char_id", leaderID,
			"leaving_char_id", leavingCharacterID,
			"error", err)
	}

	// 5. 服务端扇出（默认档 `on`）：把仍在同一实例内、**已经结算完**的队员一起送回城镇。
	//    上一轮刻意不做这件事（理由与本次为什么可以安全做，见
	//    dungeon_party_completion_return_fanout.go 文件头：调用点在队长 `session.dungeon.mu`
	//    之外，任何时刻只持有一个会话锁，且队员自己的 op72 会被 `runtime` 已摘除的闸门挡下）。
	//    回退档（`..._FANOUT=legacy`）下这里一个字节都不发、不记事件，与改动前逐字节相同。
	//    返回值仍是"留在本场实例内、被刻意留着不管的成员数"（语义不变，扇出结果记在
	//    `game-party-dungeon-completion-return-fanout` 事件里）。
	s.fanOutCompletedDungeonReturnToPartyMembers(
		session,
		instance,
		leavingCharacterID,
		source+"_fanout",
	)
	return len(remainingInDungeon)
}
