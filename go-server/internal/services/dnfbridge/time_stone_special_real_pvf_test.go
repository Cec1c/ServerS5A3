package dnfbridge

import (
	"context"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// The six Special dungeon MAPs refer to item group 0. Group 1 alone leaves
// every concentrated Tera stone in these rooms without a materialized drop.
func TestRealPVFTimeStoneSpecialMapsAreGuaranteed(t *testing.T) {
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
	dungeon, found := table.FindDungeon(315)
	if !found {
		t.Fatal("Special dungeon 315 absent")
	}
	mapIDs := []int64{44107, 44113, 44116, 400219, 44110, 44119}
	total := 0
	for _, mapID := range mapIDs {
		room, found := table.FindMap(mapID)
		if !found {
			t.Fatalf("map %d absent", mapID)
		}
		scene := worldmap.DungeonRoomScene{
			Map:                   worldmap.ResolvedMap{Map: room},
			SpecialPassiveObjects: room.SpecialPassiveObjects,
		}
		wanted := 0
		for _, object := range room.SpecialPassiveObjects {
			if object.ObjectID >= 109000301 && object.ObjectID <= 109000303 {
				wanted++
			}
		}
		if wanted != 6 {
			t.Fatalf("map %d has %d Tera stones, want 6", mapID, wanted)
		}
		for _, seed := range []uint32{1, 17, 0xdeadbeef} {
			plan, err := currentDungeonPVFPassiveItemPlan(dungeon, scene, seed)
			if err != nil {
				t.Fatalf("map %d seed %d: %v", mapID, seed, err)
			}
			if len(plan) != wanted {
				t.Fatalf("map %d seed %d planned %d/%d stone drops", mapID, seed, len(plan), wanted)
			}
			for _, row := range plan {
				if row.itemID != 10158700 || row.objectIndex < 0 || row.objectIndex >= len(room.SpecialPassiveObjects) {
					t.Fatalf("map %d seed %d has wrong drop %+v", mapID, seed, row)
				}
			}
		}
		total += wanted
	}
	if total != 36 {
		t.Fatalf("Special dungeon materialized %d/36 stone drops", total)
	}
	t.Logf("Special dungeon: %d MAPs, %d breakable Tera stones, 100%% time stones", len(mapIDs), total)
}
