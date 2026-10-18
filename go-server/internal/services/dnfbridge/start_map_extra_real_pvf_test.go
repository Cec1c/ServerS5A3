package dnfbridge

import (
	"context"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFTimeStonePassivePlanMatchesOrdinaryMaps(t *testing.T) {
	file := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if file == "" {
		t.Skip("matching real PVF required")
	}
	archive, err := platformpvf.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct{ dungeon, mapID int64 }{{310, 36765}, {313, 33753}} {
		dungeon, ok := table.FindDungeon(pair.dungeon)
		if !ok {
			t.Fatal("dungeon absent", pair.dungeon)
		}
		room, ok := table.FindMap(pair.mapID)
		if !ok {
			t.Fatal("map absent", pair.mapID)
		}
		scene := worldmap.DungeonRoomScene{Map: worldmap.ResolvedMap{Map: room}, SpecialPassiveObjects: room.SpecialPassiveObjects}
		plan, err := currentDungeonPVFPassiveItemPlan(dungeon, scene, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan) != 4 {
			t.Fatalf("dungeon=%d map=%d plan=%+v", pair.dungeon, pair.mapID, plan)
		}
		for _, row := range plan {
			if row.itemID != 10158700 {
				t.Fatalf("ordinary time stone identity=%d", row.itemID)
			}
		}
		t.Logf("dungeon=%d map=%d passive_objects=%d planned_time_stone_rows=%d", pair.dungeon, pair.mapID, len(room.SpecialPassiveObjects), len(plan))
	}
}
