package inventory

import (
	dnfplacement "longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 两种金库共用寻位规则；各自的持久化所有者与事务保持。
func planCargoMove(cmd Command, src, dst map[string]dnfrepo.ItemStack, state *dnfrepo.CharacterContainerState, accountCapacity int16) (Command, error) {
	if cmd.SourceListType == cmd.DestinationListType ||
		(cmd.SourceListType != 2 && cmd.SourceListType != 12 && cmd.DestinationListType != 2 && cmd.DestinationListType != 12) {
		return cmd, nil
	}
	source, found := src[slotKey(cmd.SourceListType, cmd.SourceSlotIndex)]
	if !found {
		return cmd, nil
	}
	destination, occupied := dst[slotKey(cmd.DestinationListType, cmd.DestinationSlotIndex)]
	if !occupied {
		return cmd, nil
	}
	if err := moveSlotAllowed(cmd, source, cmd.DestinationListType, cmd.DestinationSlotIndex, state); err != nil {
		return cmd, err
	}
	count := normalizeMoveCount(source, int64(cmd.MoveCount))
	if canStack(source, destination, count) {
		return cmd, nil
	}
	candidates := moveMergeCandidates(accountCargoMergeCandidates(dst, cmd.DestinationListType, accountCapacity), cmd, source, cmd.DestinationListType, state)
	if slot, _, ok := findCompatibleStackSlot(candidates, cmd.DestinationListType, source, count); ok {
		cmd.DestinationSlotIndex = slot
		return cmd, nil
	}
	for _, slot := range cargoEmptySlotCandidates(cmd, source, state, accountCapacity) {
		if _, occupied := dst[slotKey(cmd.DestinationListType, slot)]; occupied {
			continue
		}
		if moveSlotAllowed(cmd, source, cmd.DestinationListType, slot, state) != nil {
			continue
		}
		cmd.DestinationSlotIndex = slot
		return cmd, nil
	}
	return cmd, ErrCargoIncompatibleStack
}

func cargoEmptySlotCandidates(cmd Command, source dnfrepo.ItemStack, state *dnfrepo.CharacterContainerState, accountCapacity int16) []int16 {
	var slots []int16
	appendRange := func(start, end int16) {
		for slot := int(start); slot <= int(end); slot++ {
			slots = append(slots, int16(slot))
		}
	}
	switch cmd.DestinationListType {
	case listTypeAccountCargo:
		appendRange(0, accountCapacity-1)
	case listTypePersonalCargo:
		end := int16(151)
		if state != nil {
			end = int16(state.PersonalCargoSlotCount) - 1
		}
		appendRange(0, end)
	case listTypeMain:
		if cmd.ItemPlacement != nil {
			policy, err := cmd.ItemPlacement(source)
			if err != nil || policy.ListType != 0 {
				return nil
			}
			if policy.PreferQuickSlots && policy.AllowQuickSlots {
				appendRange(3, 8)
			}
			appendRange(policy.Start, policy.End)
			if !policy.PreferQuickSlots && policy.AllowQuickSlots {
				appendRange(3, 8)
			}
		} else {
			appendRange(3, 288)
		}
	}
	filtered := slots[:0]
	for _, slot := range slots {
		if dnfplacement.SlotOpen(cmd.DestinationListType, slot, state) {
			filtered = append(filtered, slot)
		}
	}
	return filtered
}

func allowMoveAutoMerge(cmd Command) bool {
	return cmd.SourceListType != cmd.DestinationListType || (cmd.SourceListType != listTypePersonalCargo && cmd.SourceListType != listTypeAccountCargo)
}
