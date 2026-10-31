package dnfbridge

import (
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
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

// a02RewardPartyWithSessionGenerations rebuilds the test party with the
// generations actually assigned to the bound sessions. r195RewardParty keeps
// the historical fixed generation 1 and is still appropriate for display-only
// fixtures. Using it before an op13 regression would make a departure by the
// later-bound member hit the generation-drift repair branch.
//
// firstAntonFlowFixture already created a generation-1 party. Clear that
// fixture setup before rebuilding so the manager does not retain a stale
// second membership. These LeaveCurrent calls are setup cleanup only; the
// regression events below are dispatched through op13/op665.
func a02RewardPartyWithSessionGenerations(t *testing.T, s *Service, sessions ...*gameSession) {
	t.Helper()
	if len(sessions) == 0 {
		t.Fatal("no sessions for reward party")
	}
	members := make([]party.RuntimePartyMember, 0, len(sessions))
	for _, session := range sessions {
		identity, bound := s.boundGameSessionCharacterSnapshot(session)
		if !bound {
			t.Fatalf("session character %d is not bound", session.selectedCharacterID)
		}
		members = append(members, party.RuntimePartyMember{
			UserID:            identity.character,
			SessionGeneration: identity.generation,
		})
	}
	m := s.runtimePartyManagerForService()
	for _, member := range members {
		m.LeaveCurrent(member.UserID)
	}
	result := m.Create(members[0], alignedcmd.PartyState{})
	if !result.OK {
		t.Fatalf("create reward party: %s", result.Reason)
	}
	for _, member := range members[1:] {
		if joined := m.Join(result.Party.ID, member); !joined.OK {
			t.Fatalf("join reward party %d: %s", member.UserID, joined.Reason)
		}
	}
}

func TestR195RewardDisplayUsesCurrentPartyWithoutChangingEntitlement(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  byte
		phase byte
	}{
		{name: "anton_phase0", mode: 1, phase: 0},
		{name: "luke_phase1", mode: 2, phase: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{}
			p := &gameSession{selectedCharacterID: 19}
			state := runtimeRaidState{Members: []runtimeRaidMemberState{{CharID: 19, GroupIndex: 1}, {CharID: 20, GroupIndex: 1}, {CharID: 21, GroupIndex: 2}}, Battle: &runtimeRaidBattle{Mode: tc.mode, Phase: tc.phase}}
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
		})
	}
}

func TestR195RealRewardDisplayDepartureKeepsBothOwnedGrants(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	peer, ok := f.service.onlineGameSession(20)
	if !ok {
		t.Fatal("fixture peer 20 is not online")
	}
	a02RewardPartyWithSessionGenerations(t, f.service, session, peer)
	flow := f.state.Battle.RewardFlow[0]
	// Drive op13 for 20 through the real command dispatcher. The party above
	// stores the bound generations, so this takes the normal manager.Leave
	// path rather than the generation-drift repair branch. 20 remains in the
	// raid, so the frozen flow entitlement must stay intact.
	handled, err := f.service.handleAlignedGameCommand(
		peer,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketLeaveParty),
		nil,
	)
	if err != nil || !handled {
		t.Fatalf("op13 leave party: handled=%t err=%v", handled, err)
	}
	manager := f.service.runtimePartyManagerForService()
	if _, still := manager.SnapshotByUser(20, 0); still {
		t.Fatal("op13 did not remove 20 from the central party manager")
	}
	if _, still := manager.SnapshotByUser(19, 0); !still {
		t.Fatal("op13 removed 19 from the central party manager")
	}
	state := cloneRuntimeRaidState(*f.state)
	if runtimeRaidMemberIndex(state, 20) < 0 {
		t.Fatal("ordinary-party leave removed 20 from the raid")
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 659, []byte{1})
	for kind, body := range antonNativeCardBodies(t, f) {
		if (kind == 0 || kind == 1) && (body[1] != 1 || body[2] != 19 || body[3] != 0) {
			t.Fatal("manual screen retained former member", body)
		}
	}
	if len(flow.Recipients) != 2 || flow.Recipients[0].CharacterID != 19 || flow.Recipients[1].CharacterID != 20 {
		t.Fatalf("display removed entitlement: %+v", flow.Recipients)
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
