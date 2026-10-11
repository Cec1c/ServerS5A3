package buffswap

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

// Candidate-only domain tests: they intentionally use the new API/fields and
// are excluded from the shared scaffolding baseline RED comparison.
type reviewOwnedInventory struct {
	record       repo.InventoryRecord
	found        bool
	loads, saves int
}

func (r *reviewOwnedInventory) Load(context.Context, string) (repo.InventoryRecord, bool, error) {
	r.loads++
	if r.loads > 1 {
		return repo.InventoryRecord{}, false, errors.New("unexpected second inventory I/O")
	}
	return r.record, r.found, nil
}
func (r *reviewOwnedInventory) Save(context.Context, repo.InventoryRecord) error {
	r.saves++
	return errors.New("unexpected inventory save")
}

type reviewOwnedEquipment struct {
	record       repo.EquipmentRecord
	found        bool
	loads, saves int
}

func (r *reviewOwnedEquipment) Load(context.Context, string) (repo.EquipmentRecord, bool, error) {
	r.loads++
	if r.loads > 1 {
		return repo.EquipmentRecord{}, false, errors.New("unexpected second equipment I/O")
	}
	return r.record, r.found, nil
}
func (r *reviewOwnedEquipment) Save(context.Context, repo.EquipmentRecord) error {
	r.saves++
	return errors.New("unexpected equipment save")
}

func TestReviewBuffOwnedSnapshotRejectsReturnedOtherOwner(t *testing.T) {
	for _, kind := range []string{"inventory", "equipment"} {
		t.Run(kind, func(t *testing.T) {
			inv := &reviewOwnedInventory{record: repo.InventoryRecord{CharacterID: "6"}, found: true}
			eq := &reviewOwnedEquipment{record: repo.EquipmentRecord{CharacterID: "6"}, found: true}
			if kind == "inventory" {
				inv.record.CharacterID = "7"
				inv.record.Slots = map[string]repo.ItemStack{"0:54": {ItemID: 123, Count: 1}}
			} else {
				eq.record.CharacterID = "7"
				eq.record.Entries = map[string]repo.EquipmentEntry{"12": {ItemID: 123}}
			}
			_, err := readOwnedSources(context.Background(), repo.Group{Inventory: inv, Equipment: eq}, "6", OwnedProjectionOptions{})
			if !errors.Is(err, repo.ErrBuffLegacyOwnerMismatch) {
				t.Fatalf("returned owner accepted: %v", err)
			}
			if inv.saves+eq.saves != 0 {
				t.Fatal("guard wrote assets")
			}
		})
	}
}

func TestReviewBuffOwnedSnapshotMissingRecordsDiscardPayload(t *testing.T) {
	inv := &reviewOwnedInventory{record: repo.InventoryRecord{CharacterID: "7", Slots: map[string]repo.ItemStack{"0:54": {ItemID: 123, Count: 1}}}}
	eq := &reviewOwnedEquipment{record: repo.EquipmentRecord{CharacterID: "7", Entries: map[string]repo.EquipmentEntry{"12": {ItemID: 123}}}}
	view, err := readOwnedSources(context.Background(), repo.Group{Inventory: inv, Equipment: eq}, "6", OwnedProjectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.inventory.Slots)+len(view.equipment.Entries)+len(view.currentContainers) != 0 {
		t.Fatal("found=false payload borrowed")
	}
	state := State{WireProfile: CurrentWireProfile, Slots: map[byte]Binding{12: {List: 0, Index: 54, ItemID: 123}}}
	got, err := resolveOwnedProjection(context.Background(), repo.Group{Inventory: inv, Equipment: eq}, "6", state, view)
	if err != nil || len(got.State.Slots) != 0 || got.Omitted != 1 {
		t.Fatalf("missing source projected: %+v / %v", got, err)
	}
	if inv.loads != 1 || eq.loads != 1 {
		t.Fatal("missing snapshot reloaded live sources")
	}
}

func TestReviewBuffOwnedSnapshotResolveUsesSingleDetachedView(t *testing.T) {
	inv := &reviewOwnedInventory{record: repo.InventoryRecord{CharacterID: "6", Slots: map[string]repo.ItemStack{"0:55": {ItemID: 123, Count: 1, Extra: map[string]string{instanceKey: "actual-instance", "quality": "9"}}}}, found: true}
	eq := &reviewOwnedEquipment{record: repo.EquipmentRecord{CharacterID: "6", Entries: map[string]repo.EquipmentEntry{}}, found: true}
	group := repo.Group{Inventory: inv, Equipment: eq}
	view, err := readOwnedSources(context.Background(), group, "6", OwnedProjectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a base repository changing after the locked/detached view was read.
	inv.record.Slots["0:55"] = repo.ItemStack{ItemID: 999, Count: 1}
	state := State{WireProfile: CurrentWireProfile, Skill: 38, Slots: map[byte]Binding{12: {List: 0, Index: 54, ItemID: 123, Instance: "actual-instance", SourceKind: SourceCurrentInstance}}}
	got, err := resolveOwnedProjection(context.Background(), group, "6", state, view)
	if err != nil {
		t.Fatal(err)
	}
	b, ok := got.State.Slots[12]
	if !ok || b.Index != 55 || b.ItemID != 123 || got.Items[12].CurrentStack.Extra["quality"] != "9" {
		t.Fatalf("identity/metadata diverged from first view: %+v", got)
	}
	if inv.loads != 1 || eq.loads != 1 || inv.saves+eq.saves != 0 {
		t.Fatalf("cold resolution performed warehouse I/O: inv=%d eq=%d saves=%d", inv.loads, eq.loads, inv.saves+eq.saves)
	}
	detached := view.readOnlyGroup("6")
	row, _, err := detached.Inventory.Load(context.Background(), "6")
	if err != nil {
		t.Fatal(err)
	}
	row.Slots["0:55"].Extra["quality"] = "mutated"
	row2, _, _ := detached.Inventory.Load(context.Background(), "6")
	if row2.Slots["0:55"].Extra["quality"] != "9" {
		t.Fatal("readonly clone alias escaped")
	}
	if err := detached.Inventory.Save(context.Background(), row); err == nil {
		t.Fatal("detached view permitted save")
	}
}
