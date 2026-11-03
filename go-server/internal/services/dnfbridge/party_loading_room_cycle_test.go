package dnfbridge

import (
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

func moveLoadingCycleFixture(t *testing.T, s *Service, leader, follower *gameSession) {
	t.Helper()
	body := make([]byte, dungeoncmd.MoveMapRequestSize)
	body[0] = 1
	if err := s.handleDungeonMoveMap(leader, body); err != nil {
		t.Fatal(err)
	}
	for _, member := range []*gameSession{leader, follower} {
		scene, ok := member.dungeon.runtime.Session.Scene()
		if !ok || scene.Coordinate.X != 1 {
			t.Fatal("fixture did not commit both members to the next room")
		}
	}
}

func TestPartyLoadingFastRoomMoveDoesNotRegroupPreviousRoom(t *testing.T) {
	s, leader, follower := partyMovePreflightFixture(t)
	partyID, members, ok := s.partyDungeonLoadingBarrierMembers(leader)
	if !ok {
		t.Fatal("fixture has no party barrier")
	}
	s.partyDungeonLoadingBarrierArrive(leader, partyID, members)
	release, _, _, _, old := s.partyDungeonLoadingBarrierArrive(follower, partyID, members)
	if !release {
		t.Fatal("initial room did not release")
	}
	moveLoadingCycleFixture(t, s, leader, follower)
	// Pin the old cycle inside the duplicate-op37 grace period. A new room
	// must not be mistaken for a retransmission regardless of wall clock speed.
	s.partyDungeonLoadingMu.Lock()
	old.released = time.Now().Add(time.Hour)
	s.partyDungeonLoadingMu.Unlock()
	release, first, _, _, next := s.partyDungeonLoadingBarrierArrive(leader, partyID, members)
	if next == old || release || !first {
		t.Fatalf("new room reused previous release: same=%t release=%t first=%t", next == old, release, first)
	}
	t.Cleanup(func() {
		if next.Timer != nil {
			next.Timer.Stop()
		}
	})
	// The peer arrives beyond the old cycle's grace period, just as the
	// observed room transition crossed that boundary between member arrivals.
	s.partyDungeonLoadingMu.Lock()
	old.released = time.Now().Add(-time.Hour)
	s.partyDungeonLoadingMu.Unlock()
	release, first, _, _, last := s.partyDungeonLoadingBarrierArrive(follower, partyID, members)
	if last != next || !release || !first {
		t.Fatal("new room lost the first member's readiness across the old grace boundary")
	}
	if release, first, _, _, duplicate := s.partyDungeonLoadingBarrierArrive(leader, partyID, members); duplicate != next || !release || first {
		t.Fatal("same-room duplicate no longer coalesces into the released cycle")
	}
}

func TestPartyLoadingOldRoomCallbackCannotReleaseNextRoom(t *testing.T) {
	s, leader, follower := partyMovePreflightFixture(t)
	partyID, members, ok := s.partyDungeonLoadingBarrierMembers(leader)
	if !ok {
		t.Fatal("fixture has no party barrier")
	}
	cycle := r195RegisterLoadingCycle(t, s, partyID, leader, follower)
	moveLoadingCycleFixture(t, s, leader, follower)
	leader.conn.(*bufferConn).write.Reset()
	follower.conn.(*bufferConn).write.Reset()
	s.releasePartyDungeonLoadingCycle(partyID, cycle, members, members, "delayed_previous_room_release")
	if leader.conn.(*bufferConn).write.Len() != 0 || follower.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("an old room's queued release wrote into the next room")
	}
}

func TestPartyLoadingCachedRevisitDoesNotRestoreOldOwner(t *testing.T) {
	s, leader, follower := partyMovePreflightFixture(t)
	old := snapshotPartyDungeonLoadingOwner(leader)
	moveLoadingCycleFixture(t, s, leader, follower)
	// Clear both scene projections, then take the actual cached path back.
	for _, peer := range []*gameSession{leader, follower} {
		runtime := peer.dungeon.runtime
		for _, monster := range runtime.Room.Snapshot().Monsters {
			key := monster.ObjectKey
			if peer == leader {
				if _, _, err := runtime.Room.CommitActorDeathReport(key, runtime.Session); err != nil {
					t.Fatal(err)
				}
			} else if _, err := runtime.Session.MarkHostileDefeated(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.handleDungeonMoveMap(leader, make([]byte, dungeoncmd.MoveMapRequestSize)); err != nil {
		t.Fatal(err)
	}
	if leader.dungeon.runtime.Room != old.room {
		t.Fatal("fixture did not return to the same cached room object")
	}
	if snapshotPartyDungeonLoadingOwner(leader) == old {
		t.Fatal("returning to a cached room revived an earlier visit's loading callback")
	}
}

func TestPartyLoadingStaggeredMoveRetainsOnlyNewRoomArrivals(t *testing.T) {
	s, leader, follower := partyMovePreflightFixture(t)
	partyID, members, ok := s.partyDungeonLoadingBarrierMembers(leader)
	if !ok {
		t.Fatal("fixture has no party barrier")
	}
	s.partyDungeonLoadingBarrierArrive(follower, partyID, members)
	body := make([]byte, dungeoncmd.MoveMapRequestSize)
	body[0] = 1
	if err := s.handleDungeonMoveMapPlanned(leader, body, nil, false); err != nil {
		t.Fatal(err)
	}
	release, _, _, _, next := s.partyDungeonLoadingBarrierArrive(leader, partyID, members)
	if release || next.Ready[follower.selectedCharacterID] {
		t.Fatal("a peer still in the old room supplied readiness for the new room")
	}
	t.Cleanup(func() {
		if next.Timer != nil {
			next.Timer.Stop()
		}
	})
	scene, _ := leader.dungeon.runtime.Session.Scene()
	plan := runtimePartyDungeonMovePlan{target: runtimeDungeonRoomKeyFromScene(scene), seed: leader.dungeon.runtime.Seed}
	if err := s.handleDungeonMoveMapPlanned(follower, body, &plan, false); err != nil {
		t.Fatal(err)
	}
	release, _, _, _, last := s.partyDungeonLoadingBarrierArrive(follower, partyID, members)
	if !release || !last.Ready[leader.selectedCharacterID] {
		t.Fatal("the later room commit lost the already-ready member of the new room")
	}
}
