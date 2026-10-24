package dnfbridge

import "testing"

func TestR185LoadingUnchangedOwnerKeepsReadyAcrossPeerReplacement(t *testing.T) {
	s, leader, peer, _, _, trace := partyDungeonSyncFixture(t)
	runPartyDungeonSyncEntry(t, s, leader)
	partyID, members, ok := s.partyDungeonLoadingBarrierMembers(leader)
	if !ok {
		t.Fatal("missing active party")
	}
	release, _, _, _, old := s.partyDungeonLoadingBarrierArrive(leader, partyID, members)
	if release || !old.Ready[leader.selectedCharacterID] {
		t.Fatal("leader arrival was not held for peer")
	}
	// The peer's new scene is committed after the unchanged leader's op37.
	peer.dungeon.mu.Lock()
	replacement := *peer.dungeon.runtime
	peer.dungeon.runtime = &replacement
	peer.dungeon.runToken++
	peer.dungeon.mu.Unlock()
	if !s.deferPartyDungeonPostStartMapPlacement(peer, &replacement, "replacement") {
		t.Fatal("replacement placement not deferred")
	}
	next := s.currentPartyDungeonLoadingCycle(partyID)
	t.Cleanup(func() {
		s.partyDungeonLoadingMu.Lock()
		if next.Timer != nil {
			next.Timer.Stop()
		}
		s.partyDungeonLoadingMu.Unlock()
	})
	if next == old || next.Released {
		t.Fatal("peer replacement did not create an unreleased cycle")
	}
	s.forceReleasePartyDungeonLoadingCycle(partyID, old, "late_old_timer")
	if next.Released {
		t.Fatal("retired timer released replacement cycle")
	}
	if !next.Ready[leader.selectedCharacterID] || next.Ready[peer.selectedCharacterID] {
		t.Fatalf("unchanged leader arrival lost or new peer arrived without op37: %v", next.Ready)
	}
	if release, _, _, _, _ := s.partyDungeonLoadingBarrierArrive(peer, partyID, members); !release {
		t.Fatal("new peer arrival still waits for an unchanged leader's second op37")
	}
	s.releasePartyDungeonLoadingCycle(partyID, next, members, members, "last_member_op37")
	if s.partyDungeonPostStartMapPlacementPending(leader) || s.partyDungeonPostStartMapPlacementPending(peer) {
		t.Fatal("release did not consume both current placements")
	}
	steps := trace.orderedSteps(t)
	for _, id := range members {
		if firstPartyDungeonSyncStep(steps, id, 37, nil) < 0 {
			t.Fatalf("member %d did not receive the finish-loading tail", id)
		}
	}
}

func TestR185LoadingChangedOwnerNeverInheritsReady(t *testing.T) {
	for _, released := range []bool{false, true} {
		t.Run(map[bool]string{false: "all_owners_changed", true: "released_cycle"}[released], func(t *testing.T) {
			s := &Service{}
			leader := &gameSession{selectedCharacterID: 99}
			peer := &gameSession{selectedCharacterID: 100}
			members := []uint16{99, 100}
			oldOwners := map[uint16]partyDungeonLoadingOwner{99: {session: leader, runtime: &runtimeDungeonState{}, token: 1}, 100: {session: peer, runtime: &runtimeDungeonState{}, token: 1}}
			s.partyDungeonLoadingMu.Lock()
			old := s.ensurePartyDungeonLoadingCycleLocked(leader, 12, members, oldOwners)
			old.Ready[99], old.Ready[100], old.Released = true, true, released
			nextOwners := map[uint16]partyDungeonLoadingOwner{99: oldOwners[99], 100: {session: peer, runtime: &runtimeDungeonState{}, token: 2}}
			if !released {
				nextOwners[99] = partyDungeonLoadingOwner{session: leader, runtime: &runtimeDungeonState{}, token: 2}
			}
			next := s.ensurePartyDungeonLoadingCycleLocked(peer, 12, members, nextOwners)
			if next.Timer != nil {
				next.Timer.Stop()
			}
			s.partyDungeonLoadingMu.Unlock()
			if next == old || len(next.Ready) != 0 {
				t.Fatalf("new scene inherited obsolete arrivals: %v", next.Ready)
			}
		})
	}
}
