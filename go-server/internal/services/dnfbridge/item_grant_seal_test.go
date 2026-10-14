package dnfbridge

import (
	"encoding/binary"
	"os"
	"sort"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestNewEquipmentGrantKeepsPVFSealInDropAndInventory(t *testing.T) {
	for _, attach := range []string{"[sealing]", "`[SEALING]`", "sealing", "[trade]", "[free]", ""} {
		t.Run(attach, func(t *testing.T) {
			definition := dungeonDropItemDefinition{ItemID: 700, Kind: dungeonDropItemEquipment, PVFPath: "equipment/test_seal.equ", EquipmentType: "[weapon]", AttachType: attach, Durability: 50, SlotStart: 9, SlotEnd: 64}
			want := byte(0)
			if attach == "[sealing]" || attach == "`[SEALING]`" || attach == "sealing" {
				want = 1
			}
			const seed = uint32(345678901)
			ground, err := currentDungeonDeathDropItemState(21, definition, 1, seed)
			if err != nil {
				t.Fatal(err)
			}
			if ground.data[0x0D] != want || binary.LittleEndian.Uint32(ground.data[6:10]) != seed || binary.LittleEndian.Uint16(ground.data[0x0B:0x0D]) != 50 {
				t.Fatalf("地面装备封装/实例/耐久错误：seal=%d want=%d", ground.data[0x0D], want)
			}
			for _, route := range []string{"拾取及任务", "商店", "普通礼包发放"} {
				t.Run(route, func(t *testing.T) {
					record := dnfrepo.InventoryRecord{}
					var err error
					switch route {
					case "拾取及任务":
						_, err = addCurrentDungeonPickupToInventory(&record, definition, 1, seed)
					case "商店":
						_, err = grantCurrentCeraShopProduct(&record, definition, 1)
					case "普通礼包发放":
						_, err = grantCurrentBoosterReward(&record, nil, currentBoosterReward{Definition: definition, Count: 1})
					}
					if err != nil {
						t.Fatal(err)
					}
					stack := record.Slots["0:9"]
					entry := currentItemListEntryFromStack(0, 9, stack)
					if len(stack.RawEntry) != currentItemListEntryWireSize || stack.RawEntry[0x0D] != want || entry.data[0x0D] != want || currentItemListBindFlag(false, stack.Extra) != want {
						t.Fatalf("新装备封装标记未持久化：want=%d extra=%+v raw=%x", want, stack.Extra, stack.RawEntry)
					}
					if !validCurrentEquipmentQualitySeed(binary.LittleEndian.Uint32(stack.RawEntry[6:10])) || stack.Count != 1 || binary.LittleEndian.Uint16(stack.RawEntry[0x0B:0x0D]) != 50 {
						t.Fatal("修补封装改变了装备数量、实例种子或耐久")
					}
				})
			}
		})
	}
}

func TestSealInitializationDoesNotChangeStackablesOrStoredUnsealedItems(t *testing.T) {
	definition := dungeonDropItemDefinition{ItemID: 600, Kind: dungeonDropItemStackable, AttachType: "[sealing]", StackableType: "[waste]", StackLimit: 99, SlotStart: 65, SlotEnd: 120}
	stack, err := buildCurrentDungeonPickupStack(3, definition, 2)
	if err != nil || stack.RawEntry[0x0D] != 0 || stack.Extra["seal_flag"] != "" {
		t.Fatalf("堆叠品被错误封装：stack=%+v err=%v", stack, err)
	}
	stored := dnfrepo.ItemStack{ItemID: 700, Count: 1, RawEntry: make([]byte, currentItemListEntryWireSize), Extra: map[string]string{"item_kind": "equipment", "seal_removed_by_first_equip": "1", "quality_seed": "345678901"}}
	if entry := currentItemListEntryFromStack(0, 9, stored); entry.data[0x0D] != 0 || stored.Extra["seal_flag"] != "" {
		t.Fatal("库存投影重新封装了已解封实例")
	}
}

func TestNewCardEquipmentGrantKeepsPVFSeal(t *testing.T) {
	source := bridgePVFSource{"monster/monster.lst": "", "stackable/stackable.lst": "", "equipment/equipment.lst": "700 `test_seal.equ`\n", "equipment/test_seal.equ": "[equipment type] `[weapon]`\n[attach type] `[sealing]`\n[durability] 50\n"}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	// 固定候选只核对实例创建，不用这份夹具声明实际翻牌概率。
	catalog.clearCardEquipmentLoaded = true
	catalog.clearCardEquipment = make(map[dungeonDropGradeRarityKey][]dungeonDropWeightedItem)
	for rarity := int64(0); rarity <= 3; rarity++ {
		catalog.clearCardEquipment[dungeonDropGradeRarityKey{Grade: 1, Rarity: rarity}] = []dungeonDropWeightedItem{{ItemID: 700, Weight: 1}}
	}
	reward, found, err := currentDungeonRandomCardItem(newCurrentDungeonDropLCG(1), catalog, 1, dungeonCardPVFItemDropReference{ValueA: 0, ValueB: 1}, time.Now().UTC())
	if err != nil || !found || reward.ItemID != 700 || reward.Extra["seal_flag"] != "1" {
		t.Fatalf("翻牌新装备封装丢失：reward=%+v found=%t err=%v", reward, found, err)
	}
	stack := dnfrepo.ItemStack{ItemID: reward.ItemID, Count: reward.Count, Extra: reward.Extra}
	if entry := currentItemListEntryFromStack(0, 9, stack); entry.data[0x0D] != 1 {
		t.Fatal("翻牌装备刷新丢失封装标记")
	}
}

func TestNewTournamentEquipmentGrantKeepsPVFSeal(t *testing.T) {
	definition := dungeonDropItemDefinition{ItemID: 700, Kind: dungeonDropItemEquipment, PVFPath: "equipment/test_seal.equ", EquipmentType: "[weapon]", AttachType: "[sealing]", Durability: 50, SlotStart: 9, SlotEnd: 64}
	bundle, err := currentTournamentRewardBundle(tournamentCardReward{ItemID: 700, StackCount: 1}, &definition)
	if err != nil || len(bundle.Items) != 1 {
		t.Fatalf("锦标赛新装备发放失败：bundle=%+v err=%v", bundle, err)
	}
	reward := bundle.Items[0]
	stack := dnfrepo.ItemStack{ItemID: reward.ItemID, Count: reward.Count, Extra: reward.Extra}
	if reward.Extra["seal_flag"] != "1" || currentItemListEntryFromStack(0, 9, stack).data[0x0D] != 1 {
		t.Fatalf("锦标赛新装备封装丢失：reward=%+v", reward)
	}
}

func TestRealPVFNewEquipmentSealSurvivesPickupAndFullPageFallback(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("设置 DNFBRIDGE_REAL_PVF_SMOKE 后验证真实封装装备")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]uint32, 0)
	for id, ref := range catalog.itemRefs {
		if ref.kind == dungeonDropItemEquipment {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	checked := 0
	for _, id := range ids {
		definition, err := catalog.ResolveItem(id)
		if err != nil {
			t.Fatal(err)
		}
		if normalizeMagicBoxPVFType(definition.AttachType) != "sealing" || currentPVFItemTargetContainer(definition).ListType != 0 {
			continue
		}
		t.Logf("真实封装装备：item=%d path=%s attach_type=%s", id, definition.PVFPath, definition.AttachType)
		for _, full := range []bool{false, true} {
			record := dnfrepo.InventoryRecord{Slots: make(map[string]dnfrepo.ItemStack)}
			if full {
				for slot := int16(9); slot <= 64; slot++ {
					record.Slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
				}
			}
			const seed = uint32(345678901)
			ground, err := currentDungeonDeathDropItemState(21, definition, 1, seed)
			if err != nil || ground.data[0x0D] != 1 {
				t.Fatalf("真实装备地面封装丢失：item=%d err=%v", id, err)
			}
			slot, err := addCurrentDungeonPickupToInventory(&record, definition, 1, seed)
			want := uint16(9)
			if full {
				want = 3
			}
			if err != nil || slot != want {
				t.Fatalf("真实装备入包失败：item=%d slot=%d want=%d err=%v", id, slot, want, err)
			}
			stack := record.Slots[currentDungeonPickupMainSlotKey(int16(slot))]
			if stack.Extra["seal_flag"] != "1" || stack.RawEntry[0x0D] != 1 || currentItemListEntryFromStack(0, int16(slot), stack).data[0x0D] != 1 || binary.LittleEndian.Uint32(stack.RawEntry[6:10]) != seed {
				t.Fatalf("真实装备入包后封装/种子丢失：item=%d stack=%+v", id, stack)
			}
			assertEquipmentMailSealClaim(t, catalog, int64(id), sealMailClaimCase{sender: "0", metadata: map[string]string{"system_mail": "true"}, want: 1}, full)
		}
		checked++
		if checked == 3 {
			break
		}
	}
	if checked != 3 {
		t.Fatalf("当前PVF未找到3件可验证的真实封装主包装备：found=%d", checked)
	}
}
