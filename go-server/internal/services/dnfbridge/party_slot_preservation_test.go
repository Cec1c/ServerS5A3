package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// snapshotWithFixedSlots keeps the delegate target at slot two and the former
// leader at slot zero. StateFor must carry those slots, and every downstream
// roster writer must use them instead of renumbering by slice position.
func snapshotWithFixedSlots() party.RuntimePartySnapshot {
	return party.RuntimePartySnapshot{
		ID:     7,
		Leader: 303,
		Settings: alignedcmd.PartyState{
			MaxMembers: 4,
		},
		Members: []party.RuntimePartyMember{
			{UserID: 101, Slot: 0, State: alignedcmd.PartyMemberState{UserID: 101, UserState: 1, HPPercent: 100, MPPercent: 100}},
			{UserID: 202, Slot: 1, State: alignedcmd.PartyMemberState{UserID: 202, UserState: 1, HPPercent: 100, MPPercent: 100}},
			{UserID: 303, Slot: 2, State: alignedcmd.PartyMemberState{UserID: 303, UserState: 1, HPPercent: 100, MPPercent: 100}},
		},
	}
}

func snapshotWithSlotHole() party.RuntimePartySnapshot {
	return party.RuntimePartySnapshot{
		ID:       8,
		Leader:   303,
		Settings: alignedcmd.PartyState{MaxMembers: 4},
		Members: []party.RuntimePartyMember{
			{UserID: 101, Slot: 0, State: alignedcmd.PartyMemberState{UserID: 101, UserState: 1, HPPercent: 100, MPPercent: 100}},
			{UserID: 303, Slot: 2, State: alignedcmd.PartyMemberState{UserID: 303, UserState: 1, HPPercent: 100, MPPercent: 100}},
		},
	}
}

func TestRuntimePartySnapshotStateForKeepsFixedSlotOrder(t *testing.T) {
	state := snapshotWithFixedSlots().StateFor(303)
	if state.UserID != 303 || !state.IsLeader {
		t.Fatalf("leader semantics lost: %+v", state)
	}
	members := runtimePartyMembers(state)
	if len(members) != 3 {
		t.Fatalf("members = %+v", members)
	}
	for slot, want := range []uint16{101, 202, 303} {
		if members[slot].UserID != want {
			t.Fatalf("slot %d uid=%d want %d; leader-first reorder still present: %+v", slot, members[slot].UserID, want, members)
		}
	}
}

func TestRuntimePartyRealtimeInfoWritesFixedSlots(t *testing.T) {
	body := runtimePartyRealtimeInfoBody(snapshotWithFixedSlots().StateFor(303))
	rows := parseRealtimeInfoRows(t, body)
	want := []realtimeInfoRow{
		{uid: 101, hp: 100, helper: 0, slot: 0},
		{uid: 202, hp: 100, helper: 0, slot: 1},
		{uid: 303, hp: 100, helper: 0, slot: 2},
	}
	if len(rows) != len(want) {
		t.Fatalf("op153 rows=%+v want %+v", rows, want)
	}
	for index := range want {
		if rows[index] != want[index] {
			t.Fatalf("op153 row[%d]=%+v want %+v body=% X", index, rows[index], want[index], body)
		}
	}
}

func TestSceneOp9WritesFixedSlotsAndLeaderRow(t *testing.T) {
	state := snapshotWithFixedSlots().StateFor(303)
	projections := make([]currentSceneOp9PartyMemberProjection, 0, len(state.Members))
	for _, member := range runtimePartyMembers(state) {
		projections = append(projections, currentSceneOp9PartyMemberProjection{State: member, Name: "p"})
	}
	body := buildCurrentSceneOp9ActorPartyDisplayBodyForProfile(
		101, dnfrepo.CharacterRecord{}, false, "p", 10, 10,
		projections, currentSceneOp9PartyLeaderSlot(projections, state.UserID), false,
	)
	rows, leaderSlot := parseOp9PartyFrame(t, body, false)
	if len(rows) != 3 {
		t.Fatalf("op9 rows=%+v want three", rows)
	}
	for slot, want := range []uint16{101, 202, 303} {
		if rows[slot].uid != want || rows[slot].slot != byte(slot) {
			t.Fatalf("op9 row[%d]=%+v want uid=%d slot=%d body=% X", slot, rows[slot], want, slot, body)
		}
	}
	if leaderSlot != 2 || rows[leaderSlot].uid != 303 {
		t.Fatalf("op9 leader row=%d uid=%d want slot 2 with leader 303 body=% X", leaderSlot, rows[leaderSlot].uid, body)
	}
}

func TestFixedSlotHoleFlowsThroughRealtimeAndSceneWriters(t *testing.T) {
	state := snapshotWithSlotHole().StateFor(303)
	members := runtimePartyMembers(state)
	if len(members) != 2 || members[0].Slot != 0 || members[0].UserID != 101 ||
		members[1].Slot != 2 || members[1].UserID != 303 {
		t.Fatalf("members after leave = %+v, want fixed slots 0 and 2 with the hole retained", members)
	}

	realtimeBody := runtimePartyRealtimeInfoBody(state)
	realtimeRows := parseRealtimeInfoRows(t, realtimeBody)
	if len(realtimeRows) != 2 || realtimeRows[0].slot != 0 || realtimeRows[1].slot != 2 {
		t.Fatalf("op153 rows after leave = %+v, want slots 0 and 2 body=% X", realtimeRows, realtimeBody)
	}

	projections := make([]currentSceneOp9PartyMemberProjection, 0, len(members))
	for _, member := range members {
		projections = append(projections, currentSceneOp9PartyMemberProjection{State: member, Name: "p"})
	}
	op9Body := buildCurrentSceneOp9ActorPartyDisplayBodyForProfile(
		101, dnfrepo.CharacterRecord{}, false, "p", 10, 10,
		projections, currentSceneOp9PartyLeaderSlot(projections, state.UserID), false,
	)
	op9Rows, leaderSlot := parseOp9PartyFrame(t, op9Body, false)
	if len(op9Rows) != 2 || op9Rows[0].slot != 0 || op9Rows[1].slot != 2 {
		t.Fatalf("op9 rows after leave = %+v, want slots 0 and 2 body=% X", op9Rows, op9Body)
	}
	if leaderSlot != 2 || op9Rows[1].uid != 303 {
		t.Fatalf("op9 leader marker=%d want the leader's fixed slot 2 body=% X", leaderSlot, op9Body)
	}
}
