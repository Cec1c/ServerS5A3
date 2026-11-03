package dnfbridge

import (
	archive "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"testing"
)

// Exercises the production shop resolver with the actual preserved NPC/product
// definitions. It does not write a player database or run the client.
func TestPublicRequiemIrisShopRestoresShelfWithoutChangingOtherShops(t *testing.T) {
	before, after := os.Getenv("PUBLIC_REQUIEM_BEFORE"), os.Getenv("PUBLIC_REQUIEM_AFTER")
	if before == "" || after == "" {
		t.Skip("actual before/candidate archives required")
	}
	a, e := archive.Open(before)
	if e != nil {
		t.Fatal(e)
	}
	b, e := archive.Open(after)
	if e != nil {
		t.Fatal(e)
	}
	old, e := newCurrentNPCShopCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	next, e := newCurrentNPCShopCatalog(b)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := old.shopItems[63]; ok {
		t.Fatal("negative control no longer has missing shop 63")
	}
	if next.shopNPCs[63] != 132 || len(next.shopItems[63]) == 0 || len(next.shopItems) != len(old.shopItems)+1 {
		t.Fatal("Iris shelf not uniquely restored")
	}
	for id, v := range old.shopItems {
		if !reflect.DeepEqual(next.shopItems[id], v) || next.shopNPCs[id] != old.shopNPCs[id] {
			t.Fatalf("unrelated shop %d changed", id)
		}
	}
	items, e := newPVFDungeonDropCatalog(b)
	if e != nil {
		t.Fatal(e)
	}
	exchanges := 0
	for id := range next.shopItems[63] {
		if e := validateCurrentNPCShopBuyShelf(next, currentNPCShopBuyRequest{ItemID: id, ShopContext: 63, ActorContext: 132}); e != nil {
			t.Fatalf("shelf item %d: %v", id, e)
		}
		if e := validateCurrentNPCShopBuyShelf(next, currentNPCShopBuyRequest{ItemID: id, ShopContext: 63, ActorContext: 28}); e == nil {
			t.Fatal("foreign NPC admitted")
		}
		p, e := resolveCurrentNPCShopPricing(next, items, id)
		if e != nil {
			t.Fatalf("item definition/price %d: %v", id, e)
		}
		if !p.Buyable {
			t.Fatalf("restored shelf item %d cannot be purchased", id)
		}
		if p.MaterialExchange {
			exchanges++
			if _, e := items.ResolveItem(p.NeedMaterialItem); e != nil {
				t.Fatalf("material for %d: %v", id, e)
			}
		}
	}
	t.Logf("Restored shop63 NPC132: %d unique products (%d material exchanges); %d other shelves unchanged", len(next.shopItems[63]), exchanges, len(old.shopItems))
}
