package dnfbridge

import (
	"errors"
	"math"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestSettlementCommittedPickupFollowsPersistedMovedStack(t *testing.T) {
	// Physical char27: item3027 was picked into5, moved to121, then item1062
	// reused5. Both are already committed; neither should block the clear.
	runtime := &runtimeDungeonState{DropOwner: newRuntimeDungeonDropOwner()}
	runtime.DropOwner.byObjectKey[421] = &runtimeDungeonDrop{ObjectKey: 421,
		Item: dungeonDropItemDefinition{ItemID: 3027}, Status: runtimeDungeonDropConsumed, DestinationSlot: 5}
	runtime.DropOwner.byObjectKey[435] = &runtimeDungeonDrop{ObjectKey: 435,
		Item: dungeonDropItemDefinition{ItemID: 1062}, Status: runtimeDungeonDropConsumed, DestinationSlot: 5}
	runtime.DropOwner.byObjectKey[421].PickupResponseBody, _ = buildCurrentDungeonNormalPickupResultBody(421, 27, 5)
	runtime.DropOwner.byObjectKey[435].PickupResponseBody, _ = buildCurrentDungeonNormalPickupResultBody(435, 27, 5)
	inventory := dnfrepo.InventoryRecord{CharacterID: "27", Slots: map[string]dnfrepo.ItemStack{
		"0:121": {ItemID: 3027, Count: 1}, "0:5": {ItemID: 1062, Count: 1},
	}}
	drops, err := currentDungeonCommittedDropReceipts(runtime, inventory)
	if err != nil || len(drops) != 2 || drops[0].ObjectKey != 5 || drops[0].TemplateID != 1062 ||
		drops[1].ObjectKey != 121 || drops[1].TemplateID != 3027 {
		t.Fatalf("moved receipts=%+v error=%v", drops, err)
	}
	if runtime.DropOwner.byObjectKey[421].DestinationSlot != 5 || inventory.Slots["0:121"].Count != 1 {
		t.Fatal("read-only result projection changed pickup or inventory state")
	}
}

func TestSettlementMovedPickupKeepsOwnershipAndAmbiguityChecks(t *testing.T) {
	for _, test := range []struct {
		name  string
		slots map[string]dnfrepo.ItemStack
	}{
		{"missing", map[string]dnfrepo.ItemStack{"0:5": {ItemID: 1062, Count: 1}}},
		{"ambiguous", map[string]dnfrepo.ItemStack{"0:121": {ItemID: 3027, Count: 1}, "0:122": {ItemID: 3027, Count: 1}}},
		{"other_list", map[string]dnfrepo.ItemStack{"1:121": {ItemID: 3027, Count: 1}}},
		{"noncanonical_key", map[string]dnfrepo.ItemStack{"0:0121": {ItemID: 3027, Count: 1}}},
		{"zero_count", map[string]dnfrepo.ItemStack{"0:121": {ItemID: 3027, Count: 0}}},
		{"negative_count", map[string]dnfrepo.ItemStack{"0:121": {ItemID: 3027, Count: -1}}},
		{"overflow_count", map[string]dnfrepo.ItemStack{"0:121": {ItemID: 3027, Count: math.MaxUint32 + 1}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &runtimeDungeonState{DropOwner: newRuntimeDungeonDropOwner()}
			runtime.DropOwner.byObjectKey[421] = &runtimeDungeonDrop{ObjectKey: 421,
				Item: dungeonDropItemDefinition{ItemID: 3027}, Status: runtimeDungeonDropConsumed, DestinationSlot: 5}
			runtime.DropOwner.byObjectKey[421].PickupResponseBody, _ = buildCurrentDungeonNormalPickupResultBody(421, 27, 5)
			_, err := currentDungeonCommittedDropReceipts(runtime, dnfrepo.InventoryRecord{CharacterID: "27", Slots: test.slots})
			if !errors.Is(err, errCurrentDungeonSettlementStateInvalid) {
				t.Fatalf("unproven relocation accepted error=%v", err)
			}
		})
	}
}

func TestSettlementMovedPickupRejectsCorruptCommittedReceipt(t *testing.T) {
	for _, test := range []struct {
		name                string
		object              uint32
		picker, slot, owner uint16
		omit                bool
	}{
		{"missing_commit", 421, 27, 5, 27, true},
		{"wrong_object", 422, 27, 5, 27, false},
		{"wrong_destination", 421, 27, 6, 27, false},
		{"wrong_owner", 421, 28, 5, 27, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			drop := &runtimeDungeonDrop{ObjectKey: 421, OwnerActorObjectKey: test.owner,
				Item: dungeonDropItemDefinition{ItemID: 3027}, Status: runtimeDungeonDropConsumed, DestinationSlot: 5}
			if !test.omit {
				drop.PickupResponseBody, _ = buildCurrentDungeonNormalPickupResultBody(test.object, test.picker, test.slot)
			}
			_, _, found := currentDungeonCommittedPickupSlot(drop, dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{"0:121": {ItemID: 3027, Count: 1}}})
			if found {
				t.Fatal("corrupt receipt accepted despite another matching inventory item")
			}
		})
	}
}
