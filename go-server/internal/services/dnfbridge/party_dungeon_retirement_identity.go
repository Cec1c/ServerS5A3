package dnfbridge

// The same lobby can start another dungeon without changing PartyID. A return
// callback runs after releasing its old runtime, so membership alone cannot
// distinguish that callback from a return belonging to the new run.
// Callers hold no dungeon lock. apply is a manager-only mutation, with no I/O.
func (s *Service) withCurrentDungeonPartyRetirement(identity boundGameSessionCharacter, instance *dungeonPartyInstance, apply func() bool) bool {
	if s == nil || identity.session == nil || instance == nil || apply == nil {
		return false
	}
	session := identity.session
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	if current := session.dungeon.runtime; current != nil && current.partyInstance != instance {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gameSessions[identity.character] != session || session.selectedCharacterID != identity.character || session.characterGeneration != identity.generation {
		return false
	}
	session.dungeonPartyRunMu.Lock()
	defer session.dungeonPartyRunMu.Unlock()
	if session.dungeonPartyRunValid {
		current, retired := session.dungeonPartyRun, instance.key
		// Room coordinates legitimately advance inside the same instance.
		current.RoomX, current.RoomY, current.MapID = 0, 0, 0
		retired.RoomX, retired.RoomY, retired.MapID = 0, 0, 0
		if current != retired {
			return false
		}
	}
	return apply()
}
