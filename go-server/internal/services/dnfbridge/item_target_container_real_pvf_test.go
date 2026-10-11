package dnfbridge

import (
	"os"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真实运行时 PVF 回归：容器/分段判定必须与运行时家族一致，并且**任何堆叠品家族
// 都不得落进装备段（list 0 9..64）**——现场报障「材料会进入装备」正是这条不变量。
//
// 用到的真实 ID（本地 runtime\data\dnf\Script.pvf 全量取证）：
//
//	24   stackable/cash/creature/creature_food.stk          [feed]               宠物饲料
//	25   stackable/cash/creature/rename_card.stk            [creature]           宠物更名卡
//	3277 stackable/material/material_3277.stk               [material]           材料
//	6515 stackable/fp/fpcubepiece_clear.stk                 [material instant item] 材料
func TestRealPVFItemTargetContainerKeepsMaterialsOutOfEquipmentSegment(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the runtime-PVF target containers")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		itemID    uint32
		wantList  byte
		wantStart int16
		wantEnd   int16
	}{
		{itemID: 24, wantList: currentPetInventoryListType, wantStart: currentPVFPetConsumableSlotStart, wantEnd: currentPVFPetConsumableSlotEnd},
		{itemID: 25, wantList: currentPetInventoryListType, wantStart: currentPVFPetConsumableSlotStart, wantEnd: currentPVFPetConsumableSlotEnd},
		{itemID: 3277, wantList: dnfrepo.MainInventoryListType, wantStart: currentPVFMainMaterialStart, wantEnd: currentPVFMainMaterialEnd},
		{itemID: 6515, wantList: dnfrepo.MainInventoryListType, wantStart: currentPVFMainMaterialStart, wantEnd: currentPVFMainMaterialEnd},
	} {
		definition, err := catalog.ResolveItem(test.itemID)
		if err != nil {
			t.Fatalf("resolve item=%d: %v", test.itemID, err)
		}
		container := currentPVFItemTargetContainer(definition)
		if container.ListType != test.wantList || container.SlotStart != test.wantStart || container.SlotEnd != test.wantEnd {
			t.Fatalf("item=%d path=%s family=%q container=%+v, want list=%d slots=%d..%d",
				test.itemID, definition.PVFPath, definition.StackableType, container,
				test.wantList, test.wantStart, test.wantEnd)
		}
		// 目录 definition 的槽位（主背包分段）必须与容器判定一致。
		if test.wantList == dnfrepo.MainInventoryListType &&
			(definition.SlotStart != container.SlotStart || definition.SlotEnd != container.SlotEnd) {
			t.Fatalf("item=%d definition slots=%d..%d disagree with container=%d..%d",
				test.itemID, definition.SlotStart, definition.SlotEnd, container.SlotStart, container.SlotEnd)
		}
	}

	scanned := 0
	for itemID, reference := range catalog.itemRefs {
		if reference.kind != dungeonDropItemStackable {
			continue
		}
		definition, err := catalog.ResolveItem(itemID)
		if err != nil {
			continue
		}
		scanned++
		container := currentPVFItemTargetContainer(definition)
		if container.ListType == dnfrepo.MainInventoryListType &&
			container.SlotStart >= currentPVFMainEquipmentSlotStart &&
			container.SlotEnd <= currentPVFMainEquipmentSlotEnd {
			t.Fatalf("stackable item=%d family=%q path=%s landed in the equipment segment %d..%d",
				itemID, definition.StackableType, definition.PVFPath,
				currentPVFMainEquipmentSlotStart, currentPVFMainEquipmentSlotEnd)
		}
	}
	if scanned < 1000 {
		t.Fatalf("scanned only %d stackable items; the runtime PVF did not load as expected", scanned)
	}
	t.Logf("scanned %d stackable families; none landed in the equipment segment", scanned)
}
