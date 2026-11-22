package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
)

// A FiendWar encounter party ends on confirmed return. Raid membership and
// personal numbers 1..8 remain untouched. Ordinary dungeon parties keep their
// existing completion policy. The frozen snapshot excludes any later party.
func (s *Service) retireCurrentFiendWarEncounterParty(p *gameSession, r *runtimeDungeonState) bool {
	if s == nil || p == nil || r == nil || r.raidEntry == nil || r.raidEntry.ChannelType != 42 {
		return false
	}
	e := r.raidEntry
	if e.fiendWarParty == nil {
		return true
	}
	// 遭遇战队伍是**临时战斗队伍**：只要还有队友待在同一个 run 的副本里，
	// 就保留它，等最后一个人出来时再退役。
	//
	// 原实现是「谁先回城谁触发退役」。两人一起打时，先出来那位会把队伍拆掉，
	// 而此时队友还在副本里 —— 他收到的是一条「队伍已解散」
	// （op9 kind=3 / party_id=0）。客户端在副本态下不认这条帧，退出后队伍框
	// 就残留了，也就是现场说的「战斗结束以后队伍不会解散」。
	// 线上日志（packet_log.txt，UTC）证据：
	//
	//	02:59:23.541  game-fiendwar-encounter-party-retired   party_id=2
	//	02:59:23.572  game-dungeon-party-instance-member-left char_id=5 member_count=0
	//
	// 只差 31ms：退役发生时 char 5 还在副本里。
	if s.currentFiendWarPartyStillInDungeon(p, *e.fiendWarParty, e.RunID) {
		return true
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(p)
	if !ok || !s.boundGameSessionCharacterCurrent(identity) || !dungeonRuntimeOwnsCharacter(r, identity.character) {
		return true
	}
	s.retireCurrentFiendWarPartySnapshot(p, *e.fiendWarParty, e.RunID)
	return true
}

// Also used when a newly assembled party never commits its encounter entry.
func (s *Service) retireCurrentFiendWarPartySnapshot(p *gameSession, frozen party.RuntimePartySnapshot, runID string) {
	manager := s.runtimePartyManagerForService()
	current, found := manager.SnapshotByID(frozen.ID)
	if !found {
		s.logGameEvent(p, "game-fiendwar-encounter-party-retire-skipped",
			"party_id", frozen.ID, "run_id", runID, "reason", "party_already_replaced")
		return
	}
	// A member may already have left; a replacement member is never retired.
	for _, member := range current.Members {
		matched := false
		for _, admitted := range frozen.Members {
			matched = matched || member.UserID == admitted.UserID && member.SessionGeneration == admitted.SessionGeneration && member.Slot == admitted.Slot
		}
		if !matched {
			s.logGameEvent(p, "game-fiendwar-encounter-party-retire-skipped",
				"party_id", current.ID, "run_id", runID, "reason", "roster_changed",
				"member_char_id", member.UserID)
			return
		}
	}
	if !manager.RetireSnapshot(current) {
		s.logGameEvent(p, "game-fiendwar-encounter-party-retire-skipped",
			"party_id", current.ID, "run_id", runID, "reason", "retire_snapshot_rejected")
		return
	}
	s.closeRuntimePartyUDPRelay(int(current.ID))
	for _, member := range current.Members {
		peer, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !online || peer.generation != member.SessionGeneration {
			continue
		}
		// Do not erase a new party created after this atomic retirement.
		if _, replaced := manager.SnapshotByUser(member.UserID, 0); replaced {
			continue
		}
		s.clearRuntimePartyProjection(member.UserID, peer.generation)
		s.markFiendWarPartyClearOnTownSceneReady(peer.session)
		if err := s.clearDisbandedDungeonPartyRecords(peer.session, current.StateFor(member.UserID).Members, current.Leader, false, "fiendwar_encounter_party_end"); err != nil {
			s.logGameEvent(peer.session, "game-fiendwar-encounter-party-record-clear-failed", "party_id", current.ID, "run_id", runID, "error", err)
		}
		if err := s.sendRuntimePartyMemberRemoved(peer.session, member.UserID, alignedcmd.PartyState{}); err != nil {
			s.logGameEvent(peer.session, "game-fiendwar-encounter-party-clear-failed", "party_id", current.ID, "run_id", runID, "error", err)
		}
	}
	s.logGameEvent(p, "game-fiendwar-encounter-party-retired", "party_id", current.ID, "run_id", runID, "raid_numbers_preserved", true)
}

// currentFiendWarPartyStillInDungeon reports whether anyone other than self is
// still inside the exact encounter run. A temporary combat party must survive
// until its last member leaves: retiring it while a peer is still in the
// dungeon sends that peer an op9 remove-party the client will not apply in the
// in-dungeon state, leaving a stale party frame after it returns.
//
// Offline members do not keep the party alive — their runtime is already gone
// and the existing disconnect handling owns that seat.
func (s *Service) currentFiendWarPartyStillInDungeon(self *gameSession, frozen party.RuntimePartySnapshot, runID string) bool {
	if s == nil || self == nil || runID == "" {
		return false
	}
	for _, member := range frozen.Members {
		if member.UserID == self.selectedCharacterID {
			continue
		}
		peer, online := s.onlineGameSession(member.UserID)
		if !online || peer == nil {
			continue
		}
		peer.dungeon.mu.Lock()
		r := peer.dungeon.runtime
		still := r != nil && r.raidEntry != nil && r.raidEntry.RunID == runID
		peer.dungeon.mu.Unlock()
		if still {
			return true
		}
	}
	return false
}

// markFiendWarPartyClearOnTownSceneReady 记录"这个成员在队伍退役时仍处于回城过场"。
// 回城提交函数在调用退役前已经把 dungeon.runtime 摘掉、但 townActorOwnerChannel
// 还是副本上下文 0（见 dungeon_return_town.go 的提交点注释）。此时发出的 op9 kind3
// 只会命中"副本上下文"的那条记录，客户端城镇上下文里"自己"那条队伍记录不会被清，
// 于是队伍框残留。标记后由 ensureCurrentConfirmedDungeonReturnPlayerState 在城镇
// owner 落定、玩家状态重建完成后补发一次。
func (s *Service) markFiendWarPartyClearOnTownSceneReady(session *gameSession) {
	if session == nil {
		return
	}
	session.dungeon.mu.Lock()
	if session.confirmedDungeonReturnStatePending {
		session.fiendWarPartyClearOnTownSceneReady = true
	}
	session.dungeon.mu.Unlock()
}

// flushFiendWarPartyClearOnTownSceneReady 在城镇场景重建完成后补一条"自己"的
// op9 kind3（owner = 当前连接频道）。只有退役时确实还在过场的成员才会走到这里；
// 若这期间已经加入了新队伍，则不动新队伍。
func (s *Service) flushFiendWarPartyClearOnTownSceneReady(session *gameSession, source string) {
	if session == nil {
		return
	}
	session.dungeon.mu.Lock()
	pending := session.fiendWarPartyClearOnTownSceneReady
	session.fiendWarPartyClearOnTownSceneReady = false
	session.dungeon.mu.Unlock()
	if !pending {
		return
	}
	if state := s.runtimePartyStateForSession(session); state.PartyID > 0 {
		return
	}
	if err := s.sendRuntimePartyDeparture(session, alignedcmd.PartyState{}); err != nil {
		s.logGameEvent(session, "game-fiendwar-party-clear-town-ready-failed",
			"source", source, "char_id", session.selectedCharacterID, "error", err)
	}
}
