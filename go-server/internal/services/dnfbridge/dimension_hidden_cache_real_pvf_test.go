package dnfbridge

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// The hidden actor belongs to a dungeon maze, while map eviction replaces map
// definitions. Compare both sides after real evictions, including caller-owned
// mutations, so a correct map payload alone cannot conceal a lost association.
func TestRealPVFDimensionHiddenIdentityAfterMapEviction(t *testing.T) {
	archive := real90DropArchive(t)
	full, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	dungeons := make(map[int64]worldmap.Dungeon)
	maps := make(map[int64]worldmap.Map)
	hidden := make(map[int64]int64)
	for dungeonID := int64(7165); dungeonID <= 7179; dungeonID++ {
		dungeon, ok := full.FindDungeon(dungeonID)
		if !ok {
			t.Fatalf("missing dungeon %d", dungeonID)
		}
		dungeons[dungeonID] = dungeon
		hiddenID := int64(107000256) + (dungeonID-7165)%3
		for _, maze := range dungeon.Mazes {
			for _, spec := range maze.MapSpecifications {
				for _, mapID := range spec.MapIDs {
					m, ok := full.FindMap(mapID)
					if !ok {
						t.Fatalf("missing map %d", mapID)
					}
					maps[mapID] = m
					for _, spawn := range m.Monsters {
						if spawn.MonsterID == hiddenID {
							if !maze.HiddenBoss {
								t.Fatalf("hidden actor %d in ordinary maze", hiddenID)
							}
							hidden[mapID] = hiddenID
						}
					}
				}
			}
		}
	}
	if len(hidden) != 15 || len(maps) <= len(hidden) {
		t.Fatalf("insufficient ordinary/hidden coverage: maps=%d hidden=%d", len(maps), len(hidden))
	}
	mapIDs := make([]int64, 0, len(maps))
	for id := range maps {
		mapIDs = append(mapIDs, id)
	}
	sort.Slice(mapIDs, func(i, j int) bool { return mapIDs[i] < mapIDs[j] })
	options := worldmap.DefaultMapCacheOptions()
	options.Capacity, options.PinnedBytes = 1, 0
	for _, mode := range []string{"postload-cache", "streaming-cache"} {
		t.Run(mode, func(t *testing.T) {
			var table *worldmap.Table
			if mode == "postload-cache" {
				table = full
				if err := table.EnableMapCache(archive, options); err != nil {
					t.Fatal(err)
				}
			} else {
				var err error
				table, err = worldmap.LoadSourceCached(context.Background(), archive, worldmap.Options{}, options)
				if err != nil {
					t.Fatal(err)
				}
			}
			for pass := 0; pass < 2; pass++ {
				for _, mapID := range mapIDs {
					got, ok := table.FindMap(mapID)
					if !ok || !reflect.DeepEqual(got, maps[mapID]) {
						t.Fatalf("map %d pass %d changed after eviction", mapID, pass)
					}
					// Mutating a query result must never poison later cold loads.
					got.Name = "mutated query result"
					if len(got.Monsters) > 0 {
						got.Monsters[0].MonsterID = -1
					}
				}
				for dungeonID := int64(7165); dungeonID <= 7179; dungeonID++ {
					t.Run(fmt.Sprintf("pass-%d/dungeon-%d", pass, dungeonID), func(t *testing.T) {
						got, ok := table.FindDungeon(dungeonID)
						if !ok || !reflect.DeepEqual(got, dungeons[dungeonID]) {
							t.Fatalf("dungeon maze identity changed: %d", dungeonID)
						}
						for i := range got.Mazes {
							got.Mazes[i].HiddenBoss = !got.Mazes[i].HiddenBoss
						}
						fresh, ok := table.FindDungeon(dungeonID)
						if !ok || !reflect.DeepEqual(fresh, dungeons[dungeonID]) {
							t.Fatalf("dungeon query leaked caller mutation: %d", dungeonID)
						}
					})
				}
			}
			snapshot := table.MapCacheSnapshot()
			if !snapshot.Enabled || snapshot.Pinned != 0 || snapshot.Entries > 1 || snapshot.Errors != 0 || snapshot.Evictions < uint64(len(mapIDs)) {
				t.Fatalf("test did not force successful repeated evictions: %+v", snapshot)
			}
			t.Logf("hidden maps=%d ordinary+hidden maps=%d cache=%+v", len(hidden), len(mapIDs), snapshot)
		})
	}
}
