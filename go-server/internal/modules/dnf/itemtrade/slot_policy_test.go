package itemtrade

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"

	dnfplacement "longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestTradeUsesResolvedMaterialCategoryAndQuickSlotFallback(t *testing.T) {
	for mode := 0; mode < 3; mode++ {
		t.Run(strconv.Itoa(mode), func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			incoming := dnfrepo.ItemStack{ItemID: 3175, Count: 2, Extra: map[string]string{"item_kind": "stackable", "stackable_type": "[material]", "pvf_path": "stackable/material/test.stk", "stack_limit": "99"}}
			// 来源故意在消耗品页，收件落点必须由解析器的物品分类给出。
			sender := dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:65": incoming}}
			receiver := dnfrepo.InventoryRecord{CharacterID: "2", Slots: map[string]dnfrepo.ItemStack{}}
			if mode > 0 {
				for slot := 121; slot <= 152; slot++ {
					receiver.Slots["0:"+strconv.Itoa(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
				}
			}
			if mode == 2 {
				for slot := 3; slot <= 8; slot++ {
					receiver.Slots["0:"+strconv.Itoa(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
				}
			}
			if err := repos.Inventory.Save(ctx, sender); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, receiver); err != nil {
				t.Fatal(err)
			}
			owner, err := NewOwner(repos, func(Offer, dnfrepo.ItemStack) (dnfplacement.SlotPolicy, error) {
				return dnfplacement.SlotPolicy{ListType: 0, Start: 121, End: 176, AllowQuickSlots: true}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.Exchange(ctx, Participant{CharacterID: "1", Offers: []Offer{{TradeSlot: 0, SourceList: 0, SourceSlot: 65, Count: 2}}}, Participant{CharacterID: "2"})
			if mode == 2 {
				if !errors.Is(err, ErrInventoryFull) {
					t.Fatalf("两处皆满应整笔拒绝：%v", err)
				}
				first, _, _ := repos.Inventory.Load(ctx, "1")
				second, _, _ := repos.Inventory.Load(ctx, "2")
				if !reflect.DeepEqual(first.Slots, sender.Slots) || !reflect.DeepEqual(second.Slots, receiver.Slots) {
					t.Fatal("交易失败改变了任一方物品")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := int16(121)
			if mode == 1 {
				want = 3
			}
			got := result.Inventories["2"].Slots[slotKey(0, want)]
			if got.ItemID != 3175 || got.Count != 2 || result.Received["2"][0].DestinationSlot != want {
				t.Fatalf("交易分类落点错误：want=%d result=%+v", want, result)
			}
		})
	}
}

func TestTradeEquipmentUsesBagThenQuickSlots(t *testing.T) {
	for mode := 0; mode < 3; mode++ {
		t.Run(strconv.Itoa(mode), func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			incoming := dnfrepo.ItemStack{ItemID: 700, Count: 1, Extra: map[string]string{"item_kind": "equipment", "equipment_type": "[weapon]"}}
			sender := dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:65": incoming}}
			receiver := dnfrepo.InventoryRecord{CharacterID: "2", Slots: map[string]dnfrepo.ItemStack{"0:3": incoming}}
			if mode > 0 {
				for slot := 9; slot <= 40; slot++ {
					receiver.Slots["0:"+strconv.Itoa(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
				}
			}
			if mode == 2 {
				for slot := 4; slot <= 8; slot++ {
					receiver.Slots["0:"+strconv.Itoa(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
				}
			}
			if err := repos.Inventory.Save(ctx, sender); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, receiver); err != nil {
				t.Fatal(err)
			}
			owner, err := NewOwner(repos, func(Offer, dnfrepo.ItemStack) (dnfplacement.SlotPolicy, error) {
				return dnfplacement.SlotPolicy{ListType: 0, Start: 9, End: 64, AllowQuickSlots: true}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.Exchange(ctx, Participant{CharacterID: "1", Offers: []Offer{{TradeSlot: 0, SourceList: 0, SourceSlot: 65, Count: 1}}}, Participant{CharacterID: "2"})
			if mode == 2 {
				first, _, _ := repos.Inventory.Load(ctx, "1")
				second, _, _ := repos.Inventory.Load(ctx, "2")
				if !errors.Is(err, ErrInventoryFull) || !reflect.DeepEqual(first.Slots, sender.Slots) || !reflect.DeepEqual(second.Slots, receiver.Slots) {
					t.Fatalf("满包交易未整笔回滚：err=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := int16(9)
			if mode == 1 {
				want = 4
			}
			got := result.Inventories["2"].Slots[slotKey(0, want)]
			if got.ItemID != 700 || got.Count != 1 || result.Received["2"][0].DestinationSlot != want || result.Inventories["2"].Slots["0:3"].Count != 1 || len(result.Inventories["1"].Slots) != 0 {
				t.Fatalf("交易装备落点错误或合并已有装备：want=%d result=%+v", want, result)
			}
		})
	}
}
