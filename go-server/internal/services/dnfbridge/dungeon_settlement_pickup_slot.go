package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"math"
	"strconv"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// A committed pickup may be moved before the boss dies. Its original pickup
// slot can then contain another item, so the result projection must resolve
// its current persisted bag slot. A missing or ambiguous item still fails;
// warehouse rows, invalid counts and uncommitted drops are never substitutes.
func currentDungeonCommittedPickupSlot(
	drop *runtimeDungeonDrop,
	inventory dnfrepo.InventoryRecord,
) (uint16, dnfrepo.ItemStack, bool) {
	if drop == nil || drop.Status != runtimeDungeonDropConsumed || drop.Item.ItemID == 0 ||
		drop.DestinationSlot == 0 || drop.DestinationSlot > math.MaxInt16 {
		return 0, dnfrepo.ItemStack{}, false
	}
	valid := func(stack dnfrepo.ItemStack) bool {
		return stack.ItemID == int64(drop.Item.ItemID) && stack.Count > 0 && stack.Count <= math.MaxUint32
	}
	if stack, found := inventory.Slots[currentDungeonPickupMainSlotKey(int16(drop.DestinationSlot))]; found && valid(stack) {
		return drop.DestinationSlot, stack, true
	}
	// The cached response is frozen only after the pickup transaction commits.
	// It must still prove this exact object, picker and original destination;
	// searching by template alone could otherwise mask a corrupt runtime receipt.
	picker, proven := currentDungeonCommittedPickupProof(drop)
	if !proven || (drop.OwnerActorObjectKey != 0 && picker != drop.OwnerActorObjectKey) {
		return 0, dnfrepo.ItemStack{}, false
	}
	var resolvedSlot uint16
	var resolvedStack dnfrepo.ItemStack
	for key, stack := range inventory.Slots {
		listType, slot, parsed := parseSceneInventorySlotKey(key)
		if !parsed || listType != 0 || slot <= 0 || key != currentDungeonPickupMainSlotKey(slot) || !valid(stack) {
			continue
		}
		if resolvedSlot != 0 {
			return 0, dnfrepo.ItemStack{}, false
		}
		resolvedSlot, resolvedStack = uint16(slot), stack
	}
	return resolvedSlot, resolvedStack, resolvedSlot != 0
}

func currentDungeonCommittedPickupProof(drop *runtimeDungeonDrop) (uint16, bool) {
	if drop == nil || drop.Status != runtimeDungeonDropConsumed || drop.DestinationSlot == 0 ||
		len(drop.PickupResponseBody) != currentDungeonPickupResultSize {
		return 0, false
	}
	picker := binary.LittleEndian.Uint16(drop.PickupResponseBody[4:6])
	if picker == 0 || (drop.OwnerActorObjectKey != 0 && picker != drop.OwnerActorObjectKey && !drop.PartyShared && !drop.MapOwnedPassiveObject) {
		return 0, false
	}
	proof, err := buildCurrentDungeonNormalPickupResultBody(drop.ObjectKey, picker, drop.DestinationSlot)
	return picker, err == nil && bytes.Equal(proof, drop.PickupResponseBody)
}

// This is a read-only presentation check, never a grant. The cached op14 row
// exists only after the pickup commits. Match its complete equipment payload,
// not just a template/quality value; only slot and wearing durability may differ.
// Warehouse, foreign-owner and ambiguous rows cannot substitute for the pickup.
func currentDungeonCommittedPickupIsEquipped(drop *runtimeDungeonDrop, inventory dnfrepo.InventoryRecord, equipment dnfrepo.EquipmentRecord) bool {
	if drop == nil || drop.Item.Kind != dungeonDropItemEquipment || drop.Amount != 1 ||
		drop.Item.PVFPath == "" || inventory.CharacterID == "" || equipment.CharacterID != inventory.CharacterID {
		return false
	}
	picker, proven := currentDungeonCommittedPickupProof(drop)
	if !proven || strconv.FormatUint(uint64(picker), 10) != equipment.CharacterID {
		return false
	}
	body := drop.PickupItemUpdateBody
	if len(body) != 3+currentItemListEntryWireSize || body[0] != dnfrepo.MainInventoryListType ||
		binary.LittleEndian.Uint16(body[1:3]) != 1 {
		return false
	}
	row := body[3:]
	if binary.LittleEndian.Uint16(row[:2]) != drop.DestinationSlot ||
		binary.LittleEndian.Uint32(row[2:6]) != drop.Item.ItemID {
		return false
	}
	matches := 0
	for key, entry := range equipment.Entries {
		if entry.ItemID != int64(drop.Item.ItemID) || key != strconv.Itoa(int(entry.SlotIndex)) ||
			entry.Extra["source"] != "dungeon_pvf_drop_pickup" || entry.Extra["item_kind"] != "equipment" ||
			entry.Extra["pvf_path"] != drop.Item.PVFPath || !currentEquipmentEntryHasCurrentRaw77(entry) {
			continue
		}
		projected, valid := currentItemListEntryFromEquipment(entry)
		if valid && bytes.Equal(row[2:11], projected.data[2:11]) && bytes.Equal(row[13:], projected.data[13:]) {
			matches++
		}
	}
	return matches == 1
}
