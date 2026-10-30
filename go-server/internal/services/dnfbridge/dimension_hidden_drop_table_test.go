package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestDimensionHiddenDropTableRejectsMalformedRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"missing field", "0 10303668"},
		{"noninteger", "0 `item` 100"},
		{"negative", "0 10303668 -1"},
		{"threshold exceeds roll range", "0 10303668 10001"},
		{"duplicate tier item", "0 10303668 100 0 10303668 200"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := dnfpvf.Parse("dimensiondungeoninfo.etc", "[hidden boss drop table]\n"+tc.body+"\n[/hidden boss drop table]\n")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseCurrentDimensionHiddenDropTable(doc); err == nil {
				t.Fatal("malformed hidden boss drop table was accepted")
			}
		})
	}
}

func TestRealPVFDimensionHiddenDropTableIdentity(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	s, _, _ := newDungeonDailyCountTestService(t, path)
	doc, err := s.currentDimensionInfoDocument()
	if err != nil {
		t.Fatal(err)
	}
	byTier, err := parseCurrentDimensionHiddenDropTable(doc)
	if err != nil {
		t.Fatal(err)
	}
	for tier := 0; tier <= 2; tier++ {
		if len(byTier[tier]) != 11 {
			t.Fatalf("tier %d has %d rows, want 11", tier, len(byTier[tier]))
		}
	}
	if got := byTier[0][0]; got.ItemID != 10303668 || got.RawValue != 100 {
		t.Fatalf("first raw row = %+v", got)
	}
	if got := byTier[2][10]; got.ItemID != 100352761 || got.RawValue != 250 {
		t.Fatalf("last raw row = %+v", got)
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for tier, rows := range byTier {
		for _, row := range rows {
			if _, err := items.ResolveItem(row.ItemID); err != nil {
				t.Fatalf("tier %d item %d unresolved: %v", tier, row.ItemID, err)
			}
		}
	}
}

func TestDimensionHiddenDropSelectionUsesRoomStream(t *testing.T) {
	rng := newCurrentDungeonDropLCG(37)
	rows := []currentDimensionHiddenDropRow{
		{Tier: 2, ItemID: 10303668, RawValue: currentDungeonDropDenominator},
		{Tier: 2, ItemID: 100352756, RawValue: currentDungeonDropDenominator},
	}
	selected, err := currentDimensionHiddenDropItems(rows, rng)
	if err != nil || len(selected) != 2 || selected[0] != rows[0].ItemID || selected[1] != rows[1].ItemID || rng.Seed() == 37 {
		t.Fatalf("selected=%v seed=%d err=%v", selected, rng.Seed(), err)
	}
	if _, err := currentDimensionHiddenDropItems([]currentDimensionHiddenDropRow{{Tier: 2, ItemID: 10303668, RawValue: currentDungeonDropDenominator + 1}}, rng); err == nil {
		t.Fatal("out-of-range threshold was accepted")
	}
}

func TestRealPVFDimensionHiddenBossDropPlansPickup(t *testing.T) {
	archive := real90DropArchive(t)
	mapTable, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	monsterCatalog, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	dungeon, ok := mapTable.FindDungeon(7167) // PVF tier 2
	if !ok {
		t.Fatal("tier-2 dimension dungeon missing")
	}
	mazeIndex := -1
	var boss runtimeDungeonMonster
	for i, maze := range dungeon.Mazes {
		if !maze.HiddenBoss {
			continue
		}
		for _, spec := range maze.MapSpecifications {
			for _, mapID := range spec.MapIDs {
				m, found := mapTable.FindMap(mapID)
				if !found {
					t.Fatal(mapID)
				}
				for _, spawn := range m.Monsters {
					def, found, err := monsterCatalog.Find(spawn.MonsterID)
					if err != nil {
						t.Fatal(err)
					}
					if found && def.DimensionHiddenBoss {
						mazeIndex = i
						boss = runtimeDungeonMonster{Spawn: spawn, Definition: def}
						break
					}
				}
				if mazeIndex >= 0 {
					break
				}
			}
			if mazeIndex >= 0 {
				break
			}
		}
		if mazeIndex >= 0 {
			break
		}
	}
	if mazeIndex < 0 {
		t.Fatal("PVF hidden boss missing")
	}
	s, runtime, repositories := prepareDimensionDungeonDropTest(t)
	s.dungeonMonsterTable = monsterCatalog
	runtime.Dungeon = dungeon
	runtime.MazeIndex = mazeIndex
	runtime.dimensionHiddenHighReward = true
	runtime.Character.Level = 90
	runtime.Request.Difficulty = 2
	dimension, err := currentDungeonDimensionDrops(monsterCatalog)
	if err != nil {
		t.Fatal(err)
	}
	special := make(map[uint32]bool)
	for _, row := range dimension.HiddenBossByTier[2] {
		special[row.ItemID] = true
	}
	scene, _ := runtime.Session.Scene()
	visit, err := runtime.currentDungeonRoomVisit(scene)
	if err != nil {
		t.Fatal(err)
	}
	for seed := uint32(1); seed <= 64; seed++ {
		visit.DropRNG = seed
		wires, err := s.planCurrentDungeonMonsterDrops(runtime, boss, 99)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		for _, wire := range wires {
			itemID := binary.LittleEndian.Uint32(wire.Item.data[2:6])
			if !special[itemID] {
				continue
			}
			session := &gameSession{selectedCharacterID: 99, conn: &bufferConn{}, dungeon: dungeonSessionState{runtime: runtime}}
			if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(wire.SceneObjectKey)); err != nil {
				t.Fatal(err)
			}
			bag, _, err := repositories.Inventory.Load(context.Background(), "99")
			if err != nil {
				t.Fatal(err)
			}
			for _, stack := range bag.Slots {
				if stack.ItemID == int64(itemID) && stack.Count > 0 {
					return
				}
			}
			t.Fatalf("special drop %d was not picked up", itemID)
		}
	}
	t.Fatal("64 deterministic hidden-boss deaths yielded no PVF special item")
}
