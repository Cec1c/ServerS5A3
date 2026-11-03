package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestRealPVFConfiguredRadianceDropAndPickup(t *testing.T) {
	path := os.Getenv("DNF_RADIANCE_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNF_RADIANCE_PVF_SMOKE to the recipe candidate")
	}
	t.Setenv("DNF_WORLDMAP_REAL_PVF_SMOKE", path)
	a := real90DropArchive(t)
	s, r, repos := prepareCurrentDungeonDropTest(t, 1, 3227)
	s.options.currentClientVersion, s.options.pvfPath, s.initialEquipmentArchive = "official-24.9", path, a
	var err error
	s.dungeonMonsterTable, err = newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	special, err := currentDungeonSpecialDrops(s.dungeonMonsterTable)
	if err != nil {
		t.Fatal(err)
	}
	if len(special.MaterialByDungeon) != 8 {
		t.Fatal("unexpected configured dungeon scope", len(special.MaterialByDungeon))
	}
	text, err := a.ReadText("dungeon/dungeon.lst")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dnfpvf.Parse("dungeon/dungeon.lst", text)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[int64]string{}
	for _, entry := range dnfpvf.ParseList(doc) {
		paths[entry.ID] = entry.Path
	}
	scene, _ := r.Session.Scene()
	visit, _ := r.currentDungeonRoomVisit(scene)
	m := r.Room.Snapshot().Monsters[0]
	var total int64
	for _, tc := range []struct {
		id, monster int64
		name, rank  string
	}{
		{310, 71001, "时间广场", "[boss]"},
		{311, 63767, "兽人峡谷", "[boss]"},
		{312, 65736, "恐怖的栖息地", "[boss]"},
		{313, 65707, "疾风地带", "[boss]"},
		{314, 63796, "红色魔女之森", "[boss]"},
		{315, 63767, "血色防线", "[boss]"},
		{316, 63804, "魔界裂缝", "[boss]"},
		{7539, 107002063, "时空裂缝", "[normal]"},
		{7539, 107002064, "时空裂缝", "[boss]"},
	} {
		docPath, body, err := readDungeonDropText(a, "dungeon", paths[tc.id])
		if err != nil {
			t.Fatal(err)
		}
		doc, err := dnfpvf.Parse(docPath, body)
		if err != nil {
			t.Fatal(err)
		}
		r.Dungeon = worldmap.ParseDungeon(tc.id, docPath, doc)
		if r.Dungeon.Metadata.Name != tc.name {
			t.Fatal("dungeon identity", tc.id, r.Dungeon.Metadata.Name)
		}
		want := [4]int{0, 0, 0, 100000}
		if tc.id == 7539 {
			want = [4]int{10000, 10000, 10000, 100000}
		}
		rules := special.MaterialByDungeon[tc.id]
		if len(rules) != 1 || rules[0].ItemID != testRadianceItem || rules[0].Probability != want {
			t.Fatal("candidate rates", tc.id, rules)
		}
		m.Spawn.MonsterID, m.Spawn.Level, m.Spawn.Rank = tc.monster, r.Dungeon.Metadata.BasisLevel.Value, tc.rank
		definition, found, err := s.dungeonMonsterTable.Find(tc.monster)
		if err != nil || !found {
			t.Fatal("monster definition", tc.monster, err)
		}
		m.Definition = definition
		useRiftPipeline := tc.rank == "[boss]" && (tc.id == 316 || tc.id == 7539)
		if useRiftPipeline {
			if tc.id == 316 {
				r.riftLoot, err = s.prepareCurrentRiftLoot(3)
			} else {
				r.riftLoot, err = s.prepareCurrentTimeRiftLoot(r.Dungeon, 1)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		session := &gameSession{selectedCharacterID: 99, conn: &bufferConn{}, dungeon: dungeonSessionState{runtime: r}}
		var selected uint32
		for seed := uint32(0); seed < 2048 && selected == 0; seed++ {
			visit.DropRNG, r.NextObjectKey, r.DropOwner = seed, 200, newRuntimeDungeonDropOwner()
			var wires []currentDungeonDeathDropWire
			if useRiftPipeline {
				wires, err = s.planCurrentRiftBossDrops(session, r, scene)
			} else {
				wires, err = s.planCurrentDungeonMonsterDrops(r, m, 99)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, wire := range wires {
				if binary.LittleEndian.Uint32(wire.Item.data[2:6]) == testRadianceItem {
					if selected != 0 || binary.LittleEndian.Uint32(wire.Item.data[6:10]) != 1 {
						t.Fatal("material count")
					}
					selected = wire.SceneObjectKey
				}
			}
		}
		if selected == 0 {
			t.Fatal("no material generated", tc.id, tc.monster)
		}
		for attempt := 0; attempt < 2; attempt++ {
			if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(selected)); err != nil {
				t.Fatal(err)
			}
		}
		bag, found, err := repos.Inventory.Load(context.Background(), "99")
		if err != nil || !found {
			t.Fatal("inventory load", err)
		}
		var amount int64
		for _, stack := range bag.Slots {
			if stack.ItemID == int64(testRadianceItem) {
				amount += stack.Count
			}
		}
		total++
		if amount != total {
			t.Fatal("duplicate pickup", amount, total)
		}
		t.Logf("%s %s monster=%d: real definition, configured chance, ground item and idempotent pickup passed", tc.name, tc.rank, tc.monster)
	}
	for _, id := range []int64{309, 317, 5000, 7538, 7540} {
		if len(special.MaterialByDungeon[id]) != 0 {
			t.Fatal("unexpected outside rule", id)
		}
	}
}
