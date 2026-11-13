package dnfbridge

// Eligibility is frozen at clear; visibility follows the viewer's current
// squad. Personal completion is handled separately from this shared roster.
func (s *Service) currentRaidPartyRewardDisplayMembers(session *gameSession, state runtimeRaidState) map[uint16]bool {
	visible := map[uint16]bool{}
	if session == nil || state.Battle == nil {
		return visible
	}
	owner := session.selectedCharacterID
	index := runtimeRaidMemberIndex(state, owner)
	if index < 0 {
		return visible
	}
	visible[owner] = true
	group := state.Members[index].GroupIndex
	if state.Battle.Mode == 3 {
		// Fiend War retires its temporary encounter party at clear.
		for _, member := range state.Members {
			if member.GroupIndex == group {
				visible[member.CharID] = true
			}
		}
		return visible
	}
	party, found := s.runtimePartyManagerForService().SnapshotByUser(owner, 0)
	if !found {
		return visible
	}
	for _, peer := range party.Members {
		if i := runtimeRaidMemberIndex(state, peer.UserID); i >= 0 && state.Members[i].GroupIndex == group {
			visible[peer.UserID] = true
		}
	}
	return visible
}
