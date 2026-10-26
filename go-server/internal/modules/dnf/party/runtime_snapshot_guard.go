package party

import "errors"

var ErrRuntimePartyChanged = errors.New("runtime party membership changed")

func (m *RuntimePartyManager) WithSoloMember(userID uint16, apply func() error) error {
	if m == nil || userID == 0 || apply == nil {
		return ErrRuntimePartyChanged
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.byUser[userID] != 0 {
		return ErrRuntimePartyChanged
	}
	return apply()
}

// WithSnapshot holds membership/leader/slot identity through a short commit.
// apply must not call the party manager or perform network writes.
func (m *RuntimePartyManager) WithSnapshot(expected RuntimePartySnapshot, apply func() error) error {
	if m == nil || expected.ID == 0 || apply == nil {
		return ErrRuntimePartyChanged
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.parties[expected.ID]
	if p == nil || p.leader != expected.Leader || p.count() != len(expected.Members) {
		return ErrRuntimePartyChanged
	}
	seen := map[uint16]bool{}
	for _, want := range expected.Members {
		got := p.member(want.UserID)
		if seen[want.UserID] || got == nil || got.SessionGeneration != want.SessionGeneration || got.Slot != want.Slot || m.byUser[want.UserID] != expected.ID {
			return ErrRuntimePartyChanged
		}
		seen[want.UserID] = true
	}
	return apply()
}
