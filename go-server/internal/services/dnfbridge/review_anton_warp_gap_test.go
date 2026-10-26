package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func actualAntonHatchWarpOwner(t *testing.T) (*Service, *gameSession, *runtimeDungeonState, uint32) {
	return actualAntonHatchWarpOwnerFor(t, 0)
}

// actualAntonHatcheryEnergyOwnerForCorner builds the real 218 energy owner in
// the corner that produces the requested hatchery. It deliberately does not
// require that hatchery to be open, so the disabled-221 entry control starts
// from the same actual source scene as the accepted 222/223/224 cases.
func actualAntonHatcheryEnergyOwnerForCorner(t *testing.T, corner uint32) (*Service, *gameSession, *runtimeDungeonState, *raid.AntonProgression) {
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
	coord, ok := reviewAntonHatcheryCorners[corner]
	if !ok {
		t.Fatalf("unknown Anton hatchery corner %d", corner)
	}
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
	s.worldMapTable = source.worldMapTable
	s.worldMapResolver = source.worldMapResolver
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
	return s, session, r, progress
}

func actualAntonHatchWarpOwnerFor(t *testing.T, wanted uint32) (*Service, *gameSession, *runtimeDungeonState, uint32) {
	t.Helper()
	if wanted == 0 {
		wanted = 222
	}
	if wanted != 222 && wanted != 223 && wanted != 224 {
		t.Fatalf("hatchery %d is outside the fixed open set 222/223/224", wanted)
	}
	s, session, r, progress := actualAntonHatcheryEnergyOwnerForCorner(t, wanted)
	states := reviewAntonHatcheryStates(progress)
	if state, found := states[wanted]; !found || state != 0 {
		t.Fatalf("actual Anton policy left hatchery %d state=%d found=%t, want open", wanted, state, found)
	}
	if !s.currentRaidDungeonSelectionAllowed(session, wanted) {
		t.Fatalf("current battle rejects actual open hatchery %d", wanted)
	}
	return s, session, r, wanted
}

func TestReviewActualAntonHatcheryWarpOpenHatcheries234CurrentCorners(t *testing.T) {
	for _, wanted := range reviewAntonHatcheryPolicyIDs {
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
