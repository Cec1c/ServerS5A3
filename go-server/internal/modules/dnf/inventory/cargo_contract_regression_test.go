package inventory

import (
	"context"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestCrossCargoDifferentItemsMustNotSwap(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	before := dnfrepo.InventoryRecord{CharacterID: "1",
		Slots:     map[string]dnfrepo.ItemStack{"0:65": {ItemID: 101, Count: 5, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/a.stk"}}},
		Warehouse: map[string]dnfrepo.ItemStack{"2:0": {ItemID: 102, Count: 8, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/b.stk"}}}}
	if err := repos.Inventory.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Move(ctx, Command{SelectedCharacterID: 1, SourceListType: 0, SourceSlotIndex: 65, DestinationListType: 2, DestinationSlotIndex: 0, MoveCount: 5})
	if err != nil {
		t.Fatal(err)
	}
	after, _, loadErr := repos.Inventory.Load(ctx, "1")
	if loadErr != nil || !reflect.DeepEqual(before.Warehouse["2:0"], after.Warehouse["2:0"]) || len(after.Slots) != 0 || after.Warehouse["2:1"].ItemID != 101 || after.Warehouse["2:1"].Count != 5 || result.DestinationSlotIndex != 1 {
		t.Fatalf("不同物品不能交换，应寻到真实空格: before=%+v after=%+v result=%+v err=%v", before, after, result, loadErr)
	}
}

func TestCurrentCargoAckMustRefreshActualChangedRows(t *testing.T) {
	cmd := Command{SourceListType: 0, SourceSlotIndex: 65, DestinationListType: 2, DestinationSlotIndex: 0}
	result := MoveResult{CharacterID: "1", SourceListType: 0, SourceSlotIndex: 65, DestinationListType: 2, DestinationSlotIndex: 0, MoveCount: 5, Mode: "move", Changed: true}
	applied := ownerAppliedMoveResult(context.Background(), nil, 19, cmd, result)
	if len(applied.ItemSlotRefreshes) != 2 {
		t.Fatalf("跨仓库移动成功后必须同步两格权威物品行，实际=%+v", applied)
	}
}
