package expertjob

import (
	"context"
	"errors"
	"reflect"
	"testing"

	d "longheng.io/server/internal/modules/dnf/disjoint"
	r "longheng.io/server/internal/modules/dnf/repository"
	m "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestDisjointStoreLedgerFeeAndMaterialsAtomic(t *testing.T) {
	for _, self := range []bool{false, true} {
		for _, reject := range []bool{false, true} {
			ctx := context.Background()
			g := m.NewMemoryGroup()
			for _, id := range []string{"19", "20"} {
				if err := g.Account.Save(ctx, r.AccountRecord{AccountID: "a" + id, Metadata: map[string]string{r.AccountEpicPieceMetadataKey(10300000): "7"}}); err != nil {
					t.Fatal(err)
				}
				if err := g.Character.Save(ctx, r.CharacterRecord{CharacterID: id, AccountID: "a" + id, Stats: map[string]int64{"gold": 1000, "endurance": 300}}); err != nil {
					t.Fatal(err)
				}
				if err := g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{"0:9": {ItemID: 5002, Count: 1}, "0:10": {ItemID: 5002, Count: 1}}}); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := func() []any {
				var out []any
				for _, id := range []string{"19", "20"} {
					a, _, _ := g.Account.Load(ctx, "a"+id)
					c, _, _ := g.Character.Load(ctx, id)
					i, _, _ := g.Inventory.Load(ctx, id)
					w, _, _ := g.AccountInventory.Load(ctx, "a"+id)
					out = append(out, a, c, i, w)
				}
				return out
			}
			before := snapshot()
			owner := "20"
			if self {
				owner = "19"
			}
			failure := errors.New("second item rejected")
			err := DisjointStore(ctx, g, StoreDisjointCommand{AccountID: "a19", RequesterID: "19", OwnerID: owner, OwnerAccountID: "a" + owner, Project: func(a *StoreDisjointAssets) error {
				for _, slot := range []string{"0:9", "0:10"} {
					delete(a.RequesterInventory.Slots, slot)
					if _, err := d.CreditEpicPiece(&d.Assets{Account: a.Account}, 10300000, 30); err != nil {
						return err
					}
					if !self {
						a.Requester.Stats["gold"] -= 100
						a.Owner.Stats["gold"] += 100
					}
					a.Owner.Stats["endurance"]--
				}
				a.Warehouse.Slots["0:358"] = r.ItemStack{ItemID: 3037, Count: 200}
				if reject {
					return failure
				}
				return nil
			}})
			if reject {
				if !errors.Is(err, failure) || !reflect.DeepEqual(before, snapshot()) {
					t.Fatalf("self=%t rollback failed: %v", self, err)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			after := snapshot()
			if r.ParseAccountEpicPieceBalance(after[0].(r.AccountRecord).Metadata[r.AccountEpicPieceMetadataKey(10300000)]) != 67 {
				t.Fatal("requester did not receive both piece grants")
			}
			if !reflect.DeepEqual(before[4], after[4]) || !reflect.DeepEqual(before[7], after[7]) {
				t.Fatal("store owner's account assets changed")
			}
			gold := int64(800)
			if self {
				gold = 1000
			}
			if after[1].(r.CharacterRecord).Stats["gold"] != gold || len(after[2].(r.InventoryRecord).Slots) != 0 || after[3].(r.AccountInventoryRecord).Slots["0:358"].Count != 200 {
				t.Fatal("wallet/inventory/rewards mismatch")
			}
		}
	}
}

func TestDisjointStoreRejectsAccountSpoof(t *testing.T) {
	ctx := context.Background()
	g := m.NewMemoryGroup()
	_ = g.Account.Save(ctx, r.AccountRecord{AccountID: "attacker"})
	_ = g.Character.Save(ctx, r.CharacterRecord{CharacterID: "19", AccountID: "victim"})
	called := false
	err := DisjointStore(ctx, g, StoreDisjointCommand{AccountID: "attacker", RequesterID: "19", OwnerID: "19", Project: func(*StoreDisjointAssets) error { called = true; return nil }})
	if err == nil || called {
		t.Fatal("forged account reached projection")
	}
}
