package inventory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	r "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestQuickEquipmentCommandKeepsPageGuardIndependent(t *testing.T) {
	cmd := NewMoveCommand(alignedcmd.Request{InventoryItemPlacement: quickEquipmentTestPolicy}, MoveItemspaceRequest{SourceListType: 0, SourceSlotIndex: 9, DestinationListType: 0, DestinationSlotIndex: 3})
	if cmd.QuickEquipmentPlacement == nil || cmd.ItemPlacement != nil {
		t.Fatal("同背包必须传快捷装备分类，普通分页仍保持原开关语义")
	}
	cmd = NewMoveCommand(alignedcmd.Request{InventoryItemPlacement: quickEquipmentTestPolicy}, MoveItemspaceRequest{SourceListType: 2, SourceSlotIndex: 0, DestinationListType: 0, DestinationSlotIndex: 3})
	if cmd.QuickEquipmentPlacement == nil || cmd.ItemPlacement == nil {
		t.Fatal("金库存取必须同时保持原分页与快捷装备检查")
	}
}

func quickEquipmentTestPolicy(stack r.ItemStack) (itemplacement.SlotPolicy, error) {
	if stack.ItemID == 999 {
		return itemplacement.SlotPolicy{}, fmt.Errorf("测试PVF读取失败")
	}
	return itemplacement.SlotPolicy{ListType: 0, Start: 9, End: 64, AllowQuickSlots: true,
		SingleQuickEquipment: stack.ItemID == 100 || stack.ItemID == 101}, nil
}

func quickEquipmentTestStack(id int64) r.ItemStack {
	return r.ItemStack{ItemID: id, Count: 1, Extra: map[string]string{"item_kind": "equipment", "实例": fmt.Sprint(id)}}
}

func TestQuickEquipmentMoveLimit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		slots    map[string]int64
		from, to int16
		reject   bool
	}{
		{"首件放入", map[string]int64{"0:9": 100}, 9, 3, false},
		{"不同快捷装备", map[string]int64{"0:3": 100, "0:9": 101}, 9, 4, true},
		{"同名第二件", map[string]int64{"0:3": 100, "0:9": 100}, 9, 8, true},
		{"有普通装备可放首件", map[string]int64{"0:3": 200, "0:9": 100}, 9, 8, false},
		{"普通装备不互斥", map[string]int64{"0:3": 200, "0:9": 201}, 9, 4, false},
		{"原件换格", map[string]int64{"0:3": 100}, 3, 8, false},
		{"原件与普通装备换格", map[string]int64{"0:3": 100, "0:8": 200}, 3, 8, false},
		{"直接替换", map[string]int64{"0:3": 100, "0:9": 101}, 9, 3, false},
		{"反向替换", map[string]int64{"0:3": 100, "0:9": 101}, 3, 9, false},
		{"取下", map[string]int64{"0:3": 100}, 3, 9, false},
		{"反向空格不能绕过", map[string]int64{"0:3": 100, "0:9": 101}, 4, 9, true},
		{"反向交换不能绕过", map[string]int64{"0:3": 100, "0:4": 200, "0:9": 101}, 4, 9, true},
		{"第二件替换普通物品不能绕过", map[string]int64{"0:3": 100, "0:4": 200, "0:9": 101}, 9, 4, true},
		{"历史多件可取下", map[string]int64{"0:3": 100, "0:4": 101}, 4, 9, false},
		{"历史多件可换格", map[string]int64{"0:3": 100, "0:4": 101}, 4, 8, false},
		{"其它快捷格无法分类则拒绝新增", map[string]int64{"0:3": 999, "0:9": 100}, 9, 4, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			before := r.InventoryRecord{CharacterID: "77", Slots: map[string]r.ItemStack{}}
			for key, id := range tc.slots {
				before.Slots[key] = quickEquipmentTestStack(id)
			}
			saveTestInventory(t, ctx, repos, before)
			o, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			_, err = o.Move(ctx, Command{SelectedCharacterID: 77, SourceListType: 0, SourceSlotIndex: tc.from, DestinationListType: 0, DestinationSlotIndex: tc.to, MoveCount: 1, ItemPlacement: quickEquipmentTestPolicy})
			after := loadTestInventory(t, ctx, repos, "77")
			if tc.reject {
				if !errors.Is(err, ErrMoveItemPlacement) || !reflect.DeepEqual(before.Slots, after.Slots) {
					t.Fatalf("应拒绝且保持实例：%v %+v", err, after)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := r.CloneInventory(before).Slots
			a, aok := want[slotKey(0, tc.from)]
			b, bok := want[slotKey(0, tc.to)]
			delete(want, slotKey(0, tc.from))
			delete(want, slotKey(0, tc.to))
			if aok {
				want[slotKey(0, tc.to)] = a
			}
			if bok {
				want[slotKey(0, tc.from)] = b
			}
			if !reflect.DeepEqual(want, after.Slots) {
				t.Fatalf("移动结果或实例不符：%+v", after.Slots)
			}
		})
	}
}

func TestQuickEquipmentConcurrentMovesUseTransaction(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, r.InventoryRecord{CharacterID: "77", Slots: map[string]r.ItemStack{"0:9": quickEquipmentTestStack(100), "0:10": quickEquipmentTestStack(101)}})
	o, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := int16(0); i < 2; i++ {
		wg.Add(1)
		go func(i int16) {
			defer wg.Done()
			_, err := o.Move(ctx, Command{SelectedCharacterID: 77, SourceListType: 0, SourceSlotIndex: 9 + i, DestinationListType: 0, DestinationSlotIndex: 3 + i, MoveCount: 1, ItemPlacement: quickEquipmentTestPolicy})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrMoveItemPlacement) {
			t.Fatal(err)
		}
	}
	bag := loadTestInventory(t, ctx, repos, "77")
	quick := 0
	for slot := int16(3); slot <= 8; slot++ {
		if bag.Slots[slotKey(0, slot)].ItemID > 0 {
			quick++
		}
	}
	if success != 1 || quick != 1 || len(bag.Slots) != 2 {
		t.Fatalf("并发重复装备：success=%d bag=%+v", success, bag)
	}
}

func TestQuickEquipmentPersonalAndAccountCargoCannotAddSecond(t *testing.T) {
	for _, list := range []byte{listTypePersonalCargo, listTypeAccountCargo} {
		t.Run(fmt.Sprint(list), func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			before := r.InventoryRecord{CharacterID: "77", Slots: map[string]r.ItemStack{"0:3": quickEquipmentTestStack(100)}, Warehouse: map[string]r.ItemStack{}}
			if list == listTypePersonalCargo {
				before.Warehouse[slotKey(list, 0)] = quickEquipmentTestStack(101)
			}
			saveTestInventory(t, ctx, repos, before)
			before = loadTestInventory(t, ctx, repos, "77")
			if err := repos.Account.Save(ctx, r.AccountRecord{AccountID: "acc", Metadata: map[string]string{"account_cargo_created": "true", "account_cargo_level": "8"}}); err != nil {
				t.Fatal(err)
			}
			account := r.AccountInventoryRecord{AccountID: "acc", Slots: map[string]r.ItemStack{"12:0": quickEquipmentTestStack(101)}}
			if err := repos.AccountInventory.Save(ctx, account); err != nil {
				t.Fatal(err)
			}
			o, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			_, err = o.Move(ctx, Command{AccountID: "acc", SelectedCharacterID: 77, SourceListType: list, SourceSlotIndex: 0, DestinationListType: 0, DestinationSlotIndex: 4, MoveCount: 1, ItemPlacement: quickEquipmentTestPolicy})
			after := loadTestInventory(t, ctx, repos, "77")
			cargo, _, loadErr := repos.AccountInventory.Load(ctx, "acc")
			if !errors.Is(err, ErrMoveItemPlacement) || loadErr != nil || !reflect.DeepEqual(before.Slots, after.Slots) || !reflect.DeepEqual(before.Warehouse, after.Warehouse) || !reflect.DeepEqual(account.Slots, cargo.Slots) {
				t.Fatalf("仓库绕过或资产变化：%v %+v %+v", err, after, cargo)
			}
		})
	}
}
