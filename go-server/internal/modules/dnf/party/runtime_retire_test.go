package party

import (
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"testing"
)

func TestRetireSnapshotCannotRemoveChangedOrReplacementParty(t *testing.T) {
	m := NewRuntimePartyManager()
	first := m.Create(testRuntimePartyMember(101, 1), alignedcmd.PartyState{MaxMembers: 4})
	joined := m.Join(first.Party.ID, testRuntimePartyMember(202, 1))
	if m.RetireSnapshot(first.Party) {
		t.Fatal("stale singleton removed joined peer")
	}
	if !m.RetireSnapshot(joined.Party) {
		t.Fatal("exact party did not retire")
	}
	for _, id := range []uint16{101, 202} {
		if _, ok := m.SnapshotByUser(id, 0); ok {
			t.Fatal("retired party still addressable")
		}
	}
	next := m.Create(testRuntimePartyMember(101, 1), alignedcmd.PartyState{MaxMembers: 4})
	if m.RetireSnapshot(joined.Party) {
		t.Fatal("old snapshot retired replacement")
	}
	if got, ok := m.SnapshotByUser(101, 1); !ok || got.ID != next.Party.ID {
		t.Fatal("replacement lost")
	}
}
