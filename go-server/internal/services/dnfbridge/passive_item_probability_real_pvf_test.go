package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFPassiveMaterialProbabilityAndGuaranteedTimeStone(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		dungeon int64
		item    uint32
		rate    int
	}{
		{101, 3341, 1500}, {102, 3341, 1500}, {103, 3341, 1500}, {104, 3341, 1500},
		{190, 10099829, 2600}, {191, 10099829, 2600}, {192, 10099829, 2600},
		{193, 10099829, 2600}, {194, 10099829, 2600}, {195, 10099829, 2600},
		{310, 10158700, 10000}, {312, 10158700, 10000}, {313, 10158700, 10000},
		{314, 10158700, 10000}, {315, 10158700, 10000}, {317, 10158700, 10000},
	} {
		t.Run(fmt.Sprint(tc.dungeon), func(t *testing.T) {
			dungeon, ok := table.FindDungeon(tc.dungeon)
			if !ok {
				t.Fatal("missing dungeon", tc.dungeon)
			}
			// A single eligible MAP draw isolates the real DGN's group-0 probability.
			// Complete real-MAP coverage below also checks actual building children.
			_, one := passiveProbabilityFixture(t, "0 -1 0", 1)
			count := 0
			for seed := uint32(0); seed < 10000; seed++ {
				plan, err := currentDungeonPVFPassiveItemPlan(dungeon, one, seed)
				if err != nil {
					t.Fatal(err)
				}
				if len(plan) > 1 {
					t.Fatal("extra draws", plan)
				}
				for _, row := range plan {
					if row.itemID != tc.item {
						t.Fatal("wrong item", row)
					}
					count++
				}
			}
			if tc.rate == 10000 {
				if count != 10000 {
					t.Fatal("time stone is not guaranteed", count)
				}
			} else if count < tc.rate-200 || count > tc.rate+200 {
				t.Fatalf("configured %d/10000: got %d/10000", tc.rate, count)
			}

			seen := map[int64]bool{}
			maps, attempts, dropped := 0, 0, 0
			for _, maze := range dungeon.Mazes {
				specs := append(append(append([]worldmap.MapSpecification{}, maze.MapSpecifications...), maze.BossSpecifications...), maze.LayeredSpecifications...)
				for _, spec := range specs {
					for _, id := range spec.MapIDs {
						if seen[id] {
							continue
						}
						seen[id] = true
						room, ok := table.FindMap(id)
						if !ok {
							t.Fatal("missing map", id)
						}
						draws := 0
						for _, object := range room.SpecialPassiveObjects {
							for _, spawn := range object.Spawns {
								if strings.Trim(strings.ToLower(spawn.Kind), "[] ") == "item" && spawn.Code == 0 && spawn.Level > 0 {
									draws += int(spawn.Level)
								}
							}
						}
						if draws == 0 {
							continue
						}
						maps++
						scene := worldmap.DungeonRoomScene{Map: worldmap.ResolvedMap{Map: room}, SpecialPassiveObjects: room.SpecialPassiveObjects}
						for seed := uint32(0); seed < 128; seed++ {
							plan, err := currentDungeonPVFPassiveItemPlan(dungeon, scene, seed)
							if err != nil {
								t.Fatal(err)
							}
							n := 0
							for _, row := range plan {
								if row.itemID == tc.item {
									n++
								}
							}
							if n > draws || (tc.rate == 10000 && n != draws) {
								t.Fatalf("map %d drops %d from %d eligible buildings", id, n, draws)
							}
							attempts += draws
							dropped += n
						}
					}
				}
			}
			if maps == 0 || attempts == 0 || dropped == 0 {
				t.Fatalf("no real building coverage: maps=%d draws=%d drops=%d", maps, attempts, dropped)
			}
			if tc.rate < 10000 && (dropped*10000/attempts < tc.rate-400 || dropped*10000/attempts > tc.rate+400) {
				t.Fatalf("real buildings: %d/%d, expected near %d/10000", dropped, attempts, tc.rate)
			}
			t.Logf("item=%d configured=%d/10000 samples=%d/10000 real_maps=%d building_drops=%d/%d", tc.item, tc.rate, count, maps, dropped, attempts)
		})
	}
}
