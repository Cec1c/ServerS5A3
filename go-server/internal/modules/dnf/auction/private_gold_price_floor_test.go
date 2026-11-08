package auction

import (
	"context"
	"errors"
	"fmt"
	shop "longheng.io/server/internal/modules/dnf/cerashop"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"reflect"
	"testing"
	"time"
)

func goldPriceFloorFixture(t *testing.T) (context.Context, repo.Group, *GoldConsignOwner, time.Time) {
	t.Helper()
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	now := time.Unix(1800000000, 0).UTC()
	for _, id := range []string{"seller", "buyer"} {
		account := repo.AccountRecord{AccountID: "a-" + id}
		shop.SetBalance(&account, 1_000_000)
		if err := r.Account.Save(ctx, account); err != nil {
			t.Fatal(err)
		}
		gold := int64(0)
		if id == "seller" {
			gold = 300_000_000
		}
		if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: account.AccountID, Name: id, Level: 90, Stats: map[string]int64{"gold": gold}}); err != nil {
			t.Fatal(err)
		}
		if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: id, Slots: map[string]repo.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
	}
	o, err := NewGoldConsignOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	o.now = func() time.Time { return now }
	if err := o.EnsureEscrow(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: EscrowCharacterID, Slots: map[string]repo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	return ctx, r, o, now
}

func TestGoldConsignMinimumPriceAllTiers(t *testing.T) {
	tiers := GoldConsignTiers()
	if len(tiers) != 17 {
		t.Fatalf("got %d tiers, want 17", len(tiers))
	}
	for _, tier := range tiers {
		got, ok := GoldConsignMinimumPrice(tier.ItemID)
		if !ok || got != tier.Amount/1000 {
			t.Fatalf("tier %d amount %d: got minimum %d, ok=%t", tier.ItemID, tier.Amount, got, ok)
		}
	}
	if got, ok := GoldConsignMinimumPrice(0); ok || got != 0 {
		t.Fatalf("non-tier returned minimum %d, ok=%t", got, ok)
	}
}

func TestGoldConsignRegisterRejectsBelowFloorWithoutAssetChange(t *testing.T) {
	for _, tier := range GoldConsignTiers() {
		tier := tier
		t.Run(fmt.Sprintf("item-%d-amount-%d", tier.ItemID, tier.Amount), func(t *testing.T) {
			ctx, r, o, _ := goldPriceFloorFixture(t)
			minimum, ok := GoldConsignMinimumPrice(tier.ItemID)
			if !ok || minimum <= 0 {
				t.Fatal("missing fixed floor")
			}
			beforeSeller, _, _ := r.Character.Load(ctx, "seller")
			beforeEscrow, _, _ := r.Character.Load(ctx, EscrowCharacterID)
			beforeInventory, _, _ := r.Inventory.Load(ctx, EscrowCharacterID)
			_, err := o.Register(ctx, GoldConsignRegisterCommand{ListingID: 1, SellerID: "seller", AccountID: "a-seller", GoldItemID: tier.ItemID, PriceCera: minimum - 1, DurationHour: 24})
			if !errors.Is(err, ErrGoldConsignPriceRejected) {
				t.Fatalf("below-floor registration error = %v", err)
			}
			afterSeller, _, _ := r.Character.Load(ctx, "seller")
			afterEscrow, _, _ := r.Character.Load(ctx, EscrowCharacterID)
			afterInventory, _, _ := r.Inventory.Load(ctx, EscrowCharacterID)
			if beforeSeller.Stats["gold"] != afterSeller.Stats["gold"] || beforeEscrow.Stats["gold"] != afterEscrow.Stats["gold"] || !reflect.DeepEqual(beforeInventory, afterInventory) || len(afterInventory.Slots) != 0 {
				t.Fatalf("rejected registration changed assets: seller=%+v escrow=%+v inventory=%+v", afterSeller.Stats, afterEscrow.Stats, afterInventory.Slots)
			}
		})
	}
}

func TestGoldConsignRegisterAndBuyAtFixedFloorBoundaries(t *testing.T) {
	for _, tier := range GoldConsignTiers() {
		tier := tier
		for _, boundary := range []struct {
			name  string
			delta int64
		}{{"equal", 0}, {"above", 1}} {
			boundary := boundary
			t.Run(fmt.Sprintf("item-%d-amount-%d-%s", tier.ItemID, tier.Amount, boundary.name), func(t *testing.T) {
				ctx, r, o, _ := goldPriceFloorFixture(t)
				minimum, _ := GoldConsignMinimumPrice(tier.ItemID)
				price := minimum + boundary.delta
				registered, err := o.Register(ctx, GoldConsignRegisterCommand{ListingID: 1, SellerID: "seller", AccountID: "a-seller", GoldItemID: tier.ItemID, PriceCera: price, DurationHour: 24})
				if err != nil {
					t.Fatalf("register at %d: %v", price, err)
				}
				if registered.Listing.PriceCera != price || registered.Listing.GoldAmount != tier.Amount || registered.GoldAfter != 300_000_000-tier.Amount {
					t.Fatalf("unexpected registration: %+v", registered)
				}
				escrow, _, _ := r.Character.Load(ctx, EscrowCharacterID)
				if escrow.Stats["gold"] != tier.Amount {
					t.Fatalf("escrow gold = %d, want %d", escrow.Stats["gold"], tier.Amount)
				}
				bought, err := o.Buy(ctx, GoldConsignBuyCommand{ListingID: 1, BuyerID: "buyer", AccountID: "a-buyer", GoldItemID: tier.ItemID, ExpectedCera: price})
				if err != nil {
					t.Fatalf("buy at %d: %v", price, err)
				}
				if bought.Listing.PriceCera != price || bought.Listing.GoldAmount != tier.Amount || bought.CeraAfter != 1_000_000-price {
					t.Fatalf("unexpected purchase: %+v", bought)
				}
				buyerAccount, _, _ := r.Account.Load(ctx, "a-buyer")
				if shop.Balance(buyerAccount) != 1_000_000-price {
					t.Fatalf("buyer balance = %d, want %d", shop.Balance(buyerAccount), 1_000_000-price)
				}
				inventory, _, _ := r.Inventory.Load(ctx, EscrowCharacterID)
				if _, exists := inventory.Slots[goldConsignKey(goldConsignListingPrefix, 1)]; exists {
					t.Fatal("active listing remained after purchase")
				}
				if _, exists := inventory.Slots[goldConsignKey(goldConsignSoldPrefix, 1)]; !exists {
					t.Fatal("completed sale history missing")
				}
			})
		}
	}
}

func TestGoldConsignFixedFloorIgnoresHighHistoricalAverage(t *testing.T) {
	ctx, r, o, now := goldPriceFloorFixture(t)
	tier := GoldConsignTiers()[0]
	minimum, _ := GoldConsignMinimumPrice(tier.ItemID)
	inventory, _, _ := r.Inventory.Load(ctx, EscrowCharacterID)
	if inventory.Slots == nil {
		inventory.Slots = map[string]repo.ItemStack{}
	}
	history := GoldConsignListing{ID: 1, SellerID: "history-seller", SellerName: "history", SellerAccountID: "a-history", BuyerID: "history-buyer", GoldItemID: tier.ItemID, GoldAmount: tier.Amount, PriceCera: 100_000, FeePercent: 5, CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour), SoldAt: now.Add(-90 * time.Minute)}
	inventory.Slots[goldConsignKey(goldConsignSoldPrefix, history.ID)] = goldConsignStack(history)
	if err := r.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	average, count, err := o.AverageCeraPrice(ctx, tier.ItemID)
	if err != nil || average != history.PriceCera || count != 1 {
		t.Fatalf("history average = %d (%d), error = %v", average, count, err)
	}
	registered, err := o.Register(ctx, GoldConsignRegisterCommand{ListingID: 2, SellerID: "seller", AccountID: "a-seller", GoldItemID: tier.ItemID, PriceCera: minimum, DurationHour: 24})
	if err != nil {
		t.Fatalf("fixed minimum %d rejected against history average %d: %v", minimum, average, err)
	}
	if registered.Listing.PriceCera != minimum || registered.Listing.FeePercent != GoldConsignBaseFeePercent {
		t.Fatalf("unexpected registration: %+v", registered)
	}
}

func seedLegacyGoldPriceFloorListing(t *testing.T, ctx context.Context, r repo.Group, now time.Time, id uint64, price int64) int64 {
	t.Helper()
	tier := GoldConsignTiers()[0]
	seller, found, err := r.Character.Load(ctx, "seller")
	if err != nil || !found {
		t.Fatalf("load seller: found=%t error=%v", found, err)
	}
	escrow, found, err := r.Character.Load(ctx, EscrowCharacterID)
	if err != nil || !found {
		t.Fatalf("load escrow: found=%t error=%v", found, err)
	}
	inventory, found, err := r.Inventory.Load(ctx, EscrowCharacterID)
	if err != nil || !found {
		t.Fatalf("load inventory: found=%t error=%v", found, err)
	}
	if inventory.Slots == nil {
		inventory.Slots = map[string]repo.ItemStack{}
	}
	seller.Stats["gold"] -= tier.Amount
	escrow.Stats["gold"] += tier.Amount
	listing := GoldConsignListing{ID: id, SellerID: "seller", SellerName: "seller", SellerAccountID: "a-seller", GoldItemID: tier.ItemID, GoldAmount: tier.Amount, PriceCera: price, FeePercent: GoldConsignBaseFeePercent, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
	inventory.Slots[goldConsignKey(goldConsignListingPrefix, id)] = goldConsignStack(listing)
	if err := r.Character.Save(ctx, seller); err != nil {
		t.Fatal(err)
	}
	if err := r.Character.Save(ctx, escrow); err != nil {
		t.Fatal(err)
	}
	if err := r.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	return seller.Stats["gold"]
}

func TestGoldConsignLegacyBelowFloorListingCanStillBuyOrCancel(t *testing.T) {
	const legacyPrice = 111
	for _, action := range []string{"buy", "cancel"} {
		action := action
		t.Run(action, func(t *testing.T) {
			ctx, r, o, now := goldPriceFloorFixture(t)
			sellerGold := seedLegacyGoldPriceFloorListing(t, ctx, r, now, 9, legacyPrice)
			switch action {
			case "buy":
				got, err := o.Buy(ctx, GoldConsignBuyCommand{ListingID: 9, BuyerID: "buyer", AccountID: "a-buyer", GoldItemID: GoldConsignTiers()[0].ItemID, ExpectedCera: legacyPrice})
				if err != nil || got.Listing.PriceCera != legacyPrice {
					t.Fatalf("legacy buy = %+v, error = %v", got, err)
				}
			case "cancel":
				got, err := o.Cancel(ctx, "seller", "a-seller", 9)
				if err != nil || got.GoldAfter != sellerGold || goldMailCount(t, ctx, r, "seller") != 1 {
					t.Fatalf("legacy cancel = %+v, error = %v", got, err)
				}
				escrow, _, _ := r.Character.Load(ctx, EscrowCharacterID)
				if escrow.Stats["gold"] != 0 {
					t.Fatalf("escrow gold after cancel = %d", escrow.Stats["gold"])
				}
			}
			inventory, _, _ := r.Inventory.Load(ctx, EscrowCharacterID)
			if _, exists := inventory.Slots[goldConsignKey(goldConsignListingPrefix, 9)]; exists {
				t.Fatal("legacy active listing remains")
			}
		})
	}
}
