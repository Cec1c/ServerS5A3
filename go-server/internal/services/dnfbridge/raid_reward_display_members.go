package dnfbridge

// Reward entitlement is frozen at completion, but a party card belongs on
// another player's screen only while both are in the current party and raid
// group. Never use a session's potentially stale party projection here.
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
		// Fiend War encounter parties are retired on clear. Its existing raid
		// roster display is independent of that temporary party lifecycle.
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
