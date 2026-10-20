package buffswap

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

// Candidate-only, excluded from shared bridge RED comparison.
func TestReviewBuffOwnedSnapshotCurrentAliasCold(t *testing.T) {
	for _, kind := range []string{"inventory", "equipment"} {
		t.Run(kind, func(t *testing.T) {
			inv := &reviewOwnedInventory{record: repo.InventoryRecord{CharacterID: "6", Slots: map[string]repo.ItemStack{}}, found: true}
			eq := &reviewOwnedEquipment{record: repo.EquipmentRecord{CharacterID: "6", Entries: map[string]repo.EquipmentEntry{}}, found: true}
			b := Binding{List: 0, Index: 1, ItemID: 123, Instance: "instance", SourceKind: SourceCurrentInstance}
			if kind == "inventory" {
				inv.record.Slots["0:01"] = repo.ItemStack{ItemID: 123, Count: 1, Extra: map[string]string{instanceKey: "instance", "quality": "9"}}
			} else {
				b.List = 3
				eq.record.Entries["01"] = repo.EquipmentEntry{SlotIndex: 1, ItemID: 123, Extra: map[string]string{instanceKey: "instance", "quality": "9"}}
			}
			group := repo.Group{Inventory: inv, Equipment: eq}
			view, err := readOwnedSources(context.Background(), group, "6", OwnedProjectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := resolveOwnedProjection(context.Background(), group, "6", State{WireProfile: CurrentWireProfile, Slots: map[byte]Binding{12: b}}, view)
			if err != nil || len(got.State.Slots) != 1 || got.Omitted != 0 {
				t.Fatalf("alias cold recovery lost source: %+v %v", got, err)
			}
			row := got.Items[12]
			if kind == "inventory" {
				if !row.HasCurrentStack || row.CurrentStack.Extra["quality"] != "9" {
					t.Fatal("wrong inventory alias metadata")
				}
			} else if !row.HasCurrentEquipment || row.CurrentEquipment.Extra["quality"] != "9" {
				t.Fatal("wrong equipment alias metadata")
			}
			if inv.loads != 1 || eq.loads != 1 {
				t.Fatal("alias resolution reloaded warehouse")
			}
		})
	}
}
func TestReviewBuffOwnedSnapshotDuplicateParsedLocationOmits(t *testing.T) {
	for _, kind := range []string{"inventory", "equipment"} {
		t.Run(kind, func(t *testing.T) {
			inv := &reviewOwnedInventory{record: repo.InventoryRecord{CharacterID: "6", Slots: map[string]repo.ItemStack{}}, found: true}
			eq := &reviewOwnedEquipment{record: repo.EquipmentRecord{CharacterID: "6", Entries: map[string]repo.EquipmentEntry{}}, found: true}
			b := Binding{List: 0, Index: 1, ItemID: 123}
			if kind == "inventory" {
				for _, k := range []string{"0:1", "0:01"} {
					inv.record.Slots[k] = repo.ItemStack{ItemID: 123, Count: 1}
				}
			} else {
				b.List = 3
				for _, k := range []string{"1", "01"} {
					eq.record.Entries[k] = repo.EquipmentEntry{SlotIndex: 1, ItemID: 123}
				}
			}
			group := repo.Group{Inventory: inv, Equipment: eq}
			view, err := readOwnedSources(context.Background(), group, "6", OwnedProjectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := resolveOwnedProjection(context.Background(), group, "6", State{WireProfile: CurrentWireProfile, Slots: map[byte]Binding{12: b}}, view)
			if err != nil || len(got.Items) != 0 || len(got.State.Slots) != 0 || got.Omitted != 1 {
				t.Fatalf("duplicate alias selected map winner: %+v %v", got, err)
			}
		})
	}
}
func TestReviewBuffOwnedSnapshotNilAndCancelledContext(t *testing.T) {
	inv := &reviewOwnedInventory{record: repo.InventoryRecord{CharacterID: "6"}, found: true}
	eq := &reviewOwnedEquipment{record: repo.EquipmentRecord{CharacterID: "6"}, found: true}
	view, err := readOwnedSources(nil, repo.Group{Inventory: inv, Equipment: eq}, "6", OwnedProjectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	clone := view.readOnlyGroup("6")
	if _, _, err := clone.Inventory.Load(nil, "6"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := clone.Equipment.Load(nil, "6"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := resolveOwnedProjection(ctx, repo.Group{}, "6", State{WireProfile: CurrentWireProfile, Slots: map[byte]Binding{12: {List: 0, Index: 1, ItemID: 123}}}, view)
	if !errors.Is(err, context.Canceled) || len(got.Items) != 0 {
		t.Fatalf("cancelled partial projection: %+v %v", got, err)
	}
}
