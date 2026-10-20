package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
	"time"
)

func actualAntonHatchWarpOwner(t *testing.T) (*Service, *gameSession, *runtimeDungeonState, uint32) {
	return actualAntonHatchWarpOwnerFor(t, 0)
}

func actualAntonHatchWarpOwnerFor(t *testing.T, wanted uint32) (*Service, *gameSession, *runtimeDungeonState, uint32) {
	t.Helper()
	source, dgn, topology, _, _ := actualAntonRandomizedBridgeFixture(t)
	program, err := raid.LoadAntonProgram(source.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	base := now.Add(-181 * time.Second)
	progress, err := program.BeginPhase(1, base, 12345)
	if err != nil {
		t.Fatal(err)
	}
	progress, err = progress.Tick(now)
	if err != nil {
		t.Fatal(err)
	}
	var target uint32
	for seed := uint64(12345); seed < 12473; seed++ {
		if seed != 12345 {
			progress, err = program.BeginPhase(1, base, seed)
			if err != nil {
				t.Fatal(err)
			}
			progress, err = progress.Tick(now)
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, d := range progress.Dungeons() {
			if d.ID >= 221 && d.ID <= 224 && d.State == 0 && (wanted == 0 || wanted == d.ID) {
				target = d.ID
				break
			}
		}
		if target != 0 {
			break
		}
	}
	if target == 0 {
		t.Fatal("actual hatchery not opened")
	}
	coords := map[uint32]worldmap.RoomCoordinate{221: {X: 3, Y: 0}, 222: {X: 3, Y: 4}, 223: {X: 0, Y: 4}, 224: {X: 0, Y: 0}}
	coord := coords[target]
	topology.Start = &coord
	run, err := worldmap.NewDungeonRun(topology)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := worldmap.NewDungeonSession(run)
	if err != nil {
		t.Fatal(err)
	}
	scene, _ := ds.Scene()
	room, _, err := newRuntimeDungeonRoom(scene, source.dungeonMonsterTable, firstDungeonMonsterObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = room.AnnounceAllActors(ds); err != nil {
		t.Fatal(err)
	}
	s, session, r, b := raidLifeRuntimeFixture(t)
	s.initialEquipmentArchive = source.initialEquipmentArchive
	s.dungeonMonsterTable = source.dungeonMonsterTable
	table, err := worldmap.LoadSource(context.Background(), source.initialEquipmentArchive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.worldMapTable = table
	s.worldMapResolver, err = worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	repos, _ := s.repositoryGroup()
	char, _, err := repos.Character.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	char.Level = 90
	if err = repos.Character.Save(context.Background(), char); err != nil {
		t.Fatal(err)
	}
	r.Dungeon = dgn
	r.Session = ds
	r.Room = room
	r.lifecycleToken = 1
	session.dungeon.runToken = 1
	r.Request = dungeoncmd.SelectDungeonRequest{DungeonID: 218, Difficulty: 2}
	r.raidEntry.Phase = 1
	r.raidEntry.Attempt = progress.Epoch()
	session.postStartMapPlayerStateSent = true
	b.Phase = 1
	b.Anton = progress
	b.Dungeons = progress.Dungeons()
	b.EnterAfter = base
	b.Deadline = now.Add(time.Hour)
	if !s.currentRaidDungeonSelectionAllowed(session, target) {
		t.Fatal("current battle rejects actual open hatchery")
	}
	return s, session, r, target
}

func TestReviewActualAntonHatcheryWarpAllFourCurrentCorners(t *testing.T) {
	for _, wanted := range []uint32{221, 222, 223, 224} {
		t.Run(fmt.Sprint(wanted), func(t *testing.T) {
			s, session, old, target := actualAntonHatchWarpOwnerFor(t, wanted)
			body := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: target, Difficulty: 2})
			if err := s.handleDungeonSelectUpper(session, body); err != nil {
				t.Fatal(err)
			}
			if session.dungeon.runtime == old || session.dungeon.runtime == nil || session.dungeon.runtime.Dungeon.ID != int64(wanted) {
				t.Fatal("actual corner did not reach matching opened hatchery")
			}
		})
	}
}
func TestReviewActualAntonHatcheryWarpPrepareKeepsLegalActiveOwner(t *testing.T) {
	s, session, _, target := actualAntonHatchWarpOwner(t)
	_, _, err := s.prepareDungeonRuntime(context.Background(), session, dungeoncmd.SelectDungeonRequest{DungeonID: target, Difficulty: 2})
	if errors.Is(err, errDungeonAlreadyActive) {
		t.Fatalf("legal actual corner-room 218 -> %d blocked by active guard: %v", target, err)
	}
	if err != nil {
		t.Fatal(err)
	}
}
func TestReviewActualAntonHatcheryWarpPublicSelectCommitsTarget(t *testing.T) {
	s, session, old, target := actualAntonHatchWarpOwner(t)
	body := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: target, Difficulty: 2})
	if err := s.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime == old || session.dungeon.runtime == nil || session.dungeon.runtime.Dungeon.ID != int64(target) {
		t.Fatalf("actual public op16 retained energy owner instead of legal hatchery %d", target)
	}
}
