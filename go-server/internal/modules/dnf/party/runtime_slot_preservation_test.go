package party

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// Delegate leader must not renumber the fixed runtime slots. The new leader keeps
// the slot it already occupied; the retired leader keeps slot zero as an ordinary
// member. R52's rebuildLocked/add path put the new leader back at slot zero and
// shifted everyone else, which is the slot reorder seen by op9/op153.
func TestRuntimePartyManagerTransferLeaderPreservesAssignedSlots(t *testing.T) {
	manager := NewRuntimePartyManager()
	created := manager.Create(testRuntimePartyMember(101, 1), alignedcmd.PartyState{MaxMembers: 4})
	for _, userID := range []uint16{202, 303} {
		if result := manager.Join(created.Party.ID, testRuntimePartyMember(userID, 1)); !result.OK {
			t.Fatalf("join %d = %+v", userID, result)
		}
	}

	transferred := manager.TransferLeader(101, 1, 303, 1)
	if !transferred.OK || !transferred.LeaderMoved || transferred.Retired == nil {
		t.Fatalf("transfer result = %+v", transferred)
	}
	if transferred.Party.Leader != 303 {
		t.Fatalf("leader = %d, want 303", transferred.Party.Leader)
	}
	for userID, want := range map[uint16]byte{101: 0, 202: 1, 303: 2} {
		if got := runtimePartySlot(transferred.Party, userID); got != want {
			t.Fatalf("member %d slot=%d want preserved slot %d (%+v)", userID, got, want, transferred.Party.Members)
		}
	}

	state := transferred.Party.StateFor(303)
	if state.UserID != 303 || !state.IsLeader {
		t.Fatalf("leader semantics lost: %+v", state)
	}
	members := runtimePartyMemberStatesBySlot(state)
	if len(members) != 3 {
		t.Fatalf("state members = %+v", members)
	}
	for slot, want := range []uint16{101, 202, 303} {
		if members[slot].UserID != want {
			t.Fatalf("state slot %d uid=%d want %d (leader-first reorder still present: %+v)", slot, members[slot].UserID, want, members)
		}
	}
}

func TestRuntimePartyManagerPreservesSlotHoleThroughLeaveAndRejoin(t *testing.T) {
	manager := NewRuntimePartyManager()
	created := manager.Create(testRuntimePartyMember(101, 1), alignedcmd.PartyState{MaxMembers: 4})
	for _, userID := range []uint16{202, 303, 404} {
		if result := manager.Join(created.Party.ID, testRuntimePartyMember(userID, 1)); !result.OK {
			t.Fatalf("join %d = %+v", userID, result)
		}
	}
	if transferred := manager.TransferLeader(101, 1, 303, 1); !transferred.OK {
		t.Fatalf("transfer = %+v", transferred)
	}
	if left := manager.Leave(202, 1); !left.OK || left.LeaderMoved {
		t.Fatalf("nonleader leave = %+v", left)
	}

	snapshot, found := manager.SnapshotByUser(303, 1)
	if !found || snapshot.Leader != 303 {
		t.Fatalf("leader snapshot = %+v found=%t", snapshot, found)
	}
	for userID, want := range map[uint16]byte{101: 0, 303: 2, 404: 3} {
		if got := runtimePartySlot(snapshot, userID); got != want {
			t.Fatalf("member %d slot=%d want %d after hole (%+v)", userID, got, want, snapshot.Members)
		}
	}
	state := snapshot.StateFor(303)
	if len(state.Members) != 3 || state.Members[0].Slot != 0 || state.Members[1].Slot != 2 || state.Members[2].Slot != 3 {
		t.Fatalf("StateFor lost the hole: %+v", state.Members)
	}

	rejoined := manager.Join(snapshot.ID, testRuntimePartyMember(505, 1))
	if !rejoined.OK {
		t.Fatalf("rejoin = %+v", rejoined)
	}
	if got := runtimePartySlot(rejoined.Party, 505); got != 1 {
		t.Fatalf("rejoined member slot=%d want the released hole 1 (%+v)", got, rejoined.Party.Members)
	}
	if got := runtimePartySlot(rejoined.Party, 303); got != 2 {
		t.Fatalf("leader moved after rejoin: slot=%d want 2 (%+v)", got, rejoined.Party.Members)
	}
}

func TestRuntimePartyManagerLeaderLeaveKeepsSurvivorSlotsAndRejoinFillsHole(t *testing.T) {
	manager := NewRuntimePartyManager()
	created := manager.Create(testRuntimePartyMember(101, 1), alignedcmd.PartyState{MaxMembers: 4})
	if joined := manager.Join(created.Party.ID, testRuntimePartyMember(202, 1)); !joined.OK {
		t.Fatalf("join = %+v", joined)
	}
	left := manager.Leave(101, 1)
	if !left.OK || !left.LeaderMoved || left.Party.Leader != 202 {
		t.Fatalf("leader leave = %+v", left)
	}
	if got := runtimePartySlot(left.Party, 202); got != 1 {
		t.Fatalf("successor slot=%d want preserved 1 (%+v)", got, left.Party.Members)
	}
	rejoined := manager.Join(left.Party.ID, testRuntimePartyMember(303, 1))
	if !rejoined.OK || runtimePartySlot(rejoined.Party, 303) != 0 {
		t.Fatalf("rejoin after leader leave = %+v", rejoined)
	}
	if got := runtimePartySlot(rejoined.Party, 202); got != 1 {
		t.Fatalf("leader moved on rejoin: slot=%d want 1", got)
	}
}

func TestRuntimePartyManagerRepositionKeepsNonzeroLeaderAndAllowsEmptySlotZero(t *testing.T) {
	manager := NewRuntimePartyManager()
	created := manager.Create(testRuntimePartyMember(101, 1), alignedcmd.PartyState{MaxMembers: 4})
	for _, userID := range []uint16{202, 303} {
		if result := manager.Join(created.Party.ID, testRuntimePartyMember(userID, 1)); !result.OK {
			t.Fatalf("join %d = %+v", userID, result)
		}
	}
	if transferred := manager.TransferLeader(101, 1, 303, 1); !transferred.OK {
		t.Fatalf("transfer = %+v", transferred)
	}
	if moved := manager.Reposition(303, 1, 1, 0); !moved.OK {
		t.Fatalf("move nonleader into empty slot zero = %+v", moved)
	}
	snapshot, _ := manager.SnapshotByUser(303, 1)
	if snapshot.Leader != 303 || runtimePartySlot(snapshot, 303) != 2 || runtimePartySlot(snapshot, 202) != 0 {
		t.Fatalf("reposition changed leader or fixed slots: %+v", snapshot.Members)
	}
	if rejected := manager.Reposition(303, 1, 2, 1); rejected.Reason != "invalid_source_slot" {
		t.Fatalf("leader reposition = %+v", rejected)
	}
}

func runtimePartyMemberStatesBySlot(state alignedcmd.PartyState) []alignedcmd.PartyMemberState {
	return append([]alignedcmd.PartyMemberState(nil), state.Members...)
}
