package dnfbridge

import (
	"testing"
	"time"
)

func TestPartyLoadingDeadlineIsolatesOnlyMissingMember(t *testing.T) {
	s, a, b, ac, bc, _ := partyDungeonSyncFixture(t)

	if p, ok := s.bootstrapManagedRuntimePartyForSession(a); !ok || len(p.Members) != 2 {
		t.Fatal("party not registered")
	}
	runPartyDungeonSyncEntry(t, s, a)
	s.options.currentClientVersion = "official-24.9"
	partyID, members, ok := s.partyDungeonLoadingBarrierMembers(a)
	if !ok {
		t.Fatal("no barrier")
	}
	_, _, _, _, cycle := s.partyDungeonLoadingBarrierArrive(a, partyID, members)
	cycle.Timer.Stop()
	cycle.started = time.Now().Add(-26 * time.Second)
	s.forceReleasePartyDungeonLoadingCycle(partyID, cycle, "deadline_test")
	if uint16(s.runtimePartyStateForSession(a).PartyID) != partyID {
		t.Fatal("healthy member lost party identity")
	}
	if s.runtimePartyStateForSession(b).PartyID != 0 {
		t.Fatal("missing member remains in authoritative party")
	}
	if !partyDungeonSyncConnHasPlacement(t, ac) {
		t.Fatal("healthy member still blocked")
	}
	if partyDungeonSyncConnHasPlacement(t, bc) {
		t.Fatal("missing member received dungeon activation")
	}
	if !b.dungeon.runtime.townReturnPending {
		t.Fatal("missing member did not receive normal town return")
	}
}

func TestPartyLoadingDeadlineReturnsAllWhenNobodyReady(t *testing.T) {
	s, a, b, ac, bc, _ := partyDungeonSyncFixture(t)

	if p, ok := s.bootstrapManagedRuntimePartyForSession(a); !ok || len(p.Members) != 2 {
		t.Fatal("party not registered")
	}
	runPartyDungeonSyncEntry(t, s, a)
	s.options.currentClientVersion = "official-24.9"
	partyID, _, _ := s.partyDungeonLoadingBarrierMembers(a)
	cycle := s.currentPartyDungeonLoadingCycle(partyID)
	cycle.Timer.Stop()
	cycle.started = time.Now().Add(-26 * time.Second)
	s.forceReleasePartyDungeonLoadingCycle(partyID, cycle, "deadline_test")
	for _, p := range []*gameSession{a, b} {
		if s.runtimePartyStateForSession(p).PartyID != 0 {
			t.Fatal("unready member remains in party")
		}
		if !p.dungeon.runtime.townReturnPending {
			t.Fatal("unready member not returning")
		}
	}
	if partyDungeonSyncConnHasPlacement(t, ac) || partyDungeonSyncConnHasPlacement(t, bc) {
		t.Fatal("empty readiness released dungeon activation")
	}
}

func TestPartyLoadingPreparationDeadlineKeepsPreparedMember(t *testing.T) {
	s, a, b, ac, bc, _ := partyDungeonSyncFixture(t)
	if _, ok := s.bootstrapManagedRuntimePartyForSession(a); !ok {
		t.Fatal("party not registered")
	}
	runPartyDungeonSyncEntry(t, s, a)
	s.options.currentClientVersion = "official-24.9"
	partyID, members, _ := s.partyDungeonLoadingBarrierMembers(a)
	s.partyDungeonLoadingBarrierArrive(a, partyID, members)
	_, _, _, _, cycle := s.partyDungeonLoadingBarrierArrive(b, partyID, members)
	cycle.preparing = make(map[uint16]*partyLoadingPreparation)
	for id, owner := range cycle.owners {
		cycle.preparing[id] = &partyLoadingPreparation{owner: owner, wantTail: true}
	}
	s.releasePartyDungeonLoadingMemberPhase(partyID, cycle, a.selectedCharacterID, cycle.owners[a.selectedCharacterID], true, "prepared", 0)
	cycle.started = time.Now().Add(-26 * time.Second)
	s.checkPartyLoadingPreparation(partyID, cycle, "deadline_test")
	if s.runtimePartyStateForSession(b).PartyID != 0 || uint16(s.runtimePartyStateForSession(a).PartyID) != partyID {
		t.Fatal("wrong preparation member retired")
	}
	if !partyDungeonSyncConnHasPlacement(t, ac) || partyDungeonSyncConnHasPlacement(t, bc) {
		t.Fatal("preparation release did not match readiness")
	}
}
