package worldmap

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestDungeonRunOnlyAllowsDeclaredDirectedWarp(t *testing.T) {
	table := topologyTestTable()
	doc := parseTestDocument(t, "dungeon/warps.dgn", "[warp map condition]\n[source grid pos]\n0 0\n[dest grid pos]\n2 1\n[/warp map condition]\n")
	parsed := ParseDungeon(800, "dungeon/warps.dgn", doc)
	table.dungeons[0].SourceSections = parsed.SourceSections
	resolver, err := NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	topology, err := BuildDungeonTopology(resolver, 800, 0)
	if err != nil {
		t.Fatal(err)
	}
	run, err := NewDungeonRun(topology)
	if err != nil {
		t.Fatal(err)
	}
	before := run.Snapshot()
	if _, err := run.PreviewMoveTo(RoomCoordinate{2, 1}); !errors.Is(err, ErrRunCurrentRoomNotCleared) {
		t.Fatalf("warp skipped clear gate: %v", err)
	}
	if !reflect.DeepEqual(before, run.Snapshot()) {
		t.Fatal("preview changed run")
	}
	if err := run.MarkCurrentRoomCleared(); err != nil {
		t.Fatal(err)
	}
	if _, err := run.MoveTo(RoomCoordinate{2, 1}); err != nil {
		t.Fatalf("declared directed warp rejected: %v", err)
	}
	if err := run.MarkCurrentRoomCleared(); err != nil {
		t.Fatal(err)
	}
	before = run.Snapshot()
	if _, err := run.MoveTo(RoomCoordinate{0, 0}); !errors.Is(err, ErrRoomNotAdjacent) {
		t.Fatalf("unlisted reverse was allowed: %v", err)
	}
	if !reflect.DeepEqual(before, run.Snapshot()) {
		t.Fatal("refused reverse changed run")
	}
}

func TestRealPVFForgottenDeclaredWarpRoomTransitions(t *testing.T) {
	file := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if file == "" {
		t.Skip("requires real PVF")
	}
	archive, err := platformpvf.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	table, err := LoadSource(context.Background(), archive, Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	topology, err := BuildDungeonTopology(resolver, 4000, 0)
	if err != nil {
		t.Fatal(err)
	}
	run, err := NewDungeonRun(topology)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.MoveTo(RoomCoordinate{8, 6}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		pos   RoomCoordinate
		mapID int64
	}{{RoomCoordinate{0, 6}, 11252}, {RoomCoordinate{8, 6}, 11251}, {RoomCoordinate{1, 7}, 11206}, {RoomCoordinate{8, 6}, 11251}} {
		before := run.Snapshot()
		preview, err := run.PreviewMoveTo(want.pos)
		if err != nil {
			t.Fatalf("PVF warp %s -> %s rejected: %v", before.Current, want.pos, err)
		}
		if !reflect.DeepEqual(before, run.Snapshot()) {
			t.Fatal("warp preflight mutated run")
		}
		room, err := run.MoveTo(want.pos)
		if err != nil || room.Map == nil || room.Map.Map.ID != want.mapID || preview.Map.Map.ID != want.mapID {
			t.Fatalf("warp target=%+v error=%v", room, err)
		}
	}
	before := run.Snapshot()
	if _, err := run.MoveTo(RoomCoordinate{4, 9}); !errors.Is(err, ErrRoomNotAdjacent) && !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("unlisted warp allowed: %v", err)
	}
	if !reflect.DeepEqual(before, run.Snapshot()) {
		t.Fatal("refused warp mutated run")
	}
}

func TestDeclaredWarpMalformedAndSentinelEntriesDoNotAuthorizeMoves(t *testing.T) {
	valid := "[source grid pos]\n0 0\n[dest grid pos]\n2 1\n"
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"directed", valid, true},
		{"missing destination", "[source grid pos]\n0 0\n", false},
		{"orphan destination", "[dest grid pos]\n2 1\n", false},
		{"noninteger", "[source grid pos]\n0.0 0\n[dest grid pos]\n2 1\n", false},
		{"extra coordinate", valid + "[source grid pos]\n0 0 1\n[dest grid pos]\n2 1\n", false},
		{"unknown condition", valid + "[unknown]\n1\n", false},
		{"no wildcard", "[source grid pos]\n-1 -1\n[dest grid pos]\n2 1\n", false},
		{"outside byte", "[source grid pos]\n0 0\n[dest grid pos]\n256 1\n", false},
		{"outside maze", "[source grid pos]\n0 0\n[dest grid pos]\n3 1\n", false},
		{"closed cell", "[source grid pos]\n0 0\n[dest grid pos]\n1 1\n", false},
		{"self and sentinel preserve concrete", "[source grid pos]\n0 0\n[dest grid pos]\n0 0\n[source grid pos]\n-1 -1\n[dest grid pos]\n2 1\n" + valid, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table := topologyTestTable()
			doc := parseTestDocument(t, "dungeon/warp.dgn", "[warp map condition]\n"+tc.body+"[/warp map condition]\n")
			table.dungeons[0].SourceSections = ParseDungeon(800, "dungeon/warp.dgn", doc).SourceSections
			resolver, err := NewResolver(table)
			if err != nil {
				t.Fatal(err)
			}
			topology, err := BuildDungeonTopology(resolver, 800, 0)
			if err != nil {
				t.Fatal(err)
			}
			room, _ := topology.Room(RoomCoordinate{0, 0})
			if roomHasWarpTarget(room, RoomCoordinate{2, 1}) != tc.want {
				t.Fatalf("warp targets=%v diagnostics=%v", room.WarpTargets, topology.Diagnostics)
			}
			if len(room.WarpTargets) > 0 {
				room.WarpTargets[0] = RoomCoordinate{99, 99}
				again, _ := topology.Room(RoomCoordinate{0, 0})
				if again.WarpTargets[0] == room.WarpTargets[0] {
					t.Fatal("Room exposed mutable warp targets")
				}
			}
		})
	}
}

func TestRealPVFForgottenWarpSessionPreservesRevisitActors(t *testing.T) {
	file := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if file == "" {
		t.Skip("requires real PVF")
	}
	archive, err := platformpvf.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	table, err := LoadSource(context.Background(), archive, Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	topology, err := BuildDungeonTopology(resolver, 4000, 0)
	if err != nil {
		t.Fatal(err)
	}
	run, err := NewDungeonRun(topology)
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewDungeonSession(run)
	if err != nil {
		t.Fatal(err)
	}
	first, err := session.MoveByteTransition(8, 6, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Scene.ExpectedHostiles) < 2 {
		t.Fatal("real hub hostiles absent")
	}
	for i, ref := range first.Scene.ExpectedHostiles {
		if err := session.BindHostileObject(ref, uint32(600+i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := session.MarkHostileDefeated(600); err != nil {
		t.Fatal(err)
	}
	cached := session.Snapshot().Scene
	away, err := session.MoveByteTransition(0, 6, nil)
	if err != nil || away.Revisit {
		t.Fatalf("warp out: %v", err)
	}
	before := session.Snapshot()
	if _, err := session.MoveByteTransition(8, 6, nil); !errors.Is(err, ErrRevisitSceneRequired) {
		t.Fatalf("uncached revisit accepted: %v", err)
	}
	if !reflect.DeepEqual(before, session.Snapshot()) {
		t.Fatal("failed revisit changed session")
	}
	back, err := session.MoveByteTransition(8, 6, &cached)
	if err != nil || !back.Revisit {
		t.Fatalf("cached warp return: %v", err)
	}
	if !reflect.DeepEqual(back.Scene.RuntimeObjects, cached.RuntimeObjects) || !reflect.DeepEqual(back.Scene.DefeatedObjects, cached.DefeatedObjects) || back.Scene.Cleared != cached.Cleared {
		t.Fatal("warp reset prior actors or clearance")
	}
}
