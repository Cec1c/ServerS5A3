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
