package dnfbridge

import (
	"context"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Check every real ordinary/hidden maze, including cached reads. Rank and the
// map's boss designation are not sufficient to identify Iflius.
func TestRealPVFDimensionHiddenMonsterIdentity(t *testing.T) {
	archive := real90DropArchive(t)
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	hiddenMaps := map[int64]bool{}
	for dungeonID := int64(7165); dungeonID <= 7179; dungeonID++ {
		t.Run(fmt.Sprint(dungeonID), func(t *testing.T) {
			dungeon, ok := table.FindDungeon(dungeonID)
			if !ok {
				t.Fatalf("missing dungeon %d", dungeonID)
			}
			hiddenID := int64(107000256) + (dungeonID-7165)%3
			seenHidden, seenOrdinary := false, false
			for _, maze := range dungeon.Mazes {
				for _, spec := range maze.MapSpecifications {
					for _, mapID := range spec.MapIDs {
						m, ok := table.FindMap(mapID)
						if !ok {
							t.Fatalf("missing map %d", mapID)
						}
						for _, spawn := range m.Monsters {
							want := spawn.MonsterID == hiddenID
							for read := 0; read < 2; read++ {
								def, ok, err := catalog.Find(spawn.MonsterID)
								if err != nil || !ok {
									t.Fatalf("monster %d: found=%t err=%v", spawn.MonsterID, ok, err)
								}
								if def.DimensionHiddenBoss != want {
									t.Fatalf("map=%d monster=%d read=%d hidden=%t want=%t", mapID, spawn.MonsterID, read, def.DimensionHiddenBoss, want)
								}
							}
							if want {
								if !maze.HiddenBoss {
									t.Fatalf("hidden monster %d in ordinary maze", hiddenID)
								}
								hiddenMaps[mapID] = true
								seenHidden = true
							} else {
								seenOrdinary = true
							}
						}
					}
				}
			}
			if !seenHidden || !seenOrdinary {
				t.Fatalf("missing comparison: hidden=%t ordinary=%t", seenHidden, seenOrdinary)
			}
		})
	}
	if len(hiddenMaps) != 15 {
		t.Fatalf("hidden maps=%d want=15", len(hiddenMaps))
	}
}
