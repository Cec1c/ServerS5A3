package dnfbridge

import (
	"sync"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

type partyMoveOverlapConn struct {
	bufferConn
	once        sync.Once
	beforeWrite func()
}

func (c *partyMoveOverlapConn) Write(p []byte) (int, error) {
	c.once.Do(c.beforeWrite)
	return c.bufferConn.Write(p)
}

// A queued member op45 can run after leader preflight but before the internal
// follow request. Both packets name the same valid room; they must not create
// different room seeds or turn a duplicate transition into a disconnect.
func TestPartyMoveOverlappingMemberRequestUsesOneRoom(t *testing.T) {
	s, leader, follower := partyMovePreflightFixture(t)
	seed := uint32(1000)
	s.dungeonSeed = func() (uint32, error) { seed++; return seed, nil }
	body := make([]byte, dungeoncmd.MoveMapRequestSize)
	body[0] = 1
	conn := &partyMoveOverlapConn{beforeWrite: func() {
		if err := s.handleDungeonMoveMap(follower, body); err != nil {
			t.Errorf("early member: %v", err)
		}
	}}
	leader.conn = conn
	if err := s.handleDungeonMoveMap(leader, body); err != nil {
		t.Fatalf("overlapping room requests disconnected leader: %v", err)
	}
	lr, fr := leader.dungeon.runtime, follower.dungeon.runtime
	ls, _ := lr.Session.Scene()
	fs, _ := fr.Session.Scene()
	if runtimeDungeonRoomKeyFromScene(ls) != runtimeDungeonRoomKeyFromScene(fs) || lr.Room != fr.Room || lr.Seed != fr.Seed {
		t.Fatalf("party room split: leader=%s/%d seed=%d follower=%s/%d seed=%d shared=%t", ls.Coordinate, ls.Map.Map.ID, lr.Seed, fs.Coordinate, fs.Map.Map.ID, fr.Seed, lr.Room == fr.Room)
	}
	for _, wire := range [][]byte{conn.write.Bytes(), follower.conn.(*bufferConn).write.Bytes()} {
		starts := 0
		for _, packet := range splitAllFinishBridgePackets(t, wire) {
			if packet.Header.MsgID == currentDungeonStartNotification {
				starts++
			}
		}
		if starts != 1 {
			t.Fatalf("start-map sends=%d want one", starts)
		}
	}
}

func TestPartyMoveMemberArrivesBeforeLeaderUsesOneRoom(t *testing.T) {
	s, leader, follower := partyMovePreflightFixture(t)
	seed := uint32(1000)
	s.dungeonSeed = func() (uint32, error) { seed++; return seed, nil }
	body := make([]byte, dungeoncmd.MoveMapRequestSize)
	body[0] = 1
	if err := s.handleDungeonMoveMap(follower, body); err != nil {
		t.Fatal(err)
	}
	if err := s.handleDungeonMoveMap(leader, body); err != nil {
		t.Fatal(err)
	}
	lr, fr := leader.dungeon.runtime, follower.dungeon.runtime
	ls, _ := lr.Session.Scene()
	fs, _ := fr.Session.Scene()
	if ls.Coordinate.X != 1 || runtimeDungeonRoomKeyFromScene(ls) != runtimeDungeonRoomKeyFromScene(fs) || lr.Room != fr.Room || lr.Seed != fr.Seed {
		t.Fatalf("early member split party: leader=%s/%d seed=%d follower=%s/%d seed=%d", ls.Coordinate, ls.Map.Map.ID, lr.Seed, fs.Coordinate, fs.Map.Map.ID, fr.Seed)
	}
}

func TestPartyMoveCompletedPlanRequiresExactInstanceRoomAndSeed(t *testing.T) {
	for _, change := range []string{"instance", "room", "seed", "member", "cache"} {
		t.Run(change, func(t *testing.T) {
			s, leader, follower := partyMovePreflightFixture(t)
			body := make([]byte, dungeoncmd.MoveMapRequestSize)
			body[0] = 1
			if err := s.handleDungeonMoveMap(leader, body); err != nil {
				t.Fatal(err)
			}
			r := follower.dungeon.runtime
			scene, _ := r.Session.Scene()
			plan := runtimePartyDungeonMovePlan{target: runtimeDungeonRoomKeyFromScene(scene), seed: r.Seed, instance: r.partyInstance, room: r.Room}
			if !runtimePartyMovePlanCommitted(r, plan) {
				t.Fatal("exact plan not recognized")
			}
			switch change {
			case "instance":
				plan.instance = &dungeonPartyInstance{}
			case "room":
				plan.room = &runtimeDungeonRoom{}
			case "seed":
				plan.seed++
			case "member":
				delete(r.partyInstance.memberRuntimes, follower.selectedCharacterID)
			case "cache":
				r.Rooms = nil
			}
			if runtimePartyMovePlanCommitted(r, plan) {
				t.Fatal("stale ownership accepted as completed")
			}
		})
	}
}

func TestPartyMoveCachedRoomsKeepSharedSeedAndRetiredActors(t *testing.T) {
	s, leader, follower := partyMovePreflightFixture(t)
	startRoom, startSeed := leader.dungeon.runtime.Room, leader.dungeon.runtime.Seed
	seed := uint32(1000)
	s.dungeonSeed = func() (uint32, error) { seed++; return seed, nil }
	move := func(x byte) {
		t.Helper()
		body := make([]byte, dungeoncmd.MoveMapRequestSize)
		body[0] = x
		if err := s.handleDungeonMoveMap(leader, body); err != nil {
			t.Fatal(err)
		}
		lr, fr := leader.dungeon.runtime, follower.dungeon.runtime
		scene, _ := lr.Session.Scene()
		if scene.Coordinate.X != int64(x) || lr.Room != fr.Room || lr.Seed != fr.Seed {
			t.Fatalf("cached move split party at x=%d", x)
		}
	}
	move(1)
	lr, fr := leader.dungeon.runtime, follower.dungeon.runtime
	targetRoom, targetSeed := lr.Room, lr.Seed
	for _, m := range targetRoom.Snapshot().Monsters {
		if _, _, err := targetRoom.CommitActorDeathReport(m.ObjectKey, lr.Session); err != nil {
			t.Fatal(err)
		}
		if _, err := fr.Session.MarkHostileDefeated(m.ObjectKey); err != nil {
			t.Fatal(err)
		}
	}
	move(0)
	if lr.Room != startRoom || lr.Seed != startSeed {
		t.Fatal("entry revisit changed original room or seed")
	}
	move(1)
	if lr.Room != targetRoom || lr.Seed != targetSeed {
		t.Fatal("target revisit changed original room or seed")
	}
	for _, r := range []*runtimeDungeonState{lr, fr} {
		scene, _ := r.Session.Scene()
		if !scene.Cleared {
			t.Fatal("revisit revived retired room")
		}
	}
}
