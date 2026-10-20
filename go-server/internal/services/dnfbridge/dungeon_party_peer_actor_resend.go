package dnfbridge

// 本文件处理"副本内队友 actor 的补发时机"。
//
// # 现场与判据（2026-09-25 04:05，频道 10，客户端 trace）
//
// 队长侧（先入场的那一个）：队友 actor 被建出来后 **14~41ms** 就被客户端清掉
//
//	04:05:13.166 town-copresence-dispatch before opcode=2 mode=0 owner=0 key=3
//	04:05:13.166 local-actor-create phase=after object_key=3 result=58A0E080
//	04:05:13.193 opcode=9 owner=0 key=3 kind=0
//	04:05:13.207 town co-presence actor context removed key=3      ← 41ms 后被删
//
// 队员侧（后入场的那一个）同一条 actor 一直留着。
//
// # 为什么"晚到的那个"能留住
//
// 服务端把队友 actor 投影给收包方的时机是**收包方自己**的 `op29 commit`
// （`projectDungeonPartyPeerActors` ← `select_dungeon_after_op29_commit_party_peer_copresence_*`）。
// 而客户端在收到 op29 之后还要走 `finish loading (op37)` 才真正进入新场景。
// 先入场的会话在 op29 commit 那一刻**房间里只有自己**、客户端随后还要重算/重建场景 actor 表；
// 后入场的会话拿到投影时前面那个人的场景早已稳定。
// ⇒ 这份投影落在"场景还没就绪"的窗口里，是有时序依据的解释（不是猜想：两条
// `game-dungeon-party-peer-actor-send` 的 source 都是 `select_dungeon_after_op29_commit_*`，
// 而 finish-loading 的 `game-main-finish-loading-state-send` 在 packet_log 里排在其后）。
//
// # 修法
//
// 在**收包方自己的 finish-loading（op37 主状态 + op30 + post-state 全部发完）之后**，
// 对同一场副本实例内的其它成员**补发一次**队友 actor 投影（op2 mode0/mode1 + op9 记录，
// owner 保持副本上下文 0），并记 `game-dungeon-party-peer-actor-resend`。
//
//   - **只补发队友，绝不重发本地自身**：`actor != receiver` 才发（复用
//     `sendDungeonPartyPeerActorState` 的既有约束）；
//   - **幂等**：同一 (收包会话, 副本实例) 只补一次，换场/换实例后重新允许；
//   - 换房间（op45）也会把 finish-loading 门闩复位（`currentFinishLoadingStateSent=false`），
//     所以每次过图都会在新房间重新补一轮 —— 这正是"队员卡在等待队员到达"需要的那一轮。

func (s *Service) resendDungeonPartyPeerActorsAfterFinishLoading(session *gameSession, source string) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return
	}
	// 只在**副本场景**里补发：城镇的 finish-loading 有自己的 actor 重建路径，
	// 这里的补发只针对"队友 actor 投影落在收包方场景还没就绪的窗口里"那条。
	if !s.sessionHasActiveDungeonRuntime(session) {
		return
	}
	state := runtimePartyStateSnapshot(session)
	members := runtimePartyMembers(state)
	if state.PartyID <= 0 || len(members) < 2 {
		return
	}
	runKey, ready := s.currentDungeonPartyRunKey(session)
	if !ready {
		return
	}
	// 幂等键 = (收包会话, 副本实例号)。换房间不改 party id，但 runKey 里的 room 会变，
	// 所以幂等键带上房间坐标：同房间只补一次，进入新房间允许再补一次。
	if !s.claimDungeonPeerActorResend(session, runKey) {
		return
	}
	resent := make([]uint16, 0, len(members))
	for _, member := range members {
		if member.UserID == 0 || member.UserID == session.selectedCharacterID {
			continue
		}
		peer, online := s.onlineGameSession(member.UserID)
		if !online || peer == nil || peer == session {
			continue
		}
		peerKey, peerReady := s.currentDungeonPartyRunKey(peer)
		if !peerReady || peerKey != runKey {
			continue
		}
		if err := s.sendDungeonPartyPeerActorState(
			session,
			peer,
			source+"_peer_actor_resend",
		); err != nil {
			s.releaseDungeonPeerActorResend(session, runKey)
			s.logGameEvent(session, "game-dungeon-party-peer-actor-resend-failed",
				"source", source,
				"char_id", session.selectedCharacterID,
				"peer_char_id", member.UserID,
				"error", err)
			return
		}
		resent = append(resent, member.UserID)
	}
	if len(resent) == 0 {
		if dungeonPartyPeerActorLandingResendEnabled() {
			// 一个都没补（对端还没有运行键）⇒ **退还名额**。名额在循环之前就被占掉，
			// 不退的话这名收包方在本房间里再也不会被补发 —— 现场指纹就是
			// `char_id=1`（先落位的那一方）的 resend 全日志 0 条。
			// 退还之后，"后落位一方落地"那条路（dungeon_party_peer_actor_landing_resend.go）
			// 才有名额把队友 actor 再推给它一次。
			s.releaseDungeonPeerActorResend(session, runKey)
		}
		return
	}
	// 队友 actor 重新建出来之后，紧接着把**已上报坐标**补一次，避免新 actor 停在默认点
	// （形状与城镇 town_copresence_move_0x0016 一致；受 DNFBRIDGE_PARTY_DUNGEON_PEER_POSITION 控制）。
	positionSent := s.broadcastDungeonPartyPeerPosition(session, source+"_peer_actor_resend_position")
	s.logGameEvent(session, "game-dungeon-party-peer-actor-resend",
		"source", source,
		"char_id", session.selectedCharacterID,
		"party_id", state.PartyID,
		"resent_peer_char_ids", resent,
		"position_records", positionSent,
		"run_key_room", dungeonRoomCoordinate(runKey.RoomX, runKey.RoomY),
		"run_key_map_id", runKey.MapID,
		"trigger", "receiver_finish_loading",
		"reason", "peer actor projection landed before the receiving client finished its scene rebuild")
}

// claimDungeonPeerActorResend 返回 true = 这次调用占用了补发名额（同一 conn+房间只补一次）。
func (s *Service) claimDungeonPeerActorResend(session *gameSession, key dungeonPartyRunKey) bool {
	if session == nil {
		return false
	}
	marker := dungeonPeerActorResendKey{RoomX: key.RoomX, RoomY: key.RoomY, MapID: key.MapID, InstanceID: key.PartyID}
	session.dungeonPartyRunMu.Lock()
	defer session.dungeonPartyRunMu.Unlock()
	if session.dungeonPeerActorResent == nil {
		session.dungeonPeerActorResent = make(map[dungeonPeerActorResendKey]bool, 4)
	}
	if session.dungeonPeerActorResent[marker] {
		return false
	}
	session.dungeonPeerActorResent[marker] = true
	return true
}

func (s *Service) releaseDungeonPeerActorResend(session *gameSession, key dungeonPartyRunKey) {
	if session == nil {
		return
	}
	marker := dungeonPeerActorResendKey{RoomX: key.RoomX, RoomY: key.RoomY, MapID: key.MapID, InstanceID: key.PartyID}
	session.dungeonPartyRunMu.Lock()
	delete(session.dungeonPeerActorResent, marker)
	session.dungeonPartyRunMu.Unlock()
}

// dungeonPeerActorResendKey 是补发的幂等键：同一房间只补一次，进入新房间重新允许。
type dungeonPeerActorResendKey struct {
	RoomX      int64
	RoomY      int64
	MapID      int64
	InstanceID uint16
}

// sessionHasActiveDungeonRuntime 报告本会话当前是否持有一份活跃的副本运行时。
func (s *Service) sessionHasActiveDungeonRuntime(session *gameSession) bool {
	if session == nil {
		return false
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	return session.dungeon.runtime != nil && session.dungeon.runtime.Session != nil
}
