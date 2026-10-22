package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const currentRaidTableReturnSource = "current_exe_raid_battle_table_op72_active_giveup"

// Caller holds dungeon.mu. v153's Anton 018774EF and Luke 0110DC13 standby
// buttons both emit 72/{1,2}. Keep completed runs on their result/reward route; an active
// return is neither completion nor permission to retry or enter another map.
func (s *Service) currentRaidTableActiveReturnReady(session *gameSession, runtime *runtimeDungeonState) bool {
	if s.options.currentClientVersion != "official-24.9" || session == nil ||
		runtime == nil || runtime.Session == nil || runtime.raidEntry == nil ||
		session.dungeon.runtime != runtime || !s.currentRaidAuthenticated(session) ||
		!dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) ||
		runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive ||
		runtime.Session.Snapshot().Run.DungeonID != runtime.Dungeon.ID {
		return false
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return false
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	// raidMu -> s.mu matches existing presence ownership. Snapshot acquisition
	// above releases s.mu before raidMu, so no inverse nesting is introduced.
	if !s.boundGameSessionCharacterCurrent(identity) {
		return false
	}
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if state == nil || state.Battle == nil {
		return false
	}
	b := state.Battle
	if b.Mode == 2 {
		if b.Single || b.ChannelType != 32 || b.Anton == nil || !b.Anton.IsLuke() ||
			s.validateCurrentRaidDungeonEntryBindingLocked(session, runtime) != nil ||
			!(&raid.LukeProgression{AntonProgression: b.Anton}).HasEnteredRun(runtime.raidEntry.RunID, uint32(runtime.Dungeon.ID)) {
			return false
		}
	} else if b.Mode != 1 || s.validateCurrentRaidDungeonEntryLocked(session, runtime) != nil {
		return false
	}
	e := runtime.raidEntry
	p, exists := b.Participation[session.selectedCharacterID]
	if !exists || p.Session != session || p.Runtime != runtime || p.Generation != identity.generation ||
		p.RunID != e.RunID || p.DungeonID != uint32(runtime.Dungeon.ID) || p.Phase != e.Phase || p.Attempt != e.Attempt {
		return false
	}
	if _, accepted := b.ClearedRuns[e.RunID]; accepted {
		return false
	}
	return true
}
