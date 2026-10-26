package party

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

func TestLeaveExpectedPreservesReplacementBetweenSnapshotAndMutation(t *testing.T) {
	m := NewRuntimePartyManager()
	first := m.Create(testRuntimePartyMember(101, 1), alignedcmd.PartyState{MaxMembers: 4})
	m.Join(first.Party.ID, testRuntimePartyMember(202, 1))
	before, _ := m.SnapshotByUser(202, 1)
	next := m.Create(testRuntimePartyMember(202, 1), alignedcmd.PartyState{MaxMembers: 4})
	if result := m.LeaveExpected(202, 1, before.ID); result.OK || result.Reason != "stale_party" {
		t.Fatalf("old raid departure mutated replacement: %+v", result)
	}
	if current, ok := m.SnapshotByUser(202, 1); !ok || current.ID != next.Party.ID {
		t.Fatal("replacement party lost")
	}
	if result := m.LeaveExpected(202, 2, next.Party.ID); result.OK || result.Reason != "stale_session" {
		t.Fatal("stale session retired replacement")
	}
	if result := m.LeaveExpected(202, 1, next.Party.ID); !result.OK || !result.Disbanded {
		t.Fatal("current membership could not leave")
	}
}
