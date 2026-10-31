package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestRealPVFBirthBudAreaMaterialDropAndPickup(t *testing.T) {
	a := real90DropArchive(t)
	s, r, repos := prepareCurrentDungeonDropTest(t, 1, 3227)
	var err error
	s.dungeonMonsterTable, err = newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := s.dungeonMonsterTable.DropCatalog()
	if err != nil {
		t.Fatal(err)
	}
	item, err := catalog.ResolveItem(currentDungeonBirthBudItemID)
	if err != nil || item.Name != "诞生之芽" || normalizeDungeonDropStackableType(item.StackableType) != "[material]" {
		t.Fatalf("真实诞生之芽物品不匹配：%+v %v", item, err)
	}
	itemText, err := a.ReadText(item.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	itemDoc, err := dnfpvf.Parse(item.PVFPath, itemText)
	if err != nil {
		t.Fatal(err)
	}
	if rate, found := itemDoc.Int("creation rate"); found && rate != 0 {
		t.Fatal("真实区域材料新增了普通生成率，需要重新核对来源", rate)
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
		if entry.ID >= 310 && entry.ID <= 317 {
			paths[entry.ID] = entry.Path
		}
	}
	scene, _ := r.Session.Scene()
	visit, err := r.currentDungeonRoomVisit(scene)
	if err != nil {
		t.Fatal(err)
	}
	m := r.Room.Snapshot().Monsters[0]
	var total int64
	for _, tc := range []struct {
		id      int64
		monster int64
		name    string
	}{{310, 71001, "时间广场"}, {311, 63767, "兽人峡谷"}, {312, 65736, "恐怖的栖息地"}, {313, 65707, "疾风地带"}, {314, 63796, "红色魔女之森"}, {315, 71001, "血色防线"}} {
		docPath, body, err := readDungeonDropText(a, "dungeon", paths[tc.id])
		if err != nil {
			t.Fatal(err)
		}
		doc, err := dnfpvf.Parse(docPath, body)
		if err != nil {
			t.Fatal(err)
		}
		dungeon := worldmap.ParseDungeon(tc.id, docPath, doc)
		if dungeon.Metadata.Name != tc.name || !dungeon.Metadata.BasisLevel.Set {
			t.Fatalf("真实区域副本不匹配：%d %+v", tc.id, dungeon.Metadata)
		}
		r.Dungeon = dungeon
		r.Request.Difficulty = 4
		m.Spawn.MonsterID, m.Spawn.Level, m.Spawn.Rank = tc.monster, dungeon.Metadata.BasisLevel.Value, "[boss]"
		definition, found, err := s.dungeonMonsterTable.Find(tc.monster)
		if err != nil || !found {
			t.Fatal("缺真实领主模板", tc.monster, err)
		}
		m.Definition = definition
		var selected uint32
		for seed := uint32(0); seed < 256 && selected == 0; seed++ {
			visit.DropRNG, r.NextObjectKey, r.DropOwner = seed, 200, newRuntimeDungeonDropOwner()
			wires, err := s.planCurrentDungeonMonsterDrops(r, m, 99)
			if err != nil {
				t.Fatal(err)
			}
			for _, wire := range wires {
				if binary.LittleEndian.Uint32(wire.Item.data[2:6]) == currentDungeonBirthBudItemID {
					if selected != 0 || binary.LittleEndian.Uint32(wire.Item.data[6:10]) != 1 {
						t.Fatal("真实区域材料数量有误")
					}
					selected = wire.SceneObjectKey
				}
			}
		}
		if selected == 0 {
			t.Fatal("真实区域材料未进入掉落链", tc.id)
		}
		session := &gameSession{selectedCharacterID: 99, conn: &bufferConn{}, dungeon: dungeonSessionState{runtime: r}}
		for attempt := 0; attempt < 2; attempt++ {
			if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(selected)); err != nil {
				t.Fatal(err)
			}
		}
		bag, found, err := repos.Inventory.Load(context.Background(), "99")
		if err != nil || !found {
			t.Fatal("真实物品拾取后背包读取失败", err)
		}
		var amount int64
		for _, stack := range bag.Slots {
			if stack.ItemID == int64(currentDungeonBirthBudItemID) {
				amount += stack.Count
			}
		}
		total++
		if amount != total || r.DropOwner.byObjectKey[selected].Status != runtimeDungeonDropConsumed {
			t.Fatalf("%s真实材料重复拾取去重失败：%d，期望%d", tc.name, amount, total)
		}
		t.Logf("%s：真实PVF领主%d生成诞生之芽，地面登记、拾取保存及重复拾取通过", tc.name, tc.monster)
	}
	for _, id := range []int64{316, 317} {
		if paths[id] == "" {
			t.Fatal("缺真实范围外边界副本", id)
		}
		if _, ok := currentDungeonAreaMaterialPoolEntry(id); ok {
			t.Fatal("专用裂缝或任务图误入区域材料范围", id)
		}
	}
}
