package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 地下城、领主、物品与制作材料编号取真实 PVF；房间和资产写入使用隔离内存仓储。
func TestRealPVFCarbonCrystalBossDropAndPickup(t *testing.T) {
	a := real90DropArchive(t)
	s, r, repos := prepareCurrentDungeonDropTest(t, 0, 3227)
	var err error
	s.dungeonMonsterTable, err = newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	const dgnPath = "dungeon/cataclysm/powerstation/grandine_station_a.dgn"
	text, err := a.ReadText(dgnPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := dnfpvf.Parse(dgnPath, text)
	if err != nil {
		t.Fatal(err)
	}
	dungeon := worldmap.ParseDungeon(r.Dungeon.ID, dgnPath, doc)
	if !dungeon.Metadata.BasisLevel.Set || dungeon.Metadata.BasisLevel.Value != 82 || dungeon.Metadata.NoFatigue {
		t.Fatal("真实格蓝迪等级或疲劳规则变化")
	}
	r.Dungeon.Metadata, r.Dungeon.Path = dungeon.Metadata, dungeon.Path

	text, err = a.ReadText("map/map.lst")
	if err != nil {
		t.Fatal(err)
	}
	doc, err = dnfpvf.Parse("map/map.lst", text)
	if err != nil {
		t.Fatal(err)
	}
	var mapPath string
	for _, entry := range dnfpvf.ParseList(doc) {
		if entry.ID == 53224 {
			mapPath = entry.Path
			break
		}
	}
	if mapPath == "" {
		t.Fatal("真实格蓝迪领主地图53224缺失")
	}
	mapPath, text, err = readDungeonDropText(a, "map", mapPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err = dnfpvf.Parse(mapPath, text)
	if err != nil {
		t.Fatal(err)
	}
	m := r.Room.Snapshot().Monsters[0]
	foundBoss := false
	for _, spawn := range worldmap.ParseMap(53224, mapPath, doc).Monsters {
		definition, found, err := s.dungeonMonsterTable.Find(spawn.MonsterID)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			continue
		}
		m.Spawn, m.Definition = spawn, definition
		if currentDungeonOrdinaryMonsterLooksBoss(m) {
			foundBoss = true
			break
		}
	}
	if !foundBoss {
		t.Fatal("真实格蓝迪地图未找到领主")
	}
	text, err = a.ReadText(currentEpicProductionPVFPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err = dnfpvf.Parse(currentEpicProductionPVFPath, text)
	if err != nil {
		t.Fatal(err)
	}
	need := doc.Ints("must need item")
	if len(need) != 2 || need[0] != int64(currentDungeonCarbonCrystalItemID) || need[1] != 1 {
		t.Fatalf("真实制作必需材料不匹配：%v", need)
	}
	wires, err := s.planCurrentDungeonMonsterDrops(r, m, 99)
	if err != nil {
		t.Fatal(err)
	}
	rows := carbonCrystalTestWires(wires)
	if len(rows) != 1 || binary.LittleEndian.Uint32(rows[0].Item.data[6:10]) != 1 {
		t.Fatalf("真实领主未固定产出一个碳结晶体：%d条", len(rows))
	}
	registered := r.DropOwner.byObjectKey[rows[0].SceneObjectKey]
	if registered == nil || registered.Item.StackableType != "[material]" || registered.Amount != 1 {
		t.Fatal("真实材料未注册到地面账本")
	}
	session := &gameSession{selectedCharacterID: 99, conn: &bufferConn{}, dungeon: dungeonSessionState{runtime: r}}
	for i := 0; i < 2; i++ {
		if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(rows[0].SceneObjectKey)); err != nil {
			t.Fatal(err)
		}
	}
	inventory, found, err := repos.Inventory.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("背包读取失败：%v", err)
	}
	var count int64
	for _, stack := range inventory.Slots {
		if stack.ItemID == int64(currentDungeonCarbonCrystalItemID) {
			count += stack.Count
		}
	}
	if count != 1 || registered.Status != runtimeDungeonDropConsumed {
		t.Fatalf("真实材料拾取入库或去重失败，数量%d 状态%v", count, registered.Status)
	}
	r.Dungeon.Metadata.NoFatigue = true
	wires, err = s.planCurrentDungeonMonsterDrops(r, m, 99)
	if err != nil || len(carbonCrystalTestWires(wires)) != 0 {
		t.Fatalf("疲劳豁免后仍产出碳结晶体：%v", err)
	}
	t.Logf("真实格蓝迪Lv%d、领主%d、材料%d：固定产出、拾取保存、重复拾取和疲劳豁免通过", dungeon.Metadata.BasisLevel.Value, m.Spawn.MonsterID, currentDungeonCarbonCrystalItemID)
}
