package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
)

// Pin membership before repository work and recheck after acquiring the wire.
// A leave can finish while names/equipment or the transport callback are being
// prepared. Its removal must not be followed by an older roster publication.
// HP/MP changes are deliberately excluded: they do not retire a roster.
func (s *Service) currentPartyProjectionMembershipGuard(actor boundGameSessionCharacter, expected *alignedcmd.PartyState) func() bool {
	manager := s.runtimePartyManagerForService()
	before, managed := manager.SnapshotByUser(actor.character, 0)
	cached := runtimePartyStateSnapshot(actor.session)
	if expected != nil && expected.PartyID <= 0 && managed {
		// An explicit removal cannot retire a lobby that still exists, or a
		// replacement membership created before this delayed sender started.
		return func() bool { return false }
	}
	if expected != nil && expected.PartyID > 0 {
		current := cached
		if managed {
			current = before.StateFor(actor.character)
		}
		if !sameCurrentPartyProjectionState(*expected, current) {
			return func() bool { return false }
		}
	}
	return func() bool {
		after, found := manager.SnapshotByUser(actor.character, 0)
		if managed || found {
			return managed == found && sameCurrentPartyProjectionMembership(before, after)
		}
		// Legacy/session-only projections still exist. Guard their exact roster
		// too, without creating a manager membership as a side effect.
		return sameCurrentPartyProjectionState(cached, runtimePartyStateSnapshot(actor.session))
	}
}

// Bind, projection and manager locks follow the cleanup order. The manager
// callback only writes the cache; it never sends packets or calls the manager.
func (s *Service) storeCurrentPartyProjection(identity boundGameSessionCharacter, state alignedcmd.PartyState) bool {
	if s == nil || identity.session == nil || identity.character == 0 {
		return false
	}
	manager := s.runtimePartyManagerForService()
	expected, managed := manager.SnapshotByUser(identity.character, 0)
	if state.PartyID > 0 && (!managed || !sameCurrentPartyProjectionState(state, expected.StateFor(identity.character))) {
		return false
	}
	if state.PartyID > 0 {
		state = expected.StateFor(identity.character)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	primary := s.gameSessions[identity.character]
	registeredAuxiliary := state.PartyID > 0 && primary != nil && primary.selectedCharacterID == identity.character &&
		identity.session.connID != "" && s.liveGameSessions[identity.session.connID] == identity.session
	if (primary != identity.session && !registeredAuxiliary) || identity.session.selectedCharacterID != identity.character || identity.session.characterGeneration != identity.generation {
		return false
	}
	identity.session.party.mu.Lock()
	defer identity.session.party.mu.Unlock()
	apply := func() error {
		identity.session.party.state = cloneRuntimePartyState(state)
		return nil
	}
	if state.PartyID <= 0 {
		return manager.WithSoloMember(identity.character, apply) == nil
	}
	return manager.WithSnapshot(expected, apply) == nil
}

// Registered auxiliary transports share the character's manager membership,
// but a retired or rebound socket must not inherit a new projection.
func (s *Service) repairCurrentPartyProjection(session *gameSession, state alignedcmd.PartyState) bool {
	s.mu.Lock()
	identity := boundGameSessionCharacter{session: session, character: session.selectedCharacterID, generation: session.characterGeneration}
	s.mu.Unlock()
	return s.storeCurrentPartyProjection(identity, state)
}

func sameCurrentPartyProjectionMembership(a, b party.RuntimePartySnapshot) bool {
	if a.ID != b.ID || a.Leader != b.Leader || len(a.Members) != len(b.Members) {
		return false
	}
	for _, left := range a.Members {
		found := false
		for _, right := range b.Members {
			if left.UserID == right.UserID && left.SessionGeneration == right.SessionGeneration && left.Slot == right.Slot {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sameCurrentPartyProjectionState(a, b alignedcmd.PartyState) bool {
	if a.PartyID != b.PartyID || a.UserID != b.UserID {
		return false
	}
	left, right := runtimePartyMembers(a), runtimePartyMembers(b)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].UserID != right[index].UserID || left[index].Slot != right[index].Slot {
			return false
		}
	}
	return true
}
