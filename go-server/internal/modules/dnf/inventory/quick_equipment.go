package inventory

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// 在背包事务内按实际PVF分类检查；不信任旧行的Extra，也不按物品ID单独限重。
// 目标原装备会被换出，同一件在快捷格之间移动不会增加数量。历史多件允许取下和换格。
func validateQuickEquipmentPlacement(items map[string]r.ItemStack, incoming r.ItemStack, alreadyQuick bool, toList byte, toSlot int16, resolve func(r.ItemStack) (itemplacement.SlotPolicy, error)) error {
	if resolve == nil || !isMainItemQuickSlot(toList, toSlot) {
		return nil
	}
	policy, err := resolve(incoming)
	if err != nil {
		return err
	}
	if !policy.SingleQuickEquipment {
		return nil
	}
	if incoming.Count != 1 {
		return fmt.Errorf("%w: 快捷装备数量无效 item=%d count=%d", ErrMoveItemPlacement, incoming.ItemID, incoming.Count)
	}
	if alreadyQuick {
		return nil
	}
	for slot := int16(3); slot <= 8; slot++ {
		if slot == toSlot {
			continue
		}
		stack, found := items[slotKey(listTypeMain, slot)]
		if !found || stack.ItemID <= 0 || stack.Count <= 0 {
			continue
		}
		held, err := resolve(stack)
		if err != nil {
			return fmt.Errorf("%w: 核对快捷格%d: %v", ErrMoveItemPlacement, slot, err)
		}
		if held.SingleQuickEquipment {
			return fmt.Errorf("%w: 快捷栏只能装备一件快捷装备，已有槽位%d item=%d", ErrMoveItemPlacement, slot, stack.ItemID)
		}
	}
	return nil
}

func validateMoveQuickEquipment(cmd Command, src, dst map[string]r.ItemStack) error {
	resolve := cmd.QuickEquipmentPlacement
	if resolve == nil {
		resolve = cmd.ItemPlacement
	}
	source, sourceOK := src[slotKey(cmd.SourceListType, cmd.SourceSlotIndex)]
	dest, destOK := dst[slotKey(cmd.DestinationListType, cmd.DestinationSlotIndex)]
	if sourceOK {
		if err := validateQuickEquipmentPlacement(dst, source, isMainItemQuickSlot(cmd.SourceListType, cmd.SourceSlotIndex), cmd.DestinationListType, cmd.DestinationSlotIndex, resolve); err != nil {
			return err
		}
	}
	// 交换或反向空格请求也必须检查移入快捷格的另一端。
	if destOK && (!sourceOK || !canStack(source, dest, normalizeMoveCount(source, int64(cmd.MoveCount)))) {
		return validateQuickEquipmentPlacement(src, dest, isMainItemQuickSlot(cmd.DestinationListType, cmd.DestinationSlotIndex), cmd.SourceListType, cmd.SourceSlotIndex, resolve)
	}
	return nil
}
