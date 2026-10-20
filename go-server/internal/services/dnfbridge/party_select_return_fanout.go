package dnfbridge

import (
	"context"
	"os"
	"strings"
)

// 本文件是运维 2026-09-29 第 3 条的服务端那一半：
//
//	「组队进入副本选择界面后 如果队长没有选择地图直接返回城镇 队员会被卡在副本选择界面
//	  （队员视角等待超时后左上角的血量ui出现异常）」
//
// ── 现场（`packet_log.txt`，逐行；窗口内全库仅此一次"有队伍的选择页回城"）──────────
//
// 2026-09-29T05:50Z，party_id=1，队长 char4/`conn_id=game-002700`，
// 队员 char1/`conn_id=game-002698`：
//
//	:239204 队长 op132 回城（`has_runtime=false`, `party_id=1`, 05:50:23.830）
//	:239208 `game-dungeon-select-back-to-village-committed`（op24 **只**写给队长）
//	:239265 `SEND kind=game-upper conn_id=game-002698 pkt_seq=409 msg_id=22
//	        owner=town_copresence_move_0x0016 hex=001600190000007c64b6e500000000000400c800550105aa00`
//	        ⇒ 城市场景位移帧（actor key 4 / x=200 / y=341 / dir 5）被灌进"还在渲染选图页"的客户端
//	:239276 队员**自己**发 op132（05:50:47.927，落后 24.095 s）—— 到这一刻才解卡
//
// 中间这 24.095 秒里发给队员的报文**总共 5 条，全是 `msg_id=22`**；对
// `conn_id=game-002698` 查 `blocked|skipped|suppressed` 命中 **0**。
//
// ── 根因 ──────────────────────────────────────────────────────────────────────
//
// `dungeon_return_town.go` 的"选择页回城"分支（`:98-151`）**只处理请求者本人**：
// op24 只写给 `session`（`:132`），清旗子/重布防全部是 session 局部（`:136-140`、`:149`），
// 然后 `:150 return nil`。**全支没有任何队伍成员遍历**。
//
// 全树唯一把回城扇出给队友的原语 `rebuildRuntimePartyTownCoPresenceAfterReturn`
// （`party_town_return.go:17`）有 3 个调用点
// （`dungeon_party_completion_return.go:199`、`game_enter_select_dungeon.go:131`/`:160`），
// **都不在这条支上**；而且它对每名成员都要求
// `currentPartyTownReturnSceneReady`（`party_town_return.go:30`）—— 队员这时**正停在选图页**
// （`enterSelectDungeon*` 三旗还立着、`hasDungeonRuntime=false`），那条判据天然不成立
// ⇒ 即便把它挂上去也会静默 `return nil`。
//
// 所以这不是"包被抑制"，而是"**这条分支上根本没有队友的代码**"；这也是为什么现场
// 找不到任何 `suppressed`/`blocked`/`skipped` 事件可指认。
//
// ── 修法（复用，不发明）────────────────────────────────────────────────────────
//
// 对**同一支队伍里、此刻正停在选图页**的每一名成员，走**它自己那条已经完全验证过的
// 选择页回城路径**：同一个 `prepareCurrentDungeonTownTransitionForSession`、
// 同一条 op24（`currentSceneTransitionMsgID`）、同一串收尾
// （`clearCurrentDungeonSelectContext` → `resetDungeonReturnSceneGates` →
// `returnTownFinishLoadingAckOnly` → `confirmedDungeonReturnStatePending` →
// `markBackToVillageEnterSelectPending` → `clearCurrentTownSelectorOrigin`）。
// **不新增任何包型**：现场 :239276 证明队员自己发 op132 时客户端就是靠这一串解卡的，
// 这里只是替他把同一串提前发出去。
//
// 安全闸门（不做无根据的"抢人"）：
//
//	· 只有 `currentDungeonSelectTownReturnReady(member)` 为 true（= 队员确实停在选图页、
//	  城镇原点已绑、位置有效）才发；判据与队长自己那条分支的准入**完全同一个函数**；
//	· 队员已经持有副本运行时（在图里）⇒ 一个字节都不发（那是通关回城/放弃回城的地盘）；
//	· 队员已经有 `townReturnPending` ⇒ 不发（幂等）；
//	· 队伍号/队长号必须与扇出方一致，否则跳过。
//
// 锁序：扇出在队长**已经释放** `session.dungeon.mu` 之后执行；任何时刻只持有**一个**
// `member.dungeon.mu`，且不与 `instance.mu` 嵌套（`runtimePartyStateSnapshot` 是只读快照）。
//
// ── 开关 ─────────────────────────────────────────────────────────────────────
//
//	`DNFBRIDGE_PARTY_SELECT_RETURN_FANOUT`
//	  - 默认（空/未设置/非法值）= **on**（修复档）：把停在选图页的队员一起带回城镇；
//	  - `off` / `0` / `false` / `no` / `legacy` = 逐字节回到改动前（不扇出、不记本文件的
//	    事件；队员仍靠自己的 op132，现场即为 24.095 秒后才解卡）。
const dungeonPartySelectReturnFanoutEnv = "DNFBRIDGE_PARTY_SELECT_RETURN_FANOUT"

func dungeonPartySelectReturnFanoutEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(dungeonPartySelectReturnFanoutEnv))) {
	case "0", "off", "false", "no", "legacy":
		return false
	default:
		return true
	}
}

func dungeonPartySelectReturnFanoutMode() string {
	if dungeonPartySelectReturnFanoutEnabled() {
		return cloneAvatarAppearanceOn
	}
	return cloneAvatarAppearanceOff
}

// fanOutDungeonSelectReturnToPartyMembers 把"队长在选择页直接返回城镇"扇出给同一支队伍里
// **此刻正停在选图页**的其它成员，让每一名成员都收到**自己的** op24 并走完同一串收尾。
// 返回真正被送回城镇的成员数。
//
// 调用方必须**不在**任何 `session.dungeon.mu` 里。
func (s *Service) fanOutDungeonSelectReturnToPartyMembers(session *gameSession, source string) int {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return 0
	}
	if !dungeonPartySelectReturnFanoutEnabled() {
		return 0
	}
	state := runtimePartyStateSnapshot(session)
	members := runtimePartyMembers(state)
	if state.PartyID <= 0 || len(members) < 2 {
		// 单人 / 没有活队伍：与改动前逐字节相同。
		return 0
	}
	fannedOut := 0
	for _, member := range members {
		if member.UserID == 0 || member.UserID == session.selectedCharacterID {
			continue
		}
		peer, online := s.onlineGameSession(member.UserID)
		if !online || peer == nil || peer == session {
			continue
		}
		// 只读地看一眼这名队员的运行时状态，**立刻释放**它的 dungeon 锁；
		// 真正的发送在下面重新上锁。
		peer.dungeon.mu.Lock()
		peerRuntime := peer.dungeon.runtime
		holdsRuntime := peerRuntime != nil && peerRuntime.Session != nil
		pendingReturn := peerRuntime != nil && peerRuntime.townReturnPending
		peerCharacterID := peer.selectedCharacterID
		peer.dungeon.mu.Unlock()
		peerState := runtimePartyStateSnapshot(peer)

		selectorReady := false
		selectorReason := ""
		if !holdsRuntime {
			selectorReady, selectorReason = currentDungeonSelectTownReturnReady(peer)
		}
		decision := "fan_out"
		reason := "member_is_waiting_on_the_confirmed_dungeon_select_page"
		switch {
		case peerCharacterID == 0 || peerCharacterID != member.UserID:
			decision, reason = "skip", "member_selected_character_changed"
		case peerState.PartyID != state.PartyID || peerState.UserID != state.UserID:
			decision, reason = "skip", "member_party_or_leader_changed"
		case holdsRuntime:
			decision, reason = "skip", "member_holds_a_dungeon_runtime"
		case pendingReturn:
			decision, reason = "skip", "member_already_has_a_pending_town_return"
		case !selectorReady:
			decision, reason = "skip", selectorReason
		}
		delivered := false
		if decision == "fan_out" {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			transition, err := s.prepareCurrentDungeonTownTransitionForSession(ctx, peer, peerCharacterID)
			cancel()
			if err != nil {
				decision, reason = "failed", "member_town_transition_origin_unavailable"
				s.logGameEvent(session, "game-party-dungeon-select-return-fanout-failed",
					"source", source,
					"leader_char_id", state.UserID,
					"member_char_id", member.UserID,
					"error", err)
			} else if err := s.sendDungeonSelectReturnToMember(peer, transition, source); err != nil {
				decision, reason = "failed", "member_select_town_return_send_failed"
				s.logGameEvent(session, "game-party-dungeon-select-return-fanout-failed",
					"source", source,
					"leader_char_id", state.UserID,
					"member_char_id", member.UserID,
					"error", err)
			} else {
				delivered = true
				fannedOut++
			}
		}
		// 这一行是"下一次实测一眼可判"的那一条。
		s.logGameEvent(session, "game-party-dungeon-select-return-fanout",
			"source", source,
			"party_id", state.PartyID,
			"leader_char_id", state.UserID,
			"member_char_id", member.UserID,
			"member_holds_runtime", holdsRuntime,
			"member_town_return_pending", pendingReturn,
			"member_select_return_ready", selectorReady,
			"decision", decision,
			"delivered", delivered,
			"reason", reason,
			"switch", dungeonPartySelectReturnFanoutEnv,
			"mode", dungeonPartySelectReturnFanoutMode())
	}
	return fannedOut
}

// sendDungeonSelectReturnToMember 在一名队员自己的会话上走完"选择页回城"那一条已证路径：
// 同一条 op24 + 同一串收尾。与 `dungeon_return_town.go:96-150` 的队长分支**同形**，
// 只是执行者是另一名会话。
func (s *Service) sendDungeonSelectReturnToMember(
	member *gameSession,
	transition currentDungeonTownTransition,
	source string,
) error {
	if s == nil || member == nil {
		return nil
	}
	member.dungeon.mu.Lock()
	runtime := member.dungeon.runtime
	if runtime != nil && runtime.Session != nil {
		// 二次确认：上锁之后这名队员已经不在选图页了（例如它自己刚发过 op132）⇒ 谁也不抢谁。
		member.dungeon.mu.Unlock()
		return nil
	}
	s.logGameEvent(member, "game-dungeon-select-back-to-village-op24-send",
		"char_id", member.selectedCharacterID,
		"response_msg_id", currentSceneTransitionMsgID,
		"classification", 0,
		"town_id", transition.TownID,
		"area_id", transition.AreaID,
		"row_count", 1,
		"actor_object_key", transition.ActorObjectKey,
		"position_x", transition.PositionX,
		"position_y", transition.PositionY,
		"direction", transition.Direction,
		"area_state", transition.AreaState,
		"position_source", transition.PositionSource,
		"body_len", len(transition.Body),
		"source", source,
		"body_source", "current_exe_sub_1D901D0_typed")
	if err := s.sendGameUpperRawClass(member, currentSceneTransitionMsgID, transition.Body, 0); err != nil {
		member.dungeon.mu.Unlock()
		return err
	}
	clearCurrentDungeonSelectContext(member)
	resetDungeonReturnSceneGates(member)
	member.returnTownFinishLoadingAckOnly = true
	member.confirmedDungeonReturnStatePending = true
	markBackToVillageEnterSelectPending(member)
	s.logGameEvent(member, "game-dungeon-select-back-to-village-committed",
		"char_id", member.selectedCharacterID,
		"town_id", transition.TownID,
		"area_id", transition.AreaID,
		"selector_context_cleared", true,
		"finish_loading_state_rearmed", true,
		"reason", "leader_left_the_dungeon_select_page_and_the_party_member_is_returned_with_it")
	member.dungeon.mu.Unlock()
	clearCurrentTownSelectorOrigin(member)
	return nil
}
