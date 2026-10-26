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
//   - **幂等**：同一收包会话、完整运行键、队友绑定代次只补一次；
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
	// Each peer connection owns its resend. A late/reconnected peer must not
	// inherit a different peer's already-consumed room marker.
	resent := make([]uint16, 0, len(members))
	for _, member := range members {
		if member.UserID == 0 || member.UserID == session.selectedCharacterID {
			continue
		}
		identity, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
		peer := identity.session
		if !online || peer == nil || peer == session {
			continue
		}
		peerKey, peerReady := s.currentDungeonPartyRunKey(peer)
		if !peerReady || peerKey != runKey {
			continue
		}
		if !s.claimDungeonPeerActorResend(session, runKey, identity) {
			continue
		}
		if err := s.sendDungeonPartyPeerActorState(
			session,
			peer,
			source+"_peer_actor_resend",
		); err != nil {
			s.releaseDungeonPeerActorResend(session, runKey, identity)
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

// claimDungeonPeerActorResend reserves one resend for this run and peer binding.
func (s *Service) claimDungeonPeerActorResend(session *gameSession, key dungeonPartyRunKey, peer boundGameSessionCharacter) bool {
	if session == nil || !s.boundGameSessionCharacterCurrent(peer) {
		return false
	}
	marker := dungeonPeerActorResendKey{Run: key, Peer: peer}
	session.dungeonPartyRunMu.Lock()
	defer session.dungeonPartyRunMu.Unlock()
	if session.dungeonPeerActorResent == nil {
		session.dungeonPeerActorResent = make(map[dungeonPeerActorResendKey]bool, 4)
	}
	if session.dungeonPeerActorResent[marker] {
		return false
	}
	// Retire old socket references rather than retaining every reconnect for
	// the lifetime of a long raid room. A late old failure cannot erase marker.
	for prior := range session.dungeonPeerActorResent {
		if prior.Peer.character == peer.character && prior.Peer != peer {
			delete(session.dungeonPeerActorResent, prior)
		}
	}
	session.dungeonPeerActorResent[marker] = true
	return true
}

func (s *Service) releaseDungeonPeerActorResend(session *gameSession, key dungeonPartyRunKey, peer boundGameSessionCharacter) {
	if session == nil {
		return
	}
	marker := dungeonPeerActorResendKey{Run: key, Peer: peer}
	session.dungeonPartyRunMu.Lock()
	delete(session.dungeonPeerActorResent, marker)
	session.dungeonPartyRunMu.Unlock()
}

// A character ID alone cannot distinguish a reconnected peer's new actor.
type dungeonPeerActorResendKey struct {
	Run  dungeonPartyRunKey
	Peer boundGameSessionCharacter
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
