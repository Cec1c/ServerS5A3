package inventory

import (
	"longheng.io/server/internal/modules/dnf/itemplacement"
	r "longheng.io/server/internal/modules/dnf/repository"
	"math"
)

func normalizeGuildCargoStack(stack r.ItemStack, normalize func(r.ItemStack) (r.ItemStack, error)) (r.ItemStack, error) {
	result, err := normalize(cloneStack(stack))
	if err != nil {
		return r.ItemStack{}, err
	}
	if result.ItemID != stack.ItemID || result.Count != stack.Count || result.Bind != stack.Bind || !result.ExpireAt.Equal(stack.ExpireAt) {
		return r.ItemStack{}, ErrGuildCargoRequest
	}
	return cloneStack(result), nil
}

func allocateGuildCargoPolicy(inv r.InventoryRecord, source r.ItemStack, count uint32, list byte, policy itemplacement.SlotPolicy, normalize func(r.ItemStack) (r.ItemStack, error)) (GuildCargoLocation, error) {
	if policy.ListType != list {
		return GuildCargoLocation{}, ErrGuildCargoRequest
	}
	for _, slot := range policy.MergeSlots() {
		location := GuildCargoLocation{List: list, Slot: uint16(slot)}
		if !validGuildCargoLocation(location) {
			continue
		}
		candidate := inv.Slots[slotKey(list, slot)]
		if candidate.ItemID != source.ItemID || candidate.Count <= 0 || candidate.Count > math.MaxInt32-int64(count) || isEquipmentLocked(candidate) {
			continue
		}
		if normalize != nil {
			var err error
			candidate, err = normalizeGuildCargoStack(candidate, normalize)
			if err != nil {
				continue
			}
		}
		if canStack(source, candidate, int64(count)) {
			return location, nil
		}
	}
	for _, slot := range policy.EmptySlots() {
		location := GuildCargoLocation{List: list, Slot: uint16(slot)}
		if !validGuildCargoLocation(location) {
			continue
		}
		candidate := inv.Slots[slotKey(list, slot)]
		if candidate.ItemID == 0 && candidate.Count == 0 {
			return location, nil
		}
	}
	return GuildCargoLocation{}, ErrGuildCargoInventoryFull
}
