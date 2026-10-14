package inventory

import (
	"context"

	dnfplacement "longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 卡片流程只读取角色已有的主包扩容记录，不创建状态或改写金库规则。
func (o *Owner) loadMonsterCardInventory(ctx context.Context, characterID string) (dnfrepo.InventoryRecord, *dnfrepo.CharacterContainerState, bool, error) {
	record, found, err := o.repo.Load(ctx, characterID)
	if err != nil || !found {
		return record, nil, found, err
	}
	state, stateFound, err := dnfrepo.LoadCharacterContainerState(ctx, o.cardSettings, characterID)
	if err != nil || !stateFound {
		return record, nil, found, err
	}
	return record, &state, found, nil
}

// 分类沿用共用PVF策略；容量和寻位限定于卡片，独立于金库分支的辅助代码。
type cardPlacementPolicy struct {
	policy dnfplacement.SlotPolicy
	state  *dnfrepo.CharacterContainerState
}

func newCardPlacementPolicy(policy dnfplacement.SlotPolicy, state *dnfrepo.CharacterContainerState) cardPlacementPolicy {
	return cardPlacementPolicy{policy: policy, state: state}
}

func (p cardPlacementPolicy) Contains(listType byte, slot int16) bool {
	if listType != p.policy.ListType || slot < 0 {
		return false
	}
	if listType == dnfrepo.MainInventoryListType && slot >= 9 && slot <= 288 {
		expansion := uint16(0)
		if p.state != nil {
			expansion = p.state.MainSlotCount
		}
		if expansion > 24 || expansion%8 != 0 || (slot-9)%56 >= int16(32+expansion) {
			return false
		}
	}
	return (slot >= p.policy.Start && slot <= p.policy.End) ||
		(listType == dnfrepo.MainInventoryListType && slot >= 3 && slot <= 8 && (p.policy.AllowQuickSlots || p.policy.PreferQuickSlots))
}

func (p cardPlacementPolicy) MergeSlots() []int16 { return p.slots(true) }
func (p cardPlacementPolicy) EmptySlots() []int16 { return p.slots(false) }

func (p cardPlacementPolicy) slots(merging bool) []int16 {
	if p.policy.Start < 0 || p.policy.End < p.policy.Start {
		return nil
	}
	quick := p.policy.ListType == dnfrepo.MainInventoryListType && (p.policy.AllowQuickSlots || p.policy.PreferQuickSlots)
	slots := make([]int16, 0, int(p.policy.End-p.policy.Start)+7)
	appendRange := func(start, end int16) {
		for value := int(start); value <= int(end); value++ {
			slot := int16(value)
			if !p.Contains(p.policy.ListType, slot) || dnfrepo.IsAccountSharedInventorySlot(p.policy.ListType, slot) {
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
	if quick && (merging || p.policy.PreferQuickSlots) {
		appendRange(3, 8)
	}
	appendRange(p.policy.Start, p.policy.End)
	if quick && !merging && !p.policy.PreferQuickSlots {
		appendRange(3, 8)
	}
	return slots
}
