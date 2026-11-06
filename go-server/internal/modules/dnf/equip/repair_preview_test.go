package equip

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"reflect"
	"testing"
)

func TestRepairPreviewDoesNotSpendOrRepairAndSnapshotGuardIsAtomic(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	saveTestCharacter(t, ctx, g, repo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 5000}})
	saveTestEquipment(t, ctx, g, repo.EquipmentRecord{CharacterID: "77", Entries: map[string]repo.EquipmentEntry{"12": {SlotIndex: 12, ItemID: 700, RawEntry: []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 0}}}})
	saveTestInventory(t, ctx, g, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{}})
	owner, e := NewOwner(g)
	if e != nil {
		t.Fatal(e)
	}
	resolver := repairAllTestResolver(map[int64]string{700: "[weapon]"})
	before, _, _ := g.Character.Load(ctx, "77")
	eq := loadTestEquipment(t, ctx, g, "77")
	result, e := owner.RepairAll(ctx, RepairCommand{SelectedCharacterID: 77, SlotIndex: -1, QuickRepair: true, PreviewOnly: true}, resolver)
	if e != nil || result.Cost <= 0 || result.RepairedCount != 1 || result.Changed {
		t.Fatal(result, e)
	}
	after, _, _ := g.Character.Load(ctx, "77")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(eq, loadTestEquipment(t, ctx, g, "77")) {
		t.Fatal("preview persisted")
	}
	sentinel := errors.New("snapshot changed")
	_, e = owner.RepairAll(ctx, RepairCommand{SelectedCharacterID: 77, ValidateSnapshot: func(repo.CharacterRecord, repo.InventoryRecord, repo.EquipmentRecord) error { return sentinel }}, resolver)
	if !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
	after, _, _ = g.Character.Load(ctx, "77")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(eq, loadTestEquipment(t, ctx, g, "77")) {
		t.Fatal("guard persisted")
	}
	committed, e := owner.RepairAll(ctx, RepairCommand{SelectedCharacterID: 77, QuickRepair: true}, resolver)
	if e != nil || committed.Cost != result.Cost || !committed.Changed {
		t.Fatal(committed, e)
	}
}
