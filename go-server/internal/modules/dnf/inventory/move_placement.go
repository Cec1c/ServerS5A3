package inventory

import (
	"context"
	"errors"
	"fmt"

	dnfplacement "longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrMoveSlotLocked         = errors.New("物品目标格尚未解锁")
	ErrMoveItemPlacement      = errors.New("物品不能放入该分类页")
	ErrCargoIncompatibleStack = errors.New("仓库目标格已有不能合并的物品")
	ErrMoveCannotStack        = errors.New("该物品不能合并到目标堆叠")
	ErrMoveCannotSwap         = errors.New("部分数量不能交换整组物品")
	ErrCargoInsertRejected    = errors.New("该物品不能存入金库")
)

func (o *Owner) loadInventoryWithContainerState(ctx context.Context, characterID string) (dnfrepo.InventoryRecord, bool, error) {
	record, found, err := o.repo.Load(ctx, characterID)
	if err == nil && found {
		err = dnfrepo.BindInventoryContainerState(ctx, o.settings, &record)
	}
	return record, found, err
}

func moveSlotCapacityAllowed(list byte, slot int16, state *dnfrepo.CharacterContainerState) error {
	if !dnfplacement.SlotOpen(list, slot, state) {
		return fmt.Errorf("%w: list=%d slot=%d", ErrMoveSlotLocked, list, slot)
	}
	return nil
}

func moveSlotAllowed(cmd Command, moving dnfrepo.ItemStack, list byte, slot int16, state *dnfrepo.CharacterContainerState) error {
	if err := moveSlotCapacityAllowed(list, slot, state); err != nil {
		return err
	}
	if (list == listTypePersonalCargo || list == listTypeAccountCargo) && cmd.SourceListType != cmd.DestinationListType && cmd.CargoInsert != nil {
		if err := cmd.CargoInsert(moving, list); err != nil {
			return fmt.Errorf("%w: %v", ErrCargoInsertRejected, err)
		}
	}
	// 金库没有主包分页；主包目标必须由实际PVF分类判断，不能沿用仓库来源格。
	if list != listTypeMain || cmd.ItemPlacement == nil {
		return nil
	}
	policy, err := cmd.ItemPlacement(moving)
	if err != nil {
		return err
	}
	allowed := false
	if policy.ListType == list {
		for _, candidate := range policy.MergeSlots() {
			if candidate == slot {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		return fmt.Errorf("%w: item=%d list=%d slot=%d", ErrMoveItemPlacement, moving.ItemID, list, slot)
	}
	return nil
}

func validateMovePlacement(cmd Command, src, dst map[string]dnfrepo.ItemStack, state *dnfrepo.CharacterContainerState) error {
	source, sourceOK := src[slotKey(cmd.SourceListType, cmd.SourceSlotIndex)]
	destination, destinationOK := dst[slotKey(cmd.DestinationListType, cmd.DestinationSlotIndex)]
	if !sourceOK && !destinationOK {
		return nil
	}
	if sourceOK {
		if err := moveSlotAllowed(cmd, source, cmd.DestinationListType, cmd.DestinationSlotIndex, state); err != nil {
			return err
		}
	} else {
		return moveSlotAllowed(cmd, destination, cmd.SourceListType, cmd.SourceSlotIndex, state)
	}
	if destinationOK && !canStack(source, destination, normalizeMoveCount(source, int64(cmd.MoveCount))) {
		if cmd.SourceListType == cmd.DestinationListType {
			if source.ItemID == destination.ItemID && isStackable(source) && isStackable(destination) {
				return ErrMoveCannotStack
			}
			if canSplitStack(source, normalizeMoveCount(source, int64(cmd.MoveCount))) {
				return ErrMoveCannotSwap
			}
		}
		// 当前EXE跨金库的op19消费者只处理移入/合并，不支持服务端单方面执行交换。
		if cmd.SourceListType != cmd.DestinationListType &&
			(cmd.SourceListType == listTypePersonalCargo || cmd.DestinationListType == listTypePersonalCargo ||
				cmd.SourceListType == listTypeAccountCargo || cmd.DestinationListType == listTypeAccountCargo) {
			return ErrCargoIncompatibleStack
		}
		return moveSlotAllowed(cmd, destination, cmd.SourceListType, cmd.SourceSlotIndex, state)
	}
	return nil
}

func moveMergeCandidates(items map[string]dnfrepo.ItemStack, cmd Command, moving dnfrepo.ItemStack, list byte, state *dnfrepo.CharacterContainerState) map[string]dnfrepo.ItemStack {
	result := make(map[string]dnfrepo.ItemStack)
	for key, stack := range items {
		keyList, slot, ok := parseSlotKey(key)
		if ok && keyList == list && moveSlotAllowed(cmd, moving, list, slot, state) == nil {
			result[key] = stack
		}
	}
	return result
}
