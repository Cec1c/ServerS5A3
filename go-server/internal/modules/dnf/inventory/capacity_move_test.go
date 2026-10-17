package inventory

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"

	dnfplacement "longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func capacityTestSettings(characterID string, expansion int) dnfrepo.SettingsRecord {
	return dnfrepo.SettingsRecord{Scope: dnfrepo.CharacterContainerStateScope(characterID), Values: map[string]string{
		"main_list_param16": strconv.Itoa(expansion), "avatar_list_param16": "0", "personal_cargo_list_param16": "8",
	}}
}

func TestCargoWithdrawalChecksFamilyAndUnlockedCapacity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		slot      int16
		expansion int
		want      error
	}{
		{"材料不能进入消耗品页", 65, 0, ErrMoveItemPlacement},
		{"材料不能进入未解锁格", 153, 0, ErrMoveSlotLocked},
		{"已扩容允许材料格", 153, 8, nil},
		{"材料允许快捷格", 3, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			before := dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{}, Warehouse: map[string]dnfrepo.ItemStack{"2:0": {ItemID: 101, Count: 5, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/a.stk", "stackable_type": "[material]"}}}}
			if err := repos.Inventory.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			before, _, _ = repos.Inventory.Load(ctx, "1")
			if err := repos.Settings.Save(ctx, capacityTestSettings("1", tc.expansion)); err != nil {
				t.Fatal(err)
			}
			owner, _ := NewOwner(repos)
			cmd := Command{SelectedCharacterID: 1, SourceListType: 2, SourceSlotIndex: 0, DestinationListType: 0, DestinationSlotIndex: tc.slot, MoveCount: 5,
				ItemPlacement: func(stack dnfrepo.ItemStack) (dnfplacement.SlotPolicy, error) {
					return dnfplacement.SlotPolicy{ListType: 0, Start: 121, End: 176, AllowQuickSlots: true}, nil
				}}
			got, err := owner.Move(ctx, cmd)
			after, _, loadErr := repos.Inventory.Load(ctx, "1")
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if tc.want != nil {
				if !errors.Is(err, tc.want) || !reflect.DeepEqual(before.Slots, after.Slots) || !reflect.DeepEqual(before.Warehouse, after.Warehouse) {
					t.Fatalf("拒绝应保留资产: err=%v result=%+v after=%+v", err, got, after)
				}
				ack := ownerRejectedMoveResult(19, cmd, err)
				if !ack.ResponseAllowed || len(ack.UpperResponses) != 1 || !reflect.DeepEqual(ack.UpperResponses[0].Body, []byte{0, 17, 2, 0}) {
					t.Fatalf("错误移动应结束原生拖拽: %+v", ack)
				}
			} else {
				if err != nil || !got.Changed || after.Slots[slotKey(0, tc.slot)].Count != 5 || len(after.Warehouse) != 0 {
					t.Fatalf("合法取出失败: %+v %v after=%+v", got, err, after)
				}
			}
		})
	}
}

func TestCargoAutoMergeSkipsWrongPageAndLockedHistoricalRows(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	stack := dnfrepo.ItemStack{ItemID: 101, Count: 5, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/a.stk", "stackable_type": "[material]", "stack_limit": "100"}}
	before := dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:65": stack, "0:153": stack, "0:125": stack}, Warehouse: map[string]dnfrepo.ItemStack{"2:0": stack}}
	if err := repos.Inventory.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	if err := repos.Settings.Save(ctx, capacityTestSettings("1", 0)); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "acc"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := NewOwner(repos)
	cmd := Command{AccountID: "acc", SelectedCharacterID: 1, SourceListType: 2, SourceSlotIndex: 0, DestinationListType: 0, DestinationSlotIndex: 121, MoveCount: 5,
		ItemPlacement: func(dnfrepo.ItemStack) (dnfplacement.SlotPolicy, error) {
			return dnfplacement.SlotPolicy{ListType: 0, Start: 121, End: 176, AllowQuickSlots: true}, nil
		}}
	got, err := owner.Move(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	after, _, _ := repos.Inventory.Load(ctx, "1")
	if got.Mode != "auto_stack" || got.DestinationSlotIndex != 125 || after.Slots["0:65"].Count != 5 || after.Slots["0:153"].Count != 5 || after.Slots["0:125"].Count != 10 || len(after.Warehouse) != 0 {
		t.Fatalf("应只并入已开放的材料格: %+v after=%+v", got, after)
	}
	applied := ownerAppliedMoveResult(ctx, repos.Settings, 19, cmd, got)
	if len(applied.UpperResponses) != 3 || len(applied.AuthoritativeListRefreshes) != 1 || applied.AuthoritativeListRefreshes[0] != listTypeMain || applied.UpperResponses[0].Body[9] != 125 {
		t.Fatalf("ACK与刷新必须指向实际合并格: %+v", applied)
	}
}

func TestSortTooManyItemsForUnlockedCapacityKeepsEveryItem(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	slots := map[string]dnfrepo.ItemStack{}
	for i := int16(9); i <= 41; i++ {
		slots[slotKey(0, i)] = dnfrepo.ItemStack{ItemID: int64(i), Count: 1}
	}
	before := dnfrepo.InventoryRecord{CharacterID: "1", Slots: slots}
	if err := repos.Inventory.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	if err := repos.Settings.Save(ctx, capacityTestSettings("1", 0)); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "acc"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := NewOwner(repos)
	_, err := owner.Sort(ctx, Command{AccountID: "acc", SelectedCharacterID: 1, SourceListType: 0, Category: 1})
	after, _, _ := repos.Inventory.Load(ctx, "1")
	if !errors.Is(err, ErrMoveSlotLocked) || !reflect.DeepEqual(before.Slots, after.Slots) {
		t.Fatalf("33件超过32格时不能丢物品: err=%v after=%+v", err, after)
	}
}

func TestAccountCargoWithdrawalRejectsWrongPageWithoutChangingEitherOwner(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "acc", Metadata: map[string]string{"account_cargo_level": "8"}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Settings.Save(ctx, capacityTestSettings("1", 0)); err != nil {
		t.Fatal(err)
	}
	before := dnfrepo.AccountInventoryRecord{AccountID: "acc", Slots: map[string]dnfrepo.ItemStack{"12:0": {ItemID: 101, Count: 5}}}
	if err := repos.AccountInventory.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	owner, _ := NewOwner(repos)
	_, err := owner.Move(ctx, Command{AccountID: "acc", SelectedCharacterID: 1, SourceListType: 12, SourceSlotIndex: 0, DestinationListType: 0, DestinationSlotIndex: 65, MoveCount: 5,
		ItemPlacement: func(dnfrepo.ItemStack) (dnfplacement.SlotPolicy, error) {
			return dnfplacement.SlotPolicy{ListType: 0, Start: 121, End: 176, AllowQuickSlots: true}, nil
		}})
	after, _, _ := repos.AccountInventory.Load(ctx, "acc")
	character, _, _ := repos.Inventory.Load(ctx, "1")
	if !errors.Is(err, ErrMoveItemPlacement) || !reflect.DeepEqual(before.Slots, after.Slots) || len(character.Slots) != 0 {
		t.Fatalf("账号仓取错分页应保留双方资产: err=%v account=%+v character=%+v", err, after, character)
	}
}
