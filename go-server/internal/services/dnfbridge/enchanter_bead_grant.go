package dnfbridge

import (
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"time"
)

// Beads retain the selected card's upgrade byte. Compare it before merging,
// and stamp new rows before they become visible to later grants in the batch.
func grantCurrentExpertJobBeads(inventory *dnfrepo.InventoryRecord, definition dungeonDropItemDefinition, count uint32, upgrade byte, now time.Time) error {
	if inventory == nil || count == 0 || definition.Kind != dungeonDropItemStackable {
		return errCurrentCeraShopProductUnavailable
	}
	if definition.DailyDeleteItem {
		var err error
		definition, err = currentPVFItemDefinitionForGrantAt(definition, now)
		if err != nil {
			return err
		}
	} else if !definition.ExpirationDate.IsZero() {
		definition.ExpirationDate = currentPVFRenewedRetiredExpirationDate(definition.ExpirationDate, now)
	}
	if inventory.Slots == nil {
		inventory.Slots = make(map[string]dnfrepo.ItemStack)
	}
	policy := currentPVFItemPlacementPolicy(definition).ForGrant()
	policy.Start, policy.End = definition.SlotStart, definition.SlotEnd
	policy = policy.ForInventory(inventory)
	identity := currentPVFGrantIdentity(definition)
	limit := int64(math.MaxInt32)
	if definition.StackLimit > 0 && definition.StackLimit < limit {
		limit = definition.StackLimit
	}
	remaining := int64(count)
	for _, slot := range policy.MergeSlots() {
		if remaining == 0 {
			return nil
		}
		key := currentDungeonPickupMainSlotKey(slot)
		stack, ok := inventory.Slots[key]
		level, valid := dnfrepo.MonsterCardUpgradeCount(stack)
		if !ok || stack.Count <= 0 || stack.Count >= limit || currentNPCShopItemLocked(stack) || !valid || level != upgrade || !currentItemStackExpirationMatches(stack, definition.ExpirationDate) || !dnfrepo.CanMergeItemStack(stack, identity) {
			continue
		}
		add := remaining
		if available := limit - stack.Count; add > available {
			add = available
		}
		stack.Count += add
		currentExpertJobSetEnchantUpgradeCount(&stack, upgrade)
		entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, slot, stack)
		stack.RawEntry = append([]byte(nil), entry.data[:]...)
		inventory.Slots[key] = stack
		remaining -= add
	}
	for _, slot := range policy.EmptySlots() {
		if remaining == 0 {
			return nil
		}
		key := currentDungeonPickupMainSlotKey(slot)
		if _, occupied := inventory.Slots[key]; occupied {
			continue
		}
		amount := remaining
		if amount > limit {
			amount = limit
		}
		stack, err := buildCurrentDungeonPickupStack(slot, definition, uint32(amount))
		if err != nil {
			return err
		}
		currentExpertJobSetEnchantUpgradeCount(&stack, upgrade)
		inventory.Slots[key] = stack
		remaining -= amount
	}
	if remaining != 0 {
		return errDungeonPickupInventoryFull
	}
	return nil
}
