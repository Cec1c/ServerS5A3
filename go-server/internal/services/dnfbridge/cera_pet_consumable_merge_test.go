package dnfbridge

import (
	"encoding/binary"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 运行时 Script.pvf 的 stackable/cash/creature/creature_food.stk（宠物饲料 item 24）
// 与 rename_card.stk（更名卡 item 25）**没有 [stack limit] 段**，解析结果
// StackLimit == 0 = 无上限。旧实现把整段合并循环放在 `if definition.StackLimit > 0`
// 里面，于是这两族宠物消耗品每次发放都跳过合并、在 list 7 的 189..238 里另开一格。
//
// 现场只读查库（角色 3，2026-09-24）：
//
//	7:189  item 24 ×600  Extra: item_kind=stackable pvf_path=stackable/cash/creature/creature_food.stk
//	                            stackable_type=[feed] source=cera_package last_grant_source=cera_package
//	                            cera_package_source_item=490701976
//	7:190  item 24 ×300  Extra: 与 7:189 逐键相同
//
// 两行 Extra 完全一致、只差 raw 行里的数量，所以拆行的唯一解释就是"这条路根本没走合并"。
func TestGrantCurrentCeraShopPetConsumableMergesWithoutPVFStackLimit(t *testing.T) {
	definition := dungeonDropItemDefinition{
		ItemID:        24,
		Kind:          dungeonDropItemStackable,
		PVFPath:       "stackable/cash/creature/creature_food.stk",
		StackableType: "[feed]",
		SlotStart:     currentCeraShopPetConsumableSlotStart,
		SlotEnd:       currentCeraShopPetConsumableSlotEnd,
	}
	inventory := &dnfrepo.InventoryRecord{
		CharacterID: "3",
		Slots: map[string]dnfrepo.ItemStack{
			"7:189": {
				ItemID:   24,
				Count:    600,
				RawEntry: petConsumableTestRawEntry(189, 24, 600),
				Extra: map[string]string{
					"item_kind":                "stackable",
					"pvf_path":                 "stackable/cash/creature/creature_food.stk",
					"stackable_type":           "[feed]",
					"source":                   "cera_package",
					"last_grant_source":        "cera_package",
					"cera_package_source_item": "490701976",
				},
			},
		},
	}
	slots, err := grantCurrentCeraShopPetConsumable(inventory, definition, 300)
	if err != nil {
		t.Fatalf("grant pet feed: %v", err)
	}
	if len(slots) != 1 || slots[0] != 189 {
		t.Fatalf("touched slots=%v, want [189]", slots)
	}
	if len(inventory.Slots) != 1 {
		t.Fatalf("pet feed was split into extra rows: %+v", inventory.Slots)
	}
	merged := inventory.Slots["7:189"]
	if merged.Count != 900 {
		t.Fatalf("merged count=%d, want 900", merged.Count)
	}
	// 记账键必须原样保留（合并的是新发放的数量，不是把旧行的来源改掉）。
	if merged.Extra["source"] != "cera_package" || merged.Extra["cera_package_source_item"] != "490701976" {
		t.Fatalf("merge must keep the existing row's bookkeeping: %+v", merged.Extra)
	}
	// raw 行必须跟着新数量重建（客户端读的就是这一行）。
	if len(merged.RawEntry) != currentItemListEntryWireSize {
		t.Fatalf("raw entry len=%d, want %d", len(merged.RawEntry), currentItemListEntryWireSize)
	}
	if got := binary.LittleEndian.Uint32(merged.RawEntry[6:10]); got != 900 {
		t.Fatalf("raw amount=%d, want 900", got)
	}
}

// 身份或实例到期时间冲突时不得合并：另开一格是正确行为（宁可多一格，也不能把
// 更名卡并进饲料、或把两个不同到期时间的实例合成一份）。
func TestGrantCurrentCeraShopPetConsumableRefusesIdentityAndExpiryConflicts(t *testing.T) {
	expire := time.Date(2028, time.April, 4, 22, 0, 0, 0, time.UTC)
	definition := dungeonDropItemDefinition{
		ItemID:        24,
		Kind:          dungeonDropItemStackable,
		PVFPath:       "stackable/cash/creature/creature_food.stk",
		StackableType: "[feed]",
		SlotStart:     currentCeraShopPetConsumableSlotStart,
		SlotEnd:       currentCeraShopPetConsumableSlotEnd,
	}
	for _, test := range []struct {
		name    string
		seed    dnfrepo.ItemStack
		wantNew string
	}{
		{
			name: "different PVF identity",
			seed: dnfrepo.ItemStack{ItemID: 24, Count: 5, Extra: map[string]string{
				"item_kind": "stackable", "pvf_path": "stackable/cash/creature/rename_card.stk", "stackable_type": "[creature]",
			}},
			wantNew: "7:190",
		},
		{
			name: "different stackable type on the same item id",
			seed: dnfrepo.ItemStack{ItemID: 24, Count: 5, Extra: map[string]string{
				"item_kind": "stackable", "pvf_path": "stackable/cash/creature/creature_food.stk", "stackable_type": "[creature]",
			}},
			wantNew: "7:190",
		},
		{
			name: "existing row carries a different instance expiry",
			seed: dnfrepo.ItemStack{ItemID: 24, Count: 5, ExpireAt: expire.Add(time.Hour), Extra: map[string]string{
				"item_kind": "stackable", "pvf_path": "stackable/cash/creature/creature_food.stk", "stackable_type": "[feed]",
				"expire_time": "1838502000",
			}},
			wantNew: "7:190",
		},
	} {
		inventory := &dnfrepo.InventoryRecord{
			CharacterID: "3",
			Slots:       map[string]dnfrepo.ItemStack{"7:189": test.seed},
		}
		if _, err := grantCurrentCeraShopPetConsumable(inventory, definition, 3); err != nil {
			t.Fatalf("%s: grant: %v", test.name, err)
		}
		if got := inventory.Slots["7:189"].Count; got != 5 {
			t.Fatalf("%s: conflicting row was merged into (count=%d, want 5)", test.name, got)
		}
		created, found := inventory.Slots[test.wantNew]
		if !found || created.ItemID != 24 || created.Count != 3 {
			t.Fatalf("%s: expected a fresh row %s with count 3, slots=%+v", test.name, test.wantNew, inventory.Slots)
		}
	}
}

// PVF 明确写了堆叠上限时上限必须生效：满格的行不吸收、超出的部分另开一格。
func TestGrantCurrentCeraShopPetConsumableHonorsPVFStackLimit(t *testing.T) {
	definition := dungeonDropItemDefinition{
		ItemID:        24,
		Kind:          dungeonDropItemStackable,
		PVFPath:       "stackable/cash/creature/creature_food.stk",
		StackableType: "[feed]",
		StackLimit:    1000,
		SlotStart:     currentCeraShopPetConsumableSlotStart,
		SlotEnd:       currentCeraShopPetConsumableSlotEnd,
	}
	inventory := &dnfrepo.InventoryRecord{
		CharacterID: "3",
		Slots: map[string]dnfrepo.ItemStack{
			"7:189": {ItemID: 24, Count: 990, Extra: map[string]string{
				"item_kind": "stackable", "pvf_path": "stackable/cash/creature/creature_food.stk", "stackable_type": "[feed]",
			}},
		},
	}
	slots, err := grantCurrentCeraShopPetConsumable(inventory, definition, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := inventory.Slots["7:189"].Count; got != 1000 {
		t.Fatalf("existing row count=%d, want capped 1000", got)
	}
	if overflow, found := inventory.Slots["7:190"]; !found || overflow.Count != 10 {
		t.Fatalf("overflow row=%+v found=%t, want count 10 at 7:190", overflow, found)
	}
	if len(slots) != 2 || slots[0] != 189 || slots[1] != 190 {
		t.Fatalf("touched slots=%v, want [189 190]", slots)
	}
}

// petConsumableTestRawEntry 造一行当前客户端读的 0x77 行镜像（slot/item/amount 三个
// 关键字段），用于证明合并后的 raw 行是重新编码过的。
func petConsumableTestRawEntry(slot int16, itemID uint32, count uint32) []byte {
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], uint16(slot))
	binary.LittleEndian.PutUint32(raw[2:6], itemID)
	binary.LittleEndian.PutUint32(raw[6:10], count)
	return raw
}
