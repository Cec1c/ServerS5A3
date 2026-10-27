package inventory

import (
	"context"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestIntegratedCargoSortPreservesLockedSlotAndPurchasedCapacity(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	settings := capacityTestSettings("1", 0)
	settings.Values["personal_cargo_list_param16"] = "24"
	if err := repos.Settings.Save(ctx, settings); err != nil {
		t.Fatal(err)
	}
	rows := map[string]dnfrepo.ItemStack{
		"2:0":  {ItemID: 900, Count: 1, Extra: map[string]string{SortLockExtraKey: "1"}},
		"2:23": {ItemID: 100, Count: 1},
		"2:22": {ItemID: 200, Count: 1},
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "1", Warehouse: rows}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Sort(ctx, Command{SelectedCharacterID: 1, SourceListType: 2, Category: 11})
	if err != nil || result.EndSlot != 23 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	after, _, err := repos.Inventory.Load(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Warehouse) != 3 || after.Warehouse["2:0"].ItemID != 900 || !IsSortLocked(after.Warehouse["2:0"]) || after.Warehouse["2:1"].ItemID != 100 || after.Warehouse["2:2"].ItemID != 200 {
		t.Fatalf("sort lost capacity or lock: %+v", after.Warehouse)
	}
}
