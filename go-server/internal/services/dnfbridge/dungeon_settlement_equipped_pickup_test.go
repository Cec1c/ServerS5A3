package dnfbridge

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Live tutorial char1 picked14400 into0:9 and wore it in15. The saved worn
// raw row equals the committed pickup row, while the bag no longer has14400.
func tutorialEquippedPickupFixture(t *testing.T, picker uint16) (*runtimeDungeonDrop, dnfrepo.EquipmentEntry) {
	t.Helper()
	raw, err := hex.DecodeString("0900403800006cda8f3900190000")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, make([]byte, currentItemListEntryWireSize-len(raw))...)
	var row currentItemListEntry
	copy(row.data[:], raw)
	drop := &runtimeDungeonDrop{ObjectKey: 407, OwnerActorObjectKey: picker, Amount: 1, DestinationSlot: 9, Status: runtimeDungeonDropConsumed,
		Item: dungeonDropItemDefinition{ItemID: 14400, Kind: dungeonDropItemEquipment, PVFPath: "equipment/character/common/shoulder/leather/shd_crleather.equ"}}
	drop.PickupResponseBody, err = buildCurrentDungeonNormalPickupResultBody(drop.ObjectKey, picker, 9)
	if err != nil {
		t.Fatal(err)
	}
	drop.PickupItemUpdateBody = buildCurrentItemUpdateBody(0, []currentItemListEntry{row})
	equipment := dnfrepo.EquipmentEntry{SlotIndex: 15, ItemID: 14400, RawEntry: raw, Extra: map[string]string{
		"source": "dungeon_pvf_drop_pickup", "item_kind": "equipment", "pvf_path": drop.Item.PVFPath,
		"equipment_type": "[shoulder]", "current_exe_equipment_type": "15", "quality_seed": "965728876", "durability": "25"}}
	return drop, equipment
}

func TestSettlementPickedEquipmentWornBeforeClear(t *testing.T) {
	service, runtime, session, _, _ := prepareCompletedSettlementRuntime(t)
	repositories, _ := service.repositoryGroup()
	ctx := context.Background()
	character, found, err := repositories.Character.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("character found=%t err=%v", found, err)
	}
	character.Level = 1
	character.Stats["exp"] = 54
	if err = repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err = repositories.Skill.Save(ctx, dnfrepo.SkillRecord{CharacterID: "99", Points: dnfrepo.SkillPointState{SyncedLevel: 1}}); err != nil {
		t.Fatal(err)
	}
	if err = repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "99"}); err != nil {
		t.Fatal(err)
	}
	drop, worn := tutorialEquippedPickupFixture(t, 99)
	if err = repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "99", Entries: map[string]dnfrepo.EquipmentEntry{"15": worn}}); err != nil {
		t.Fatal(err)
	}
	runtime.Character = character
	runtime.Dungeon.Metadata.BasisLevel.Set = true
	runtime.Dungeon.Metadata.BasisLevel.Value = 1
	runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Set = true
	runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Value = 0.1
	runtime.DropOwner = newRuntimeDungeonDropOwner()
	runtime.DropOwner.byObjectKey[407] = drop
	runtime.settlementPlayResultReceived = true
	runtime.settlementPlayResultBody = make([]byte, currentDungeonPlayResultBaseSize)
	resources := currentDungeonSettlementTestResources(t)
	if err = service.produceCurrentDungeonSettlementPlanWithResourcesLocked(ctx, session, runtime, resources); err != nil {
		t.Fatal(err)
	}
	if runtime.settlementResultPlan == nil {
		t.Fatal("worn pickup blocked result packet plan")
	}
	after, _, err := repositories.Character.Load(ctx, "99")
	if err != nil {
		t.Fatal(err)
	}
	runtime.settlementResultPlan = nil
	if err = service.produceCurrentDungeonSettlementPlanWithResourcesLocked(ctx, session, runtime, resources); err != nil {
		t.Fatal(err)
	}
	replay, _, _ := repositories.Character.Load(ctx, "99")
	if after.Stats["exp"] != replay.Stats["exp"] {
		t.Fatal("result retry granted experience twice")
	}
	inventory, _, _ := repositories.Inventory.Load(ctx, "99")
	if len(inventory.Slots) != 0 || drop.DestinationSlot != 9 {
		t.Fatal("result fabricated or relocated a bag item")
	}
}

func TestSettlementEquippedPickupRejectsUnprovenRows(t *testing.T) {
	for _, name := range []string{"foreign_character", "wrong_picker", "wrong_object", "missing_ack", "missing_update", "wrong_update_item", "different_quality", "wrong_source", "wrong_path", "wrong_kind", "ambiguous", "noncanonical_slot", "stackable", "not_committed"} {
		t.Run(name, func(t *testing.T) {
			drop, worn := tutorialEquippedPickupFixture(t, 99)
			inventory := dnfrepo.InventoryRecord{CharacterID: "99"}
			equipment := dnfrepo.EquipmentRecord{CharacterID: "99", Entries: map[string]dnfrepo.EquipmentEntry{"15": worn}}
			switch name {
			case "foreign_character":
				equipment.CharacterID = "98"
			case "wrong_picker":
				drop.PickupResponseBody, _ = buildCurrentDungeonNormalPickupResultBody(407, 98, 9)
			case "wrong_object":
				drop.PickupResponseBody, _ = buildCurrentDungeonNormalPickupResultBody(408, 99, 9)
			case "missing_ack":
				drop.PickupResponseBody = nil
			case "missing_update":
				drop.PickupItemUpdateBody = nil
			case "wrong_update_item":
				drop.PickupItemUpdateBody[5]++
			case "different_quality":
				worn.RawEntry[6]++
				worn.Extra["quality_seed"] = "965728877"
			case "wrong_source":
				worn.Extra["source"] = "gm"
			case "wrong_path":
				worn.Extra["pvf_path"] = "other.equ"
			case "wrong_kind":
				worn.Extra["item_kind"] = "stackable"
			case "ambiguous":
				other := worn
				other.SlotIndex = 16
				equipment.Entries["16"] = other
			case "noncanonical_slot":
				delete(equipment.Entries, "15")
				equipment.Entries["015"] = worn
			case "stackable":
				drop.Item.Kind = dungeonDropItemStackable
			case "not_committed":
				drop.Status = runtimeDungeonDropAvailable
			}
			if currentDungeonCommittedPickupIsEquipped(drop, inventory, equipment) {
				t.Fatal("unproven equipped receipt accepted")
			}
		})
	}
}

func TestSettlementEquippedPickupKeepsBagProjectionAndDurability(t *testing.T) {
	drop, worn := tutorialEquippedPickupFixture(t, 99)
	worn.Extra["durability"] = "17"
	equipment := dnfrepo.EquipmentRecord{CharacterID: "99", Entries: map[string]dnfrepo.EquipmentEntry{"15": worn}}
	inventory := dnfrepo.InventoryRecord{CharacterID: "99"}
	if !currentDungeonCommittedPickupIsEquipped(drop, inventory, equipment) {
		t.Fatal("wearing durability prevented pickup proof")
	}
	runtime := &runtimeDungeonState{DropOwner: newRuntimeDungeonDropOwner()}
	runtime.DropOwner.byObjectKey[407] = drop
	drops, _, _, err := currentDungeonCommittedDropReceiptsFiltered(runtime, inventory, false, false, 99, equipment)
	if err != nil || len(drops) != 0 {
		t.Fatalf("equipped receipt fabricated bag result: %+v %v", drops, err)
	}
	_, err = currentDungeonCommittedDropReceipts(runtime, inventory)
	if !errors.Is(err, errCurrentDungeonSettlementStateInvalid) {
		t.Fatal("missing equipment still must reject")
	}
	inventory.Slots = map[string]dnfrepo.ItemStack{"0:9": {ItemID: 14400, Count: 1}}
	drops, _, _, err = currentDungeonCommittedDropReceiptsFiltered(runtime, inventory, false, false, 99, equipment)
	if err != nil || len(drops) != 1 || drops[0].ObjectKey != 9 {
		t.Fatalf("existing bag projection changed %+v %v", drops, err)
	}
}
