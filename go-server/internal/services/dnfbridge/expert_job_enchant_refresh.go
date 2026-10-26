package dnfbridge

import (
	"sort"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Capture possible consumption slots before the transaction removes exhausted
// stacks. The sender reloads committed repository rows, including empty-slot
// sentinels and authoritative account-shared stacks. No asset rule changes here.
func currentExpertJobEnchantRefreshSlots(request currentExpertJobStoreEnchantRequest, inventory *dnfrepo.InventoryRecord, materials []dnfexpertjob.RecipeEntry) []alignedcmd.ItemSlotRefresh {
	refreshes := []alignedcmd.ItemSlotRefresh{
		{ListType: request.TargetList, SlotIndex: request.TargetSlot},
		{ListType: request.CardList, SlotIndex: request.CardSlot},
	}
	ordinary := make(map[int64]bool, len(materials))
	for _, material := range materials {
		if fixed, shared := dnfrepo.AccountWarehouseSlotForItem(material.ItemID); shared {
			refreshes = append(refreshes, alignedcmd.ItemSlotRefresh{ListType: dnfrepo.MainInventoryListType, SlotIndex: fixed})
		} else {
			ordinary[material.ItemID] = true
		}
	}
	var materialSlots []int16
	for key, stack := range inventory.Slots {
		list, slot, ok := parseSceneInventorySlotKey(key)
		if ok && list == dnfrepo.MainInventoryListType && slot > 0 && ordinary[stack.ItemID] && stack.Count > 0 && !currentNPCShopItemLocked(stack) && !dnfrepo.IsAccountSharedInventorySlot(list, slot) {
			materialSlots = append(materialSlots, slot)
		}
	}
	sort.Slice(materialSlots, func(i, j int) bool { return materialSlots[i] < materialSlots[j] })
	for _, slot := range materialSlots {
		refreshes = append(refreshes, alignedcmd.ItemSlotRefresh{ListType: dnfrepo.MainInventoryListType, SlotIndex: slot})
	}
	return refreshes
}

func (s *Service) refreshCurrentExpertJobEnchantItems(session *gameSession, result currentExpertJobStoreUseResult) error {
	if s.options.currentClientVersion != "official-24.9" {
		return s.sendSelectedCurrentContainerListsRefresh(session, "expert_job_store_enchant_after_ack")
	}
	// v105 op13 clears the existing list before decoding rows (20251ED ->
	// 25FEB80 -> 25F3F10). The 601 result window retains an item pointer, so
	// rebuilding that list can invalidate the object it is still displaying.
	// Keep the native ACK layout and publish repository-backed changed slots.
	return s.sendSelectedIncrementalItemSlotRefreshes(session, "expert_job_store_enchant_after_ack", result.ItemRefreshes)
}
