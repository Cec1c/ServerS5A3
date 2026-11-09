package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"testing"
	"time"
)

func TestR174HatcheryStartsFreshLoadingCycle(t *testing.T) {
	s, leader, peer, oldLeader, oldPeer, target := actualAntonHatcheryTransferParty(t)
	// Native admission now requires the actual manager roster and bound
	// generations. The historical session-only party was refused before a
	// hatchery was installed, making its later loading assertion misleading.
	t.Setenv("DNFBRIDGE_PARTY_NATIVE_ADMISSION", "")
	a02RewardPartyWithSessionGenerations(t, s, leader, peer)
	snapshot, managed := s.runtimePartyManagerForService().SnapshotByUser(leader.selectedCharacterID, leader.characterGeneration)
	if !managed {
		t.Fatal("fixture lost authoritative party")
	}
	s.publishRuntimePartySnapshot(snapshot)
	partyID := snapshot.ID
	members := []uint16{99, 100}
	if release, _, _, _, _ := s.partyDungeonLoadingBarrierArrive(leader, partyID, members); release {
		t.Fatal("first energy member released")
	}
	if release, _, _, _, _ := s.partyDungeonLoadingBarrierArrive(peer, partyID, members); !release {
		t.Fatal("energy cycle not released")
	}
	s.partyDungeonLoadingMu.Lock()
	old := s.partyDungeonLoadingCycles[partyID]
	// Keep the already released period inside the merge window across expensive
	// actual PVF preparation. No timer or synthetic target runtime is injected.
	old.released = time.Now().Add(time.Hour)
	s.partyDungeonLoadingMu.Unlock()
	if err := s.handleDungeonSelectUpper(leader, encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: target, Difficulty: 2})); err != nil {
		t.Fatal(err)
	}
	s.partyNativeAdmission.mu.Lock()
	proof := s.partyNativeAdmission.bySession[leader]
	s.partyNativeAdmission.mu.Unlock()
	if proof == nil {
		t.Fatal("hatchery selection did not request current native readiness")
	}
	completeNativePartyAdmissionForTest(t, s, leader)
	if leader.dungeon.runtime == oldLeader || peer.dungeon.runtime == oldPeer || leader.dungeon.runtime.Dungeon.ID != int64(target) || peer.dungeon.runtime.Dungeon.ID != int64(target) {
		t.Fatal("readiness did not commit both hatchery owners")
	}
	if !s.partyDungeonPostStartMapPlacementPending(leader) || !s.partyDungeonPostStartMapPlacementPending(peer) {
		t.Fatal("new hatchery placement was not pending before stale callbacks")
	}
	s.partyDungeonLoadingMu.Lock()
	next := s.partyDungeonLoadingCycles[partyID]
	stale, armed := next == old || next.Released, next.Timer != nil
	s.partyDungeonLoadingMu.Unlock()
	// A previously queued timeout/release must not consume the new scene's
	// pending placement, even after its old Timer.Stop has returned.
	s.forceReleasePartyDungeonLoadingCycle(partyID, old, "stale_energy_timeout")
	s.releasePartyDungeonLoadingCycle(partyID, old, members, members, "stale_energy_release")
	if !s.partyDungeonPostStartMapPlacementPending(leader) || !s.partyDungeonPostStartMapPlacementPending(peer) {
		t.Fatal("old cycle consumed new hatchery placement")
	}
	if stale || !armed {
		t.Fatalf("new hatchery inherited old loading state: stale=%v watchdog=%v", stale, armed)
	}
	if release, _, _, _, _ := s.partyDungeonLoadingBarrierArrive(leader, partyID, members); release {
		t.Fatal("first hatchery member released without peer")
	}
	if release, _, _, _, _ := s.partyDungeonLoadingBarrierArrive(peer, partyID, members); !release {
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
