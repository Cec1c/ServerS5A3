package dungeon

import (
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestCardAndVendingRewardUseMaterialQuickSlotFallback(t *testing.T) {
	record := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{
		"0:121": {ItemID: 999, Count: 1}, "0:122": {ItemID: 999, Count: 1},
	}}
	reward := CardItemReward{Stack: dnfrepo.ItemStack{ItemID: 3175, Count: 1}, Stackable: true,
		SlotStart: 121, SlotEnd: 122, AllowQuickSlots: true, StackLimit: 99}
	slot, err := AddResolvedRewardToInventory(&record, reward, nil)
	if err != nil || slot != 3 || record.Slots["0:3"].Count != 1 {
		t.Fatalf("奖励材料满页回落=%d err=%v", slot, err)
	}
}

func TestCardRewardRespectsDeclaredStackLimitBeforeOpeningAnotherSlot(t *testing.T) {
	record := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{"0:121": {ItemID: 3175, Count: 99}}}
	reward := CardItemReward{Stack: dnfrepo.ItemStack{ItemID: 3175, Count: 1}, Stackable: true,
		SlotStart: 121, SlotEnd: 122, AllowQuickSlots: true, StackLimit: 99}
	slot, err := AddResolvedRewardToInventory(&record, reward, nil)
	if err != nil || slot != 122 || record.Slots["0:121"].Count != 99 || record.Slots["0:122"].Count != 1 {
		t.Fatalf("奖励突破上限或错位：slot=%d err=%v inventory=%+v", slot, err, record.Slots)
	}
}

func TestCardEquipmentFullPageFallsBackWithoutMerging(t *testing.T) {
	record := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{"0:3": {ItemID: 700, Count: 1}}}
	for slot := uint16(9); slot <= 64; slot++ {
		record.Slots[mainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
	}
	reward := CardItemReward{Stack: dnfrepo.ItemStack{ItemID: 700, Count: 1}, SlotStart: 9, SlotEnd: 64, AllowQuickSlots: true}
	slot, err := AddResolvedRewardToInventory(&record, reward, nil)
	if err != nil || slot != 4 || record.Slots["0:3"].Count != 1 || record.Slots["0:4"].ItemID != 700 || record.Slots["0:4"].Count != 1 {
		t.Fatalf("翻牌装备满栏回落失败或合并装备：slot=%d err=%v", slot, err)
	}
}
