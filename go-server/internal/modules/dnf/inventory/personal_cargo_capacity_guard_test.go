package inventory

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestPersonalCargoCapacityBoundaryIncludesMissingExpansionRecord(t *testing.T) {
	for _, tc := range []struct {
		capacity int
		slot     int16
		allowed  bool
	}{{0, 7, true}, {0, 8, false}, {8, 8, false}, {24, 23, true}, {24, 24, false}, {200, 199, true}, {200, 200, false}} {
		t.Run(strconv.Itoa(tc.capacity)+"/"+strconv.Itoa(int(tc.slot)), func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			before := dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:9": {ItemID: 500, Count: 1}}, Warehouse: map[string]dnfrepo.ItemStack{}}
			if err := repos.Inventory.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			if tc.capacity != 0 {
				settings := capacityTestSettings("1", 0)
				settings.Values["personal_cargo_list_param16"] = strconv.Itoa(tc.capacity)
				if err := repos.Settings.Save(ctx, settings); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ = repos.Inventory.Load(ctx, "1")
			owner, _ := NewOwner(repos)
			_, err := owner.Move(ctx, Command{SelectedCharacterID: 1, SourceListType: 0, SourceSlotIndex: 9, DestinationListType: 2, DestinationSlotIndex: tc.slot, MoveCount: 1})
			after, _, _ := repos.Inventory.Load(ctx, "1")
			if tc.allowed {
				if err != nil || after.Warehouse[slotKey(2, tc.slot)].ItemID != 500 {
					t.Fatalf("已开放格应可存入: %v %+v", err, after)
				}
			} else if !errors.Is(err, ErrMoveSlotLocked) || !reflect.DeepEqual(before, after) {
				t.Fatalf("未开放格必须拒绝且保持资产: %v %+v", err, after)
			}
		})
	}
}

func TestPersonalCargoMissingStateAutoMergeSkipsLockedHistoricalRow(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	stack := dnfrepo.ItemStack{ItemID: 100, Count: 3, Extra: map[string]string{"item_kind": "stackable", "stack_limit": "100", "pvf_path": "stackable/test.stk"}}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:121": stack}, Warehouse: map[string]dnfrepo.ItemStack{"2:8": stack}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := NewOwner(repos)
	result, err := owner.Move(ctx, Command{AccountID: "a", SelectedCharacterID: 1, SourceListType: 0, SourceSlotIndex: 121, DestinationListType: 2, DestinationSlotIndex: 0, MoveCount: 3})
	after, _, _ := repos.Inventory.Load(ctx, "1")
	if err != nil || result.DestinationSlotIndex != 0 || after.Warehouse["2:8"].Count != 3 || after.Warehouse["2:0"].Count != 3 {
		t.Fatalf("缺记录时不能并入第9格，历史物品保持: %+v %v %+v", result, err, after)
	}
}

func TestPersonalCargoMissingStateFullBaseCapacityCannotFindLockedFallback(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	rows := make(map[string]dnfrepo.ItemStack)
	for slot := int16(0); slot < 8; slot++ {
		rows[slotKey(2, slot)] = dnfrepo.ItemStack{ItemID: 100 + int64(slot), Count: 1}
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:9": {ItemID: 500, Count: 1}}, Warehouse: rows}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Inventory.Load(ctx, "1")
	owner, _ := NewOwner(repos)
	_, err := owner.Move(ctx, Command{SelectedCharacterID: 1, SourceListType: 0, SourceSlotIndex: 9, DestinationListType: 2, DestinationSlotIndex: 0, MoveCount: 1})
	after, _, _ := repos.Inventory.Load(ctx, "1")
	if !errors.Is(err, ErrCargoIncompatibleStack) || !reflect.DeepEqual(before, after) {
		t.Fatalf("基础8格满时不能寻位到未开放格: %v %+v", err, after)
	}
}

func TestPersonalCargoSharedMaterialMoveCannotBypassCapacity(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, capacity := range []int{0, 8, 24} {
			t.Run(strconv.FormatBool(reverse)+"/"+strconv.Itoa(capacity), func(t *testing.T) {
				ctx := context.Background()
				repos := memory.NewMemoryGroup()
				if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "1", Warehouse: map[string]dnfrepo.ItemStack{}}); err != nil {
					t.Fatal(err)
				}
				if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "a", Slots: map[string]dnfrepo.ItemStack{"0:354": pvfMoveStack(3227, 5, 10)}}); err != nil {
					t.Fatal(err)
				}
				if capacity != 0 {
					settings := capacityTestSettings("1", 0)
					settings.Values["personal_cargo_list_param16"] = strconv.Itoa(capacity)
					if err := repos.Settings.Save(ctx, settings); err != nil {
						t.Fatal(err)
					}
				}
				beforeCharacter, _, _ := repos.Inventory.Load(ctx, "1")
				beforeAccount, _, _ := repos.AccountInventory.Load(ctx, "a")
				cmd := Command{AccountID: "a", SelectedCharacterID: 1, SourceListType: 0, SourceSlotIndex: 354, DestinationListType: 2, DestinationSlotIndex: 8, MoveCount: 2}
				if reverse {
					cmd.SourceListType, cmd.DestinationListType = cmd.DestinationListType, cmd.SourceListType
					cmd.SourceSlotIndex, cmd.DestinationSlotIndex = cmd.DestinationSlotIndex, cmd.SourceSlotIndex
				}
				owner, _ := NewOwner(repos)
				_, err := owner.Move(ctx, cmd)
				afterCharacter, _, _ := repos.Inventory.Load(ctx, "1")
				afterAccount, _, _ := repos.AccountInventory.Load(ctx, "a")
				if capacity == 24 {
					if err != nil || afterCharacter.Warehouse["2:8"].Count != 2 || afterAccount.Slots["0:354"].Count != 3 {
						t.Fatalf("已扩容的共享材料移动应保持可用: %v %+v %+v", err, afterCharacter, afterAccount)
					}
				} else if !errors.Is(err, ErrMoveSlotLocked) || !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeAccount, afterAccount) {
					t.Fatalf("共享材料正反向移动不能绕过未解锁格: %v %+v %+v", err, afterCharacter, afterAccount)
				}
			})
		}
	}
}

func TestPersonalCargoMissingStateSortKeepsItemsWhenBaseCapacityIsInsufficient(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	rows := make(map[string]dnfrepo.ItemStack)
	for slot := int16(0); slot < 9; slot++ {
		rows[slotKey(2, slot)] = dnfrepo.ItemStack{ItemID: 100 + int64(slot), Count: 1}
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "1", Warehouse: rows}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Inventory.Load(ctx, "1")
	owner, _ := NewOwner(repos)
	_, err := owner.Sort(ctx, Command{SelectedCharacterID: 1, SourceListType: 2, Category: 11})
	after, _, _ := repos.Inventory.Load(ctx, "1")
	if !errors.Is(err, ErrMoveSlotLocked) || !reflect.DeepEqual(before, after) {
		t.Fatalf("缺记录的个人金库不能把9件整理进基础8格，旧物品应保持: %v %+v", err, after)
	}
}

func TestSharedMaterialMoveChecksBackpackUnlockedCapacity(t *testing.T) {
	for _, expansion := range []int{0, 8} {
		t.Run(strconv.Itoa(expansion), func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "a", Slots: map[string]dnfrepo.ItemStack{"0:354": pvfMoveStack(3227, 5, 10)}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Settings.Save(ctx, capacityTestSettings("1", expansion)); err != nil {
				t.Fatal(err)
			}
			beforeCharacter, _, _ := repos.Inventory.Load(ctx, "1")
			beforeAccount, _, _ := repos.AccountInventory.Load(ctx, "a")
			owner, _ := NewOwner(repos)
			_, err := owner.Move(ctx, Command{AccountID: "a", SelectedCharacterID: 1, SourceListType: 0, SourceSlotIndex: 354, DestinationListType: 0, DestinationSlotIndex: 153, MoveCount: 2})
			afterCharacter, _, _ := repos.Inventory.Load(ctx, "1")
			afterAccount, _, _ := repos.AccountInventory.Load(ctx, "a")
			if expansion == 8 {
				if err != nil || afterCharacter.Slots["0:153"].Count != 2 || afterAccount.Slots["0:354"].Count != 3 {
					t.Fatalf("已扩容材料格应可承接: %v %+v %+v", err, afterCharacter, afterAccount)
				}
			} else if !errors.Is(err, ErrMoveSlotLocked) || !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeAccount, afterAccount) {
				t.Fatalf("共享材料也不能落入未解锁主包格: %v %+v %+v", err, afterCharacter, afterAccount)
			}
		})
	}
}
