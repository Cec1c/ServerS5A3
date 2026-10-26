package dnfbridge

import (
	"reflect"
	"testing"
)

func TestR169FiendWarReturnRetiresTemporaryPartyPreservesEightSeatOwners(t *testing.T) {
	f, p := r159ActiveEncounter(t)
	s := f.service
	r := p[0].dungeon.runtime
	if r.raidEntry.fiendWarParty == nil {
		t.Fatal("live entry did not freeze temporary party")
	}
	frozen := r.raidEntry.fiendWarParty
	numbers := cloneRuntimeRaidBattle(f.state.Battle).MemberGroups
	raidMembers := append([]runtimeRaidMemberState(nil), f.state.Members...)
	// Exercise the common post-detach hook used by both confirmed return routes.
	p[0].dungeon.runtime = nil
	if !s.retireCurrentFiendWarEncounterParty(p[0], r) {
		t.Fatal("FiendWar return not handled")
	}
	for _, peer := range p {
		if _, found := s.runtimePartyManagerForService().SnapshotByUser(peer.selectedCharacterID, 0); found {
			t.Fatal("temporary membership survived return")
		}
		if got := runtimePartyStateSnapshot(peer); got.PartyID != 0 {
			t.Fatal("client party projection survived", got.PartyID)
		}
	}
	if !reflect.DeepEqual(numbers, f.state.Battle.MemberGroups) || !reflect.DeepEqual(raidMembers, f.state.Members) {
		t.Fatal("personal raid numbers changed")
	}
	next, ok := s.replaceManagedRuntimeParty(runtimeRaidGroupPartyState(*f.state, f.state.Members))
	if !ok || next.PartyID == int(frozen.ID) {
		t.Fatal("next encounter did not form fresh party")
	}
	if !s.retireCurrentFiendWarEncounterParty(p[0], r) {
		t.Fatal("old return not consumed")
	}
	if got := s.runtimePartyStateForSession(p[0]); got.PartyID != next.PartyID {
		t.Fatal("old return retired newly assembled party")
	}
}

func TestR169OrdinaryReturnDoesNotRetireParty(t *testing.T) {
	f, p := r159ActiveEncounter(t)
	r := p[0].dungeon.runtime
	saved := *r.raidEntry
	r.raidEntry.ChannelType = 0
	defer func() { *r.raidEntry = saved }()
	before := f.service.runtimePartyStateForSession(p[0])
	if f.service.retireCurrentFiendWarEncounterParty(p[0], r) {
		t.Fatal("ordinary return consumed")
	}
	if !reflect.DeepEqual(before, f.service.runtimePartyStateForSession(p[0])) {
		t.Fatal("ordinary party changed")
	}
}
