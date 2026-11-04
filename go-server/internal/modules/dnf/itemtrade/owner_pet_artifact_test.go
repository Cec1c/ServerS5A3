package itemtrade

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestPetArtifactExchangePreservesContainerAndRollsBack(t *testing.T) {
	for _, mode := range []string{"success", "full", "bound", "body_slot"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			slot := int16(140)
			if mode == "body_slot" {
				slot = 0
			}
			stack := r.ItemStack{ItemID: 9001, Count: 1, Bind: mode == "bound", RawEntry: []byte{1, 2, 3}, Extra: map[string]string{"quality_seed": "42"}}
			key := fmt.Sprintf("7:%d", slot)
			beforeA := r.InventoryRecord{CharacterID: "1", Slots: map[string]r.ItemStack{key: stack}}
			beforeB := r.InventoryRecord{CharacterID: "2", Slots: map[string]r.ItemStack{}}
			if mode == "full" {
				for i := 140; i <= 188; i++ {
					beforeB.Slots[fmt.Sprintf("7:%d", i)] = r.ItemStack{ItemID: 8000, Count: 1}
				}
			}
			for _, inv := range []r.InventoryRecord{beforeA, beforeB} {
				if err := g.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
			}
			owner, err := NewOwner(g)
			if err != nil {
				t.Fatal(err)
			}
			beforeA, _, err = g.Inventory.Load(ctx, "1")
			if err != nil {
				t.Fatal(err)
			}
			beforeB, _, err = g.Inventory.Load(ctx, "2")
			if err != nil {
				t.Fatal(err)
			}
			_, err = owner.Exchange(ctx, Participant{CharacterID: "1", Offers: []Offer{{TradeSlot: 0, SourceList: 7, SourceSlot: slot, Count: 1, ExpectedItem: 9001}}}, Participant{CharacterID: "2"})
			a, _, loadErr := g.Inventory.Load(ctx, "1")
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			b, _, loadErr := g.Inventory.Load(ctx, "2")
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if mode != "success" {
				if err == nil || !reflect.DeepEqual(a.Slots, beforeA.Slots) || !reflect.DeepEqual(b.Slots, beforeB.Slots) {
					t.Fatalf("rejection changed assets: err=%v a=%+v b=%+v", err, a.Slots, b.Slots)
				}
				return
			}
			if err != nil || len(a.Slots) != 0 || len(b.Slots) != 1 || !reflect.DeepEqual(b.Slots["7:140"], stack) {
				t.Fatalf("exchange: %v %+v %+v", err, a.Slots, b.Slots)
			}
		})
	}
}
