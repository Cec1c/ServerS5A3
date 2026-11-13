package dnfbridge

// Captured at the real return commit, while the exact runtime still owns the
// session. Room seeds change during an ordinary run; they are not run IDs.
type currentDungeonPartyRetirement struct {
	instance *dungeonPartyInstance
	token    uint64
	key      dungeonPartyRunKey
	keyValid bool
}

func captureCurrentDungeonPartyRetirementLocked(session *gameSession, runtime *runtimeDungeonState) {
	if session == nil || runtime == nil || session.dungeon.runtime != runtime || runtime.partyInstance == nil {
		return
	}
	session.dungeonPartyRunMu.Lock()
	defer session.dungeonPartyRunMu.Unlock()
	session.dungeon.partyRetirement = currentDungeonPartyRetirement{runtime.partyInstance, session.dungeon.runToken, session.dungeonPartyRun, session.dungeonPartyRunValid}
}

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
		proof := session.dungeon.partyRetirement
		committedOwner := session.dungeon.runtime == nil && proof.instance == instance && proof.token == session.dungeon.runToken && proof.keyValid && proof.key == current
		// Room coordinates legitimately advance inside the same instance.
		current.RoomX, current.RoomY, current.MapID = 0, 0, 0
		retired.RoomX, retired.RoomY, retired.MapID = 0, 0, 0
		// Navigation also replaces the room seed. A frozen raid admission
		// identifies the run independently of that seed; the instance retains
		// its entry-room key while the session records the latest room.
		// Ordinary runs still need the seed guard, as they lack this identity.
		if committedOwner || (current.RaidKey != 0 && current.RaidKey == retired.RaidKey &&
			current.RaidInstanceID != "" && current.RaidInstanceID == retired.RaidInstanceID &&
			current.RaidRunID != "" && current.RaidRunID == retired.RaidRunID) {
			current.Seed, retired.Seed = 0, 0
		}
		if current != retired {
			return false
		}
	}
	return apply()
}
