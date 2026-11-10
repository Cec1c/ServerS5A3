package dnfbridge

// currentRaidMemberPhaseClearCount projects the authoritative current-phase
// accepted clear aggregate onto the native member record's one-byte field.
// An objective-cycle reset keeps valid same-phase clear credit. Only the real
// next-phase transition clears MemberClears. Projection does not mutate it.
// Caller supplies a locked/cloned runtime raid state, never client counters.
func currentRaidMemberPhaseClearCount(state runtimeRaidState, characterID uint16) byte {
	b := state.Battle
	if !state.Started || b == nil || b.InstanceID == "" || b.Phase > 1 || characterID == 0 ||
		runtimeRaidMemberIndex(state, characterID) < 0 || b.MemberAccounts[characterID] == "" {
		return 0
	}
	registered := b.MemberClears[characterID]
	if registered > 255 {
		registered = 255
	}
	return byte(registered)
}

// Zero counters need no cached row. This keeps an untouched initial table from
// triggering a redundant roster while still detecting a phase reset to zero.
func currentRaidMemberClearCounters(state runtimeRaidState) map[uint16]byte {
	counts := make(map[uint16]byte)
	for _, member := range state.Members {
		if count := currentRaidMemberPhaseClearCount(state, member.CharID); count != 0 {
			counts[member.CharID] = count
		}
	}
	return counts
}

func sameCurrentRaidMemberClearCounters(previous, next map[uint16]byte) bool {
	if len(previous) != len(next) {
		return false
	}
	for id, count := range next {
		if previous[id] != count {
			return false
		}
	}
	return true
}
