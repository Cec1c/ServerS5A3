package dnfbridge

import "testing"

func TestPartyRelayRetirementSingletonRejoinDoesNotReuseOldLegs(t *testing.T) {
	f, _ := r132RelayFixture(t)
	state, _, ok := f.service.createOrJoinManagedRuntimeParty(f.follower, f.leader)
	if !ok || !f.service.syncRuntimePartyUDPRelay(state) {
		t.Fatal("two-member relay setup failed")
	}
	relay := f.service.partyUDPRelay
	relay.mu.Lock()
	oldLegs := make(map[currentPartyUDPRelayLegKey]*currentPartyUDPRelayLeg, len(relay.legs))
	for key, leg := range relay.legs {
		oldLegs[key] = leg
	}
	relay.mu.Unlock()
	if len(oldLegs) != 2 {
		t.Fatalf("initial leg count=%d", len(oldLegs))
	}
	if err := f.service.handleOnlineLeaveParty(f.follower, 13); err != nil {
		t.Fatal(err)
	}
	solo := f.service.runtimePartyStateForSession(f.leader)
	if solo.PartyID != state.PartyID || len(runtimePartyMembers(solo)) != 1 {
		t.Fatalf("singleton lobby was not retained: %+v", solo)
	}
	if count := r132RelayLegCount(relay); count != 0 {
		t.Fatalf("singleton kept %d retired relay legs", count)
	}
	rejoined, _, ok := f.service.createOrJoinManagedRuntimeParty(f.follower, f.leader)
	if !ok || rejoined.PartyID != state.PartyID || !f.service.syncRuntimePartyUDPRelay(rejoined) {
		t.Fatal("same lobby rejoin failed")
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if len(relay.legs) != 2 {
		t.Fatalf("rejoined leg count=%d", len(relay.legs))
	}
	for key, leg := range relay.legs {
		if oldLegs[key] == leg {
			t.Fatal("rejoin reused retired leg and its probe queue")
		}
	}
}
