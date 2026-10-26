package party

// RetireSnapshot removes one exact membership generation atomically. It does
// not transfer leadership or affect a replacement party joined meanwhile.
func (m *RuntimePartyManager) RetireSnapshot(expected RuntimePartySnapshot) bool {
	if m == nil || expected.ID == 0 || len(expected.Members) == 0 {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.parties[expected.ID]
	if p == nil || p.leader != expected.Leader || p.count() != len(expected.Members) {
		return false
	}
	seen := map[uint16]bool{}
	for _, want := range expected.Members {
		got := p.member(want.UserID)
		if seen[want.UserID] || got == nil || got.SessionGeneration != want.SessionGeneration || got.Slot != want.Slot || m.byUser[want.UserID] != expected.ID {
			return false
		}
		seen[want.UserID] = true
	}
	for _, member := range expected.Members {
		delete(m.byUser, member.UserID)
		m.clearInvitesForLocked(member.UserID, member.SessionGeneration)
	}
	delete(m.parties, expected.ID)
	return true
}
