package worldmap

import (
	"context"
	"errors"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestN2FiendWarDeclaredStartGuards(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current real PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := LoadSource(context.Background(), archive, Options{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, start, boss int64 }{{4953, 306000458, 306000460}, {4978, 306000461, 306000463}} {
		start, err := r.Resolve(ResolveRequest{DungeonID: row.id, MazeIndex: 0, X: 0, Y: 0})
		if err != nil || start.Map.ID != row.start || start.Source != ResolutionMapSpecification {
			t.Fatalf("start %d: %+v %v", row.id, start, err)
		}
		boss, err := r.Resolve(ResolveRequest{DungeonID: row.id, MazeIndex: 0, X: 1, Y: 0})
		if err != nil || boss.Map.ID != row.boss {
			t.Fatalf("boss %d: %+v %v", row.id, boss, err)
		}
	}
	// Validate both the resolver and layout path: neither may replace a missing
	// or invalid declaration with a foreign map from the shared directory.
	pos := table.mapByID[306000458]
	original := table.maps[pos]
	for _, name := range []string{"missing_area", "zero_extent", "foreign_owner"} {
		t.Run(name, func(t *testing.T) {
			m := cloneMap(original)
			switch name {
			case "missing_area":
				m.DungeonStartArea = nil
			case "zero_extent":
				m.DungeonStartArea[2] = 0
			case "foreign_owner":
				m.DungeonIDs = []int64{4954}
			}
			table.maps[pos] = m
			defer func() { table.maps[pos] = original }()
			if _, err := r.Resolve(ResolveRequest{DungeonID: 4953, MazeIndex: 0}); !errors.Is(err, ErrMapReferenceMissing) {
				t.Fatalf("invalid declaration accepted: %v", err)
			}
			if _, err := BuildDungeonLayout(r, 4953, 0, nil); !errors.Is(err, ErrMapReferenceMissing) {
				t.Fatalf("layout fell back to foreign start: %v", err)
			}
		})
	}
	di := r.byDungeon[4953]
	index := di.mazes[0]
	key := coordinateKey{0, 0}
	saved := index.bySpecification[key]
	for _, name := range []string{"missing_declaration", "ambiguous_declaration"} {
		t.Run(name, func(t *testing.T) {
			if name == "missing_declaration" {
				index.bySpecification[key] = nil
			} else {
				index.bySpecification[key] = append(append([]indexedMap(nil), saved...), indexedMap{mapPos: table.mapByID[306000461], fileType: MapFileNormal})
			}
			defer func() { index.bySpecification[key] = saved }()
			if _, err := BuildDungeonLayout(r, 4953, 0, nil); !errors.Is(err, ErrMapReferenceMissing) {
				t.Fatalf("bad declaration fell through: %v", err)
			}
		})
	}
	for _, id := range []int64{4954, 4963, 4965} {
		d, _ := table.FindDungeon(id)
		index := r.byDungeon[id].mazes[0]
		key := coordinateKey{d.Mazes[0].Start.X, d.Mazes[0].Start.Y}
		if _, handled, _ := r.fiendWarDeclaredRoom(d, d.Mazes[0], index, key); handled {
			t.Fatal("other dungeon affected", id)
		}
	}
}
