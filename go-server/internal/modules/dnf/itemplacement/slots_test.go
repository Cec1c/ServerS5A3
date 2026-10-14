package itemplacement

import (
	"reflect"
	"testing"
)

func TestSlotPolicySeparatesMergingFromNewMaterialSlots(t *testing.T) {
	policy := SlotPolicy{ListType: 0, Start: 121, End: 123, AllowQuickSlots: true}
	if got := policy.MergeSlots(); !reflect.DeepEqual(got, []int16{3, 4, 5, 6, 7, 8, 121, 122, 123}) {
		t.Fatalf("已有堆栈扫描顺序=%v", got)
	}
	if got := policy.EmptySlots(); !reflect.DeepEqual(got, []int16{121, 122, 123, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("材料新格顺序=%v", got)
	}
	policy.PreferQuickSlots = true
	if got := policy.EmptySlots(); !reflect.DeepEqual(got, policy.MergeSlots()) {
		t.Fatalf("消耗品应优先空快捷格：%v", got)
	}
}

func TestSlotPolicyKeepsEquipmentAndOtherContainersOutOfQuickSlots(t *testing.T) {
	for _, p := range []SlotPolicy{{ListType: 0, Start: 9, End: 64}, {ListType: 7, Start: 189, End: 238, AllowQuickSlots: true, PreferQuickSlots: true}, {ListType: 1, Start: 0, End: 500}} {
		for _, slots := range [][]int16{p.MergeSlots(), p.EmptySlots()} {
			if len(slots) != int(p.End-p.Start)+1 || slots[0] != p.Start || slots[len(slots)-1] != p.End {
				t.Fatalf("容器分类被改变：%+v slots=%v", p, slots)
			}
		}
	}
}

func TestSlotPolicyNeverAllocatesCharacterRowsOnAccountWarehouseCells(t *testing.T) {
	policy := SlotPolicy{ListType: 0, Start: 345, End: 359, AllowQuickSlots: true}
	for _, slots := range [][]int16{policy.MergeSlots(), policy.EmptySlots()} {
		for _, slot := range slots {
			if slot >= 354 {
				t.Fatalf("角色入包不得占账号仓库格：%d", slot)
			}
		}
	}
}
