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

func goldConsignProjectionFixture(t *testing.T) (context.Context, repo.InventoryRepository, map[string]repo.ItemStack, time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1800000000, 0).UTC()
	slots := map[string]repo.ItemStack{}
	for id := uint64(1); id <= 25; id++ {
		l := GoldConsignListing{ID: id, SellerID: "seller", SellerAccountID: "account", SellerName: "seller", GoldItemID: 2681725, PriceCera: 1000 + int64(id)*10, FeePercent: 5, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
		slots[goldConsignKey(goldConsignListingPrefix, id)] = goldConsignStack(l)
		l.BuyerID, l.SoldAt = "buyer", now
		slots[goldConsignKey(goldConsignSoldPrefix, id)] = goldConsignStack(l)
		slots[goldConsignKey(goldConsignPayoutPrefix, id)] = goldConsignStack(l)
		slots[goldConsignKey(saleHistoryPrefix, id)] = goldConsignStack(l)
		l.GoldItemID, l.ID, l.PriceCera = 2681726, id+100, 2000
		slots[goldConsignKey(goldConsignSoldPrefix, l.ID)] = goldConsignStack(l)
		l.BuyerID, l.SoldAt = "", time.Time{}
		slots[goldConsignKey(goldConsignListingPrefix, l.ID)] = goldConsignStack(l)
	}
	// Malformed, expired and uncommitted rows must not enter search or averages.
	slots[goldConsignListingPrefix+"broken"] = repo.ItemStack{ItemID: 2681725, Count: 1}
	slots[goldConsignSoldPrefix+"broken"] = repo.ItemStack{ItemID: 2681725, Count: 1}
	slots[goldConsignSoldPrefix+"unsold"] = slots[goldConsignListingPrefix+"1"]
	expired := GoldConsignListing{ID: 200, SellerID: "seller", SellerAccountID: "account", GoldItemID: 2681725, PriceCera: 1, FeePercent: 5, CreatedAt: now.Add(-time.Hour), ExpiresAt: now}
	slots[goldConsignKey(goldConsignListingPrefix, expired.ID)] = goldConsignStack(expired)
	inv := memory.NewMemoryGroup().Inventory
	if err := inv.Save(ctx, repo.InventoryRecord{CharacterID: EscrowCharacterID, Slots: slots, Warehouse: map[string]repo.ItemStack{"unrelated": {ItemID: 42, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	return ctx, inv, slots, now
}

func goldConsignProjectedSlots(all map[string]repo.ItemStack, prefix string, itemID int64) map[string]repo.ItemStack {
	out := map[string]repo.ItemStack{}
	for key, stack := range all {
		if strings.HasPrefix(key, prefix) && (itemID <= 0 || stack.ItemID == itemID) {
			out[key] = stack
		}
	}
	return out
}

func TestGoldConsignSearchReadProjectionsPreserveResults(t *testing.T) {
	ctx, inv, slots, now := goldConsignProjectionFixture(t)
	for _, ids := range [][]int64{nil, {2681725}, {2681726}, {2681725, 2681726}, {2681725, 2681725}, {-1}, {999}} {
		legacy := &auctionHistoryProjectionLegacyReader{InventoryRepository: inv}
		owner := &GoldConsignOwner{repositories: repo.Group{Inventory: legacy}, now: func() time.Time { return now }}
		want, err := owner.Search(ctx, ids)
		if err != nil || legacy.loadCalls != 1 || legacy.loadKey != EscrowCharacterID {
			t.Fatal("legacy read", err, legacy.loadCalls)
		}
		if len(ids) == 0 && (len(want) != 50 || want[0].ID != 1 || want[49].ID != 125) {
			t.Fatal("expiry, malformed row or price/ID sorting changed", want)
		}
		var itemID int64
		if len(ids) == 1 {
			itemID = ids[0]
		}
		for _, itemCapable := range []bool{false, true} {
			prefix := &auctionHistoryProjectionPrefixReader{prefixSlots: goldConsignProjectedSlots(slots, goldConsignListingPrefix, 0)}
			item := &auctionHistoryProjectionItemReader{auctionHistoryProjectionPrefixReader: prefix, itemSlots: goldConsignProjectedSlots(slots, goldConsignListingPrefix, itemID)}
			owner.repositories.Inventory = prefix
			if itemCapable {
				owner.repositories.Inventory = item
			}
			got, err := owner.Search(ctx, ids)
			if err != nil || !reflect.DeepEqual(got, want) || prefix.loadCalls != 0 {
				t.Fatalf("search ids=%v item=%v differs: err=%v got=%v want=%v", ids, itemCapable, err, got, want)
			}
			if itemCapable && itemID > 0 {
				if item.itemCalls != 1 || prefix.prefixCalls != 0 || item.itemOwner != EscrowCharacterID || item.itemPrefix != goldConsignListingPrefix || item.itemID != itemID {
					t.Fatal("single-tier query broadened")
				}
			} else if prefix.prefixCalls != 1 || prefix.owner != EscrowCharacterID || prefix.prefix != goldConsignListingPrefix || item.itemCalls != 0 {
				t.Fatal("listing-prefix query broadened")
			}
		}
	}
}

func TestGoldConsignAverageReadProjectionsPreserveRecentTwenty(t *testing.T) {
	ctx, inv, slots, _ := goldConsignProjectionFixture(t)
	for _, itemID := range []int64{2681725, 2681726, 2681727} {
		legacy := &auctionHistoryProjectionLegacyReader{InventoryRepository: inv}
		prefix := &auctionHistoryProjectionPrefixReader{prefixSlots: goldConsignProjectedSlots(slots, goldConsignSoldPrefix, 0)}
		item := &auctionHistoryProjectionItemReader{auctionHistoryProjectionPrefixReader: &auctionHistoryProjectionPrefixReader{}, itemSlots: goldConsignProjectedSlots(slots, goldConsignSoldPrefix, itemID)}
		for _, reader := range []repo.InventoryRepository{legacy, prefix, item} {
			owner := &GoldConsignOwner{repositories: repo.Group{Inventory: reader}}
			avg, count, err := owner.AverageCeraPrice(ctx, itemID)
			wantAverage, wantCount := int64(0), int64(20)
			switch itemID {
			case 2681725:
				wantAverage = 1155 // Same timestamp: IDs 25..6, exactly twenty rows.
			case 2681726:
				wantAverage = 2000
			default:
				wantCount = 0
			}
			if err != nil || avg != wantAverage || count != wantCount {
				t.Fatal(itemID, avg, count, err)
			}
		}
		if legacy.loadCalls != 1 || prefix.loadCalls != 0 || prefix.prefixCalls != 1 || prefix.prefix != goldConsignSoldPrefix || prefix.owner != EscrowCharacterID || item.loadCalls != 0 || item.prefixCalls != 0 || item.itemCalls != 1 || item.itemID != itemID || item.itemPrefix != goldConsignSoldPrefix || item.itemOwner != EscrowCharacterID {
			t.Fatal("average read broadened")
		}
	}
}

func TestGoldConsignReadProjectionErrorsDoNotFallbackOrInitialize(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("projection unavailable")
	for _, prefixName := range []string{goldConsignListingPrefix, goldConsignSoldPrefix} {
		prefix := &auctionHistoryProjectionPrefixReader{prefixErr: wantErr, loadErr: wantErr}
		item := &auctionHistoryProjectionItemReader{auctionHistoryProjectionPrefixReader: &auctionHistoryProjectionPrefixReader{}, itemErr: wantErr}
		for _, reader := range []repo.InventoryRepository{prefix, item} {
			o := &GoldConsignOwner{repositories: repo.Group{Inventory: reader}}
			var err error
			if prefixName == goldConsignListingPrefix {
				_, err = o.Search(ctx, []int64{2681725})
			} else {
				_, _, err = o.AverageCeraPrice(ctx, 2681725)
			}
			if !errors.Is(err, wantErr) {
				t.Fatal("lost query error", err)
			}
		}
		if prefix.loadCalls != 0 || item.loadCalls != 0 || item.prefixCalls != 0 {
			t.Fatal("failed projection fell back to a full read")
		}
	}
	// Validation occurs before any repository access; empty legacy repositories
	// remain empty and no escrow creation or authoritative write is needed.
	o := &GoldConsignOwner{}
	if _, _, err := o.AverageCeraPrice(ctx, -1); !errors.Is(err, ErrGoldConsignNotTier) {
		t.Fatal(err)
	}
	r := memory.NewMemoryGroup()
	o.repositories.Inventory = &auctionHistoryProjectionLegacyReader{InventoryRepository: r.Inventory}
	if rows, err := o.Search(ctx, nil); err != nil || rows != nil {
		t.Fatal(rows, err)
	}
	if avg, count, err := o.AverageCeraPrice(ctx, 2681725); err != nil || avg != 0 || count != 0 {
		t.Fatal(avg, count, err)
	}
	if _, found, err := r.Inventory.Load(ctx, EscrowCharacterID); err != nil || found {
		t.Fatal("empty read created escrow", found, err)
	}
}
