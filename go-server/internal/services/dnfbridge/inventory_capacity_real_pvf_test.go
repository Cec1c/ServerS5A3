package dnfbridge

import (
	"errors"
	"os"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFAutomaticGrantHonorsUnlockedCapacity(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("设置DNFBRIDGE_REAL_PVF_SMOKE后核对当前真实材料、装备与容量")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{3175, 3161, 20082} {
		definition, err := catalog.ResolveItem(id)
		if err != nil {
			t.Fatal(err)
		}
		policy := currentPVFItemPlacementPolicy(definition)
		start := int16(121)
		if definition.Kind == dungeonDropItemEquipment {
			start = 9
		}
		if !policy.Contains(0, start) || policy.Contains(0, 65) || !policy.Contains(0, 3) || policy.Contains(0, start+32) {
			t.Fatalf("实际PVF分类/容量错误: item=%d path=%s policy=%+v", id, definition.PVFPath, policy)
		}
		record := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{}}
		for slot := start; slot <= start+31; slot++ {
			record.Slots[currentCeraShopInventorySlotKey(0, slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
		}
		slots, err := grantCurrentCeraShopProduct(&record, definition, 1)
		if err != nil || len(slots) != 1 || slots[0] != 3 {
			t.Fatalf("开放页满应回落快捷栏: item=%d slots=%v err=%v", id, slots, err)
		}
		for slot := int16(3); slot <= 8; slot++ {
			record.Slots[currentCeraShopInventorySlotKey(0, slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
		}
		if _, err := grantCurrentCeraShopProduct(&record, definition, 1); !errors.Is(err, errDungeonPickupInventoryFull) {
			t.Fatalf("开放页及快捷栏满后不能写锁格: item=%d err=%v", id, err)
		}
		record.ContainerState = &dnfrepo.CharacterContainerState{MainSlotCount: 8}
		slots, err = grantCurrentCeraShopProduct(&record, definition, 1)
		if err != nil || len(slots) != 1 || slots[0] != uint16(start+32) {
			t.Fatalf("真实扩容后应使用新增格: item=%d slots=%v err=%v", id, slots, err)
		}
	}
}
