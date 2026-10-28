package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

func TestRealPVFGeneratedEquipmentZeroUp(t *testing.T) {
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
	ref, found := rules.ItemReference(89)
	if !found || ref.ValueA != 6 || ref.ValueB != 0 {
		t.Fatalf("真实89级引用变化，需要重新核对：%+v found=%t", ref, found)
	}
	checkItem := func(id uint32, rarity int64) {
		t.Helper()
		item, err := catalog.ResolveItem(id)
		if err != nil || item.Kind != dungeonDropItemEquipment || item.Rarity > 3 || (rarity >= 0 && item.Rarity != rarity) {
			t.Fatalf("未选到预期自然装备：%+v err=%v", item, err)
		}
		body, err := a.ReadText(item.PVFPath)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := dnfpvf.Parse(item.PVFPath, body)
		if err != nil {
			t.Fatal(err)
		}
		minimum, ok := doc.Int("minimum level")
		if !ok || minimum < 83 || minimum >= 89 {
			t.Fatalf("真实装备逃出[83,89)窗口：item=%d minimum=%d", id, minimum)
		}
	}
	for rarity := int64(0); rarity <= 3; rarity++ {
		for _, seed := range []uint32{1, 2, 17, 99} {
			rng, equivalent := newCurrentDungeonDropLCG(seed), newCurrentDungeonDropLCG(seed)
			id, found, err := catalog.selectOrdinaryMonsterEquipment(rng, 89, ref.ValueA, ref.ValueB, rarity)
			if err != nil || !found {
				t.Fatalf("89级真实普通装备仍为空：rarity=%d found=%t err=%v", rarity, found, err)
			}
			checkItem(id, rarity)
			want, wantFound, wantErr := catalog.selectOrdinaryMonsterEquipment(equivalent, 88, 5, 1, rarity)
			if wantErr != nil || !wantFound || want != id || equivalent.Seed() != rng.Seed() {
				t.Fatalf("真实等价窗口不同：got=%d want=%d err=%v", id, want, wantErr)
			}
		}
	}
	t.Run("翻牌生成", func(t *testing.T) {
		now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
		for seed := uint32(1); seed <= 32; seed++ {
			item, found, err := currentDungeonRandomCardItem(newCurrentDungeonDropLCG(seed), catalog, 89, dungeonCardPVFItemDropReference{ValueA: ref.ValueA, ValueB: ref.ValueB}, now)
			if err != nil || !found || item.Stackable || item.Extra["item_kind"] != "equipment" || item.Extra["quality_seed"] == "" {
				t.Fatalf("89级翻牌未生成装备实例：%+v found=%t err=%v", item, found, err)
			}
			checkItem(uint32(item.ItemID), -1)
		}
	})
	for _, rank := range []string{"[normal]", "[boss]"} {
		t.Run(rank, func(t *testing.T) {
			s, r, repos := prepareDimensionDungeonDropTest(t)
			s.dungeonMonsterTable = monsters
			r.Dungeon.ID = 310
			r.Dungeon.Metadata.BasisLevel.Set, r.Dungeon.Metadata.BasisLevel.Value = true, 89
			r.Request.Difficulty, r.Character.Level = 4, 89
			monster := r.Room.Snapshot().Monsters[0]
			monster.Spawn.MonsterID, monster.Spawn.Level, monster.Spawn.AutoLevel, monster.Spawn.Rank = 71001, 1, 0, rank
			definition, found, err := monsters.Find(71001)
			if err != nil || !found {
				t.Fatalf("真实领主模板缺失：found=%t err=%v", found, err)
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
					if drop.Item.Kind == dungeonDropItemEquipment {
						if binary.LittleEndian.Uint32(wire.Item.data[2:6]) != drop.Item.ItemID {
							t.Fatal("装备地面投影不一致")
						}
						checkItem(drop.Item.ItemID, -1)
						equipment = drop
						t.Logf("89级%s自然生成装备：seed=%d item=%d", rank, seed, drop.Item.ItemID)
						break
					}
				}
			}
			if equipment == nil {
				t.Fatal("89级真实普通装备始终未生成")
			}
			session := &gameSession{selectedCharacterID: 99, conn: &bufferConn{}, dungeon: dungeonSessionState{runtime: r}}
			for i := 0; i < 2; i++ {
				if err := s.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(equipment.ObjectKey)); err != nil {
					t.Fatal(err)
				}
			}
			inventory, found, err := repos.Inventory.Load(context.Background(), "99")
			if err != nil || !found {
				t.Fatal("隔离库存读取失败", err)
			}
			var count int64
			for _, slot := range inventory.Slots {
				if slot.ItemID == int64(equipment.Item.ItemID) {
					count += slot.Count
				}
			}
			if count != 1 || equipment.Status != runtimeDungeonDropConsumed {
				t.Fatalf("重复拾取未保持一次入包：count=%d status=%v", count, equipment.Status)
			}
		})
	}
}
