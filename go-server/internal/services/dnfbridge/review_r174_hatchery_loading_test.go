package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"testing"
	"time"
)

func TestR174HatcheryStartsFreshLoadingCycle(t *testing.T) {
	s, leader, peer, _, _, target := actualAntonHatcheryTransferParty(t)
	members := []uint16{99, 100}
	if release, _, _, _, _ := s.partyDungeonLoadingBarrierArrive(leader, 12, members); release {
		t.Fatal("first energy member released")
	}
	if release, _, _, _, _ := s.partyDungeonLoadingBarrierArrive(peer, 12, members); !release {
		t.Fatal("energy cycle not released")
	}
	s.partyDungeonLoadingMu.Lock()
	old := s.partyDungeonLoadingCycles[12]
	// Keep the already released period inside the merge window across expensive
	// actual PVF preparation. No timer or synthetic target runtime is injected.
	old.released = time.Now().Add(time.Hour)
	s.partyDungeonLoadingMu.Unlock()
	if err := s.handleDungeonSelectUpper(leader, encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: target, Difficulty: 2})); err != nil {
		t.Fatal(err)
	}
	s.partyDungeonLoadingMu.Lock()
	next := s.partyDungeonLoadingCycles[12]
	stale, armed := next == old || next.Released, next.Timer != nil
	s.partyDungeonLoadingMu.Unlock()
	// A previously queued timeout/release must not consume the new scene's
	// pending placement, even after its old Timer.Stop has returned.
	s.forceReleasePartyDungeonLoadingCycle(12, old, "stale_energy_timeout")
	s.releasePartyDungeonLoadingCycle(12, old, members, members, "stale_energy_release")
	if !s.partyDungeonPostStartMapPlacementPending(leader) || !s.partyDungeonPostStartMapPlacementPending(peer) {
		t.Fatal("old cycle consumed new hatchery placement")
	}
	if stale || !armed {
		t.Fatalf("new hatchery inherited old loading state: stale=%v watchdog=%v", stale, armed)
	}
	if release, _, _, _, _ := s.partyDungeonLoadingBarrierArrive(leader, 12, members); release {
		t.Fatal("first hatchery member released without peer")
	}
	if release, _, _, _, _ := s.partyDungeonLoadingBarrierArrive(peer, 12, members); !release {
		t.Fatal("both hatchery members did not release")
	}
}

func TestR174LoadingTimerCannotReleaseReplacementCycle(t *testing.T) {
	s, leader, peer, lc, pc, _ := partyDungeonSyncFixture(t)
	runPartyDungeonSyncEntry(t, s, leader)
	partyID, members, ok := s.partyDungeonLoadingBarrierMembers(leader)
	if !ok {
		t.Fatal("party missing")
	}
	old := s.currentPartyDungeonLoadingCycle(partyID)
	s.partyDungeonLoadingMu.Lock()
	old.Timer.Stop()
	old.Timer = nil
	s.partyDungeonLoadingMu.Unlock()
	// Model the committed replacement while a callback from old is queued.
	leader.dungeon.mu.Lock()
	next := *leader.dungeon.runtime
	leader.dungeon.runtime = &next
	leader.dungeon.runToken++
	leader.dungeon.mu.Unlock()
	if !s.deferPartyDungeonPostStartMapPlacement(leader, &next, "replacement") {
		t.Fatal("placement not deferred")
	}
	current := s.currentPartyDungeonLoadingCycle(partyID)
	if current == old {
		t.Fatal("replacement reuses old cycle")
	}
	beforeL, beforeP := len(lc.frames()), len(pc.frames())
	s.forceReleasePartyDungeonLoadingCycle(partyID, old, "late_old_timer")
	s.releasePartyDungeonLoadingCycle(partyID, old, members, members, "late_old_dispatch")
	if current.Released || len(lc.frames()) != beforeL || len(pc.frames()) != beforeP {
		t.Fatal("old callback released replacement")
	}
	if !s.partyDungeonPostStartMapPlacementPending(leader) || !s.partyDungeonPostStartMapPlacementPending(peer) {
		t.Fatal("old callback consumed placement")
	}
	s.forceReleasePartyDungeonLoadingCycle(partyID, current, "current_timeout")
	if s.partyDungeonPostStartMapPlacementPending(leader) || s.partyDungeonPostStartMapPlacementPending(peer) {
		t.Fatal("current timeout failed to release placement")
	}
}

func TestR174LoadingReleaseSkipsSessionThatChangedBeforeCallback(t *testing.T) {
	s, leader, peer, lc, _, _ := partyDungeonSyncFixture(t)
	runPartyDungeonSyncEntry(t, s, leader)
	partyID, members, ok := s.partyDungeonLoadingBarrierMembers(leader)
	if !ok {
		t.Fatal("missing party")
	}
	current := s.currentPartyDungeonLoadingCycle(partyID)
	leader.dungeon.mu.Lock()
	replacement := *leader.dungeon.runtime
	leader.dungeon.runtime = &replacement
	leader.dungeon.runToken++
	leader.dungeon.mu.Unlock()
	// The cycle itself still exists; its queued callback has lost the leader's
	// runtime before any subsequent arm/arrival can replace the cycle.
	leader.pendingPostStartMapMu.Lock()
	leader.pendingPostStartMapPlacement = &pendingPostStartMapPlacement{Runtime: &replacement, Source: "next"}
	leader.pendingPostStartMapMu.Unlock()
	before := len(lc.frames())
	s.releasePartyDungeonLoadingCycle(partyID, current, members, nil, "queued_before_transition")
	if len(lc.frames()) != before || !s.partyDungeonPostStartMapPlacementPending(leader) {
		t.Fatal("old callback wrote to replacement runtime or consumed its placement")
	}
	if s.partyDungeonPostStartMapPlacementPending(peer) {
		t.Fatal("unchanged peer was not released")
	}
	s.partyDungeonLoadingMu.Lock()
	if current.Timer != nil {
		current.Timer.Stop()
		current.Timer = nil
	}
	s.partyDungeonLoadingMu.Unlock()
}
