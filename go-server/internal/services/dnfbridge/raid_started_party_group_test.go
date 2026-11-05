package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"reflect"
	"testing"
)

func TestStartedRaidFreePartyIgnoresSoloSeatsAndPreservesOtherParty(t *testing.T) {
	for _, otherParty := range []bool{false, true} {
		t.Run(map[bool]string{false: "old_solo_seats", true: "other_party_in_anchor_group"}[otherParty], func(t *testing.T) {
			raid := runtimeRaidState{Started: true, CurrentLayout: true, Members: []runtimeRaidMemberState{
				{CharID: 1, GroupIndex: 1, SlotOrder: 0},
				{CharID: 2, GroupIndex: 2, SlotOrder: 0},
				{CharID: 3, GroupIndex: 1, SlotOrder: 1, OrdinaryPartySolo: !otherParty},
				{CharID: 4, GroupIndex: 1, SlotOrder: 2, OrdinaryPartySolo: !otherParty},
				{CharID: 5, GroupIndex: 1, SlotOrder: 3, OrdinaryPartySolo: true},
			}, Battle: &runtimeRaidBattle{MemberGroups: map[uint16]byte{1: 1, 2: 2, 3: 1, 4: 1, 5: 1}}}
			original := cloneRuntimeRaidState(raid)
			party := alignedcmd.PartyState{PartyID: 7, UserID: 1, Members: []alignedcmd.PartyMemberState{{UserID: 1, Slot: 0}, {UserID: 2, Slot: 2}}}
			candidate, changed := planRuntimeRaidGroupSynchronization(raid, party)
			if !changed {
				t.Fatal("free party retained inconsistent raid groups")
			}
			wantGroup := byte(1)
			if otherParty {
				wantGroup = 2
			}
			if candidate[0].GroupIndex != wantGroup || candidate[1].GroupIndex != wantGroup || candidate[1].SlotOrder != 2 {
				t.Fatalf("wrong group or fixed slot: %+v", candidate[:2])
			}
			if !reflect.DeepEqual(candidate[2:], raid.Members[2:]) || !reflect.DeepEqual(cloneRuntimeRaidState(raid), original) {
				t.Fatal("plan changed unrelated members or frozen reward state")
			}
			raid.Members = candidate
			if _, again := planRuntimeRaidGroupSynchronization(raid, party); again {
				t.Fatal("identical projection changed twice")
			}
		})
	}
}

func TestStartedRaidFreePartyDoesNotStealOccupiedGroup(t *testing.T) {
	raid := runtimeRaidState{Started: true, CurrentLayout: true, Members: []runtimeRaidMemberState{{CharID: 1, GroupIndex: 1}, {CharID: 2, GroupIndex: 2}}}
	for group := byte(1); group <= 5; group++ {
		raid.Members = append(raid.Members, runtimeRaidMemberState{CharID: uint16(10 + group), GroupIndex: group})
	}
	party := alignedcmd.PartyState{PartyID: 7, UserID: 1, Members: []alignedcmd.PartyMemberState{{UserID: 1}, {UserID: 2}}}
	before := cloneRuntimeRaidState(raid)
	if candidate, changed := planRuntimeRaidGroupSynchronization(raid, party); changed || candidate != nil {
		t.Fatal("occupied group stolen")
	}
	if !reflect.DeepEqual(before, raid) {
		t.Fatal("failed plan mutated live state")
	}
}

func TestStartedRaidFreePartyAcceptMatchesRoster(t *testing.T) {
	f := newInviteAcceptDungeonFixture(t)
	state := installPartyRaidStateForTest(t, f, 0, []runtimeRaidMemberState{
		{CharID: 200, GroupIndex: 1, SlotOrder: 1, OrdinaryPartySolo: true},
		{CharID: 201, GroupIndex: 1, SlotOrder: 2, OrdinaryPartySolo: true},
		{CharID: 202, GroupIndex: 1, SlotOrder: 3, OrdinaryPartySolo: true},
	})
	state.Started = true
	state.Battle = &runtimeRaidBattle{MemberAccounts: map[uint16]string{99: "account-1", 100: "account-1"}, MemberGroups: map[uint16]byte{99: 1, 100: 2}}
	f.formPartyThroughClientAccept(t)
	first, _ := runtimeRaidMemberForTest(*state, 99)
	second, _ := runtimeRaidMemberForTest(*state, 100)
	if first.GroupIndex != second.GroupIndex || second.SlotOrder != 1 {
		t.Fatal("accepted party remains split in raid projection")
	}
	if state.Battle.MemberGroups[100] != 2 {
		t.Fatal("reward admission changed")
	}
}
