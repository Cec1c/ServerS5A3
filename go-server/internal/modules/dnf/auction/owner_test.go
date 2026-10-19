package auction

import (
	"context"
	"errors"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// The current client addresses the account-scoped crystal/soul warehouse as
// list-0 slots 354..365 but marks the container as 36 on op183. A registration
// from there debits the account record, and the seller's own list 0 must stay
// untouched - otherwise the same stack would be visible as two owners.
func TestOwnerRegisterDebitsAccountWarehouseSource(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	now := time.Unix(1_800_000_000, 0).UTC()
	const crystalSlot = int16(358) // 无色小晶块 in the live capture
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 100, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 28253, Count: 1, RawEntry: make([]byte, 0x77)},
	})
	warehouseKey := dnfrepo.AccountSharedInventorySlotKey(crystalSlot)
	if err := repositories.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: "account-seller",
		Slots: map[string]dnfrepo.ItemStack{
			warehouseKey: {ItemID: 3037, Count: 2, RawEntry: make([]byte, 0x77)},
		},
	}); err != nil {
		t.Fatal(err)
	}

	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	owner.now = func() time.Time { return now }
	registered, err := owner.Register(ctx, RegisterCommand{
		ListingID: 21, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
		ListType: 0, SourceSlot: crystalSlot, ExpectedID: 3037, Count: 2,
		UnitPrice: 20, StartPrice: -1, BuyoutPrice: 20,
		Duration: 24 * time.Hour, ListingLimit: 5, SourceAccountOwned: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if registered.Listing.Item.ItemID != 3037 || registered.Listing.Item.Count != 2 ||
		registered.Listing.SourceSlot != crystalSlot || registered.Listing.ListType != 0 {
		t.Fatalf("unexpected listing: %+v", registered.Listing)
	}
	if _, exists := registered.Inventory.Slots[warehouseKey]; exists {
		t.Fatalf("returned inventory still holds the listed warehouse stack: %+v", registered.Inventory.Slots)
	}
	account, found, err := repositories.AccountInventory.Load(ctx, "account-seller")
	if err != nil || !found {
		t.Fatalf("account reload found=%t err=%v", found, err)
	}
	if _, exists := account.Slots[warehouseKey]; exists {
		t.Fatalf("account warehouse still holds the listed stack: %+v", account.Slots)
	}
	character, found, err := repositories.Inventory.Load(ctx, "seller")
	if err != nil || !found {
		t.Fatalf("character reload found=%t err=%v", found, err)
	}
	if len(character.Slots) != 1 || character.Slots["0:9"].Count != 1 {
		t.Fatalf("character inventory was touched: %+v", character.Slots)
	}
}

// The whole loop for a warehouse listing: the crystal leaves the account
// warehouse into escrow, a purchase parks it in the buyer's delivery outbox,
// and a cancel parks the consignment back for its seller. Both delivery and
// return ride system mail, and the mailbox is what routes 354..365 back into an
// account warehouse on claim, so neither half may write straight into a
// character's list 0 - that would hand the same stack to two different owners.
func TestOwnerAccountWarehouseListingLifecycle(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	now := time.Unix(1_800_000_000, 0).UTC()
	const crystalSlot = int16(358)
	warehouseKey := dnfrepo.AccountSharedInventorySlotKey(crystalSlot)
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 500, nil)
	seedAuctionCharacter(t, ctx, repositories, "buyer", "account-buyer", "Buyer", 5_000, nil)
	if err := repositories.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: "account-seller",
		Slots: map[string]dnfrepo.ItemStack{
			warehouseKey: {ItemID: 3037, Count: 4, RawEntry: make([]byte, 0x77)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	owner.now = func() time.Time { return now }

	// Two of four 无色小晶块 leave the warehouse; the rest stays there.
	if _, err := owner.Register(ctx, RegisterCommand{
		ListingID: 31, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
		ListType: 0, SourceSlot: crystalSlot, ExpectedID: 3037, Count: 2,
		UnitPrice: 20, BuyoutPrice: 20, StartPrice: -1,
		Duration: 24 * time.Hour, ListingLimit: 5, SourceAccountOwned: true,
	}); err != nil {
		t.Fatal(err)
	}
	account, found, err := repositories.AccountInventory.Load(ctx, "account-seller")
	if err != nil || !found {
		t.Fatalf("account reload found=%t err=%v", found, err)
	}
	if stack := account.Slots[warehouseKey]; stack.ItemID != 3037 || stack.Count != 2 {
		t.Fatalf("warehouse stack after listing=%+v want 3037x2", stack)
	}

	bought, err := owner.Buy(ctx, BuyCommand{
		BuyerID: "buyer", AccountID: "account-buyer", ListingID: 31,
		ExpectedItemID: 3037, Count: 2, Money: 40,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bought.Delivery.BuyerID != "buyer" || bought.Delivery.Item.ItemID != 3037 ||
		bought.Delivery.Item.Count != 2 || bought.Delivery.Return {
		t.Fatalf("unexpected purchase delivery: %+v", bought.Delivery)
	}
	buyerInventory, _, _ := repositories.Inventory.Load(ctx, "buyer")
	if len(buyerInventory.Slots) != 0 {
		t.Fatalf("purchase entered the buyer's bag instead of the mail outbox: %+v", buyerInventory.Slots)
	}

	// The remaining two are listed and cancelled: the consignment parks for the
	// seller and must not land in the seller's own list 0 either.
	if _, err := owner.Register(ctx, RegisterCommand{
		ListingID: 32, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
		ListType: 0, SourceSlot: crystalSlot, ExpectedID: 3037, Count: 2,
		UnitPrice: 20, BuyoutPrice: 20, StartPrice: -1,
		Duration: 24 * time.Hour, ListingLimit: 5, SourceAccountOwned: true,
	}); err != nil {
		t.Fatal(err)
	}
	returned, err := owner.Cancel(ctx, CancelCommand{
		SellerID: "seller", AccountID: "account-seller", ListingID: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !returned.Delivery.Return || returned.Delivery.BuyerID != "seller" ||
		returned.Delivery.Item.ItemID != 3037 || returned.Delivery.Item.Count != 2 {
		t.Fatalf("unexpected cancel delivery: %+v", returned.Delivery)
	}
	sellerInventory, _, _ := repositories.Inventory.Load(ctx, "seller")
	if len(sellerInventory.Slots) != 0 {
		t.Fatalf("cancel wrote into the seller's bag instead of the mail outbox: %+v", sellerInventory.Slots)
	}
	pending, err := owner.PendingDeliveries(ctx, "seller")
	if err != nil || len(pending) != 1 || !pending[0].Return || pending[0].Item.ItemID != 3037 {
		t.Fatalf("pending returns=%+v err=%v", pending, err)
	}
}

func TestOwnerRegisterBuyAndClaimProceeds(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	now := time.Unix(1_800_000_000, 0).UTC()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 100, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 1001, Count: 5, RawEntry: make([]byte, 0x77)},
	})
	seedAuctionCharacter(t, ctx, repositories, "buyer", "account-buyer", "Buyer", 1_000, nil)

	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	owner.now = func() time.Time { return now }
	registered, err := owner.Register(ctx, RegisterCommand{
		ListingID: 17, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
		ListType: 0, SourceSlot: 9, ExpectedID: 1001, Count: 2, UnitPrice: 75,
		Duration: 24 * time.Hour, ListingLimit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if registered.Listing.ID != 17 || registered.Inventory.Slots["0:9"].Count != 3 {
		t.Fatalf("unexpected register result: %+v", registered)
	}
	listings, err := owner.Search(ctx, []uint32{1001})
	if err != nil || len(listings) != 1 || listings[0].UnitPrice != 75 {
		t.Fatalf("search=%+v err=%v", listings, err)
	}
	bought, err := owner.Buy(ctx, BuyCommand{BuyerID: "buyer", AccountID: "account-buyer", ListingID: 17, ExpectedItemID: 1001, Count: 2, Money: 150})
	if err != nil {
		t.Fatal(err)
	}
	if bought.GoldAfter != 850 || bought.Destination != -1 || bought.Delivery.BuyerID != "buyer" ||
		bought.Delivery.Item.ItemID != 1001 || bought.Delivery.Item.Count != 2 {
		t.Fatalf("unexpected buy result: %+v", bought)
	}
	buyerInventory, _, _ := repositories.Inventory.Load(ctx, "buyer")
	if len(buyerInventory.Slots) != 0 {
		t.Fatalf("auction purchase entered buyer inventory instead of mail outbox: %+v", buyerInventory.Slots)
	}
	claimed, err := owner.ClaimProceeds(ctx, "seller", "account-seller")
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ClaimedGold != 150 || claimed.GoldAfter != 250 {
		t.Fatalf("unexpected claim result: %+v", claimed)
	}
	listings, err = owner.Search(ctx, nil)
	if err != nil || len(listings) != 0 {
		t.Fatalf("listing remained after full purchase: %+v err=%v", listings, err)
	}
}

func TestOwnerCancelAndFailedBuyRollback(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	now := time.Unix(1_800_000_000, 0).UTC()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 0, map[string]dnfrepo.ItemStack{
		"0:65": {ItemID: 2002, Count: 1},
	})
	seedAuctionCharacter(t, ctx, repositories, "buyer", "account-buyer", "Buyer", 10, nil)
	owner, _ := NewOwner(repositories)
	owner.now = func() time.Time { return now }
	if _, err := owner.Register(ctx, RegisterCommand{ListingID: 19, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller", ListType: 0, SourceSlot: 65, ExpectedID: 2002, Count: 1, UnitPrice: 50, Duration: time.Hour, ListingLimit: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Buy(ctx, BuyCommand{BuyerID: "buyer", AccountID: "account-buyer", ListingID: 19, ExpectedItemID: 2002, Count: 1, Money: 50}); !errors.Is(err, ErrGoldUnavailable) {
		t.Fatalf("buy error=%v, want ErrGoldUnavailable", err)
	}
	returned, err := owner.Cancel(ctx, CancelCommand{SellerID: "seller", AccountID: "account-seller", ListingID: 19})
	if err != nil {
		t.Fatal(err)
	}
	// A cancel returns the item by mail, so it leaves the seller's inventory
	// untouched and parks the row in the durable outbox until the mail commits.
	if !returned.Delivery.Return || returned.Delivery.BuyerID != "seller" ||
		returned.Delivery.Item.ItemID != 2002 || returned.Delivery.Item.Count != 1 {
		t.Fatalf("unexpected cancel delivery: %+v", returned.Delivery)
	}
	sellerInventory, _, _ := repositories.Inventory.Load(ctx, "seller")
	if _, exists := sellerInventory.Slots["0:65"]; exists {
		t.Fatalf("cancel wrote straight into the bag instead of the mail outbox: %+v", sellerInventory.Slots)
	}
	pending, err := owner.PendingDeliveries(ctx, "seller")
	if err != nil || len(pending) != 1 || pending[0].ID != returned.Delivery.ID || !pending[0].Return {
		t.Fatalf("pending returns=%+v err=%v", pending, err)
	}
	if err := owner.ConfirmDeliveryMail(ctx, ConfirmDeliveryCommand{
		DeliveryID: returned.Delivery.ID, BuyerID: "seller", ListingID: 19, ItemID: 2002, Count: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if pending, err = owner.PendingDeliveries(ctx, "seller"); err != nil || len(pending) != 0 {
		t.Fatalf("pending returns after confirm=%+v err=%v", pending, err)
	}
	buyer, _, _ := repositories.Character.Load(ctx, "buyer")
	if buyer.Stats["gold"] != 10 {
		t.Fatalf("failed buy changed gold to %d", buyer.Stats["gold"])
	}
}

func TestOwnerClaimReturnsExpiredListingAndDoesNotConsumeActiveLimit(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	now := time.Unix(1_800_000_000, 0).UTC()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 0, map[string]dnfrepo.ItemStack{
		"0:9":  {ItemID: 3003, Count: 1},
		"0:10": {ItemID: 3004, Count: 1},
	})
	owner, _ := NewOwner(repositories)
	owner.now = func() time.Time { return now }
	if _, err := owner.Register(ctx, RegisterCommand{ListingID: 21, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller", ListType: 0, SourceSlot: 9, ExpectedID: 3003, Count: 1, UnitPrice: 10, Duration: time.Minute, ListingLimit: 1}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := owner.Register(ctx, RegisterCommand{ListingID: 22, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller", ListType: 0, SourceSlot: 10, ExpectedID: 3004, Count: 1, UnitPrice: 20, Duration: time.Hour, ListingLimit: 1}); err != nil {
		t.Fatalf("expired listing still consumed limit: %v", err)
	}
	claimed, err := owner.ClaimProceeds(ctx, "seller", "account-seller")
	if err != nil {
		t.Fatal(err)
	}
	// An expired listing leaves by mail like a cancelled one, so nothing is
	// written into the seller's bag and the outbox holds the return.
	if claimed.ClaimedGold != 0 || len(claimed.ExpiredReturns) != 1 ||
		claimed.ExpiredReturns[0].Item.ItemID != 3003 || !claimed.ExpiredReturns[0].Expired ||
		!claimed.ExpiredReturns[0].Return {
		t.Fatalf("expired claim=%+v", claimed)
	}
	if _, exists := claimed.Inventory.Slots["0:9"]; exists {
		t.Fatalf("expired listing was written into the bag: %+v", claimed.Inventory.Slots)
	}
	pending, err := owner.PendingDeliveries(ctx, "seller")
	if err != nil || len(pending) != 1 || !pending[0].Expired || pending[0].ID != claimed.ExpiredReturns[0].ID {
		t.Fatalf("expired outbox=%+v err=%v", pending, err)
	}
	listings, err := owner.Search(ctx, nil)
	if err != nil || len(listings) != 1 || listings[0].ID != 22 {
		t.Fatalf("active listings=%+v err=%v", listings, err)
	}
}

func TestOwnerPurchaseCreatesDurableMailPayoutOutboxAndConfirmRemovesIt(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 0, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 4004, Count: 4},
	})
	seedAuctionCharacter(t, ctx, repositories, "buyer", "account-buyer", "Buyer", 1000, nil)
	owner, _ := NewOwner(repositories)
	if _, err := owner.Register(ctx, RegisterCommand{ListingID: 31, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller", ListType: 0, SourceSlot: 9, ExpectedID: 4004, Count: 4, UnitPrice: 25, Duration: time.Hour, ListingLimit: 5}); err != nil {
		t.Fatal(err)
	}
	bought, err := owner.Buy(ctx, BuyCommand{BuyerID: "buyer", AccountID: "account-buyer", ListingID: 31, ExpectedItemID: 4004, Count: 2, Money: 50})
	if err != nil {
		t.Fatal(err)
	}
	if bought.Payout.ID != "auction:payout:31:2" || bought.Payout.Gold != 50 || bought.Payout.SoldCount != 2 {
		t.Fatalf("buy payout=%+v", bought.Payout)
	}
	if bought.Delivery.ID != "auction:delivery:31:2" || bought.Delivery.BuyerID != "buyer" || bought.Delivery.Item.ItemID != 4004 || bought.Delivery.Item.Count != 2 {
		t.Fatalf("buy delivery=%+v", bought.Delivery)
	}
	deliveries, err := owner.PendingDeliveries(ctx, "buyer")
	if err != nil || len(deliveries) != 1 || deliveries[0].ID != bought.Delivery.ID {
		t.Fatalf("pending deliveries=%+v err=%v", deliveries, err)
	}
	if err := owner.ConfirmDeliveryMail(ctx, ConfirmDeliveryCommand{DeliveryID: bought.Delivery.ID, BuyerID: "buyer", ListingID: 31, ItemID: 4004, Count: 2}); err != nil {
		t.Fatal(err)
	}
	deliveries, err = owner.PendingDeliveries(ctx, "buyer")
	if err != nil || len(deliveries) != 0 {
		t.Fatalf("pending deliveries after confirm=%+v err=%v", deliveries, err)
	}
	payouts, err := owner.PendingPayouts(ctx, "seller")
	if err != nil || len(payouts) != 1 || payouts[0] != bought.Payout {
		t.Fatalf("pending=%+v err=%v", payouts, err)
	}
	if err := owner.ConfirmPayoutMail(ctx, ConfirmPayoutCommand{PayoutID: bought.Payout.ID, SellerID: "seller", Gold: 50}); err != nil {
		t.Fatal(err)
	}
	payouts, err = owner.PendingPayouts(ctx, "seller")
	if err != nil || len(payouts) != 0 {
		t.Fatalf("pending after confirm=%+v err=%v", payouts, err)
	}
	escrow, _, _ := repositories.Character.Load(ctx, EscrowCharacterID)
	if escrow.Stats["gold"] != 0 {
		t.Fatalf("escrow gold after confirmed mail=%d", escrow.Stats["gold"])
	}
}

func TestOwnerRegisterAllowsOnlyExplicitlyExpectedSealedEquipment(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 0, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 5005, Count: 1, Bind: true, RawEntry: make([]byte, 0x77)},
	})
	owner, _ := NewOwner(repositories)
	command := RegisterCommand{
		ListingID: 41, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
		ListType: 0, SourceSlot: 9, ExpectedID: 5005, Count: 1, UnitPrice: 100,
		Duration: time.Hour, ListingLimit: 5,
	}
	if _, err := owner.Register(ctx, command); !errors.Is(err, ErrItemUnavailable) {
		t.Fatalf("implicit sealed register error=%v, want ErrItemUnavailable", err)
	}
	inventory, _, _ := repositories.Inventory.Load(ctx, "seller")
	if !inventory.Slots["0:9"].Bind || inventory.Slots["0:9"].ItemID != 5005 {
		t.Fatalf("rejected sealed item changed inventory: %+v", inventory.Slots)
	}

	command.ExpectedBind = true
	command.AllowSealed = true
	registered, err := owner.Register(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if !registered.Listing.Item.Bind || registered.Listing.Item.ItemID != 5005 {
		t.Fatalf("sealed listing=%+v", registered.Listing)
	}
}

// TestOwnerKeepsOpeningBidAndSeparatesEveryRefusedPayment covers the money
// half of a consignment: the seller's opening bid has to survive escrow so the
// client can render it, and the three ways a payment can be wrong have to stay
// distinguishable instead of collapsing into one gold error.
func TestOwnerKeepsOpeningBidAndSeparatesEveryRefusedPayment(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	now := time.Unix(1_800_000_000, 0).UTC()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 0, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 1001, Count: 2},
	})
	seedAuctionCharacter(t, ctx, repositories, "buyer", "account-buyer", "Buyer", 1_000, nil)
	owner, _ := NewOwner(repositories)
	owner.now = func() time.Time { return now }

	if _, err := owner.Register(ctx, RegisterCommand{
		ListingID: 21, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
		ListType: 0, SourceSlot: 9, ExpectedID: 1001, Count: 2, UnitPrice: 75, StartPrice: 30,
		Duration: 24 * time.Hour, ListingLimit: 5,
	}); err != nil {
		t.Fatal(err)
	}
	listings, err := owner.Search(ctx, []uint32{1001})
	if err != nil || len(listings) != 1 {
		t.Fatalf("search=%+v err=%v", listings, err)
	}
	if listings[0].StartPrice != 30 || listings[0].UnitPrice != 75 || listings[0].BidUnitPrice() != 30 {
		t.Fatalf("opening bid did not survive escrow: %+v", listings[0])
	}

	// Buy is the buyout path and takes exactly the buyout total. 60 is the
	// opening-bid total: a genuine bid, which belongs to Bid, so Buy has to
	// refuse it as the wrong amount rather than settle it.
	for name, probe := range map[string]struct {
		command BuyCommand
		want    error
	}{
		"opening_bid_total": {BuyCommand{BuyerID: "buyer", AccountID: "account-buyer", ListingID: 21, ExpectedItemID: 1001, Money: 60, WholeListing: true}, ErrPriceMismatch},
		"overpayment":       {BuyCommand{BuyerID: "buyer", AccountID: "account-buyer", ListingID: 21, ExpectedItemID: 1001, Money: 200, WholeListing: true}, ErrPriceMismatch},
		"apiece_shortfall":  {BuyCommand{BuyerID: "buyer", AccountID: "account-buyer", ListingID: 21, ExpectedItemID: 1001, Count: 1, Money: 30}, ErrPriceMismatch},
	} {
		if _, err := owner.Buy(ctx, probe.command); !errors.Is(err, probe.want) {
			t.Fatalf("%s: err=%v, want %v", name, err, probe.want)
		}
	}
	buyer, _, _ := repositories.Character.Load(ctx, "buyer")
	escrowInventory, _, _ := repositories.Inventory.Load(ctx, EscrowCharacterID)
	if buyer.Stats["gold"] != 1_000 || escrowInventory.Slots[listingKey(21)].Count != 2 {
		t.Fatalf("refused payment moved state: gold=%d escrow=%+v", buyer.Stats["gold"], escrowInventory.Slots)
	}

	bought, err := owner.Buy(ctx, BuyCommand{BuyerID: "buyer", AccountID: "account-buyer", ListingID: 21, ExpectedItemID: 1001, Money: 150, WholeListing: true})
	if err != nil {
		t.Fatal(err)
	}
	if bought.GoldAfter != 850 || bought.Payout.Gold != 150 {
		t.Fatalf("buyout at the listed total=%+v", bought)
	}
	// The pricing keys are bookkeeping for the auction house, not part of the
	// item; a delivered stack that kept them would carry them into the buyer's
	// bag through the mail attachment.
	for _, key := range []string{"start_price", "unit_price", "seller_id"} {
		if _, present := bought.Delivery.Item.Extra[key]; present {
			t.Fatalf("delivered item still carries %s: %+v", key, bought.Delivery.Item.Extra)
		}
	}
}

// TestOwnerListingEscrowedBeforeStartPriceExistedStaysBuyoutOnly protects the
// rows already sitting in escrow on a running server. They have no start_price
// key, and rejecting them over that would strand a live consignment.
func TestOwnerListingEscrowedBeforeStartPriceExistedStaysBuyoutOnly(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	now := time.Unix(1_800_000_000, 0).UTC()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 0, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 1001, Count: 1},
	})
	owner, _ := NewOwner(repositories)
	owner.now = func() time.Time { return now }
	if _, err := owner.Register(ctx, RegisterCommand{
		ListingID: 22, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
		ListType: 0, SourceSlot: 9, ExpectedID: 1001, Count: 1, UnitPrice: 75, StartPrice: -1,
		Duration: 24 * time.Hour, ListingLimit: 5,
	}); err != nil {
		t.Fatal(err)
	}
	escrow, _, _ := repositories.Inventory.Load(ctx, EscrowCharacterID)
	stack := escrow.Slots[listingKey(22)]
	delete(stack.Extra, "start_price")
	escrow.Slots[listingKey(22)] = stack
	if err := repositories.Inventory.Save(ctx, escrow); err != nil {
		t.Fatal(err)
	}
	listings, err := owner.Search(ctx, nil)
	if err != nil || len(listings) != 1 {
		t.Fatalf("legacy listing was dropped: %+v err=%v", listings, err)
	}
	if listings[0].StartPrice != -1 || listings[0].BidUnitPrice() != 75 {
		t.Fatalf("legacy listing=%+v, want buyout-only", listings[0])
	}
}

// TestOwnerRegisterBidOnlyListingSellsAtTheOpeningBid covers the listing shape
// the client allows but this owner used to refuse: an opening bid with no
// 一口价. There is no buyout to sell at, so the opening bid is the asking
// price - Buy still settles only on that exact total, and the buyout price
// slots stay empty. A row escrowed before the buyout became optional carries no
// buyout_price key and must keep reading as a buyout listing.
func TestOwnerRegisterBidOnlyListingSellsAtTheOpeningBid(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 0, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 1001, Count: 2},
	})
	seedAuctionCharacter(t, ctx, repositories, "buyer", "account-buyer", "Buyer", 1000, nil)
	owner, _ := NewOwner(repositories)
	if _, err := owner.Register(ctx, RegisterCommand{
		ListingID: 41, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
		ListType: 0, SourceSlot: 9, ExpectedID: 1001, Count: 2, UnitPrice: 30, StartPrice: 30, BuyoutPrice: -1,
		Duration: 24 * time.Hour, ListingLimit: 5,
	}); err != nil {
		t.Fatal(err)
	}
	listings, err := owner.Search(ctx, nil)
	if err != nil || len(listings) != 1 {
		t.Fatalf("bid-only listing was dropped: %+v err=%v", listings, err)
	}
	if listings[0].HasBuyout() || listings[0].BuyoutUnitPrice() != 0 || listings[0].BidUnitPrice() != 30 {
		t.Fatalf("bid-only listing=%+v, want no buyout and a 30 opening bid", listings[0])
	}
	// A row written before the key existed keeps its buyout.
	escrow, _, _ := repositories.Inventory.Load(ctx, EscrowCharacterID)
	stack := escrow.Slots[listingKey(41)]
	stack.Extra = cloneExtra(stack.Extra)
	delete(stack.Extra, "buyout_price")
	escrow.Slots[listingKey(41)] = stack
	if err := repositories.Inventory.Save(ctx, escrow); err != nil {
		t.Fatal(err)
	}
	legacy, err := owner.Search(ctx, nil)
	if err != nil || len(legacy) != 1 {
		t.Fatalf("legacy listing was dropped: %+v err=%v", legacy, err)
	}
	if !legacy[0].HasBuyout() || legacy[0].BuyoutUnitPrice() != 30 {
		t.Fatalf("legacy listing=%+v, want a buyout equal to the unit price", legacy[0])
	}
}

// TestOwnerRegisterRejectsAskingPriceThatMatchesNeitherControl keeps UnitPrice
// pinned to a price the client actually renders: the buyout when there is one,
// the opening bid when there is not.
func TestOwnerRegisterRejectsAskingPriceThatMatchesNeitherControl(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 0, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 1001, Count: 1},
	})
	owner, _ := NewOwner(repositories)
	for name, command := range map[string]RegisterCommand{
		"buyout_below_unit_price":   {UnitPrice: 75, StartPrice: 30, BuyoutPrice: 60},
		"bid_only_not_asking_price": {UnitPrice: 75, StartPrice: 30, BuyoutPrice: -1},
	} {
		command.ListingID, command.SellerID, command.AccountID, command.SellerName = 44, "seller", "account-seller", "Seller"
		command.ListType, command.SourceSlot, command.ExpectedID, command.Count = 0, 9, 1001, 1
		command.Duration, command.ListingLimit = 24*time.Hour, 5
		if _, err := owner.Register(ctx, command); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("%s: err=%v, want ErrInvalidRequest", name, err)
		}
	}
	inventory, _, _ := repositories.Inventory.Load(ctx, "seller")
	if inventory.Slots["0:9"].Count != 1 {
		t.Fatalf("rejected registration consumed the source stack: %+v", inventory.Slots)
	}
}

// TestOwnerRegistrationDepositAndSaleFee walks the money: the deposit leaves
// the seller's wallet into escrow at registration, the commission is withheld
// from the sale, and the deposit comes back folded into the proceeds once the
// listing sells out. Gold is conserved throughout - nothing is minted, and the
// fee is the only amount that stays behind.
func TestOwnerRegistrationDepositAndSaleFee(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 5005, Count: 2},
	})
	seedAuctionCharacter(t, ctx, repositories, "buyer", "account-buyer", "Buyer", 1_000, nil)
	owner, _ := NewOwner(repositories)
	registered, err := owner.Register(ctx, RegisterCommand{
		ListingID: 51, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
		ListType: 0, SourceSlot: 9, ExpectedID: 5005, Count: 2, UnitPrice: 75,
		Deposit: ListingDepositGold, FeePercent: SaleFeePercent,
		Duration: time.Hour, ListingLimit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if registered.GoldAfter != 20_000-ListingDepositGold {
		t.Fatalf("deposit not taken: gold_after=%d", registered.GoldAfter)
	}
	escrowCharacter, _, _ := repositories.Character.Load(ctx, EscrowCharacterID)
	if escrowCharacter.Stats["gold"] != ListingDepositGold {
		t.Fatalf("deposit did not reach escrow: %d", escrowCharacter.Stats["gold"])
	}
	bought, err := owner.Buy(ctx, BuyCommand{
		BuyerID: "buyer", AccountID: "account-buyer", ListingID: 51,
		ExpectedItemID: 5005, Count: 2, Money: 150,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantFee := int64(150) * SaleFeePercent / 100
	wantProceeds := 150 - wantFee + ListingDepositGold
	if bought.Payout.Gold != wantProceeds {
		t.Fatalf("payout=%d, want %d (150 - %d fee + %d deposit)", bought.Payout.Gold, wantProceeds, wantFee, ListingDepositGold)
	}
	if err := owner.ConfirmPayoutMail(ctx, ConfirmPayoutCommand{PayoutID: bought.Payout.ID, SellerID: "seller", Gold: wantProceeds}); err != nil {
		t.Fatal(err)
	}
	escrowCharacter, _, _ = repositories.Character.Load(ctx, EscrowCharacterID)
	if escrowCharacter.Stats["gold"] != wantFee {
		t.Fatalf("escrow keeps %d after payout, want only the %d commission", escrowCharacter.Stats["gold"], wantFee)
	}
}

// TestOwnerSalePayoutFreezesTheStatementOnTheOutboxRow pins the breakdown the
// seller's 成交 letter prints. It rides the payout row rather than being
// recomputed at mail time, because the listing row is gone once the last piece
// sells and a deferred retry has to print the same letter.
func TestOwnerSalePayoutFreezesTheStatementOnTheOutboxRow(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 5005, Count: 2},
	})
	seedAuctionCharacter(t, ctx, repositories, "buyer", "account-buyer", "Buyer", 1_000, nil)
	owner, _ := NewOwner(repositories)
	if _, err := owner.Register(ctx, RegisterCommand{
		ListingID: 61, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
		ListType: 0, SourceSlot: 9, ExpectedID: 5005, Count: 2, UnitPrice: 30,
		Deposit: ListingDepositGold, FeePercent: SaleFeePercent,
		Duration: time.Hour, ListingLimit: 5,
	}); err != nil {
		t.Fatal(err)
	}

	// Half the stack first: the deposit stays with the remaining piece, so the
	// statement must not claim it came back.
	partial, err := owner.Buy(ctx, BuyCommand{
		BuyerID: "buyer", AccountID: "account-buyer", ListingID: 61,
		ExpectedItemID: 5005, Count: 1, Money: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !partial.Payout.HasStatement {
		t.Fatal("partial sale payout carries no statement")
	}
	if got := partial.Payout.Statement; got.GrossGold != 30 || got.FeeGold != 1 || got.DepositGold != 0 || got.NetGold != 29 {
		t.Fatalf("partial statement=%+v want 30/1/0/29", got)
	}
	if partial.Payout.Gold != partial.Payout.Statement.NetGold {
		t.Fatalf("payout gold=%d but statement net=%d", partial.Payout.Gold, partial.Payout.Statement.NetGold)
	}
	// Read it back the way the bridge does.
	pending, err := owner.PendingPayouts(ctx, "seller")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending payouts=%+v err=%v", pending, err)
	}
	if !pending[0].HasStatement || pending[0].Statement.GrossGold != 30 ||
		pending[0].Statement.FeeGold != 1 || pending[0].Statement.DepositGold != 0 ||
		pending[0].Statement.NetGold != 29 {
		t.Fatalf("reloaded statement=%+v", pending[0].Statement)
	}
	if err := owner.ConfirmPayoutMail(ctx, ConfirmPayoutCommand{
		PayoutID: pending[0].ID, SellerID: "seller", Gold: pending[0].Gold,
	}); err != nil {
		t.Fatal(err)
	}

	// The last piece brings the deposit home.
	if _, err := owner.Buy(ctx, BuyCommand{
		BuyerID: "buyer", AccountID: "account-buyer", ListingID: 61,
		ExpectedItemID: 5005, Count: 1, Money: 30,
	}); err != nil {
		t.Fatal(err)
	}
	pending, err = owner.PendingPayouts(ctx, "seller")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending payouts after the sellout=%+v err=%v", pending, err)
	}
	final := pending[0]
	if !final.HasStatement {
		t.Fatalf("the sellout payout carries no statement: %+v", final)
	}
	if got := final.Statement; got.GrossGold != 30 || got.FeeGold != 1 ||
		got.DepositGold != ListingDepositGold || got.NetGold != 30-1+ListingDepositGold {
		t.Fatalf("sellout statement=%+v", got)
	}
	if final.Gold != final.Statement.NetGold {
		t.Fatalf("sellout payout gold=%d but statement net=%d", final.Gold, final.Statement.NetGold)
	}
}

// A refunded bid rides the same outbox and must never print a sale statement:
// its letter keeps the bid-refund wording instead of an 出售价 breakdown.
func TestOwnerRefundPayoutCarriesNoSaleStatement(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 5005, Count: 1},
	})
	seedAuctionCharacter(t, ctx, repositories, "rival", "account-rival", "Rival", 1_000, nil)
	owner, _ := NewOwner(repositories)
	if _, err := owner.Register(ctx, RegisterCommand{
		ListingID: 71, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
		ListType: 0, SourceSlot: 9, ExpectedID: 5005, Count: 1, UnitPrice: 400,
		StartPrice: 100, BuyoutPrice: 400, Deposit: ListingDepositGold, FeePercent: SaleFeePercent,
		Duration: time.Hour, ListingLimit: 5,
	}); err != nil {
		t.Fatal(err)
	}
	// A standing bid, then a cancel that has to release it.
	if _, err := owner.Bid(ctx, BidCommand{
		ListingID: 71, BidderID: "rival", AccountID: "account-rival",
		ExpectedItemID: 5005, Money: 100,
	}); err != nil {
		t.Fatal(err)
	}
	cancelled, err := owner.Cancel(ctx, CancelCommand{SellerID: "seller", AccountID: "account-seller", ListingID: 71})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Refund.ID == "" {
		t.Fatalf("the cancel released no bid: %+v", cancelled)
	}
	if cancelled.Refund.HasStatement {
		t.Fatalf("a refunded bid claimed a sale statement: %+v", cancelled.Refund)
	}
	// The outbox read agrees: the row only ever carried the refund wording.
	refunds, err := owner.PendingPayouts(ctx, "rival")
	if err != nil || len(refunds) != 1 {
		t.Fatalf("rival payouts=%+v err=%v", refunds, err)
	}
	if refunds[0].HasStatement || !refunds[0].Refund {
		t.Fatalf("reloaded refund=%+v want a statement-free refund", refunds[0])
	}
}

// TestOwnerCancelForfeitsTheRegistrationDeposit pins the cancel money:
//
// The client's own confirmation dialog is the authority for this rule - dstr
// 60149 reads
//
//	确定取消上架吗？
//	取消上架后， 信誉度会下降， 且无法获得缴纳的押金。
//	取消的拍卖品将以邮件的形式发放。
//
// i.e. a manual cancel loses the deposit you paid. So the return mail carries
// the item and NO gold, no refund_gold key is written, and the 10000 stays in
// the escrow wallet as a gold sink exactly like the sale commission. Only an
// expiry still refunds it.
func TestOwnerCancelForfeitsTheRegistrationDeposit(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 5005, Count: 1},
	})
	owner, _ := NewOwner(repositories)
	if _, err := owner.Register(ctx, RegisterCommand{
		ListingID: 52, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
		ListType: 0, SourceSlot: 9, ExpectedID: 5005, Count: 1, UnitPrice: 75,
		Deposit: ListingDepositGold, FeePercent: DiscountedSaleFeePercent,
		Duration: time.Hour, ListingLimit: 5,
	}); err != nil {
		t.Fatal(err)
	}
	escrowCharacter, _, _ := repositories.Character.Load(ctx, EscrowCharacterID)
	if escrowCharacter.Stats["gold"] != ListingDepositGold {
		t.Fatalf("deposit was not escrowed at registration: %d", escrowCharacter.Stats["gold"])
	}
	cancelled, err := owner.Cancel(ctx, CancelCommand{SellerID: "seller", AccountID: "account-seller", ListingID: 52})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Delivery.Gold != 0 {
		t.Fatalf("cancel refund=%d, want 0 - a manual cancel forfeits the %d deposit",
			cancelled.Delivery.Gold, ListingDepositGold)
	}
	if !cancelled.Delivery.Return {
		t.Fatal("a cancel must still return the item through the outbox")
	}
	// The item itself still rides home.
	if cancelled.Delivery.Item.ItemID != 5005 || cancelled.Delivery.Item.Count != 1 {
		t.Fatalf("returned item=%+v", cancelled.Delivery.Item)
	}
	// The forged deposit is still in escrow: nothing has left it yet.
	escrowCharacter, _, _ = repositories.Character.Load(ctx, EscrowCharacterID)
	if escrowCharacter.Stats["gold"] != ListingDepositGold {
		t.Fatalf("escrow gold=%d, want the forfeited %d", escrowCharacter.Stats["gold"], ListingDepositGold)
	}
	pending, err := owner.PendingDeliveries(ctx, "seller")
	if err != nil || len(pending) != 1 || pending[0].Gold != 0 {
		t.Fatalf("outbox=%+v err=%v want one gold-free return", pending, err)
	}
	// The mail is confirmed with 0 gold, so the deposit can never be paid back.
	if err := owner.ConfirmDeliveryMail(ctx, ConfirmDeliveryCommand{
		DeliveryID: cancelled.Delivery.ID, BuyerID: "seller", ListingID: 52,
		ItemID: 5005, Count: 1, Gold: 0,
	}); err != nil {
		t.Fatal(err)
	}
	escrowCharacter, _, _ = repositories.Character.Load(ctx, EscrowCharacterID)
	if escrowCharacter.Stats["gold"] != ListingDepositGold {
		t.Fatalf("escrow gold=%d after confirming a cancel, want the forfeited %d kept",
			escrowCharacter.Stats["gold"], ListingDepositGold)
	}
	outbox, err := owner.PendingDeliveries(ctx, "seller")
	if err != nil || len(outbox) != 0 {
		t.Fatalf("outbox after confirm=%+v err=%v", outbox, err)
	}
}

// The sibling path is unchanged on purpose: an expired listing (流拍) is not the
// seller's own doing, so its mail still refunds the deposit.
func TestOwnerExpiryStillRefundsTheRegistrationDeposit(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 5005, Count: 1},
	})
	owner, _ := NewOwner(repositories)
	if _, err := owner.Register(ctx, RegisterCommand{
		ListingID: 52, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
		ListType: 0, SourceSlot: 9, ExpectedID: 5005, Count: 1, UnitPrice: 75,
		Deposit: ListingDepositGold, FeePercent: SaleFeePercent,
		Duration: time.Nanosecond, ListingLimit: 5,
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := owner.ReturnExpiredListings(ctx, "seller", "account-seller"); err != nil {
		t.Fatal(err)
	}
	pending, err := owner.PendingDeliveries(ctx, "seller")
	if err != nil || len(pending) != 1 {
		t.Fatalf("expiry outbox=%+v err=%v", pending, err)
	}
	if pending[0].Gold != ListingDepositGold {
		t.Fatalf("expiry refund=%d, want the %d deposit back", pending[0].Gold, ListingDepositGold)
	}
}

// TestOwnerRegisterRejectsUnaffordableDepositWithoutTakingTheItem keeps a
// seller who cannot cover the deposit from losing the item to escrow.
func TestOwnerRegisterRejectsUnaffordableDepositWithoutTakingTheItem(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", ListingDepositGold-1, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 5005, Count: 1},
	})
	owner, _ := NewOwner(repositories)
	if _, err := owner.Register(ctx, RegisterCommand{
		ListingID: 53, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
		ListType: 0, SourceSlot: 9, ExpectedID: 5005, Count: 1, UnitPrice: 75,
		Deposit: ListingDepositGold, FeePercent: SaleFeePercent,
		Duration: time.Hour, ListingLimit: 5,
	}); !errors.Is(err, ErrGoldUnavailable) {
		t.Fatalf("err=%v, want ErrGoldUnavailable", err)
	}
	inventory, _, _ := repositories.Inventory.Load(ctx, "seller")
	if inventory.Slots["0:9"].Count != 1 {
		t.Fatalf("refused registration consumed the source stack: %+v", inventory.Slots)
	}
	listings, err := owner.Search(ctx, nil)
	if err != nil || len(listings) != 0 {
		t.Fatalf("refused registration still listed: %+v err=%v", listings, err)
	}
}

// TestSaleFeePercentForFollowsTheAuctionCoupon pins the one coupon effect that
// carries a number chosen by the operator rather than decoded from the client.
func TestSaleFeePercentForFollowsTheAuctionCoupon(t *testing.T) {
	if got := SaleFeePercentFor(false); got != SaleFeePercent {
		t.Fatalf("no coupon fee=%d, want %d", got, SaleFeePercent)
	}
	if got := SaleFeePercentFor(true); got != DiscountedSaleFeePercent {
		t.Fatalf("coupon fee=%d, want %d", got, DiscountedSaleFeePercent)
	}
	if DiscountedSaleFeePercent >= SaleFeePercent {
		t.Fatalf("the coupon must reduce the fee: %d vs %d", DiscountedSaleFeePercent, SaleFeePercent)
	}
	// A listing registered before charges existed settles free.
	free := Listing{Deposit: 0, FeePercent: 0}
	if free.SaleFee(1000) != 0 || free.SellerProceeds(1000, true) != 1000 {
		t.Fatalf("legacy listing is no longer free: fee=%d proceeds=%d", free.SaleFee(1000), free.SellerProceeds(1000, true))
	}
}

func TestOwnerRegisterRejectsOpeningBidAboveBuyoutWithoutTakingTheItem(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 0, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 1001, Count: 1},
	})
	owner, _ := NewOwner(repositories)
	for name, startPrice := range map[string]int64{"above_buyout": 76, "below_sentinel": -2} {
		if _, err := owner.Register(ctx, RegisterCommand{
			ListingID: 23, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
			ListType: 0, SourceSlot: 9, ExpectedID: 1001, Count: 1, UnitPrice: 75, StartPrice: startPrice,
			Duration: 24 * time.Hour, ListingLimit: 5,
		}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("%s: err=%v, want ErrInvalidRequest", name, err)
		}
	}
	inventory, _, _ := repositories.Inventory.Load(ctx, "seller")
	if inventory.Slots["0:9"].Count != 1 {
		t.Fatalf("rejected registration consumed the source stack: %+v", inventory.Slots)
	}
}

// TestOwnerBiddingLifecycle walks the whole 竞拍 path the client's bidding
// opcode drives, following 86JP's AuctionBidService.TryBid and the shared
// cancel/expire transition in AuctionReturnService: a first bid at the opening
// price, an outbid that refunds the loser, the leader raising their own bid for
// only the difference, and the expiry that hands the item to the winner and the
// proceeds to the seller.
func TestOwnerBiddingLifecycle(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	now := time.Unix(1_800_000_000, 0).UTC()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 1001, Count: 2, RawEntry: make([]byte, 0x77)},
	})
	seedAuctionCharacter(t, ctx, repositories, "first", "account-first", "First", 1_000, nil)
	seedAuctionCharacter(t, ctx, repositories, "second", "account-second", "Second", 1_000, nil)

	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	owner.now = func() time.Time { return now }
	// Opening bid 30/item (total 60), buyout 75/item (total 150).
	if _, err := owner.Register(ctx, RegisterCommand{
		ListingID: 41, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
		ListType: 0, SourceSlot: 9, ExpectedID: 1001, Count: 2, UnitPrice: 75,
		StartPrice: 30, BuyoutPrice: 75, Deposit: ListingDepositGold, FeePercent: SaleFeePercent,
		Duration: 24 * time.Hour, ListingLimit: 5,
	}); err != nil {
		t.Fatal(err)
	}

	for name, probe := range map[string]struct {
		command BidCommand
		want    error
	}{
		"under_opening": {BidCommand{BidderID: "first", AccountID: "account-first", ListingID: 41, ExpectedItemID: 1001, Money: 59}, ErrBidTooLow},
		"at_buyout":     {BidCommand{BidderID: "first", AccountID: "account-first", ListingID: 41, ExpectedItemID: 1001, Money: 150}, ErrBidTooLow},
		"own_listing":   {BidCommand{BidderID: "seller", AccountID: "account-seller", ListingID: 41, ExpectedItemID: 1001, Money: 100}, ErrBuyerIsSeller},
		"wrong_item":    {BidCommand{BidderID: "first", AccountID: "account-first", ListingID: 41, ExpectedItemID: 1002, Money: 100}, ErrItemUnavailable},
	} {
		if _, err := owner.Bid(ctx, probe.command); !errors.Is(err, probe.want) {
			t.Fatalf("%s: err=%v, want %v", name, err, probe.want)
		}
	}
	first, _, _ := repositories.Character.Load(ctx, "first")
	if first.Stats["gold"] != 1_000 {
		t.Fatalf("refused bid moved gold: %d", first.Stats["gold"])
	}

	opened, err := owner.Bid(ctx, BidCommand{BidderID: "first", AccountID: "account-first", ListingID: 41, ExpectedItemID: 1001, Money: 60})
	if err != nil {
		t.Fatal(err)
	}
	if opened.GoldAfter != 940 || opened.Refund.ID != "" {
		t.Fatalf("opening bid=%+v", opened)
	}
	// The bid has to become the listing's current price: that is the number the
	// client reads back as "beat this".
	listings, err := owner.Search(ctx, []uint32{1001})
	if err != nil || len(listings) != 1 || listings[0].BidTotal != 60 ||
		listings[0].BidderID != "first" || listings[0].DisplayBidTotal() != 60 {
		t.Fatalf("standing bid did not survive escrow: %+v err=%v", listings, err)
	}
	if _, err := owner.Bid(ctx, BidCommand{BidderID: "second", AccountID: "account-second", ListingID: 41, ExpectedItemID: 1001, Money: 60}); !errors.Is(err, ErrBidTooLow) {
		t.Fatalf("matching the standing bid must not win it: %v", err)
	}

	outbid, err := owner.Bid(ctx, BidCommand{BidderID: "second", AccountID: "account-second", ListingID: 41, ExpectedItemID: 1001, Money: 90})
	if err != nil {
		t.Fatal(err)
	}
	if outbid.GoldAfter != 910 || outbid.Refund.SellerID != "first" || outbid.Refund.Gold != 60 || !outbid.Refund.Refund {
		t.Fatalf("outbid did not release the previous bid: %+v", outbid)
	}
	refunds, err := owner.PendingPayouts(ctx, "first")
	if err != nil || len(refunds) != 1 || refunds[0].Gold != 60 || !refunds[0].Refund {
		t.Fatalf("refund outbox=%+v err=%v", refunds, err)
	}
	if err := owner.ConfirmPayoutMail(ctx, ConfirmPayoutCommand{PayoutID: refunds[0].ID, SellerID: "first", Gold: 60}); err != nil {
		t.Fatal(err)
	}

	// The leader raising their own bid pays only the difference: the earlier 90
	// is already escrowed under their name.
	raised, err := owner.Bid(ctx, BidCommand{BidderID: "second", AccountID: "account-second", ListingID: 41, ExpectedItemID: 1001, Money: 120})
	if err != nil {
		t.Fatal(err)
	}
	if raised.GoldAfter != 880 || raised.Refund.ID != "" {
		t.Fatalf("self-raise charged more than the difference: %+v", raised)
	}

	// Nothing settles before the deadline, and once past it the listing must
	// not go home to its seller while a bid stands on it.
	if settled, err := owner.SettleWonListings(ctx); err != nil || len(settled.Settlements) != 0 {
		t.Fatalf("settled early: %+v err=%v", settled, err)
	}
	owner.now = func() time.Time { return now.Add(25 * time.Hour) }
	returned, err := owner.ReturnExpiredListings(ctx, "seller", "account-seller")
	if err != nil || len(returned.ExpiredReturns) != 0 {
		t.Fatalf("a listing under bid must not be returned to its seller: %+v err=%v", returned, err)
	}

	settled, err := owner.SettleWonListings(ctx)
	if err != nil || len(settled.Settlements) != 1 {
		t.Fatalf("settlement=%+v err=%v", settled, err)
	}
	settlement := settled.Settlements[0]
	if settlement.Delivery.BuyerID != "second" || settlement.Delivery.Item.Count != 2 {
		t.Fatalf("winner delivery=%+v", settlement.Delivery)
	}
	// 120 bid - 5% commission + the deposit riding home.
	if settlement.Payout.SellerID != "seller" || settlement.Payout.Gold != 120-6+ListingDepositGold {
		t.Fatalf("seller payout=%+v", settlement.Payout)
	}
	// Settling twice must not duplicate either half.
	if again, err := owner.SettleWonListings(ctx); err != nil || len(again.Settlements) != 0 {
		t.Fatalf("second sweep produced rows again: %+v err=%v", again, err)
	}
	deliveries, err := owner.PendingDeliveries(ctx, "second")
	if err != nil || len(deliveries) != 1 || deliveries[0].Item.Count != 2 {
		t.Fatalf("winner outbox=%+v err=%v", deliveries, err)
	}
	if _, present := deliveries[0].Item.Extra["bid_total"]; present {
		t.Fatalf("delivered item still carries bid bookkeeping: %+v", deliveries[0].Item.Extra)
	}
}

// TestOwnerOutbidGoldStaysAvailableToTheSameListing is the case a player hit
// with 10,000 gold and an 8,000 bid outstanding: once outbid, the 8,000 is
// queued for return by mail, so the wallet still reads 2,000 and the next
// raise is refused for lack of funds even though the money is sitting in
// escrow against that very listing. A re-bid has to consume its own pending
// release, not wait for the mailbox.
func TestOwnerOutbidGoldStaysAvailableToTheSameListing(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	now := time.Unix(1_800_000_000, 0).UTC()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 1001, Count: 1, RawEntry: make([]byte, 0x77)},
	})
	seedAuctionCharacter(t, ctx, repositories, "player", "account-player", "Player", 10_000, nil)
	seedAuctionCharacter(t, ctx, repositories, "rival", "account-rival", "Rival", 10_000, nil)

	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	owner.now = func() time.Time { return now }
	if _, err := owner.Register(ctx, RegisterCommand{
		ListingID: 61, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
		ListType: 0, SourceSlot: 9, ExpectedID: 1001, Count: 1, UnitPrice: 20_000,
		StartPrice: 100, BuyoutPrice: 20_000, Deposit: ListingDepositGold, FeePercent: SaleFeePercent,
		Duration: 24 * time.Hour, ListingLimit: 5,
	}); err != nil {
		t.Fatal(err)
	}

	opened, err := owner.Bid(ctx, BidCommand{BidderID: "player", AccountID: "account-player", ListingID: 61, ExpectedItemID: 1001, Money: 8_000})
	if err != nil {
		t.Fatal(err)
	}
	if opened.GoldAfter != 2_000 {
		t.Fatalf("first bid left %d gold, want 2000", opened.GoldAfter)
	}
	if _, err := owner.Bid(ctx, BidCommand{BidderID: "rival", AccountID: "account-rival", ListingID: 61, ExpectedItemID: 1001, Money: 8_500}); err != nil {
		t.Fatal(err)
	}
	// The wallet has not seen the 8,000 again - it is queued for return.
	player, _, _ := repositories.Character.Load(ctx, "player")
	if player.Stats["gold"] != 2_000 {
		t.Fatalf("outbid refund reached the wallet early: %d", player.Stats["gold"])
	}

	raised, err := owner.Bid(ctx, BidCommand{BidderID: "player", AccountID: "account-player", ListingID: 61, ExpectedItemID: 1001, Money: 9_000})
	if err != nil {
		t.Fatalf("re-bid refused while the bidder's own 8000 sat in escrow: %v", err)
	}
	// Only 9000-8000 leaves the wallet, and the release is gone: it became
	// part of the new bid rather than a second payment.
	if raised.GoldAfter != 1_000 {
		t.Fatalf("re-bid left %d gold, want 1000", raised.GoldAfter)
	}
	refunds, err := owner.PendingPayouts(ctx, "player")
	if err != nil || len(refunds) != 0 {
		t.Fatalf("consumed release still queued: %+v err=%v", refunds, err)
	}
	// The rival's 8,500 is the only thing owed back now, and the escrow wallet
	// holds exactly that plus the standing bid.
	rivalRefunds, err := owner.PendingPayouts(ctx, "rival")
	if err != nil || len(rivalRefunds) != 1 || rivalRefunds[0].Gold != 8_500 {
		t.Fatalf("rival release=%+v err=%v", rivalRefunds, err)
	}
	escrowCharacter, _, _ := repositories.Character.Load(ctx, EscrowCharacterID)
	if escrowCharacter.Stats["gold"] != 8_500+9_000+ListingDepositGold {
		t.Fatalf("escrow wallet=%d, want the rival release plus the standing bid plus the deposit", escrowCharacter.Stats["gold"])
	}
	listings, err := owner.Search(ctx, nil)
	if err != nil || len(listings) != 1 || listings[0].BidTotal != 9_000 || listings[0].BidderID != "player" {
		t.Fatalf("standing bid=%+v err=%v", listings, err)
	}

	// The rival's release goes back to their wallet rather than their mailbox,
	// because the client refuses to send a bid larger than the wallet.
	goldAfter, err := owner.RefundBidToBidder(ctx, rivalRefunds[0])
	if err != nil || goldAfter != 10_000 {
		t.Fatalf("refund to wallet=%d err=%v, want the rival whole again", goldAfter, err)
	}
	rival, _, _ := repositories.Character.Load(ctx, "rival")
	escrowCharacter, _, _ = repositories.Character.Load(ctx, EscrowCharacterID)
	if rival.Stats["gold"] != 10_000 || escrowCharacter.Stats["gold"] != 9_000+ListingDepositGold {
		t.Fatalf("wallet=%d escrow=%d after the refund", rival.Stats["gold"], escrowCharacter.Stats["gold"])
	}
	// Paying the same row twice must be a no-op, not a second credit.
	goldAfter, err = owner.RefundBidToBidder(ctx, rivalRefunds[0])
	if err != nil || goldAfter != 10_000 {
		t.Fatalf("replayed refund=%d err=%v", goldAfter, err)
	}
	rival, _, _ = repositories.Character.Load(ctx, "rival")
	if rival.Stats["gold"] != 10_000 {
		t.Fatalf("replayed refund paid twice: %d", rival.Stats["gold"])
	}
}

// TestOwnerBuyoutReleasesStandingBid pins the two ways a buyout meets a bid:
// somebody else's bid is refunded, and the bidder's own bid counts towards the
// buyout so only the difference is charged. Both follow 86JP's TryBuy.
func TestOwnerBuyoutReleasesStandingBid(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	now := time.Unix(1_800_000_000, 0).UTC()
	seedAuctionCharacter(t, ctx, repositories, "seller", "account-seller", "Seller", 20_000, map[string]dnfrepo.ItemStack{
		"0:9":  {ItemID: 1001, Count: 2, RawEntry: make([]byte, 0x77)},
		"0:10": {ItemID: 1001, Count: 2, RawEntry: make([]byte, 0x77)},
	})
	seedAuctionCharacter(t, ctx, repositories, "bidder", "account-bidder", "Bidder", 1_000, nil)
	seedAuctionCharacter(t, ctx, repositories, "buyer", "account-buyer", "Buyer", 1_000, nil)

	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	owner.now = func() time.Time { return now }
	for _, probe := range []struct {
		id   uint64
		slot int16
	}{{51, 9}, {52, 10}} {
		if _, err := owner.Register(ctx, RegisterCommand{
			ListingID: probe.id, SellerID: "seller", AccountID: "account-seller", SellerName: "Seller",
			ListType: 0, SourceSlot: probe.slot, ExpectedID: 1001, Count: 2, UnitPrice: 75,
			StartPrice: 30, BuyoutPrice: 75, Deposit: ListingDepositGold, FeePercent: SaleFeePercent,
			Duration: 24 * time.Hour, ListingLimit: 5,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := owner.Bid(ctx, BidCommand{BidderID: "bidder", AccountID: "account-bidder", ListingID: 51, ExpectedItemID: 1001, Money: 90}); err != nil {
		t.Fatal(err)
	}
	// A listing under bid cannot be split.
	if _, err := owner.Buy(ctx, BuyCommand{BuyerID: "buyer", AccountID: "account-buyer", ListingID: 51, ExpectedItemID: 1001, Count: 1, Money: 75}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("partial buyout over a bid: %v", err)
	}
	boughtOver, err := owner.Buy(ctx, BuyCommand{BuyerID: "buyer", AccountID: "account-buyer", ListingID: 51, ExpectedItemID: 1001, Money: 150, WholeListing: true})
	if err != nil {
		t.Fatal(err)
	}
	if boughtOver.GoldAfter != 850 || boughtOver.Refund.SellerID != "bidder" || boughtOver.Refund.Gold != 90 {
		t.Fatalf("buyout over another bid=%+v", boughtOver)
	}

	if _, err := owner.Bid(ctx, BidCommand{BidderID: "bidder", AccountID: "account-bidder", ListingID: 52, ExpectedItemID: 1001, Money: 90}); err != nil {
		t.Fatal(err)
	}
	bidderBefore, _, _ := repositories.Character.Load(ctx, "bidder")
	ownBuyout, err := owner.Buy(ctx, BuyCommand{BuyerID: "bidder", AccountID: "account-bidder", ListingID: 52, ExpectedItemID: 1001, Money: 150, WholeListing: true})
	if err != nil {
		t.Fatal(err)
	}
	// Only 150-90 leaves the wallet, and nobody is refunded.
	if ownBuyout.GoldAfter != bidderBefore.Stats["gold"]-60 || ownBuyout.Refund.ID != "" {
		t.Fatalf("own-bid buyout charged the full price: before=%d after=%+v", bidderBefore.Stats["gold"], ownBuyout)
	}
	// The seller is paid on the buyout, not on the netted charge.
	if ownBuyout.Payout.Gold != 150-7+ListingDepositGold {
		t.Fatalf("seller proceeds=%+v", ownBuyout.Payout)
	}
}

func seedAuctionCharacter(t *testing.T, ctx context.Context, repositories dnfrepo.Group, characterID, accountID, name string, gold int64, slots map[string]dnfrepo.ItemStack) {
	t.Helper()
	if slots == nil {
		slots = make(map[string]dnfrepo.ItemStack)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: characterID, AccountID: accountID, Name: name, Stats: map[string]int64{"gold": gold}}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: characterID, Slots: slots}); err != nil {
		t.Fatal(err)
	}
}
