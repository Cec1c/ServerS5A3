package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Actual PVF marks named monsters in the MOB, while their MAP spawn rank is
// normal. Do not replace that rank in the fixture: it is the regression trigger.
func TestDimensionImageRulesRealNamedAndBoss(t *testing.T) {
	archive := real90DropArchive(t)
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	dimension, err := currentDungeonDimensionDrops(catalog)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[uint32]bool{}
	career := dimension.ChronicleByCareer[currentDungeonDimensionChronicleKey{Job: 0, GrowType: 1}]
	for _, pool := range [][]uint32{career.Normal, career.Set} {
		for _, id := range pool {
			allowed[id] = true
		}
	}
	cases := []struct {
		id   int64
		tier int
		path string
		boss bool
	}{
		{7100, 0, "map/dimension/impossible02.map", false},
		{7100, 1, "map/dimension/impossible02.map", false},
		{7100, 2, "map/dimension/impossible02.map", false},
		{7100, 0, "map/dimension/impossible06.map", true},
		{7100, 1, "map/dimension/impossible06.map", true},
		{7100, 2, "map/dimension/impossible06.map", true},
	}
	// Cover both special ranks in all fifteen PVF internal variants, selecting
	// actual map spawns without changing their rank or named flag.
	for id := int64(7165); id <= 7179; id++ {
		dungeon, ok := table.FindDungeon(id)
		if !ok {
			t.Fatal(id)
		}
		for _, boss := range []bool{false, true} {
			chosenPath := ""
			for _, maze := range dungeon.Mazes {
				for _, spec := range maze.MapSpecifications {
					for _, mapID := range spec.MapIDs {
						m, found := table.FindMap(mapID)
						if !found {
							t.Fatal(mapID)
						}
						for _, spawn := range m.Monsters {
							def, found, err := catalog.Find(spawn.MonsterID)
							if err != nil || !found {
								t.Fatal(spawn.MonsterID, err)
							}
							actor, _ := currentDungeonMonsterActorType(spawn)
							if (boss && actor == 3) || (!boss && actor != 3 && def.Scalars["named_monster"] == 1) {
								chosenPath = m.Path
								break
							}
						}
						if chosenPath != "" {
							break
						}
					}
					if chosenPath != "" {
						break
					}
				}
				if chosenPath != "" {
					break
				}
			}
			if chosenPath == "" {
				t.Fatalf("no real special monster: %d boss=%t", id, boss)
			}
			cases = append(cases, struct {
				id   int64
				tier int
				path string
				boss bool
			}{id, int((id - 7165) % 3), chosenPath, boss})
		}
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d/tier%d/boss%t", tc.id, tc.tier, tc.boss), func(t *testing.T) {
			text, err := archive.ReadText(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := worldmap.ParseDocument(tc.path, text)
			if err != nil {
				t.Fatal(err)
			}
			realMap := worldmap.ParseMap(1, tc.path, doc)
			dungeon, ok := table.FindDungeon(tc.id)
			if !ok {
				t.Fatal(tc.id)
			}
			var chosen *runtimeDungeonMonster
			for _, spawn := range realMap.Monsters {
				definition, found, err := catalog.Find(spawn.MonsterID)
				if err != nil || !found {
					t.Fatal(spawn.MonsterID, err)
				}
				named := false
				for _, section := range definition.Sections {
					if section == "is named monster" {
						named = true
					}
				}
				actor, _ := currentDungeonMonsterActorType(spawn)
				if (tc.boss && actor == 3) || (!tc.boss && named) {
					m := runtimeDungeonMonster{Spawn: spawn, Definition: definition}
					chosen = &m
					break
				}
			}
			if chosen == nil {
				t.Fatal("real special monster missing", tc.path)
			}
			for _, seed := range []uint32{1, 17, 99991} {
				s, r, repos := prepareDimensionDungeonDropTest(t)
				s.dungeonMonsterTable = catalog
				r.Dungeon = dungeon
				r.Character.Level = 90
				r.Character.Job = "0"
				r.Character.Stats["grow_type"] = 1
				r.Request.Difficulty = uint8(tc.tier)
				scene, _ := r.Session.Scene()
				visit, err := r.currentDungeonRoomVisit(scene)
				if err != nil {
					t.Fatal(err)
				}
				visit.DropRNG = seed
				wires, err := s.planCurrentDungeonMonsterDrops(r, *chosen, 99)
				if err != nil {
					t.Fatal(err)
				}
				got := map[uint32]uint32{}
				rows := map[uint32]int{}
				equipment := 0
				session := &gameSession{selectedCharacterID: 99, conn: &bufferConn{}, dungeon: dungeonSessionState{runtime: r}}
				for _, wire := range wires {
					id := binary.LittleEndian.Uint32(wire.Item.data[2:6])
					if allowed[id] {
						equipment++
					}
					if id != 3311 && id != 10303569 {
						continue
					}
					got[id] += binary.LittleEndian.Uint32(wire.Item.data[6:10])
					rows[id]++
					for retry := 0; retry < 2; retry++ {
						if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(wire.SceneObjectKey)); err != nil {
							t.Fatal(err)
						}
					}
				}
				fragments, memory := uint32(7), [3]uint32{0, 4, 5}[tc.tier]
				if tc.boss {
					fragments = 28
					memory = [3]uint32{0, 16, 28}[tc.tier]
				}
				if got[3311] != fragments || got[10303569] != memory || rows[3311] != 1 || rows[10303569] > 1 || equipment != 1 {
					t.Fatalf("seed=%d spawnRank=%s fragments=%v rows=%v careerEquipment=%d want=%d/%d/1", seed, chosen.Spawn.Rank, got, rows, equipment, fragments, memory)
				}
				bag, _, err := repos.Inventory.Load(context.Background(), "99")
				if err != nil {
					t.Fatal(err)
				}
				counts := map[int64]int64{}
				for _, stack := range bag.Slots {
					counts[stack.ItemID] += stack.Count
				}
				if counts[3311] != int64(fragments) || counts[10303569] != int64(memory) {
					t.Fatal("pickup/replay", counts)
				}
			}
		})
	}
}
