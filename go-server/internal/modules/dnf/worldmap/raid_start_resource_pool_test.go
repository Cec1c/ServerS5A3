package worldmap

import (
	"context"
	p "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestRealPVFRaidStartPhysicalCoordinateResourceAndGuards(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires actual369d")
	}
	a, err := p.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := LoadSource(context.Background(), a, Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	top, err := BuildDungeonTopology(resolver, 219, 0)
	if err != nil {
		t.Fatal(err)
	}
	room, ok := top.Room(*top.Start)
	if !ok {
		t.Fatal("actual start absent")
	}
	maps, ok := resolver.raidStartResourcePool(219, 0, room)
	if !ok || len(maps) != 1 || maps[0].ID != 19206 {
		t.Fatal("actual unique same-owner start not resolved", maps, ok)
	}
	dp, mp := table.dungeonByID[219], table.mapByID[19206]
	originalD, originalM := table.dungeons[dp], table.maps[mp]
	for _, name := range []string{"ordinary", "multiple_maze", "quest", "foreign_owner", "shared_owner", "gate_mismatch", "physical_in_bounds", "ambiguous"} {
		t.Run(name, func(t *testing.T) {
			table.dungeons[dp] = cloneDungeon(originalD)
			table.maps[mp] = cloneMap(originalM)
			d, m := &table.dungeons[dp], &table.maps[mp]
			switch name {
			case "ordinary":
				d.SourceSections = nil
			case "multiple_maze":
				d.Mazes = append(d.Mazes, d.Mazes[0])
			case "quest":
				d.Mazes[0].QuestConnection = []int64{1}
			case "foreign_owner":
				m.DungeonID.Value = 218
			case "shared_owner":
				m.DungeonIDs = append(m.DungeonIDs, 218)
			case "gate_mismatch":
				m.Greed = "CC CC"
			case "physical_in_bounds":
				m.Path = "map/219_BlackVolcano/19206(1,0)S.map"
			}
			r, err := NewResolver(table)
			if err != nil {
				t.Fatal(err)
			}
			if name == "ambiguous" {
				i := r.byDungeon[219]
				i.mazes[0].byOwnership[coordinateKey{7, 0}] = []indexedMap{{mapPos: table.mapByID[19205], fileType: MapFileStart}}
				r.byDungeon[219] = i
			}
			if _, ok := r.raidStartResourcePool(219, 0, room); ok {
				t.Fatal("unsafe fallback accepted", name)
			}
		})
	}
	table.dungeons[dp] = originalD
	table.maps[mp] = originalM
	r, _ := NewResolver(table)
	if _, ok := r.raidStartResourcePool(219, 0, DungeonRoom{Coordinate: room.Coordinate, Start: true, Boss: true, Neighbors: room.Neighbors}); ok {
		t.Fatal("boss fallback accepted")
	}
}
