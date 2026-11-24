package dnfbridge

import (
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// 2026-11-20 90CN raid 1507329 at the phase-1 start (19:52:36): 15 and 9 had
// left group 1's party and kept raw group 1 as solos; party {12,67,14,204} was
// then materialized in group 1. The frozen snapshot became 1:6,2:3,3:4,4:4.
func TestStartedRaidPartyMovesHiddenSoloSeatsOutOfItsGroup(t *testing.T) {
	seat := func(id uint16, group, slot byte, solo bool) runtimeRaidMemberState {
		return runtimeRaidMemberState{CharID: id, GroupIndex: group, SlotOrder: slot, OrdinaryPartySolo: solo}
	}
	raid := runtimeRaidState{Started: true, CurrentLayout: true, Members: []runtimeRaidMemberState{
		seat(12, 1, 0, false), seat(67, 1, 1, true), seat(14, 1, 2, true), seat(15, 1, 3, true), seat(9, 1, 1, true),
		seat(48, 2, 0, false), seat(50, 2, 1, false), seat(27, 2, 2, false),
		seat(26, 3, 0, false), seat(114, 3, 1, false), seat(16, 3, 2, false), seat(133, 3, 3, false),
		seat(5, 4, 0, false), seat(32, 4, 1, false), seat(86, 4, 2, false), seat(24, 4, 3, false),
		seat(204, 4, 0, true),
	}}
	party := alignedcmd.PartyState{PartyID: 20, UserID: 12, Members: []alignedcmd.PartyMemberState{
		{UserID: 12, Slot: 0}, {UserID: 67, Slot: 1}, {UserID: 14, Slot: 2}, {UserID: 204, Slot: 3},
	}}
	candidate, changed := planRuntimeRaidGroupSynchronization(raid, party)
	if !changed {
		t.Fatal("party was not materialized")
	}
	byID := map[uint16]runtimeRaidMemberState{}
	for _, member := range candidate {
		byID[member.CharID] = member
	}
	for _, id := range []uint16{12, 67, 14, 204} {
		if byID[id].GroupIndex != 1 {
			t.Fatal("party left its anchor group", byID[id])
		}
	}
	for _, id := range []uint16{15, 9} {
		if byID[id].GroupIndex == 1 || !byID[id].OrdinaryPartySolo {
			t.Fatal("hidden solo seat still shares the full party group", byID[id])
		}
	}
	if runtimeRaidHasOversizedSeatGroup(raid, candidate) {
		t.Fatal("phase start would still freeze a group above four")
	}
	frozen := map[byte]int{}
	for _, member := range candidate {
		frozen[member.GroupIndex]++
	}
	if frozen[1] != 4 || frozen[2] != 4 || frozen[3] != 4 || frozen[4] != 4 || frozen[5] != 1 {
		t.Fatal("unexpected seat layout", frozen)
	}
}

func TestRelocateOverflowSolosKeepsPinnedReservingAndActiveRows(t *testing.T) {
	raid := runtimeRaidState{Started: true, CurrentLayout: true, Members: []runtimeRaidMemberState{
		{CharID: 1, GroupIndex: 1, SlotOrder: 0}, {CharID: 2, GroupIndex: 1, SlotOrder: 1},
		{CharID: 3, GroupIndex: 1, SlotOrder: 2, OrdinaryPartySolo: true},
		{CharID: 4, GroupIndex: 1, SlotOrder: 3, OrdinaryPartySolo: true},
		{CharID: 5, GroupIndex: 1, SlotOrder: 0, OrdinaryPartySolo: true},
		{CharID: 6, GroupIndex: 2, SlotOrder: 0},
	}, Battle: &runtimeRaidBattle{Phase: 0, MemberAccounts: map[uint16]string{3: "a3"},
		Participation: map[uint16]currentRaidParticipation{3: {RunID: "run", Phase: 0}}}}
	members := append([]runtimeRaidMemberState(nil), raid.Members...)
	moved, fits := relocateRuntimeRaidOverflowSolos(raid, members, map[uint16]bool{4: true})
	// 1,2 reserve; 3 is in an active run; 4 is pinned; only 5 may move.
	if !fits || !reflect.DeepEqual(moved, []uint16{5}) || members[4].GroupIndex != 2 || members[4].SlotOrder != 1 {
		t.Fatal("wrong relocation", moved, fits, members)
	}
	for i := 0; i < 4; i++ {
		if members[i] != raid.Members[i] {
			t.Fatal("non-movable row changed", members[i])
		}
	}
	// Legacy layout: every row reserves its seat, nothing is hidden or moved.
	legacy := runtimeRaidState{Members: append([]runtimeRaidMemberState(nil), raid.Members...)}
	if moved, _ := relocateRuntimeRaidOverflowSolos(legacy, legacy.Members, nil); len(moved) != 0 {
		t.Fatal("legacy rows moved", moved)
	}
}
