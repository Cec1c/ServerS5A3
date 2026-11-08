package dnfbridge

import (
	"reflect"
	"testing"
)

func TestRaidLivePartyProjectionFirstJoinAndRepeatedPartyChanges(t *testing.T) {
	f := newR195SoloPrefixFixture(t)
	initial := f.state(t)
	// Creation and approval assign legal raid seats but do not create a party.
	for _, member := range initial.Members {
		if member.GroupIndex == 0 || member.OrdinaryPartySolo {
			t.Fatal("fixture must exercise untouched initial raid placement")
		}
	}
	assertProjection := func(wantSolo bool) {
		t.Helper()
		before := f.state(t)
		body, err := f.service.buildRuntimeRaidRosterBody(before)
		if err != nil {
			t.Fatal(err)
		}
		rows := parseR195CurrentRaidRoster(t, body, f.raidKey)
		for _, id := range []uint16{f.leaderID, f.memberID} {
			row := r195SoloPrefixRosterMember(t, rows, id)
			raw := mustR195SoloPrefixMember(t, before, id)
			group, slot := raw.GroupIndex, raw.SlotOrder
			if wantSolo {
				group, slot = 0, 0
			}
			if row.GroupIndex != group || row.SlotOrder != slot {
				t.Fatalf("character %d group/slot=%d/%d want %d/%d", id, row.GroupIndex, row.SlotOrder, group, slot)
			}
		}
		f.memberConn.write.Reset()
		if err := f.service.sendRuntimeRaidMetadata(f.member, before, 2); err != nil {
			t.Fatal(err)
		}
		packet, _ := splitGameServerUpperPacket(t, f.memberConn.write.Bytes())
		want := mustR195SoloPrefixMember(t, before, f.leaderID).GroupIndex
		if wantSolo {
			want = 0
		}
		if got := r195SoloPrefixMetadataLeaderGroup(t, packet.Body, f.raidKey); got != want {
			t.Fatalf("metadata group=%d want=%d", got, want)
		}
		if !reflect.DeepEqual(before, f.state(t)) {
			t.Fatal("wire projection mutated raid authority")
		}
	}
	assertProjection(true)
	for i := 0; i < 3; i++ {
		f.formOrdinaryParty(t)
		// A stale marker must not override the actual ordinary party.
		f.setSolo(t, f.leaderID, true)
		f.setSolo(t, f.memberID, true)
		assertProjection(false)
		f.leaveOrdinaryParty(t, f.member)
		// Reproduce initial admission and stale cached group information.
		f.setSolo(t, f.leaderID, false)
		f.setSolo(t, f.memberID, false)
		assertProjection(true)
	}
	f.installBattle(t)
	frozen := f.frozen(t)
	assertProjection(true)
	f.assertFrozen(t, frozen)
}

// These dispatch fixtures create/join a raid without forming ordinary parties.
// Their wire expectation is solo; their authoritative planned seats stay intact.
func raidAllSoloWireFixture(state runtimeRaidState) runtimeRaidState {
	state.Members = append([]runtimeRaidMemberState(nil), state.Members...)
	for i := range state.Members {
		state.Members[i].OrdinaryPartySolo = true
	}
	return state
}
