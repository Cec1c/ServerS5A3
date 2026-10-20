package dnfbridge

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 材料先叠加同物品，新开格优先所属页，页满才使用快捷格。
func TestMaterialPlacementUsesCategoryForPickupAndShop(t *testing.T) {
	for _, family := range []string{"[material]", "`[MATERIAL]`", "[material] 4", "[material instant item]", "[material expert job]"} {
		start, end := dungeonDropStackableSlotRange(family)
		definition := dungeonDropItemDefinition{ItemID: 3175, Kind: dungeonDropItemStackable,
			PVFPath: "stackable/material/test.stk", StackableType: family, StackLimit: 99, SlotStart: start, SlotEnd: end}
		for _, route := range []string{"pickup", "shop"} {
			t.Run(family+"/"+route, func(t *testing.T) {
				grant := func(record *dnfrepo.InventoryRecord, amount uint32) error {
					if route == "pickup" {
						_, err := addCurrentDungeonPickupToInventory(record, definition, amount)
						return err
					}
					_, err := grantCurrentCeraShopProduct(record, definition, amount)
					return err
				}
				t.Run("empty_quick_slots", func(t *testing.T) {
					record := dnfrepo.InventoryRecord{}
					if err := grant(&record, 2); err != nil {
						t.Fatal(err)
					}
					if got := record.Slots[currentDungeonPickupMainSlotKey(start)]; got.ItemID != 3175 || got.Count != 2 || len(record.Slots) != 1 {
						t.Fatalf("材料未进入所属分类页：%+v", record.Slots)
					}
				})
				t.Run("merge_existing_quick_stack_before_category", func(t *testing.T) {
					key := currentDungeonPickupMainSlotKey(start)
					record := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{"0:3": {ItemID: 3175, Count: 5}, key: {ItemID: 3175, Count: 7}}}
					if err := grant(&record, 2); err != nil {
						t.Fatal(err)
					}
					if record.Slots["0:3"].Count != 7 || record.Slots[key].Count != 7 || len(record.Slots) != 2 {
						t.Fatalf("材料应先并入已有快捷格同物品：%+v", record.Slots)
					}
				})
				t.Run("full_category_falls_back_to_empty_quick_slot", func(t *testing.T) {
					record := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{}}
					for slot := start; slot <= end; slot++ {
						record.Slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 99}
					}
					before := dnfrepo.CloneInventory(record)
					if err := grant(&record, 1); err != nil {
						t.Fatal(err)
					}
					if record.Slots["0:3"].ItemID != 3175 || record.Slots["0:3"].Count != 1 {
						t.Fatal("材料满页未回落空快捷格")
					}
					delete(record.Slots, "0:3")
					if !reflect.DeepEqual(record, before) {
						t.Fatal("回落时改变了已有分类页")
					}
					for slot := int16(3); slot <= 8; slot++ {
						record.Slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 99}
					}
					before = dnfrepo.CloneInventory(record)
					if err := grant(&record, 1); !errors.Is(err, errDungeonPickupInventoryFull) {
						t.Fatalf("两处皆满时应拒绝：%v", err)
					}
					if !reflect.DeepEqual(record, before) {
						t.Fatal("两处皆满时改变背包")
					}
				})
			})
		}
	}
}

func TestMailboxAndPickupShareQuickSlotEligibility(t *testing.T) {
	for _, family := range []string{"[waste]", "[booster]", "[etc]", "[material]", "[material] 4", "[material instant item]", "[material expert job]", "[quest]", "[avatar emblem]", "[feed]", "[creature]", "[flag gem]"} {
		t.Run(family, func(t *testing.T) {
			start, end := dungeonDropStackableSlotRange(family)
			definition := dungeonDropItemDefinition{ItemID: 3175, Kind: dungeonDropItemStackable, StackableType: family, StackLimit: 99, SlotStart: start, SlotEnd: end}
			mail, err := mailboxItemResolutionFromPVF(definition, false)
			if err != nil {
				t.Fatal(err)
			}
			pickup := currentDungeonPickupPlacement(definition, 1)
			if mail.PreferQuickSlot != pickup.Definition.PreferQuickSlot || mail.AllowQuickSlots != pickup.Definition.AllowQuickSlots {
				t.Fatalf("邮件与拾取快捷栏资格不同：mail=%t pickup=%t", mail.PreferQuickSlot, pickup.Definition.PreferQuickSlot)
			}
		})
	}
}

func TestConsumablePlacementPrefersQuickSlotsAndEquipmentUsesBag(t *testing.T) {
	for _, family := range []string{"[waste]", "[booster]", "[etc]", "[recipe]", "[enchant waste]"} {
		t.Run(family, func(t *testing.T) {
			definition := dungeonDropItemDefinition{ItemID: 600, Kind: dungeonDropItemStackable, StackableType: family, StackLimit: 99, SlotStart: 65, SlotEnd: 120}
			for _, full := range []bool{false, true} {
				record := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{}}
				want := uint16(3)
				if full {
					want = 65
					for slot := int16(3); slot <= 8; slot++ {
						record.Slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
					}
				}
				slot, err := addCurrentDungeonPickupToInventory(&record, definition, 1)
				if err != nil || slot != want {
					t.Fatalf("消耗品落格=%d，期望=%d，err=%v", slot, want, err)
				}
				slots, err := grantCurrentCeraShopProduct(&record, definition, 2)
				if err != nil || len(slots) != 1 || slots[0] != want || record.Slots[currentDungeonPickupMainSlotKey(int16(want))].Count != 3 {
					t.Fatalf("购买应叠加至已有消耗品：slots=%v err=%v", slots, err)
				}
			}
		})
	}
	record := dnfrepo.InventoryRecord{}
	definition := dungeonDropItemDefinition{ItemID: 700, Kind: dungeonDropItemEquipment, SlotStart: 9, SlotEnd: 64}
	slot, err := addCurrentDungeonPickupToInventory(&record, definition, 1)
	if err != nil || slot != 9 {
		t.Fatalf("装备落格=%d，期望装备页 9，err=%v", slot, err)
	}
	mail, err := mailboxItemResolutionFromPVF(definition, false)
	if err != nil || mail.PreferQuickSlot {
		t.Fatalf("装备邮件错误获得快捷栏资格：%+v err=%v", mail, err)
	}
}

func TestEquipmentPlacementUsesBagThenQuickSlots(t *testing.T) {
	definition := dungeonDropItemDefinition{ItemID: 700, Kind: dungeonDropItemEquipment, EquipmentType: "[weapon]", SlotStart: 9, SlotEnd: 64}
	for _, route := range []string{"拾取及任务", "商店", "礼盒"} {
		for mode := 0; mode < 3; mode++ {
			t.Run(route+"/"+strconv.Itoa(mode), func(t *testing.T) {
				record := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{"0:3": {ItemID: 700, Count: 1, Extra: map[string]string{"item_kind": "equipment"}}}}
				want := int16(9)
				if mode > 0 {
					want = 4
					for slot := int16(9); slot <= 64; slot++ {
						record.Slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
					}
				}
				if mode == 2 {
					for slot := int16(4); slot <= 8; slot++ {
						record.Slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
					}
				}
				before := dnfrepo.CloneInventory(record)
				var err error
				switch route {
				case "拾取及任务":
					_, err = addCurrentDungeonPickupToInventory(&record, definition, 1)
				case "商店":
					_, err = grantCurrentCeraShopProduct(&record, definition, 1)
				case "礼盒":
					_, err = grantCurrentBoosterReward(&record, nil, currentBoosterReward{Definition: definition, Count: 1})
				}
				if mode == 2 {
					if !errors.Is(err, errDungeonPickupInventoryFull) || !reflect.DeepEqual(record, before) {
						t.Fatalf("两处皆满必须拒绝且不改背包：err=%v", err)
					}
					return
				}
				if err != nil || record.Slots[currentDungeonPickupMainSlotKey(want)].ItemID != 700 || record.Slots[currentDungeonPickupMainSlotKey(want)].Count != 1 {
					t.Fatalf("装备落点错误：want=%d slots=%+v err=%v", want, record.Slots, err)
				}
				delete(record.Slots, currentDungeonPickupMainSlotKey(want))
				if !reflect.DeepEqual(record, before) {
					t.Fatal("入包合并了已有装备或改变了其他格")
				}
			})
		}
	}
	mail, err := mailboxItemResolutionFromPVF(definition, false)
	if err != nil || !mail.AllowQuickSlots || mail.PreferQuickSlot || !currentDungeonPickupPlacement(definition, 1).Definition.AllowQuickSlots {
		t.Fatalf("装备邮件与拾取没有共同满栏回落资格：mail=%+v err=%v", mail, err)
	}
	for _, family := range []string{"[creature]", "[artifact red]", "[coat avatar]", "[flag]"} {
		definition.EquipmentType = family
		if policy := currentPVFItemPlacementPolicy(definition); policy.AllowQuickSlots || policy.PreferQuickSlots || policy.ListType == 0 {
			t.Fatalf("独立容器装备错误获得主包快捷栏资格：family=%s policy=%+v", family, policy)
		}
	}
}

// 使用现场买入的物品及当前 PVF，通过真实购买处理器检查扣款、落库和 ACK/op14 槽位。
func TestRealPVFObservedMaterialPurchaseUsesMaterialTab(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("设置 DNFBRIDGE_REAL_PVF_SMOKE 后核对现场材料")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	shop, err := newCurrentNPCShopCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, itemID := range []uint32{3175, 3161} {
		t.Run(strconv.FormatUint(uint64(itemID), 10), func(t *testing.T) {
			definition, err := items.ResolveItem(itemID)
			if err != nil {
				t.Fatal(err)
			}
			text, err := archive.ReadText(definition.PVFPath)
			if err != nil {
				t.Fatal(err)
			}
			document, err := dnfpvf.Parse(definition.PVFPath, text)
			if err != nil {
				t.Fatal(err)
			}
			name, _ := document.Text("name")
			t.Logf("现场物品 %d：名称=%s，家族=%s，路径=%s，分类槽位=%d..%d", itemID, name, definition.StackableType, definition.PVFPath, definition.SlotStart, definition.SlotEnd)
			if definition.SlotStart != 121 || definition.SlotEnd != 176 {
				t.Fatalf("现场材料分类改变：%+v", definition)
			}
			pricing, err := resolveCurrentNPCShopPricing(shop, items, itemID)
			if err != nil || !pricing.Buyable || pricing.MaterialExchange {
				t.Fatalf("购买定价=%+v err=%v", pricing, err)
			}
			for mode := 0; mode < 3; mode++ {
				full := mode > 0
				t.Run("mode="+strconv.Itoa(mode), func(t *testing.T) {
					slots := map[string]dnfrepo.ItemStack{}
					if full {
						for slot := int16(121); slot <= 176; slot++ {
							slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 99}
						}
					}
					if mode == 2 {
						for slot := int16(3); slot <= 8; slot++ {
							slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 99}
						}
					}
					repositories := mustCurrentNPCShopRepositories(t, 1000, slots)
					service := currentNPCShopTestService(repositories, shop, items)
					connection := &bufferConn{}
					session := &gameSession{conn: connection, connID: "material-placement", selectedCharacterID: 19}
					if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketBuyItem), currentNPCShopBuyTestBody(itemID, 1, 1, 0)); err != nil {
						t.Fatal(err)
					}
					ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
					inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
					character, _, _ := repositories.Character.Load(context.Background(), "19")
					if mode == 2 {
						if ack.Body[0] != 0 || character.Stats["gold"] != 1000 || !reflect.DeepEqual(inventory.Slots, slots) {
							t.Fatalf("满包必须拒绝且不扣款：ACK=%x gold=%d inventory=%+v", ack.Body, character.Stats["gold"], inventory.Slots)
						}
						return
					}
					wantSlot := int16(121)
					if mode == 1 {
						wantSlot = 3
					}
					if ack.Body[0] != 1 || inventory.Slots[currentDungeonPickupMainSlotKey(wantSlot)].ItemID != int64(itemID) || inventory.Slots[currentDungeonPickupMainSlotKey(wantSlot)].Count != 1 || character.Stats["gold"] != 1000-pricing.BuyGold {
						t.Fatalf("购买未写入材料页：ACK=%x inventory=%+v gold=%d", ack.Body, inventory.Slots, character.Stats["gold"])
					}
					update, trailing := splitGameServerUpperPacket(t, rest)
					if update.Header.MsgID != 14 || len(trailing) != 0 {
						t.Fatalf("物品刷新包不匹配：%+v trailing=%d", update.Header, len(trailing))
					}
					assertCurrentNPCShopIncrementalRows(t, update.Body, uint32(1000-pricing.BuyGold), wantSlot, itemID, 1)
				})
			}
			record := dnfrepo.InventoryRecord{}
			slot, err := addCurrentDungeonPickupToInventory(&record, definition, 1)
			if err != nil || slot != 121 {
				t.Fatalf("现场材料拾取落点=%d err=%v", slot, err)
			}
		})
	}
}
