package dnfbridge

import (
	"os"
	"strings"
)

// 本文件修"**放弃副本 → 双方回城 → 重新组队 → 队长进图，队员卡在城镇**"。
//
// # 操作者报障（原文）
//
//	「在队长放弃组队后 双方都返回城镇 然后重新组队 队长进入副本选择 队员看不到
//	  队长能进图 进图后队员状态异常，队员视角是卡在城镇没有重新进入的。」
//
// # 现场逐行（packet_log.txt，2026-09-28；队长 3 = conn game-000009，队员 6 = conn game-000028）
//
// 先是一轮**正常**的组队进图（队伍 2，两人都进了 instance 2，队员也读了条）：
//
//	:144808 EVENT kind=game-dungeon-party-instance-created instance_id=2 party_id=2 dungeon_id=310
//	        seed=1194847781 char_id=6 t=2026-09-28T15:37:15.446658252Z
//	:144901 EVENT kind=game-party-dungeon-loading-ready party_id=2 char_id=3 slot=0 release=false t=15:37:17.536
//	:144907 EVENT kind=game-party-dungeon-loading-ready party_id=2 char_id=6 slot=1 release=true  t=15:37:17.914
//
// 队长在副本里放弃（上一轮的解散路径），**队员留在图内**，随后自己也回城：
//
//	:145666 EVENT kind=game-dungeon-party-instance-member-left instance_id=2 party_id=2
//	        char_id=3 member_count=1 source=back_to_village_committed
//	:145963 …（队员侧）char_id=6 member_count=0 source=back_to_village_committed
//	:145341 EVENT kind=game-dungeon-back-to-village-committed conn_id=game-000028 char_id=6
//	        run_status=abandoned town_id=31 area_id=2
//	        source=current_exe_op35_position_after_pending_town_return
//
// 双方回城后**重新组队**（队伍 3，队长 3），队长选图进副本；队员被"准备好"了、
// 也落了同一个实例，但**它的客户端从头到尾没有装过选图页**，于是没有读条、没有 op37：
//
//	:145665 EVENT kind=game-party-dungeon-entry-member-prepared conn_id=game-000028 party_id=3
//	        leader_char_id=3 char_id=6 dungeon_id=310 map_id=36751 t=2026-09-28T15:37:38.946925521Z
//	:145666 EVENT kind=game-dungeon-party-instance-created instance_id=3 party_id=3 char_id=6 …
//	:145666 EVENT kind=game-dungeon-party-instance-joined conn_id=game-000009 instance_id=3 char_id=3 …
//	（:145700 前后）EVENT kind=game-upper-current-dungeon-context-op27-duplicate-skipped
//	        conn_id=game-000028 char_id=6 reason=town_enter_select_page_already_committed_before_real_op16
//	:145758 EVENT kind=game-party-dungeon-loading-ready party_id=3 char_id=3 slot=0 release=false
//	:145882 EVENT kind=game-party-dungeon-loading-released party_id=3 char_id=3
//	        trigger=timeout_no_member_reported_op37        ← 10 秒超时放行，队长独自进图
//	:145957 EVENT kind=game-party-dungeon-loading-released party_id=3 char_id=6
//	        trigger=timeout_no_member_reported_op37
//	:145764 EVENT kind=game-town-set-user-area-blocked conn_id=game-000028 char_id=6 town_id=31
//	        area_id=1 position_x=55 position_y=329 reason=town_scene_player_state_not_finalized
//	        ← 队员的客户端**还在城镇**（它在向城镇上报自己的坐标）
//
// 同一形状在**上一轮修复之前**就已存在，不是解散路径引入的（2026-09-28 13:41:35 那次
// 队伍 3、`instance_id=2`）：`game-party-dungeon-loading-ready` 只有 `char_id=3`，
// 随后同样是 `trigger=timeout_no_member_reported_op37`。
//
// # 机制（file:line）
//
// `game_enter_select_dungeon.go:378-380 prepareRuntimePartyFollowerEnterSelect`：
//
//	if follower.enterSelectDungeonContextSent && follower.enterSelectDungeonSent {
//		return true, nil            ← 「选择页早装好了」：**一个字节都不发**
//	}
//
// 这两面旗是"本会话的选图页（op15 → op5/op27）已经写给客户端"的记账。它们的清除点只有：
//
//	clearCurrentDungeonSelectContext        dungeon_return_town.go:242-253
//	  ← 通关回城的 `sendCurrentCompletedDungeonReturnToTown:513` 调了它
//	  ← 结算页"选择其它地下城"的 `town_enter_select.go:184` 也调了它
//	handleTownSetUserArea                   town_move.go:331-333（城镇过图成功时）
//	town_prev_village.go:379 / return_select_character（换角色）
//
// **中途放弃那条回城提交点没清**：`commitPendingDungeonReturnForSceneRequest`
// （`dungeon_return_town.go:684`，提交点在 `:719-726`）只调 `resetDungeonReturnSceneGates`
// （`:825-839`，那份清单里**没有** `enterSelectDungeon*`）。于是：
//
//  1. 队员在进本前开过选图页 ⇒ 三面旗为 true；
//  2. 它放弃回城（op42 → op35 提交）⇒ 旗子**原样留着**；客户端回城后也没有再发
//     一条能被 `town_move.go:331` 清的城镇 op36（现场它的 op36 是进图中途那条，
//     而且被 `town_scene_player_state_not_finalized` 挡掉了）；
//  3. 重新组队、队长进图 ⇒ `prepareRuntimePartyFollowerEnterSelect` 命中 `:378`
//     直接返回 true ⇒ **队员侧选择页投影整批没发**（op23 area=0xff 脱离 / op2
//     mode0+mode1 / op15 成功 / op36 / op5 / op27 一条都没有；现场窗口内
//     `conn_id=game-000028` 只收到 op11/op9/op153/op22 这些队伍帧）；
//  4. 客户端因此没有进入选择页，随后的 op28/op800/op29 也落不到它的场景上：
//     不读条 ⇒ 不发 op37 ⇒ 屏障 10 秒超时 ⇒ 队长一个人进图，队员留在城镇。
//
// # 修法
//
// 在**出图回城提交点**补上"离开副本世界就把选图页上下文作废"：
// `dungeon_return_town.go` 的中途放弃提交点（`:726` 解锁之后）调用
// `resetStaleDungeonSelectorContextAfterReturn`，做两件既有动作：
// `clearCurrentDungeonSelectContext(session)` + `clearCurrentTownSelectorOrigin(session)`
// ——与通关回城 `:513-514` **同一对**收口动作，不新增机制、不发任何包。
// 下一次进图时旗子已归零 ⇒ `:378` 不再短路 ⇒ 队员侧选择页按既有路径重装。
//
// 刻意**不**改 `game_enter_select_dungeon.go` 的 `:378` 短路：现场那次
// （15:37:15 队伍 2）队员**确实**刚自己开过选图页（同一时刻收到 op23+op2+op15+op5+op27），
// 那条短路在当时是**正确**的；只有"离开副本之后"旗子才变成谎话，所以在那里收口。
//
// # 开关
//
//	`DNFBRIDGE_PARTY_DUNGEON_SELECTOR_CONTEXT_RESET`
//	  - 默认（空/未设置/非法值）= **on**（修复档）：出图回城提交时作废本会话的选图页上下文；
//	  - `off` / `0` / `false` / `no` / `legacy` = 逐字节回到改动前（旗子原样留着、不记本文件的事件）。
//
// 与同族开关 `DNFBRIDGE_PARTY_DUNGEON_RUN_KEY_TOWN_RESET` 的取值方式一致（自读，不改 options.go）。
const dungeonPartySelectorContextResetEnv = "DNFBRIDGE_PARTY_DUNGEON_SELECTOR_CONTEXT_RESET"

func dungeonPartySelectorContextResetEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(dungeonPartySelectorContextResetEnv))) {
	case "0", "off", "false", "no", "legacy":
		return false
	default:
		return true
	}
}

// dungeonPartySelectorContextResetMode 是判读事件里的档位串。
func dungeonPartySelectorContextResetMode() string {
	if dungeonPartySelectorContextResetEnabled() {
		return "on"
	}
	return "off"
}

// resetStaleDungeonSelectorContextAfterReturn 在**出图回城提交之后**调用
// （`dungeon_return_town.go` 的中途放弃提交点）。
//
// 只有"这一档确实记着选图页上下文"时才动手并留一条事件；本来就没有上下文的会话
// （绝大多数单人回城）在这里**一个字段都不改、一个字节都不发、一行日志都不记**。
//
// 返回值 = 这次是否真的清了（供测试与后续收口使用）。
func (s *Service) resetStaleDungeonSelectorContextAfterReturn(session *gameSession, source string) bool {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return false
	}
	if !dungeonPartySelectorContextResetEnabled() {
		// 一键回退档：逐字节回到改动前 —— 旗子原样留着，不记本文件的事件。
		return false
	}
	hadSelectorContext := session.enterSelectDungeonSent ||
		session.enterSelectDungeonAckSent ||
		session.enterSelectDungeonContextSent
	session.townMu.Lock()
	hadTownOrigin := session.townSelectorOriginBound
	session.townMu.Unlock()
	if !hadSelectorContext && !hadTownOrigin {
		// 干净会话（例如从未开过选图页的单人回城）：与改动前逐字节相同。
		return false
	}
	selectorContextSent := session.enterSelectDungeonContextSent
	clearCurrentDungeonSelectContext(session)
	clearCurrentTownSelectorOrigin(session)
	s.logGameEvent(session, "game-party-dungeon-selector-context-reset-after-return",
		"source", source,
		"char_id", session.selectedCharacterID,
		"party_id", runtimePartyStateSnapshot(session).PartyID,
		"had_enter_select_sent", hadSelectorContext,
		"had_enter_select_context_sent", selectorContextSent,
		"had_town_selector_origin", hadTownOrigin,
		"selector_context_cleared", true,
		"switch", dungeonPartySelectorContextResetEnv,
		"mode", dungeonPartySelectorContextResetMode(),
		"reason", "a_stale_selector_context_would_make_the_next_party_entry_skip_the_follower_selector_projection")
	return true
}
