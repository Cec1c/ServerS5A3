package dnfbridge

// 本文件补上"**回城/回选区之后，城镇同屏投影再也不补发**"这条缺口。
//
// # 操作者报障（原文）
//
//	「返回城镇后互相在城镇内看不到，需要来回离开这个城镇才能互相看到。」
//
// # 现场（packet_log.txt，2026-09-28T14:30:3xZ，城镇 31/区域 2，两个会话 game-000008=角色3 / game-000011=角色6）
//
// 两人 14:30:33 在城镇里是**能看见**对方的（两条投影都在）：
//
//	:109279 EVENT kind=game-town-copresence-actor-state-send conn_id=game-000008
//	        source=town_copresence_actor_state_newcomer_to_existing target_char_id=3 actor_char_id=6 ...
//	:109295 EVENT kind=game-town-copresence-actor-state-send conn_id=game-000011
//	        source=town_copresence_actor_state_existing_to_newcomer target_char_id=6 actor_char_id=3 ...
//
// 随后 14:30:38 两人在城镇里重新组队，14:30:41 队长打开副本选择器、14:30:43 从选择器回到城镇
// —— 这一次回城走的是 **op35 位置 → 城镇过场后状态**（`town_post_transition_player_state.go`）：
//
//	:109497 EVENT kind=game-town-post-transition-player-state-armed conn_id=game-000008 channel_id=10
//	        source=current_exe_op35_position_without_retained_runtime_arm_direct_return_generation
//	        char_id=3 owner_context=10 generation=1 stage=1
//	        sequence=mode0_mode1_op105_optional_op102_op37_op30_op19_op120
//	:109515 EVENT kind=game-main-finish-loading-state-send conn_id=game-000008 channel_id=10
//	        source=current_exe_op35_position_without_retained_runtime_consume_direct_return_generation_op37_after_actor_and_creature_rebind
//	        char_id=3 msg_id=37 ...
//	:109535 EVENT kind=game-town-post-transition-player-state-sent ... char_id=3 stage=9 ...
//	:109612 EVENT kind=game-main-finish-loading-state-send conn_id=game-000011 ... char_id=6 ...（同一路径）
//
// **这一轮 op37/op30/op19/op120 全部发完，但一次同屏投影都没补。** 之后直到 14:31:03
// 角色 6 自己又走了一次 op36 过图（`:109644` 起）才重新出现投影：
//
//	:109684 EVENT kind=game-town-copresence-actor-state-send conn_id=game-000008
//	        source=town_copresence_actor_state_newcomer_to_existing target_char_id=3 actor_char_id=6 ...
//	        t=2026-09-28T14:31:03.790311324Z
//
// ⇒ 这就是"**需要来回离开这个城镇才能互相看到**"：只有一次新的 SET_USER_AREA 才会重新投影。
//
// # 代码上的缺口（file:line）
//
//	scene_finish_loading.go:60  sendCurrentFinishLoadingCharacterState
//	  └ :69  s.resendTownCopresencePeerActorsAfterFinishLoading(session, ...)   ← 唯一调用点
//	town_post_transition_player_state.go:169
//	  └ sendCurrentFinishLoadingCharacterStateSnapshot(...)                     ← **直接调用快照版**
//	     （绕过了上面那条收尾链，于是城镇补发一次都不会发生）
//
// 全日志 110,769 行的实测指纹完全吻合：
//
//	grep -c 'game-town-copresence-actor-resend'          → 0      （城镇补发：一次都没发生）
//	grep -c 'game-dungeon-party-peer-actor-resend'       → 4      （副本那条孪生补发：正常）
//
// 城镇那条补发本身（`resendTownCopresencePeerActorsAfterFinishLoading`，
// town_copresence_actor_resend.go:66）是好的，它只是**从来没被这条路径调用过**。
//
// # 修法
//
// `sendCurrentTownPostTransitionPlayerState` 是"城镇场景被 op24/op35 重建"的收尾点，
// 与副本的 finish-loading 收尾点同义。在它的尾段（op37/op30/op19/op120 全部发完、
// 锁已释放）之后再走一次既有补发原语，于是**每一次城镇场景重建之后都会重新投影一次同屏队友**。
//   - 只补同桶对端（复用 `PeersInSameArea`：同城+同区+同频道+同实例）；
//   - 持有活跃副本运行时的会话直接跳过（那条归 dungeon_party_peer_actor_resend.go）；
//   - 每次 post-transition 世代先**轮换一次幂等键**（`releaseTownCopresenceActorResend`），
//     保证"回到同一个区域、同一批人"也能重新投影一次 —— 这正是本缺陷的场景；
//   - 任何一步失败只记事件，绝不影响 op37 链（与既有 `...-resend-failed` 同口径）。
//
// # 开关
//
//	`DNFBRIDGE_TOWN_COPRESENCE_RESEND_ON_POST_TRANSITION`
//	  - 默认（空/未设置/非法值）= **on**（修复档）：城镇 post-transition 尾段补发一次；
//	  - `off` / `0` / `false` / `no` / `legacy` = 逐字节回到改动前（不轮换幂等键、
//	    不补发、不多一行日志）。
const currentTownCopresencePostTransitionResendEnv = "DNFBRIDGE_TOWN_COPRESENCE_RESEND_ON_POST_TRANSITION"

func currentTownCopresencePostTransitionResendEnabled() bool {
	return !currentTownPresencePublishDisabled(currentTownCopresencePostTransitionResendEnv)
}

// currentTownCopresencePostTransitionResendMode 是判读事件里的档位串。
func currentTownCopresencePostTransitionResendMode() string {
	if currentTownCopresencePostTransitionResendEnabled() {
		return cloneAvatarAppearanceOn
	}
	return cloneAvatarAppearanceOff
}

// resendTownCopresenceAfterTownPostTransition 在城镇过场后状态（op24/op35 重建）
// 全部发完之后补发一次同屏投影。幂等键每世代轮换一次，所以"回到同一区域"也会补。
func (s *Service) resendTownCopresenceAfterTownPostTransition(session *gameSession, source string) {
	if !currentTownCopresencePostTransitionResendEnabled() {
		return
	}
	if s == nil || s.onlinePlayers == nil || session == nil || session.selectedCharacterID == 0 {
		return
	}
	// 副本里的收尾归 dungeon_party_peer_actor_resend.go。
	if s.sessionHasActiveDungeonRuntime(session) {
		return
	}
	peers := s.onlinePlayers.PeersInSameArea(session.selectedCharacterID)
	// 场景重建后重新允许补一次：即使区域与对端集合都没变（本缺陷的现场就是这种），
	// 这一次也必须能重新投影。
	s.releaseTownCopresenceActorResend(session)
	if len(peers) == 0 {
		s.logGameEvent(session, "game-town-copresence-post-transition-resend-skipped",
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "no_same_area_peer_after_the_town_scene_rebuild",
			"switch", currentTownCopresencePostTransitionResendEnv,
			"mode", currentTownCopresencePostTransitionResendMode())
		return
	}
	s.logGameEvent(session, "game-town-copresence-post-transition-resend",
		"source", source,
		"char_id", session.selectedCharacterID,
		"peer_char_ids", onlinePlayerCharacterIDs(peers),
		"trigger", "town_post_transition_player_state_complete",
		"reason", "town scene rebuild invalidated every projected peer actor and nothing republished them",
		"switch", currentTownCopresencePostTransitionResendEnv,
		"mode", currentTownCopresencePostTransitionResendMode())
	s.resendTownCopresencePeerActorsAfterFinishLoading(session, source)
}

// onlinePlayerCharacterIDs 把同桶对端渲染成 id 列表（判读用，稳定排序）。
func onlinePlayerCharacterIDs(peers []onlinePlayerInfo) []uint16 {
	out := make([]uint16, 0, len(peers))
	for i := range peers {
		if peers[i].CharacterID != 0 {
			out = append(out, peers[i].CharacterID)
		}
	}
	return out
}
