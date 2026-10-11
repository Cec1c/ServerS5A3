package dnfbridge

import (
	"context"
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件是操作者第 4 条报障里"队员没有被带出副本"那一半的服务端扇出。
//
//	「通关副本后点击返回城镇：队伍虽然还在，但是状态异常；队员没有被带出副本；
//	  队员自行退出后城镇内看不到队长；队长进副本也不能带队员进入了。」
//
// # 现状：通关回城**不**扇出，队员只能靠自己的客户端
//
// 上一轮的通关回城修复（`dungeon_party_completion_return.go`）刻意只做"保留队伍"，
// 明确**不**照搬厂商 `TownHandler.cs:1619-1673 TryFanOutLeaderReturnToTownAsync`，
// 理由是"本客户端自己会发 op72，服务端再推一次会撞成两条城镇切换"。当时的现场证据是：
//
//	:109133 EVENT kind=game-dungeon-completed-town-route-committed conn_id=game-000008
//	        char_id=3 request_msg_id=72 ... t=2026-09-28T14:30:10.386146981Z
//	:109195 EVENT kind=game-dungeon-completed-town-route-committed conn_id=game-000011
//	        char_id=6 request_msg_id=72 ... t=2026-09-28T14:30:11.646211567Z   ← 落后 1.26s
//
// 这一次（party 5 / instance 3 / dungeon 310，队长 char1 + 队员 char6）**同样**成立，
// 但代价暴露出来了 —— 队员那条**不是**卡牌退出，而是 12.3 秒后的放弃兜底：
//
//	:153600 EVENT kind=game-dungeon-completed-town-route-send conn_id=game-000003 char_id=1
//	        request_msg_id=72 ... source=current_exe_op72_card_exit_return_to_town
//	        t=2026-09-28T16:14:33.4 ...
//	:153629 EVENT kind=game-party-dungeon-completion-return-party-kept conn_id=game-000003
//	        leader_char_id=1 leaving_char_id=1 leaving_is_leader=true member_count=2
//	        members_still_in_dungeon=[6] roster_frames_sent=0 ... 16:14:33.417990596Z
//	:153660 EVENT kind=game-dungeon-completed-town-route-send conn_id=game-000007 char_id=6
//	        request_msg_id=42 ... ← **op42 放弃**（CHARACTER_STATISTIC 被 conflicting_replay 挡下之后）
//	        t=2026-09-28T16:14:45.6 ...
//	:153690 EVENT kind=game-party-dungeon-completion-return-party-kept conn_id=game-000007
//	        leaving_char_id=6 leaving_is_leader=false ... t=2026-09-28T16:14:45.709848732Z
//
// 即：**队员自己的回城路径确实会 fire，但落后 12.3 秒，而且走的是 op42 放弃兜底**
// （现场客户端先发 op123 被 `game-dungeon-character-statistic-blocked
// reason=conflicting_replay_after_first_capture` 挡下，随后才 op42）。队长的
// party 框在这 12.3 秒里一直显示"队员还在图内"，操作者看到的就是"队员没有被带出副本"。
//
// # 厂商口径
//
// `TownHandler.cs:1204-1216`：`BACK_2_VILLAGE 0x84`（通关回城）调
// `TryFanOutLeaderReturnToTownAsync`，把**仍在同一 PartyDungeonInstanceId 内**的在线队员
// 一起送回城镇；`:1619-1673` 的实现按队伍名册遍历、对每名仍在图内的成员调用
// `ReturnSelfToTownAsync`。本文件就是这条口径在本仓的等价物。
//
// # 为什么这次可以安全做（上一轮拒绝的两个风险各自怎么解的）
//
//  1. **"服务端先发、客户端后发 ⇒ 两条城镇切换"**：扇出对每一名队员调用的是
//     **它自己的** `sendCurrentCompletedDungeonReturnToTown`（与 op72 完全同一条服务端路径），
//     它在 `dungeon_return_town.go:497-504` 用 `runtime.townReturnPending` 做首次尝试闸门，
//     并在提交时把 `session.dungeon.runtime` 摘掉 —— 队员自己的 op72 之后再来时，
//     `:492` 的 `session.dungeon.runtime != runtime` 闸门直接返回 nil，**不会**产生第二条路线。
//
//  2. **跨会话锁**：扇出在**队长已经释放 `session.dungeon.mu` 之后**才执行
//     （调用点在 `dungeon_return_town.go:612-616` 的锁外分支里）。锁序被钉死成
//
//     （无任何会话锁）→ instance.mu（只读快照成员表，随即释放）
//     → memberSession.dungeon.mu（一次只拿一个，且**不嵌套**）
//     → instance.mu（releasePartyDungeonInstance 内部，既有顺序）
//
//     即与既有单会话路径完全相同的 `session.dungeon.mu → instance.mu`，
//     **任何时刻都不持有两个 `session.dungeon.mu`**。`dungeonPartyInstanceMemberSessions`
//     （dungeon_party_leader_exit.go:110）本来就是为这条取舍写的只读快照。
//
// # 安全闸门（不做无根据的"抢人"）
//
// 只对**已经走完自己结算**的队员扇出：`runtime.settlementClearRewardSent`
// （与 `dungeon_card_flow.go:290` 接受队员自己 op72 的那道闸门**同一判据**）。
// 还在翻牌的队员一个字节都不发，只记一条 `decision=deferred`；
// 它自己的 op72/op42 照旧走原来的路。
//
// # 开关
//
//	`DNFBRIDGE_PARTY_DUNGEON_COMPLETION_RETURN_FANOUT`
//	  - 默认（空/未设置/非法值）= **on**（修复档）：通关回城把已结算完的队员一起带回城镇；
//	  - `off` / `0` / `false` / `no` / `legacy` = 逐字节回到改动前（不扇出、不记本文件的
//	    事件；队员仍靠自己的客户端，现场即为 12.3 秒后的 op42 兜底）。
const dungeonPartyCompletionReturnFanoutEnv = "DNFBRIDGE_PARTY_DUNGEON_COMPLETION_RETURN_FANOUT"

func dungeonPartyCompletionReturnFanoutEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(dungeonPartyCompletionReturnFanoutEnv))) {
	case "0", "off", "false", "no", "legacy":
		return false
	default:
		return true
	}
}

func dungeonPartyCompletionReturnFanoutMode() string {
	if dungeonPartyCompletionReturnFanoutEnabled() {
		return cloneAvatarAppearanceOn
	}
	return cloneAvatarAppearanceOff
}

// fanOutCompletedDungeonReturnToPartyMembers 把"通关回城"这条服务端路线扇出给
// 同一队伍副本实例内**已经结算完**的其它成员。返回真正被送回城镇的成员数。
//
// 调用方必须**不在**任何 `session.dungeon.mu` 里（见文件头的锁序说明）。
func (s *Service) fanOutCompletedDungeonReturnToPartyMembers(
	session *gameSession,
	instance *dungeonPartyInstance,
	leavingCharacterID uint16,
	source string,
) int {
	if s == nil || session == nil || instance == nil || leavingCharacterID == 0 {
		return 0
	}
	if !dungeonPartyCompletionReturnFanoutEnabled() {
		return 0
	}
	peers := dungeonPartyInstanceMemberSessions(instance, leavingCharacterID)
	if len(peers) == 0 {
		return 0
	}
	fannedOut := 0
	for _, peer := range peers {
		member := peer.Session
		if member == nil || peer.CharacterID == 0 || member == session {
			continue
		}
		// 只读地看一眼这名队员的运行时状态，**立刻释放**它的 dungeon 锁；
		// 真正的路线走 sendCurrentCompletedDungeonReturnToTown（它自己会上锁）。
		member.dungeon.mu.Lock()
		memberRuntime := member.dungeon.runtime
		holdInstance := memberRuntime != nil && memberRuntime.partyInstance == instance
		memberCharacterID := member.selectedCharacterID
		settlementDone := false
		runCompleted := false
		pendingReturn := false
		if memberRuntime != nil && memberRuntime.Session != nil {
			settlementDone = memberRuntime.settlementClearRewardSent
			runCompleted = memberRuntime.Session.Snapshot().Run.Status == worldmap.DungeonRunCompleted
			pendingReturn = memberRuntime.townReturnPending
		}
		member.dungeon.mu.Unlock()

		decision := "fan_out"
		reason := "member_finished_settlement_and_still_holds_the_completed_party_instance"
		switch {
		case memberRuntime == nil:
			decision, reason = "skip", "member_holds_no_dungeon_runtime"
		case !holdInstance:
			decision, reason = "skip", "member_no_longer_holds_the_leaders_party_instance"
		case memberCharacterID != peer.CharacterID || memberCharacterID == 0:
			decision, reason = "skip", "member_selected_character_changed"
		case pendingReturn:
			decision, reason = "skip", "member_already_has_a_pending_town_return"
		case !runCompleted:
			decision, reason = "deferred", "member_run_is_not_completed_yet"
		case !settlementDone:
			decision, reason = "deferred", "member_has_not_consumed_its_settlement_reward_screen_yet"
		}
		delivered := false
		if decision == "fan_out" {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			transition, err := s.prepareCurrentDungeonTownTransitionForSession(ctx, member, memberCharacterID)
			cancel()
			if err != nil {
				decision, reason, delivered = "failed", "member_town_transition_origin_unavailable", false
				s.logGameEvent(session, "game-party-dungeon-completion-return-fanout-failed",
					"source", source,
					"leader_char_id", leavingCharacterID,
					"member_char_id", memberCharacterID,
					"error", err)
			} else if err := s.sendCurrentCompletedDungeonReturnToTown(
				member,
				memberRuntime,
				transition,
				0,
				"completed_town_route_fanout",
			); err != nil {
				decision, reason, delivered = "failed", "member_completed_town_route_send_failed", false
				s.logGameEvent(session, "game-party-dungeon-completion-return-fanout-failed",
					"source", source,
					"leader_char_id", leavingCharacterID,
					"member_char_id", memberCharacterID,
					"error", err)
			} else {
				delivered = true
				fannedOut++
			}
		}
		// 这一行是"下一次实测一眼可判"的那一条：点名队伍/实例/队长/队员、决策、
		// 队员侧是否已经结算完、档位。
		s.logGameEvent(session, "game-party-dungeon-completion-return-fanout",
			"source", source,
			"party_id", instancePartyIDOfPartyDungeonInstance(instance),
			"instance_id", instanceIDOfPartyDungeonInstance(instance),
			"leader_char_id", leavingCharacterID,
			"member_char_id", peer.CharacterID,
			"member_selected_char_id", memberCharacterID,
			"member_holds_instance", holdInstance,
			"member_run_completed", runCompleted,
			"member_settlement_clear_reward_sent", settlementDone,
			"member_town_return_pending", pendingReturn,
			"decision", decision,
			"delivered", delivered,
			"reason", reason,
			"switch", dungeonPartyCompletionReturnFanoutEnv,
			"mode", dungeonPartyCompletionReturnFanoutMode())
	}
	return fannedOut
}

// instancePartyIDOfPartyDungeonInstance 只读实例上记的队伍号（取不到返回 0）。
func instancePartyIDOfPartyDungeonInstance(instance *dungeonPartyInstance) uint16 {
	if instance == nil {
		return 0
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	return instance.key.PartyID
}
