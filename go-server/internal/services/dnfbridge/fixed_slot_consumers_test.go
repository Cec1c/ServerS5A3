package dnfbridge

import (
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

func TestRuntimePartyMemberIndexUsesFixedSlotForSparseRoster(t *testing.T) {
	state := alignedcmd.PartyState{
		PartyID:    7,
		UserID:     303,
		MaxMembers: 4,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 101, Slot: 0},
			{UserID: 303, Slot: 2},
		},
	}
	if got, ok := runtimePartyMemberIndex(303, state); !ok || got != 2 {
		t.Fatalf("member 303 index=%d ok=%t, want fixed slot 2", got, ok)
	}
}

func TestRuntimePartyMemberIndexUsesNonzeroLeaderSlot(t *testing.T) {
	state := alignedcmd.PartyState{
		PartyID:    7,
		UserID:     303,
		MaxMembers: 4,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 101, Slot: 1},
			{UserID: 303, Slot: 2},
		},
	}
	if got, ok := runtimePartyMemberIndex(303, state); !ok || got != 2 {
		t.Fatalf("nonzero leader index=%d ok=%t, want fixed slot 2", got, ok)
	}
}

func TestRuntimePartyMemberIndexPreservesLegacyZeroSlots(t *testing.T) {
	state := alignedcmd.PartyState{
		PartyID:    7,
		UserID:     303,
		MaxMembers: 4,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 101},
			{UserID: 202},
			{UserID: 303},
		},
	}
	for userID, want := range map[uint16]byte{303: 0, 101: 1, 202: 2} {
		if got, ok := runtimePartyMemberIndex(userID, state); !ok || got != want {
			t.Fatalf("legacy member %d index=%d ok=%t, want %d", userID, got, ok, want)
		}
	}
}

func TestRuntimePartyMemberIndexKeepsSingleMemberFallback(t *testing.T) {
	for name, state := range map[string]alignedcmd.PartyState{
		"empty":      {PartyID: 7, UserID: 101, MaxMembers: 4},
		"single_row": {PartyID: 7, UserID: 101, MaxMembers: 4, Members: []alignedcmd.PartyMemberState{{UserID: 101, Slot: 3}}},
	} {
		if got, ok := runtimePartyMemberIndex(101, state); ok || got != 0 {
			t.Fatalf("%s single member index=%d ok=%t, want the existing zero/false behavior", name, got, ok)
		}
	}
}

func TestHellEligibilityRosterUsesFixedSlotForSparseRoster(t *testing.T) {
	state := alignedcmd.PartyState{
		PartyID:    7,
		UserID:     303,
		MaxMembers: 4,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 101, Slot: 0},
			{UserID: 303, Slot: 2},
		},
	}
	want := []hellEligibilityMember{{Slot: 0, CharacterID: 101}, {Slot: 2, CharacterID: 303}}
	if got := slotConsumerHellRoster(state); !reflect.DeepEqual(got, want) {
		t.Fatalf("sparse hell roster=%+v want %+v", got, want)
	}
}

func TestHellEligibilityRosterUsesNonzeroLeaderSlot(t *testing.T) {
	state := alignedcmd.PartyState{
		PartyID:    7,
		UserID:     303,
		MaxMembers: 4,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 101, Slot: 1},
			{UserID: 303, Slot: 2},
		},
	}
	want := []hellEligibilityMember{{Slot: 1, CharacterID: 101}, {Slot: 2, CharacterID: 303}}
	if got := slotConsumerHellRoster(state); !reflect.DeepEqual(got, want) {
		t.Fatalf("nonzero-leader hell roster=%+v want %+v", got, want)
	}
}

func TestHellEligibilityRosterPreservesLegacyZeroSlots(t *testing.T) {
	state := alignedcmd.PartyState{
		PartyID:    7,
		UserID:     303,
		MaxMembers: 4,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 101},
			{UserID: 202},
			{UserID: 303},
		},
	}
	want := []hellEligibilityMember{{Slot: 0, CharacterID: 303}, {Slot: 1, CharacterID: 101}, {Slot: 2, CharacterID: 202}}
	if got := slotConsumerHellRoster(state); !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy hell roster=%+v want %+v", got, want)
	}
}

func TestHellEligibilityRosterKeepsSingleMemberFallback(t *testing.T) {
	for name, state := range map[string]alignedcmd.PartyState{
		"empty":      {UserID: 101},
		"single_row": {PartyID: 7, UserID: 101, MaxMembers: 4, Members: []alignedcmd.PartyMemberState{{UserID: 101, Slot: 3}}},
	} {
		session := &gameSession{selectedCharacterID: 101}
		if state.PartyID > 0 {
			storeRuntimePartyState(session, state)
		}
		want := []hellEligibilityMember{{Slot: 0, CharacterID: 101}}
		if got := (&Service{}).hellEligibilityRoster(session); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s single hell roster=%+v want %+v", name, got, want)
		}
	}
}

func slotConsumerHellRoster(state alignedcmd.PartyState) []hellEligibilityMember {
	session := &gameSession{selectedCharacterID: state.UserID}
	storeRuntimePartyState(session, state)
	return (&Service{}).hellEligibilityRoster(session)
}
