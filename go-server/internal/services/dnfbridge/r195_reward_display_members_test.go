package dnfbridge

import (
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
)

func r195RewardParty(t *testing.T, s *Service, ids ...uint16) {
	t.Helper()
	m := s.runtimePartyManagerForService()
	result := m.Create(party.RuntimePartyMember{UserID: ids[0], SessionGeneration: 1}, alignedcmd.PartyState{})
	if !result.OK {
		t.Fatal(result.Reason)
	}
	for _, id := range ids[1:] {
		if joined := m.Join(result.Party.ID, party.RuntimePartyMember{UserID: id, SessionGeneration: 1}); !joined.OK {
			t.Fatal(joined.Reason)
		}
	}
}

func TestR195RewardDisplayUsesCurrentPartyWithoutChangingEntitlement(t *testing.T) {
	s := &Service{}
	p := &gameSession{selectedCharacterID: 19}
	state := runtimeRaidState{Members: []runtimeRaidMemberState{{CharID: 19, GroupIndex: 1}, {CharID: 20, GroupIndex: 1}, {CharID: 21, GroupIndex: 2}}, Battle: &runtimeRaidBattle{Mode: 1}}
	r195RewardParty(t, s, 19, 20, 21)
	want := map[uint16]bool{19: true, 20: true}
	if got := s.currentRaidPartyRewardDisplayMembers(p, state); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	// Keep a stale client projection deliberately; central membership wins.
	storeRuntimePartyState(p, alignedcmd.PartyState{PartyID: 99, Members: []alignedcmd.PartyMemberState{{UserID: 19}, {UserID: 20}}})
	s.runtimePartyManagerForService().LeaveCurrent(20)
	if got := s.currentRaidPartyRewardDisplayMembers(p, state); !reflect.DeepEqual(got, map[uint16]bool{19: true}) {
		t.Fatal("departed peer leaked", got)
	}
	r195RewardParty(t, s, 19, 20)
	state.Members = state.Members[:1]
	if got := s.currentRaidPartyRewardDisplayMembers(p, state); !reflect.DeepEqual(got, map[uint16]bool{19: true}) {
		t.Fatal("departed raid member leaked", got)
	}
	state.Members = nil
	if got := s.currentRaidPartyRewardDisplayMembers(p, state); len(got) != 0 {
		t.Fatal("removed viewer", got)
	}
}

func TestR195RealRewardDisplayDepartureKeepsBothOwnedGrants(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	r195RewardParty(t, f.service, 19, 20)
	flow := f.state.Battle.RewardFlow[0]
	f.service.runtimePartyManagerForService().LeaveCurrent(20)
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 659, []byte{0})
	for kind, body := range antonNativeCardBodies(t, f) {
		if (kind == 0 || kind == 1) && (body[1] != 1 || body[2] != 19 || body[3] != 0) {
			t.Fatal("manual screen retained former member", body)
		}
	}
	if len(flow.Recipients) != 2 {
		t.Fatal("display removed entitlement")
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	body := antonNativeCardBodies(t, f)[1]
	if len(body) < 4 || body[1] != 1 || body[2] != 19 || body[3] != 0 {
		t.Fatal("automatic screen retained former member", body)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, id := range []string{"19", "20"} {
		if antonGrantedItemCount(t, f, id, 10304529) != 30 || antonGrantedItemCount(t, f, id, 10093974) != 19 {
			t.Fatal("earned rewards lost", id)
		}
	}
}
