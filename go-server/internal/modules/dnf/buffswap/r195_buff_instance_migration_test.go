package buffswap

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

const r195BuffItem = int64(101040493)

func r195SaveLegacyBinding(t *testing.T, r repo.Group, list byte, index uint16) State {
	t.Helper()
	state := State{WireProfile: CurrentWireProfile, Skill: 38, Slots: map[byte]Binding{
		12: {List: list, Index: index, ItemID: r195BuffItem},
	}}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Settings.Save(context.Background(), repo.SettingsRecord{Scope: Scope("6"), Values: map[string]string{"state": string(raw), "source": "current_exe_994D20"}}); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestR195BuffUniqueLegacyBindingMigratesThenSurvivesWornInventoryWorn(t *testing.T) {
	r, ctx := fixture(t), context.Background()
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "6"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "6", Entries: map[string]repo.EquipmentEntry{
		"12": {SlotIndex: 12, ItemID: r195BuffItem, Extra: map[string]string{"item_kind": "equipment"}},
	}}); err != nil {
		t.Fatal(err)
	}
	r195SaveLegacyBinding(t, r, 3, 12)

	projection, err := LoadResolvedOwnedMigratingCurrentInstances(ctx, r, "6", "account-6")
	if err != nil {
		t.Fatal(err)
	}
	binding, found := projection.State.Slots[12]
	if !found || binding.Instance == "" || binding.SourceKind != SourceCurrentInstance || binding.List != 3 || binding.Index != 12 {
		t.Fatalf("legacy binding did not migrate: %+v", projection.State)
	}
	instance := binding.Instance
	worn, _, _ := r.Equipment.Load(ctx, "6")
	if worn.Entries["12"].Extra[instanceKey] != instance {
		t.Fatal("migration did not attach the instance to the worn item")
	}

	// Worn -> inventory. Equip/unequip owners move the same Extra map; the test
	// models that durable transfer explicitly and keeps registration untouched.
	persistedBefore, _, _ := r.Settings.Load(ctx, Scope("6"))
	item := worn.Entries["12"]
	if err := r.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "6"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "6", Slots: map[string]repo.ItemStack{
		"0:54": {ItemID: item.ItemID, Count: 1, Extra: item.Extra},
	}}); err != nil {
		t.Fatal(err)
	}
	projection, err = LoadResolvedOwnedMigratingCurrentInstances(ctx, r, "6", "account-6")
	if err != nil {
		t.Fatal(err)
	}
	if got := projection.State.Slots[12]; got.List != 0 || got.Index != 54 || got.Instance != instance {
		t.Fatalf("inventory projection crossed or lost instance: %+v", got)
	}
	persistedAfter, _, _ := r.Settings.Load(ctx, Scope("6"))
	if !reflect.DeepEqual(persistedBefore, persistedAfter) {
		t.Fatal("ordinary worn-to-inventory movement rewrote registration")
	}

	// Inventory -> worn. The same physical identity must return to the worn row.
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "6"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "6", Entries: map[string]repo.EquipmentEntry{
		"12": {SlotIndex: 12, ItemID: item.ItemID, Extra: item.Extra},
	}}); err != nil {
		t.Fatal(err)
	}
	projection, err = LoadResolvedOwnedMigratingCurrentInstances(ctx, r, "6", "account-6")
	if err != nil {
		t.Fatal(err)
	}
	if got := projection.State.Slots[12]; got.List != 3 || got.Index != 12 || got.Instance != instance {
		t.Fatalf("re-equip projection crossed or lost instance: %+v", got)
	}
	persistedAfter, _, _ = r.Settings.Load(ctx, Scope("6"))
	if !reflect.DeepEqual(persistedBefore, persistedAfter) {
		t.Fatal("ordinary inventory-to-worn movement rewrote registration")
	}
}

func TestR195BuffSameTemplateLegacyBindingNeverCrossesToAnotherInstance(t *testing.T) {
	r, ctx := fixture(t), context.Background()
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "6", Slots: map[string]repo.ItemStack{
		"0:54": {ItemID: r195BuffItem, Count: 1, Extra: map[string]string{"item_kind": "equipment", instanceKey: "registered-a"}},
		"0:55": {ItemID: r195BuffItem, Count: 1, Extra: map[string]string{"item_kind": "equipment", instanceKey: "sibling-b"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "6"}); err != nil {
		t.Fatal(err)
	}
	r195SaveLegacyBinding(t, r, 0, 54)
	before, _, _ := r.Settings.Load(ctx, Scope("6"))
	inventoryBefore, _, _ := r.Inventory.Load(ctx, "6")

	projection, err := LoadResolvedOwnedMigratingCurrentInstances(ctx, r, "6", "account-6")
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.State.Slots) != 0 || projection.Omitted != 1 {
		t.Fatalf("ambiguous legacy binding selected a same-template item: %+v", projection)
	}
	after, _, _ := r.Settings.Load(ctx, Scope("6"))
	inventoryAfter, _, _ := r.Inventory.Load(ctx, "6")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(inventoryBefore, inventoryAfter) {
		t.Fatal("ambiguous migration wrote a guessed identity")
	}
}

func TestR195BuffSameTemplateItemsFollowRegisteredInstanceAcrossSwap(t *testing.T) {
	r, ctx := fixture(t), context.Background()
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "6", Slots: map[string]repo.ItemStack{
		"0:54": {ItemID: r195BuffItem, Count: 1, Extra: map[string]string{"item_kind": "equipment", instanceKey: "registered-a"}},
		"0:55": {ItemID: r195BuffItem, Count: 1, Extra: map[string]string{"item_kind": "equipment", instanceKey: "sibling-b"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "6"}); err != nil {
		t.Fatal(err)
	}
	state := State{WireProfile: CurrentWireProfile, Skill: 38, Slots: map[byte]Binding{
		12: {List: 0, Index: 54, ItemID: r195BuffItem, Instance: "registered-a", SourceKind: SourceCurrentInstance},
	}}
	inv, _, _ := r.Inventory.Load(ctx, "6")
	inv.Slots["0:54"], inv.Slots["0:55"] = inv.Slots["0:55"], inv.Slots["0:54"]
	if err := r.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	resolved, omitted, err := Resolve(ctx, r, "6", state)
	if err != nil || omitted != 0 {
		t.Fatal("resolve failed", omitted, err)
	}
	if got := resolved.Slots[12]; got.Index != 55 || got.Instance != "registered-a" {
		t.Fatalf("registration followed slot instead of instance: %+v", got)
	}
}

func TestR195BuffInstanceMigrationRejectsForeignOwnerWithoutMutation(t *testing.T) {
	r, ctx := fixture(t), context.Background()
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "6"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "6", Entries: map[string]repo.EquipmentEntry{
		"12": {SlotIndex: 12, ItemID: r195BuffItem, Extra: map[string]string{"item_kind": "equipment"}},
	}}); err != nil {
		t.Fatal(err)
	}
	r195SaveLegacyBinding(t, r, 3, 12)
	settingsBefore, _, err := r.Settings.Load(ctx, Scope("6"))
	if err != nil {
		t.Fatal(err)
	}
	inventoryBefore, _, err := r.Inventory.Load(ctx, "6")
	if err != nil {
		t.Fatal(err)
	}
	equipmentBefore, _, err := r.Equipment.Load(ctx, "6")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := LoadResolvedOwnedMigratingCurrentInstances(ctx, r, "6", "foreign-account"); !errors.Is(err, repo.ErrBuffLegacyOwnerMismatch) {
		t.Fatalf("foreign owner migration error=%v", err)
	}
	settingsAfter, _, err := r.Settings.Load(ctx, Scope("6"))
	if err != nil {
		t.Fatal(err)
	}
	inventoryAfter, _, err := r.Inventory.Load(ctx, "6")
	if err != nil {
		t.Fatal(err)
	}
	equipmentAfter, _, err := r.Equipment.Load(ctx, "6")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(settingsBefore, settingsAfter) ||
		!reflect.DeepEqual(inventoryBefore, inventoryAfter) ||
		!reflect.DeepEqual(equipmentBefore, equipmentAfter) {
		t.Fatal("foreign owner migration mutated registration or item metadata")
	}
}
