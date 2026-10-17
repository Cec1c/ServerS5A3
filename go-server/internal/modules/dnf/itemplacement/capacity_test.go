package itemplacement

import (
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestUnexpandedInventoryDoesNotAllocateLockedSlots(t *testing.T) {
	p := SlotPolicy{ListType: 0, Start: 9, End: 64, AllowQuickSlots: true}
	slots := p.EmptySlots()
	if len(slots) != 38 || slots[31] != 40 || slots[32] != 3 {
		t.Fatalf("未扩容装备页应为32格，随后回落6个快捷格，实际=%v", slots)
	}
	for _, slot := range p.MergeSlots() {
		if slot >= 41 && slot <= 64 {
			t.Fatalf("未解锁格%d不能继续叠加", slot)
		}
	}
}

func TestUnexpandedMaterialCapacityIsSeparateFromConsumables(t *testing.T) {
	p := SlotPolicy{ListType: dnfrepo.MainInventoryListType, Start: 121, End: 176}
	slots := p.EmptySlots()
	if len(slots) != 32 || slots[0] != 121 || slots[31] != 152 {
		t.Fatalf("未扩容材料页应仅使用121..152，实际=%v", slots)
	}
}

func TestEveryExpansionStageUsesItsOwnCapacity(t *testing.T) {
	for _, expansion := range []uint16{0, 8, 16, 24} {
		for _, start := range []int16{9, 65, 121, 177, 233} {
			p := SlotPolicy{ListType: 0, Start: start, End: start + 55, ContainerState: &dnfrepo.CharacterContainerState{MainSlotCount: expansion}}
			slots := p.EmptySlots()
			if len(slots) != 32+int(expansion) || slots[len(slots)-1] != start+31+int16(expansion) {
				t.Fatalf("扩容%d分类段%d容量错误: %v", expansion, start, slots)
			}
		}
	}
	for _, expansion := range []uint16{1, 4, 25, 32} {
		if SlotOpen(0, 9, &dnfrepo.CharacterContainerState{MainSlotCount: expansion}) {
			t.Fatalf("非法扩容%d不应放行", expansion)
		}
	}
}

func TestExpansionDoesNotShrinkOtherContainers(t *testing.T) {
	state := &dnfrepo.CharacterContainerState{MainSlotCount: 0, PersonalCargoSlotCount: 8}
	for _, row := range []struct {
		list byte
		slot int16
	}{{0, 3}, {0, 8}, {0, 289}, {0, 344}, {0, 354}, {1, 500}, {7, 238}, {38, 97}} {
		if !SlotOpen(row.list, row.slot, state) {
			t.Fatalf("主包扩容不应缩减独立格: %+v", row)
		}
	}
	if !SlotOpen(2, 7, state) || SlotOpen(2, 8, state) {
		t.Fatal("个人仓库应按实际容量判断")
	}
}
