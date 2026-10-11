package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// D33 item 追加2 evidence pin: "卢克攻坚战反复提示 蔓延之圣所已通关".
//
// This file documents ONLY what the delivered source plus the real runtime
// Script.pvf prove today. It deliberately makes no protocol, timing or client
// text claim that has not been observed, and it does not change production
// behavior. Each pinned fact is a deliberate limitation probe in the style of
// TestRealPVFLukeThroneAdmissionUsesPersistedLifetimeClears: a future intended
// change must update this test on purpose.

// Fact 1 — the three phase-1 sanctums are ordinary "designated difficulty"
// dungeons with 15 mazes each. maxClearState therefore resolves to the byte-4
// ceiling, so an accepted clear at the raid difficulty (2) is exactly the op5
// clear state 3 that the current client renders as already cleared.
func TestRealPVFLukeSanctumClearStateCeilingAndMazeCount(t *testing.T) {
	f, _, _ := normalLukeFixture(t)
	ctx := context.Background()
	table, err := worldmap.LoadSource(ctx, f.service.initialEquipmentArchive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := map[int64]string{
		5054: "dungeon/Luke/Phase1/Sanctum_of_Birth.dgn",
		5055: "dungeon/Luke/Phase1/Sanctum_of_Extinction.dgn",
		5056: "dungeon/Luke/Phase1/Sanctum_of_Spread.dgn",
	}
	for id, want := range wantPaths {
		d, ok := table.FindDungeon(id)
		if !ok || d.Path != want {
			t.Fatalf("real sanctum %d path=%q want %q", id, d.Path, want)
		}
		if len(d.Mazes) != 15 {
			t.Fatalf("real sanctum %d maze count=%d want 15", id, len(d.Mazes))
		}
		if len(d.Metadata.DesignatedDifficulties) == 0 || currentDungeonMaxClearState(d) != 4 {
			t.Fatalf("real sanctum %d designated=%v maxClearState=%d want non-empty/4",
				id, d.Metadata.DesignatedDifficulties, currentDungeonMaxClearState(d))
		}
		if next := currentDungeonNextClearState(d, 2); next != 3 {
			t.Fatalf("real sanctum %d difficulty2 next clear state=%d want 3", id, next)
		}
	}
}

// Fact 2 — the real CARD SET MAP INFO contract is five overlapping-free
// three-maze windows (start 0/3/6/9/12 for every sanctum). The owned selector
// maps an accepted-clear ordinal onto the room inside its frozen window, so the
// same sanctum never re-selects a room it already cleared within one epoch.
func TestRealPVFLukeSanctumCardSetWindowOrdinals(t *testing.T) {
	f, _, _ := normalLukeFixture(t)
	config, err := raid.LoadLukeSanctumMazeConfiguration(f.service.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	if config.Dungeons != [3]uint32{5054, 5055, 5056} || len(config.Records) != 5 {
		t.Fatalf("real card set dungeons=%v records=%d", config.Dungeons, len(config.Records))
	}
	for index, row := range config.Records {
		want := uint32(3 * index)
		if row != [3]uint32{want, want, want} {
			t.Fatalf("real card set row %d=%v want uniform start %d", index, row, want)
		}
	}
	for _, id := range []uint32{5054, 5055, 5056} {
		for record := 0; record < 5; record++ {
			for ordinal := 0; ordinal < 3; ordinal++ {
				maze := 3*record + ordinal
				got, err := config.RecordForMaze(id, maze, 15)
				if err != nil || got != record {
					t.Fatalf("sanctum %d maze %d resolved record=%d err=%v want %d", id, maze, got, err, record)
				}
			}
		}
	}
}

// Fact 3 (LIMITATION) — a raid sanctum clear is not isolated from the
// character's persistent op5 dungeon-permission clear state. commitCurrentDungeonPermission
// accepts 5056 at the raid difficulty and stores clear state 3, and
// currentDungeonPermissionSnapshotEntries then re-advertises that row on every
// character-select / dungeon-select snapshot. That re-advertised row is the
// strongest server-side candidate for a repeated "already cleared" prompt, but
// the client-side render link is NOT proven here, so this is pinned, not fixed.
func TestRealPVFLukeSanctumClearLeaksIntoPersistentPermissionSnapshot(t *testing.T) {
	f, session, _ := normalLukeFixture(t)
	ctx := context.Background()
	table, err := worldmap.LoadSource(ctx, f.service.initialEquipmentArchive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.service.worldMapTable = table
	f.service.worldMapResolver, err = worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	dungeon, ok := table.FindDungeon(5056)
	if !ok {
		t.Fatal("real sanctum 5056 missing")
	}
	runtime := &runtimeDungeonState{Dungeon: dungeon}
	runtime.Request.DungeonID, runtime.Request.Difficulty = 5056, 2

	body, state, updated, err := f.service.commitCurrentDungeonPermission(ctx, session, runtime)
	if err != nil || !updated || state != 3 || len(body) == 0 {
		t.Fatalf("raid sanctum persistence updated=%v state=%d body=%d err=%v", updated, state, len(body), err)
	}
	entries, err := f.service.currentDungeonPermissionSnapshotEntries(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	foundLeak := false
	for _, entry := range entries {
		if entry.DungeonID == 5056 {
			foundLeak = true
			if entry.State != 3 {
				t.Fatalf("re-advertised sanctum 5056 state=%d want 3", entry.State)
			}
		}
	}
	if !foundLeak {
		t.Fatal("expected the pinned leak: raid sanctum 5056 absent from the permission snapshot")
	}
}
