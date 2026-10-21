package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

func TestOrdinaryMonsterEquipmentUsesDefinitionsAndKeepsLegacyPool(t *testing.T) {
	source := clearCardEquipmentTestSource()
	source[dungeonDropEquipmentDictionaryPath] = "1002 0 70 0 10001 100 `old dictionary`"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	// 旧词典只有70级；普通80级装备按真实模板的minimum level而非grade=86选择。
	id, found, err := catalog.selectOrdinaryMonsterEquipment(newCurrentDungeonDropLCG(1), 80, 0, 1, 2)
	if err != nil || !found || id != 1001 {
		t.Fatalf("高等级模板未参与普通杀怪：%d %t %v", id, found, err)
	}
	id, found, err = catalog.SelectGenericEquipment(newCurrentDungeonDropLCG(1), 70, 0, 1, 0)
	if err != nil || !found || id != 1002 {
		t.Fatalf("旧池消费者发生变化：%d %t %v", id, found, err)
	}
	_, found, err = catalog.SelectGenericEquipment(newCurrentDungeonDropLCG(1), 80, 0, 1, 2)
	if err != nil || found {
		t.Fatalf("深渊旧池回退被本次修复替换：%t %v", found, err)
	}
}

func TestOrdinaryMonsterEquipmentKeepsRarityAndNaturalEligibility(t *testing.T) {
	source := clearCardEquipmentTestSource()
	source["equipment/equipment.lst"] += "\n1004 `weapon/white.equ`\n1005 `weapon/epic.equ`\n1006 `weapon/legendary.equ`\n1007 `weapon/chronicle.equ`"
	base := source["equipment/weapon/test.equ"]
	for id, rarity := range map[int]int{1004: 0, 1005: 4, 1006: 6, 1007: 5} {
		name := map[int]string{1004: "white", 1005: "epic", 1006: "legendary", 1007: "chronicle"}[id]
		source["equipment/weapon/"+name+".equ"] = strings.Replace(base, "[rarity] 2", fmt.Sprintf("[rarity] %d", rarity), 1)
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, rarity := range []int64{-1, 4, 5, 6} {
		rng := newCurrentDungeonDropLCG(7)
		_, found, err := catalog.selectOrdinaryMonsterEquipment(rng, 80, 0, 1, rarity)
		if err != nil || found || rng.Seed() != 7 {
			t.Fatalf("专用装备进入普通池：rarity=%d found=%t err=%v", rarity, found, err)
		}
	}
	for seed := uint32(1); seed <= 32; seed++ {
		id, found, err := catalog.selectOrdinaryMonsterEquipment(newCurrentDungeonDropLCG(seed), 80, 0, 1, 2)
		if err != nil || !found || id != 1001 {
			t.Fatalf("零生成率/装扮/异品质模板混入：%d %t %v", id, found, err)
		}
		id, found, err = catalog.selectOrdinaryMonsterEquipment(newCurrentDungeonDropLCG(seed), 80, 0, 1, 3)
		if err != nil || !found || id != 1004 {
			t.Fatalf("缺品质时未按原规则回退自然白装：%d %t %v", id, found, err)
		}
	}
}

func TestOrdinaryMonsterEquipmentReusesWarmDefinitions(t *testing.T) {
	scans := 0
	source := clearCardScanSource{dungeonDropCatalogTestSource: clearCardEquipmentTestSource(), order: []string{"equipment/weapon/test.equ"}, scans: &scans}
	monsters, err := newPVFDungeonMonsterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	_ = clearCardEquipmentPoolWarmUpFields(monsters)
	catalog, err := monsters.DropCatalog()
	if err != nil {
		t.Fatal(err)
	}
	_, found, err := catalog.selectOrdinaryMonsterEquipment(newCurrentDungeonDropLCG(1), 80, 0, 1, 2)
	if err != nil || !found || scans != 1 {
		t.Fatalf("普通杀怪重复扫描模板：found=%t scans=%d err=%v", found, scans, err)
	}
}

func TestRealPVFOrdinaryMonsterEquipmentLevelsAndPickup(t *testing.T) {
	a := real90DropArchive(t)
	monsters, err := newPVFDungeonMonsterCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := monsters.DropCatalog()
	if err != nil {
		t.Fatal(err)
	}
	rules, err := currentDungeonMonsterDropRules(monsters)
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []int{5, 80, 85, 90} {
		ref, ok := rules.ItemReference(int64(level))
		if !ok {
			t.Fatal("缺真实等级引用", level)
		}
		id, found, err := catalog.selectOrdinaryMonsterEquipment(newCurrentDungeonDropLCG(1), level, ref.ValueA, ref.ValueB, 0)
		if err != nil || !found || id == 0 {
			t.Fatalf("%d级自然白装候选为空：%d %t %v", level, id, found, err)
		}
	}
	for _, rank := range []string{"[normal]", "[boss]"} {
		t.Run(rank, func(t *testing.T) {
			s, r, repos := prepareDimensionDungeonDropTest(t)
			s.dungeonMonsterTable = monsters
			r.Dungeon.ID = 310
			r.Dungeon.Metadata.BasisLevel.Set, r.Dungeon.Metadata.BasisLevel.Value = true, 90
			r.Request.Difficulty = 4
			r.Character.Level = 90
			monster := r.Room.Snapshot().Monsters[0]
			monster.Spawn.MonsterID, monster.Spawn.Level, monster.Spawn.Rank = 71001, 90, rank
			definition, found, err := monsters.Find(71001)
			if err != nil || !found {
				t.Fatal("缺真实诺尔妮模板", err)
			}
			monster.Definition = definition
			scene, _ := r.Session.Scene()
			visit, err := r.currentDungeonRoomVisit(scene)
			if err != nil {
				t.Fatal(err)
			}
			var equipment *runtimeDungeonDrop
			for seed := uint32(1); seed <= 10000 && equipment == nil; seed++ {
				visit.DropRNG, r.NextObjectKey, r.DropOwner = seed, 200, newRuntimeDungeonDropOwner()
				wires, err := s.planCurrentDungeonMonsterDrops(r, monster, 99)
				if err != nil {
					t.Fatal(err)
				}
				for _, wire := range wires {
					drop := r.DropOwner.byObjectKey[wire.SceneObjectKey]
					if drop.Item.Kind != dungeonDropItemEquipment {
						continue
					}
					if drop.Item.Rarity > 3 || binary.LittleEndian.Uint32(wire.Item.data[2:6]) != drop.Item.ItemID {
						t.Fatal("专属装备混入或物品投影不一致")
					}
					equipment = drop
					t.Logf("90级%s自然命中：种子%d、物品%d", rank, seed, drop.Item.ItemID)
					break
				}
			}
			if equipment == nil {
				t.Fatal("真实90级普通装备未生成")
			}
			session := &gameSession{selectedCharacterID: 99, conn: &bufferConn{}, dungeon: dungeonSessionState{runtime: r}}
			if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(equipment.ObjectKey)); err != nil {
				t.Fatal(err)
			}
			inventory, found, err := repos.Inventory.Load(context.Background(), "99")
			if err != nil || !found {
				t.Fatal(err)
			}
			var total int64
			for _, slot := range inventory.Slots {
				if slot.ItemID == int64(equipment.Item.ItemID) {
					total += slot.Count
				}
			}
			if total != 1 || equipment.Status != runtimeDungeonDropConsumed {
				t.Fatalf("90级装备拾取未入包：count=%d status=%v", total, equipment.Status)
			}
		})
	}
}
