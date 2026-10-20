// Package itemplacement 提供自动入包的共同选格规则；事务与协议仍由各业务负责。
package itemplacement

import dnfrepo "longheng.io/server/internal/modules/dnf/repository"

type SlotPolicy struct {
	ListType         byte
	Start, End       int16
	AllowQuickSlots  bool
	PreferQuickSlots bool
	ContainerState   *dnfrepo.CharacterContainerState
}

// ForInventory 在原分类规则上附加角色容量，不改变事务、分页和快捷栏优先级。
func (p SlotPolicy) ForInventory(record *dnfrepo.InventoryRecord) SlotPolicy {
	if record != nil {
		p.ContainerState = record.ContainerState
	}
	return p
}

func (p SlotPolicy) Contains(listType byte, slot int16) bool {
	if listType != p.ListType || !SlotOpen(listType, slot, p.ContainerState) {
		return false
	}
	return (slot >= p.Start && slot <= p.End) ||
		(listType == dnfrepo.MainInventoryListType && slot >= 3 && slot <= 8 && (p.AllowQuickSlots || p.PreferQuickSlots))
}

// MergeSlots 先查已有同身份堆栈。快捷格的序号较小，参照 86 先查快捷格再查分类页。
// 能回落快捷栏的材料也能继续叠加已有快捷格材料；此规则与新开格顺序分开。
func (p SlotPolicy) MergeSlots() []int16 {
	return p.slots(true)
}

// EmptySlots 消耗品优先空快捷格，普通装备、材料等优先所属页、页满才回落快捷格。
func (p SlotPolicy) EmptySlots() []int16 {
	return p.slots(false)
}

func (p SlotPolicy) slots(merging bool) []int16 {
	if p.Start < 0 || p.End < p.Start {
		return nil
	}
	quick := p.ListType == dnfrepo.MainInventoryListType && (p.AllowQuickSlots || p.PreferQuickSlots)
	slots := make([]int16, 0, int(p.End-p.Start)+7)
	appendRange := func(start, end int16) {
		for value := int(start); value <= int(end); value++ {
			slot := int16(value)
			if !SlotOpen(p.ListType, slot, p.ContainerState) {
				continue
			}
			// 账号晶块/灵魂格由专属路由管理，不允许自动发放写入角色背包的同号格。
			if dnfrepo.IsAccountSharedInventorySlot(p.ListType, slot) {
				continue
			}
			duplicate := false
			for _, old := range slots {
				if old == slot {
					duplicate = true
					break
				}
			}
			if !duplicate {
				slots = append(slots, slot)
			}
		}
	}
	if quick && (merging || p.PreferQuickSlots) {
		appendRange(3, 8)
	}
	appendRange(p.Start, p.End)
	if quick && !merging && !p.PreferQuickSlots {
		appendRange(3, 8)
	}
	return slots
}
