package party

// LeaveExpected checks the party generation and session in the same critical
// section as the mutation. A snapshot check followed by Leave would allow a
// concurrent team replacement to remove the member from the new party.
func (m *RuntimePartyManager) LeaveExpected(userID uint16, generation uint64, partyID uint16) RuntimePartyResult {
	if m == nil || userID == 0 || generation == 0 || partyID == 0 {
		return runtimePartyFailure("invalid_leave")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, member := m.partyMemberLocked(userID)
	if p == nil || member == nil {
		return runtimePartyFailure("not_in_party")
	}
	if p.id != partyID {
		return runtimePartyFailure("stale_party")
	}
	return m.leaveLocked(userID, generation, false)
}
