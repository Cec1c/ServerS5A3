package inventory

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dnfplacement "longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestCargoOccupiedTargetFindsCompatibleStackOrEmptySlot(t *testing.T) {
	for _, list := range []byte{2, 12} {
		for _, merge := range []bool{false, true} {
			t.Run(slotKey(list, 0)+map[bool]string{false: "空格", true: "合并"}[merge], func(t *testing.T) {
				ctx := context.Background()
				repos := memory.NewMemoryGroup()
				moving := dnfrepo.ItemStack{ItemID: 100, Count: 5, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/a.stk", "stack_limit": "100", "seal_flag": "1"}}
				other := dnfrepo.ItemStack{ItemID: 200, Count: 1}
				rows := map[string]dnfrepo.ItemStack{slotKey(list, 0): other}
				if merge {
					target := cloneStack(moving)
					target.Count = 20
					rows[slotKey(list, 4)] = target
				}
				character := dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:121": moving}, Warehouse: rows}
				if list == 12 {
					character.Warehouse = map[string]dnfrepo.ItemStack{}
					_ = repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "a", Metadata: map[string]string{"account_cargo_level": "8"}})
					_ = repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "a", Slots: rows})
				}
				_ = repos.Inventory.Save(ctx, character)
				_ = repos.Settings.Save(ctx, capacityTestSettings("1", 0))
				owner, _ := NewOwner(repos)
				cmd := Command{SelectedCharacterID: 1, AccountID: "a", SourceListType: 0, SourceSlotIndex: 121, DestinationListType: list, DestinationSlotIndex: 0, MoveCount: 5}
				result, err := owner.Move(ctx, cmd)
				if err != nil {
					t.Fatal(err)
				}
				var after map[string]dnfrepo.ItemStack
				if list == 12 {
					value, _, _ := repos.AccountInventory.Load(ctx, "a")
					after = value.Slots
				} else {
					value, _, _ := repos.Inventory.Load(ctx, "1")
					after = value.Warehouse
				}
				wantSlot := int16(1)
				wantCount := int64(5)
				if merge {
					wantSlot = 4
					wantCount = 25
				}
				if !reflect.DeepEqual(after[slotKey(list, 0)], other) || after[slotKey(list, wantSlot)].Count != wantCount || result.DestinationSlotIndex != wantSlot {
					t.Fatalf("应跳过占用目标并报告实际落点: %+v %+v", result, after)
				}
				if after[slotKey(list, wantSlot)].Extra["seal_flag"] != "1" {
					t.Fatal("寻位不能改变封装")
				}
				ack := ownerAppliedMoveResult(ctx, repos.Settings, 19, cmd, result)
				if ack.UpperResponses[0].Body[len(ack.UpperResponses[0].Body)-2] != byte(wantSlot) {
					t.Fatalf("ACK应使用真实落点: %+v", ack)
				}
			})
		}
	}
}

func TestCargoFullTargetRefusesWithoutAssetChanges(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	rows := make(map[string]dnfrepo.ItemStack)
	for slot := int16(0); slot < 8; slot++ {
		rows[slotKey(2, slot)] = dnfrepo.ItemStack{ItemID: 200 + int64(slot), Count: 1}
	}
	before := dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:9": {ItemID: 100, Count: 1}}, Warehouse: rows}
	_ = repos.Inventory.Save(ctx, before)
	_ = repos.Settings.Save(ctx, capacityTestSettings("1", 0))
	before, _, _ = repos.Inventory.Load(ctx, "1")
	owner, _ := NewOwner(repos)
	_, err := owner.Move(ctx, Command{SelectedCharacterID: 1, SourceListType: 0, SourceSlotIndex: 9, DestinationListType: 2, DestinationSlotIndex: 0})
	after, _, _ := repos.Inventory.Load(ctx, "1")
	if !errors.Is(err, ErrCargoIncompatibleStack) || !reflect.DeepEqual(before, after) {
		t.Fatalf("满仓库应拒绝且不交换/丢物: %v %+v", err, after)
	}
}

func TestSameCargoManualSplitHonorsRequestedEmptySlot(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	stack := dnfrepo.ItemStack{ItemID: 100, Count: 30, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/a.stk", "stack_limit": "100"}}
	before := dnfrepo.InventoryRecord{CharacterID: "1", Warehouse: map[string]dnfrepo.ItemStack{"2:0": stack, "2:1": cloneStack(stack)}}
	_ = repos.Inventory.Save(ctx, before)
	_ = repos.Settings.Save(ctx, capacityTestSettings("1", 0))
	owner, _ := NewOwner(repos)
	_, err := owner.Move(ctx, Command{SelectedCharacterID: 1, SourceListType: 2, SourceSlotIndex: 0, DestinationListType: 2, DestinationSlotIndex: 3, MoveCount: 10})
	if err != nil {
		t.Fatal(err)
	}
	after, _, _ := repos.Inventory.Load(ctx, "1")
	if after.Warehouse["2:0"].Count != 20 || after.Warehouse["2:1"].Count != 30 || after.Warehouse["2:3"].Count != 10 {
		t.Fatalf("同仓库拆分不能转而并入其他堆: %+v", after)
	}
}

func TestCargoWithdrawalOccupiedMainUsesCorrectPageAndQuickFallback(t *testing.T) {
	cmd := Command{SourceListType: 2, SourceSlotIndex: 0, DestinationListType: 0, DestinationSlotIndex: 121, MoveCount: 1, ItemPlacement: func(dnfrepo.ItemStack) (dnfplacement.SlotPolicy, error) {
		return dnfplacement.SlotPolicy{ListType: 0, Start: 121, End: 176, AllowQuickSlots: true}, nil
	}}
	source := dnfrepo.ItemStack{ItemID: 100, Count: 1}
	src := map[string]dnfrepo.ItemStack{"2:0": source}
	dst := map[string]dnfrepo.ItemStack{}
	for slot := int16(121); slot <= 152; slot++ {
		dst[slotKey(0, slot)] = dnfrepo.ItemStack{ItemID: 200, Count: 1}
	}
	state := &dnfrepo.CharacterContainerState{PersonalCargoSlotCount: 8}
	planned, err := planCargoMove(cmd, src, dst, state, 0)
	if err != nil || planned.DestinationSlotIndex != 3 {
		t.Fatalf("已开放材料页满应落快捷格: %+v %v", planned, err)
	}
	delete(dst, "0:122")
	planned, err = planCargoMove(cmd, src, dst, state, 0)
	if err != nil || planned.DestinationSlotIndex != 122 {
		t.Fatalf("材料页有空应优先材料页: %+v %v", planned, err)
	}
	if planned.DestinationSlotIndex == 153 || planned.DestinationSlotIndex == 65 {
		t.Fatal("不能落锁定格或其他分类页")
	}
}

func TestCargoInsertValidatorRejectsBeforeMutationAndReturnsFailure(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	before := dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:9": {ItemID: 100, Count: 1}}, Warehouse: map[string]dnfrepo.ItemStack{}}
	_ = repos.Inventory.Save(ctx, before)
	_ = repos.Settings.Save(ctx, capacityTestSettings("1", 0))
	before, _, _ = repos.Inventory.Load(ctx, "1")
	owner, _ := NewOwner(repos)
	cmd := Command{SelectedCharacterID: 1, SourceListType: 0, SourceSlotIndex: 9, DestinationListType: 2, DestinationSlotIndex: 0, CargoInsert: func(dnfrepo.ItemStack, byte) error { return errors.New("PVF禁止存入") }}
	_, err := owner.Move(ctx, cmd)
	after, _, _ := repos.Inventory.Load(ctx, "1")
	if !errors.Is(err, ErrCargoInsertRejected) || !reflect.DeepEqual(before, after) {
		t.Fatalf("禁止存入必须回滚: %v %+v", err, after)
	}
	for _, failure := range []error{err, ErrSlotNotFound, ErrAccountCargoNotCreated, ErrAccountCargoSlotOutOfRange} {
		result := ownerRejectedMoveResult(19, cmd, failure)
		if !result.ResponseAllowed || !reflect.DeepEqual(result.UpperResponses[0].Body, []byte{0, 17, 0, 2}) {
			t.Fatalf("失败应结束等待: %+v", result)
		}
	}
}

func TestCargoPartialSwapAndFullSameItemMergeMustReject(t *testing.T) {
	for _, sameItem := range []bool{false, true} {
		t.Run(map[bool]string{false: "部分移动不能整组交换", true: "同品满堆不能交换"}[sameItem], func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			row := dnfrepo.ItemStack{ItemID: 100, Count: 100, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/a.stk", "stack_limit": "100"}}
			target := dnfrepo.ItemStack{ItemID: 200, Count: 1}
			if sameItem {
				target = dnfrepo.CloneInventory(dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{"x": row}}).Slots["x"]
			}
			before := dnfrepo.InventoryRecord{CharacterID: "1", Warehouse: map[string]dnfrepo.ItemStack{"2:0": row, "2:1": target}}
			if err := repos.Inventory.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			if err := repos.Settings.Save(ctx, capacityTestSettings("1", 0)); err != nil {
				t.Fatal(err)
			}
			before, _, _ = repos.Inventory.Load(ctx, "1")
			owner, _ := NewOwner(repos)
			_, err := owner.Move(ctx, Command{SelectedCharacterID: 1, SourceListType: 2, SourceSlotIndex: 0, DestinationListType: 2, DestinationSlotIndex: 1, MoveCount: 10})
			after, _, _ := repos.Inventory.Load(ctx, "1")
			if err == nil || !reflect.DeepEqual(before.Warehouse, after.Warehouse) {
				t.Fatalf("拒绝必须保持数量与格位: err=%v after=%+v", err, after)
			}
		})
	}
}
