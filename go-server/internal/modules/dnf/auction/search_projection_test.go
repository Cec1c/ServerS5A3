package auction

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
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

func TestAuctionSearchItemProjectionPreservesFiltersAndOrder(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1800000000, 0).UTC()
	slots := map[string]repo.ItemStack{}
	for id := uint64(1); id <= 30; id++ {
		itemID := int64(6001 + id%3)
		if id == 30 {
			itemID = int64(^uint32(0))
		}
		slots[listingKey(id)] = repo.ItemStack{ItemID: itemID, Count: 2, Extra: map[string]string{
			"seller_id": "seller", "seller_name": "name", "list_type": "0", "source_slot": "9",
			"unit_price": strconv.Itoa(50 + int(id%4)), "created_at": strconv.FormatInt(now.Add(-time.Hour).Unix(), 10),
			"expires_at": strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
		}}
	}
	expired := cloneStack(slots[listingKey(3)])
	expired.Extra["expires_at"] = strconv.FormatInt(now.Unix(), 10)
	slots[listingKey(31)] = expired
	slots[listingPrefix+"bad-id"] = slots[listingKey(3)]
	slots[listingKey(32)] = repo.ItemStack{ItemID: 6001, Count: 1}
	slots[saleHistoryPrefix+"1"] = slots[listingKey(3)]
	inv := memory.NewMemoryGroup().Inventory
	if err := inv.Save(ctx, repo.InventoryRecord{CharacterID: EscrowCharacterID, Slots: slots}); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]uint32{nil, {0}, {6001}, {0, 6001, 6001}, {6001, 6002}, {999}, {^uint32(0)}} {
		t.Run(fmt.Sprint(ids), func(t *testing.T) {
			legacy := &auctionHistoryProjectionLegacyReader{InventoryRepository: inv}
			owner := &Owner{repositories: repo.Group{Inventory: legacy}, now: func() time.Time { return now }}
			want, err := owner.Search(ctx, ids)
			if err != nil || legacy.loadCalls != 1 || legacy.loadKey != EscrowCharacterID {
				t.Fatal("legacy read", err, legacy.loadCalls)
			}
			if len(ids) == 0 && (len(want) != 30 || want[0].ID != 4 || want[len(want)-1].ID != 27) {
				t.Fatal("expiry, malformed row or ordering changed", want)
			}
			wanted := map[uint32]bool{}
			var itemID int64
			for _, id := range ids {
				if id != 0 {
					wanted[id], itemID = true, int64(id)
				}
			}
			for _, itemCapable := range []bool{false, true} {
				prefix := &auctionHistoryProjectionPrefixReader{prefixSlots: goldConsignProjectedSlots(slots, listingPrefix, 0)}
				item := &auctionHistoryProjectionItemReader{auctionHistoryProjectionPrefixReader: prefix, itemSlots: goldConsignProjectedSlots(slots, listingPrefix, itemID)}
				owner.repositories.Inventory = prefix
				if itemCapable {
					owner.repositories.Inventory = item
				}
				got, err := owner.Search(ctx, ids)
				if err != nil || !reflect.DeepEqual(got, want) || prefix.loadCalls != 0 {
					t.Fatalf("projection=%v got=%+v want=%+v err=%v", itemCapable, got, want, err)
				}
				if itemCapable && len(wanted) == 1 {
					if item.itemCalls != 1 || prefix.prefixCalls != 0 || item.itemOwner != EscrowCharacterID || item.itemPrefix != listingPrefix || item.itemID != itemID {
						t.Fatal("single item search broadened or mis-scoped", item)
					}
				} else if prefix.prefixCalls != 1 || prefix.owner != EscrowCharacterID || prefix.prefix != listingPrefix || item.itemCalls != 0 {
					t.Fatal("multi-item prefix scope changed", prefix)
				}
			}
		})
	}
}

func TestAuctionSearchItemProjectionErrorDoesNotBroadenRead(t *testing.T) {
	wantErr := errors.New("item projection unavailable")
	prefix := &auctionHistoryProjectionPrefixReader{}
	item := &auctionHistoryProjectionItemReader{auctionHistoryProjectionPrefixReader: prefix, itemErr: wantErr}
	owner := &Owner{repositories: repo.Group{Inventory: item}, now: time.Now}
	if _, err := owner.Search(context.Background(), []uint32{6001}); !errors.Is(err, wantErr) {
		t.Fatal("error lost", err)
	}
	if item.itemCalls != 1 || prefix.prefixCalls != 0 || prefix.loadCalls != 0 {
		t.Fatal("failed narrow query fell back to broader read")
	}
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
