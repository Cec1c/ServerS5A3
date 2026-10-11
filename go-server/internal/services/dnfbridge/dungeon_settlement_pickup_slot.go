package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"math"

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
	if len(drop.PickupResponseBody) != currentDungeonPickupResultSize {
		return 0, dnfrepo.ItemStack{}, false
	}
	picker := binary.LittleEndian.Uint16(drop.PickupResponseBody[4:6])
	if picker == 0 || (drop.OwnerActorObjectKey != 0 && picker != drop.OwnerActorObjectKey) {
		return 0, dnfrepo.ItemStack{}, false
	}
	proof, err := buildCurrentDungeonNormalPickupResultBody(drop.ObjectKey, picker, drop.DestinationSlot)
	if err != nil || !bytes.Equal(proof, drop.PickupResponseBody) {
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
