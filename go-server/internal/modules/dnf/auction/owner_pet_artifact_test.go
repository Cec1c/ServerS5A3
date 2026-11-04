package auction

import (
	"context"
	"reflect"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestPetArtifactAuctionEscrowAndReturnAllocationKeepPetContainer(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	stack := r.ItemStack{ItemID: 9001, Count: 1, RawEntry: make([]byte, 0x77), Extra: map[string]string{"quality_seed": "42", "seal_flag": "1"}}
	seedAuctionCharacter(t, ctx, g, "seller", "account-seller", "Seller", 1000, map[string]r.ItemStack{"7:140": stack})
	owner, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Register(ctx, RegisterCommand{ListingID: 21, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller", ListType: 7, SourceSlot: 140, ExpectedID: 9001, Count: 1, UnitPrice: 100, StartPrice: -1, BuyoutPrice: 100, Duration: 24 * time.Hour, ListingLimit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if result.Listing.ListType != 7 || !reflect.DeepEqual(result.Listing.Item, stack) || len(result.Inventory.Slots) != 0 {
		t.Fatalf("listing lost container or identity: %+v", result)
	}
	if got, err := allocateSlot(map[string]r.ItemStack{"7:140": {ItemID: 8000, Count: 1}}, 7, 0); err != nil || got != 141 {
		t.Fatalf("return allocation=%d err=%v", got, err)
	}
}
