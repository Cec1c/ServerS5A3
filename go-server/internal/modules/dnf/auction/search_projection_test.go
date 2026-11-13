package auction

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type auctionSearchProjection struct {
	repo.InventoryRepository
	slots map[string]repo.ItemStack
	calls int
	fail  error
}

func (p *auctionSearchProjection) Load(context.Context, string) (repo.InventoryRecord, bool, error) {
	return repo.InventoryRecord{}, false, errors.New("search attempted complete inventory read")
}
func (p *auctionSearchProjection) LoadInventorySlotPrefix(_ context.Context, id, prefix string) (map[string]repo.ItemStack, error) {
	p.calls++
	if id != EscrowCharacterID || prefix != listingPrefix {
		return nil, errors.New("wrong projection scope")
	}
	return p.slots, p.fail
}
func TestAuctionSearchUsesReadOnlyListingProjection(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	o, _ := NewOwner(r)
	if got, err := o.Search(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty search=%v %v", got, err)
	}
	if _, found, _ := r.Character.Load(ctx, EscrowCharacterID); found {
		t.Fatal("search created escrow actor")
	}
	seedAuctionCharacter(t, ctx, r, "10", "seller", "seller", 50000, map[string]repo.ItemStack{"0:9": {ItemID: 6001, Count: 2}})
	_, err := o.Register(ctx, RegisterCommand{ListingID: 3, SellerID: "10", AccountID: "seller", SellerName: "seller", ListType: 0, SourceSlot: 9, ExpectedID: 6001, Count: 2, UnitPrice: 50, StartPrice: -1, Duration: time.Hour, ListingLimit: 5})
	if err != nil {
		t.Fatal(err)
	}
	want, err := o.Search(ctx, []uint32{6001})
	if err != nil {
		t.Fatal(err)
	}
	escrow, _, _ := r.Inventory.Load(ctx, EscrowCharacterID)
	slots := map[string]repo.ItemStack{}
	for key, stack := range escrow.Slots {
		if strings.HasPrefix(key, listingPrefix) {
			slots[key] = stack
		}
	}
	p := &auctionSearchProjection{InventoryRepository: r.Inventory, slots: slots}
	r.Inventory = p
	o, _ = NewOwner(r)
	got, err := o.Search(ctx, []uint32{6001})
	if err != nil || !reflect.DeepEqual(want, got) || p.calls != 1 {
		t.Fatalf("projection differs got=%+v want=%+v err=%v calls=%d", got, want, err, p.calls)
	}
	p.fail = errors.New("projection unavailable")
	if _, err := o.Search(ctx, nil); !errors.Is(err, p.fail) {
		t.Fatalf("error lost=%v", err)
	}
}
