package inventory

import (
	"context"
	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
)

func TestBuffRegisteredInstanceSurvivesLegalBagMoveAndSort(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "account77"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:20": {ItemID: 200, Count: 1, Extra: map[string]string{"item_kind": "equipment"}}, "0:21": {ItemID: 200, Count: 1, Extra: map[string]string{"item_kind": "equipment"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := buffswap.Apply(ctx, r, "77", "account77", buffswap.Command{Skill: 38, Slot: 12, List: 0, Index: 20}); err != nil {
		t.Fatal(err)
	}
	state, _, _ := buffswap.Load(ctx, r, "77", "account77")
	owner, err := NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := owner.Move(ctx, Command{Operation: "move_itemspace", AccountID: "account77", SelectedCharacterID: 77, SourceListType: 0, SourceSlotIndex: 20, SourceInstanceValue: 200, MoveCount: 1, DestinationListType: 0, DestinationSlotIndex: 25})
	if err != nil || !moved.Changed || moved.Mode != "move" {
		t.Fatal("bag move failed", moved, err)
	}
	out, omitted, err := buffswap.Resolve(ctx, r, "77", state)
	if err != nil || omitted != 0 || out.Slots[12].Index != 25 {
		t.Fatal("bag move lost identity", out, omitted, err)
	}
	sorted, err := owner.Sort(ctx, Command{Operation: "sort_item", AccountID: "account77", SelectedCharacterID: 77, SourceListType: 0, Category: 1})
	if err != nil || !sorted.Changed {
		t.Fatal("sort failed", sorted, err)
	}
	out, omitted, err = buffswap.Resolve(ctx, r, "77", state)
	if err != nil || omitted != 0 || out.Slots[12].Instance != state.Slots[12].Instance || out.Slots[12].Index == 25 {
		t.Fatal("sort lost identity", out, omitted, err)
	}
}

func TestBuffInstanceAccountCargoTransferPreservesItemButNotForeignBinding(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	if err := r.Account.Save(ctx, repo.AccountRecord{AccountID: "account77", Metadata: map[string]string{"account_cargo_created": "true", "account_cargo_level": "8"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"77", "78"} {
		if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: "account77"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:20": {ItemID: 200, Count: 1, Extra: map[string]string{"item_kind": "equipment"}}, "0:21": {ItemID: 200, Count: 1, Extra: map[string]string{"item_kind": "equipment"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "78", Slots: map[string]repo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	if err := buffswap.Apply(ctx, r, "77", "account77", buffswap.Command{Skill: 38, Slot: 12, List: 0, Index: 20}); err != nil {
		t.Fatal(err)
	}
	state, _, _ := buffswap.Load(ctx, r, "77", "account77")
	owner, err := NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []Command{{Operation: "move_itemspace", AccountID: "account77", SelectedCharacterID: 77, SourceListType: 0, SourceSlotIndex: 20, MoveCount: 1, DestinationListType: 12, DestinationSlotIndex: 2}, {Operation: "move_itemspace", AccountID: "account77", SelectedCharacterID: 78, SourceListType: 12, SourceSlotIndex: 2, MoveCount: 1, DestinationListType: 0, DestinationSlotIndex: 22}} {
		result, err := owner.Move(ctx, cmd)
		if err != nil || !result.Changed {
			t.Fatal("legal account transfer failed", result, err)
		}
		out, omitted, err := buffswap.Resolve(ctx, r, "77", state)
		if err != nil || omitted != 1 || len(out.Slots) != 0 {
			t.Fatal("old owner rebound template or foreign item", out, omitted, err)
		}
	}
	other, _, _ := r.Inventory.Load(ctx, "78")
	if other.Slots["0:22"].Extra["native_buff_instance"] != state.Slots[12].Instance {
		t.Fatal("transfer dropped instance")
	}
	if _, found, _ := buffswap.Load(ctx, r, "78", "account77"); found {
		t.Fatal("transfer copied character registration")
	}
}
