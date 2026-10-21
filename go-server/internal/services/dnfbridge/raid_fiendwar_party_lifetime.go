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
		return
	}
	// A member may already have left; a replacement member is never retired.
	for _, member := range current.Members {
		matched := false
		for _, admitted := range frozen.Members {
			matched = matched || member.UserID == admitted.UserID && member.SessionGeneration == admitted.SessionGeneration && member.Slot == admitted.Slot
		}
		if !matched {
			return
		}
	}
	if !manager.RetireSnapshot(current) {
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
