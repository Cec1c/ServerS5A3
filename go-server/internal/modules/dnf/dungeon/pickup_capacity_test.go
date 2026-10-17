package dungeon

import (
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPickupFullUnlockedMaterialPageFallsBackBeforeLockedCells(t *testing.T) {
	record := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{}}
	for slot := uint16(121); slot <= 152; slot++ {
		record.Slots[mainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: int64(slot) + 1000, Count: 1}
	}
	record.Slots["0:153"] = dnfrepo.ItemStack{ItemID: 100, Count: 5}
	placement := PickupItemPlacement{Definition: PickupItemDefinition{ItemID: 100, Kind: PickupItemStackable, StackLimit: 100, SlotStart: 121, SlotEnd: 176, AllowQuickSlots: true}, Amount: 1,
		BuildNew: func(int16) (dnfrepo.ItemStack, error) { return dnfrepo.ItemStack{ItemID: 100, Count: 1}, nil }}
	slot, _, err := PlacePickupItem(&record, placement)
	if err != nil || slot != 3 || record.Slots["0:153"].Count != 5 {
		t.Fatalf("开放材料页满，应回落快捷格且保留锁格旧物品: slot=%d err=%v record=%+v", slot, err, record)
	}
	for quick := uint16(3); quick <= 8; quick++ {
		record.Slots[mainSlotKey(quick)] = dnfrepo.ItemStack{ItemID: int64(quick) + 2000, Count: 1}
	}
	if _, _, err := PlacePickupItem(&record, placement); err == nil {
		t.Fatal("材料页及快捷栏都满后不能进入未解锁格")
	}
	record.ContainerState = &dnfrepo.CharacterContainerState{MainSlotCount: 8}
	slot, _, err = PlacePickupItem(&record, placement)
	if err != nil || slot != 153 || record.Slots["0:153"].Count != 6 {
		t.Fatalf("真正扩容后允许使用已有新增格: slot=%d err=%v", slot, err)
	}
}
