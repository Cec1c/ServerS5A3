package dnfbridge

import (
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 「都要堆叠」回归：所有把可堆叠道具放进背包的路径都先并入同身份的既有行，
// 判据统一是 dnfrepo.CanMergeItemStack —— 同 item 但 pvf_path / stackable_type /
// 实例到期时间冲突的行不得并，只能另开一格。

func stackMergeTestTime(unix int64) time.Time {
	return time.Unix(unix, 0).UTC()
}

func countStackMergeRows(inventory *dnfrepo.InventoryRecord, itemID int64) int {
	rows := 0
	for _, stack := range inventory.Slots {
		if stack.ItemID == itemID && stack.Count > 0 {
			rows++
		}
	}
	return rows
}

func TestGrantCurrentCeraShopProductMergesOnlySameIdentityStack(t *testing.T) {
	definition := dungeonDropItemDefinition{
		ItemID:        600,
		Kind:          dungeonDropItemStackable,
		PVFPath:       "stackable/material/new_reward.stk",
		StackableType: "[material]",
		StackLimit:    999,
		SlotStart:     121,
		SlotEnd:       176,
	}
	identityExtra := map[string]string{
		"pvf_path":       definition.PVFPath,
		"item_kind":      "stackable",
		"stackable_type": "[material]",
	}

	t.Run("同身份 → 并进既有行且不另开格", func(t *testing.T) {
		inventory := dnfrepo.InventoryRecord{
			CharacterID: "19",
			Slots: map[string]dnfrepo.ItemStack{
				"0:121": {ItemID: 600, Count: 5, Extra: identityExtra},
			},
		}
		slots, err := grantCurrentCeraShopProduct(&inventory, definition, 3)
		if err != nil {
			t.Fatalf("grantCurrentCeraShopProduct error = %v", err)
		}
		if !reflect.DeepEqual(slots, []uint16{121}) {
			t.Fatalf("slots = %v, want [121]", slots)
		}
		if got := inventory.Slots["0:121"].Count; got != 8 {
			t.Fatalf("merged count = %d, want 8", got)
		}
		if got := countStackMergeRows(&inventory, 600); got != 1 {
			t.Fatalf("item 600 rows = %d, want 1 (inventory=%+v)", got, inventory.Slots)
		}
	})

	t.Run("同 item 但 pvf_path 不同 → 不得并，另开一格", func(t *testing.T) {
		inventory := dnfrepo.InventoryRecord{
			CharacterID: "19",
			Slots: map[string]dnfrepo.ItemStack{
				"0:121": {ItemID: 600, Count: 5, Extra: map[string]string{
					"pvf_path":       "stackable/material/old_reward.stk",
					"item_kind":      "stackable",
					"stackable_type": "[material]",
				}},
			},
		}
		slots, err := grantCurrentCeraShopProduct(&inventory, definition, 3)
		if err != nil {
			t.Fatalf("grantCurrentCeraShopProduct error = %v", err)
		}
		if len(slots) != 1 || slots[0] == 121 {
			t.Fatalf("slots = %v, want one new row instead of 0:121", slots)
		}
		if got := inventory.Slots["0:121"].Count; got != 5 {
			t.Fatalf("existing row mutated: count=%d", got)
		}
		if got := countStackMergeRows(&inventory, 600); got != 2 {
			t.Fatalf("item 600 rows = %d, want 2 (inventory=%+v)", got, inventory.Slots)
		}
	})

	t.Run("实例到期时间冲突 → 不得并", func(t *testing.T) {
		inventory := dnfrepo.InventoryRecord{
			CharacterID: "19",
			Slots: map[string]dnfrepo.ItemStack{
				"0:121": {ItemID: 600, Count: 5, Extra: map[string]string{
					"pvf_path":    definition.PVFPath,
					"item_kind":   "stackable",
					"expire_time": "5000",
					"expire_unix": "5000",
				}},
			},
		}
		dated := definition
		dated.ExpirationDate = stackMergeTestTime(9000)
		slots, err := grantCurrentCeraShopProduct(&inventory, dated, 3)
		if err != nil {
			t.Fatalf("grantCurrentCeraShopProduct error = %v", err)
		}
		if len(slots) != 1 || slots[0] == 121 {
			t.Fatalf("slots = %v, want one new row instead of 0:121", slots)
		}
		if got := inventory.Slots["0:121"].Extra["expire_time"]; got != "5000" {
			t.Fatalf("existing row expiration rewritten: %q", got)
		}
	})
}

// 任务/怪物掉落直接进背包（currentQuestDropGrantStack）同样按身份合并。
func TestCurrentQuestDropGrantStackMergesOnlySameIdentity(t *testing.T) {
	definition := dungeonDropItemDefinition{
		ItemID:        10403,
		Kind:          dungeonDropItemStackable,
		PVFPath:       "stackable/quest/new_quest_item.stk",
		StackableType: "[quest]",
		SlotStart:     177,
		SlotEnd:       232,
	}

	t.Run("同身份 → 并入既有任务品行", func(t *testing.T) {
		inventory := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{
			"0:177": {ItemID: 10403, Count: 2, Extra: map[string]string{
				"pvf_path": definition.PVFPath, "item_kind": "stackable", "stackable_type": "[quest]",
			}},
		}}
		slot, err := currentQuestDropGrantStack(&inventory, definition, 3)
		if err != nil {
			t.Fatalf("currentQuestDropGrantStack error = %v", err)
		}
		if slot != 177 || inventory.Slots["0:177"].Count != 5 {
			t.Fatalf("slot=%d inventory=%+v", slot, inventory.Slots)
		}
		if got := countStackMergeRows(&inventory, 10403); got != 1 {
			t.Fatalf("item 10403 rows = %d, want 1", got)
		}
	})

	t.Run("同 item 但 pvf_path 不同 → 另开一格", func(t *testing.T) {
		inventory := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{
			"0:177": {ItemID: 10403, Count: 2, Extra: map[string]string{
				"pvf_path": "stackable/quest/old_quest_item.stk", "item_kind": "stackable", "stackable_type": "[quest]",
			}},
		}}
		slot, err := currentQuestDropGrantStack(&inventory, definition, 3)
		if err != nil {
			t.Fatalf("currentQuestDropGrantStack error = %v", err)
		}
		if slot == 177 {
			t.Fatalf("identity conflict merged into the existing row: %+v", inventory.Slots)
		}
		if got := inventory.Slots["0:177"].Count; got != 2 {
			t.Fatalf("existing row mutated: count=%d", got)
		}
		if got := countStackMergeRows(&inventory, 10403); got != 2 {
			t.Fatalf("item 10403 rows = %d, want 2 (inventory=%+v)", got, inventory.Slots)
		}
	})
}
