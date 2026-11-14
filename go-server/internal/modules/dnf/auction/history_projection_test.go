package auction

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type auctionHistoryProjectionPrefixReader struct {
	repo.InventoryRepository
	prefixSlots map[string]repo.ItemStack
	loadCalls   int
	prefixCalls int
	loadKey     string
	owner       string
	prefix      string
	loadErr     error
	prefixErr   error
}

func (p *auctionHistoryProjectionPrefixReader) Load(_ context.Context, key string) (repo.InventoryRecord, bool, error) {
	p.loadCalls++
	p.loadKey = key
	return repo.InventoryRecord{}, false, p.loadErr
}

func (p *auctionHistoryProjectionPrefixReader) LoadInventorySlotPrefix(_ context.Context, owner, prefix string) (map[string]repo.ItemStack, error) {
	p.prefixCalls++
	p.owner = owner
	p.prefix = prefix
	return p.prefixSlots, p.prefixErr
}

type auctionHistoryProjectionItemReader struct {
	*auctionHistoryProjectionPrefixReader
	itemSlots  map[string]repo.ItemStack
	itemCalls  int
	itemOwner  string
	itemPrefix string
	itemID     int64
	itemErr    error
}

func (p *auctionHistoryProjectionItemReader) LoadInventorySlotItemPrefix(_ context.Context, owner, prefix string, itemID int64) (map[string]repo.ItemStack, error) {
	p.itemCalls++
	p.itemOwner = owner
	p.itemPrefix = prefix
	p.itemID = itemID
	return p.itemSlots, p.itemErr
}

type auctionHistoryProjectionLegacyReader struct {
	repo.InventoryRepository
	loadCalls int
	loadKey   string
}

func (p *auctionHistoryProjectionLegacyReader) Load(ctx context.Context, key string) (repo.InventoryRecord, bool, error) {
	p.loadCalls++
	p.loadKey = key
	return p.InventoryRepository.Load(ctx, key)
}

func auctionHistoryProjectionEscrowSlots(t *testing.T, ctx context.Context, inventory repo.InventoryRepository, itemID int64) map[string]repo.ItemStack {
	t.Helper()
	record, found, err := inventory.Load(ctx, EscrowCharacterID)
	if err != nil || !found {
		t.Fatalf("load escrow inventory: found=%v err=%v", found, err)
	}
	slots := make(map[string]repo.ItemStack)
	for key, stack := range record.Slots {
		if strings.HasPrefix(key, saleHistoryPrefix) && (itemID <= 0 || stack.ItemID == itemID) {
			slots[key] = stack
		}
	}
	return slots
}

func completeAuctionHistoryProjectionSales(t *testing.T, ctx context.Context, owner *Owner, now time.Time) {
	t.Helper()
	for _, cmd := range []BuyCommand{
		{ListingID: 1, BuyerID: "buyer1", AccountID: "a-buyer1", ExpectedItemID: 6001, Count: 1, Money: 100},
		{ListingID: 1, BuyerID: "buyer2", AccountID: "a-buyer2", ExpectedItemID: 6001, Count: 2, Money: 200},
	} {
		if _, err := owner.Buy(ctx, cmd); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.Register(ctx, RegisterCommand{ListingID: 2, SellerID: "seller", AccountID: "a-seller", ListType: 0, SourceSlot: 10, ExpectedID: 6001, Count: 2, UnitPrice: 50, StartPrice: 50, BuyoutPrice: -1, Duration: 24 * time.Hour, ListingLimit: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Bid(ctx, BidCommand{ListingID: 2, BidderID: "buyer1", AccountID: "a-buyer1", ExpectedItemID: 6001, Money: 300}); err != nil {
		t.Fatal(err)
	}
	owner.now = func() time.Time { return now.Add(25 * time.Hour) }
	for range 3 {
		if _, err := owner.SettleWonListings(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSaleHistoryUsesHistoryPrefixProjection(t *testing.T) {
	ctx, repositories, owner, now := historyFixture(t)
	completeAuctionHistoryProjectionSales(t, ctx, owner, now)
	want, err := owner.SaleHistory(ctx, "seller", "a-seller")
	if err != nil {
		t.Fatal(err)
	}

	projection := &auctionHistoryProjectionItemReader{
		auctionHistoryProjectionPrefixReader: &auctionHistoryProjectionPrefixReader{
			InventoryRepository: repositories.Inventory,
			prefixSlots:         auctionHistoryProjectionEscrowSlots(t, ctx, repositories.Inventory, 0),
			loadErr:             errors.New("complete inventory load rejected"),
		},
		itemSlots: auctionHistoryProjectionEscrowSlots(t, ctx, repositories.Inventory, 6001),
	}
	repositories.Inventory = projection
	projected, err := mustAuctionHistoryOwner(t, repositories).SaleHistory(ctx, "seller", "a-seller")
	if err != nil || !reflect.DeepEqual(projected, want) {
		t.Fatalf("prefix projection differs: got=%+v want=%+v err=%v", projected, want, err)
	}
	if projection.prefixCalls != 1 || projection.itemCalls != 0 || projection.loadCalls != 0 {
		t.Fatalf("history selected wrong reads: prefix=%d item=%d load=%d", projection.prefixCalls, projection.itemCalls, projection.loadCalls)
	}
	if projection.owner != EscrowCharacterID || projection.prefix != saleHistoryPrefix {
		t.Fatalf("prefix scope owner=%q prefix=%q", projection.owner, projection.prefix)
	}
}

func TestRecentSaleAverageUsesItemPrefixProjection(t *testing.T) {
	ctx, repositories, owner, now := historyFixture(t)
	completeAuctionHistoryProjectionSales(t, ctx, owner, now)
	wantAverage, wantSamples, err := owner.RecentSaleAverage(ctx, 6001)
	if err != nil {
		t.Fatal(err)
	}
	if wantAverage != 120 || wantSamples != 3 {
		t.Fatalf("weighted fixture average=%d samples=%d", wantAverage, wantSamples)
	}

	projection := &auctionHistoryProjectionItemReader{
		auctionHistoryProjectionPrefixReader: &auctionHistoryProjectionPrefixReader{
			InventoryRepository: repositories.Inventory,
			prefixSlots:         auctionHistoryProjectionEscrowSlots(t, ctx, repositories.Inventory, 0),
			loadErr:             errors.New("complete inventory load rejected"),
			prefixErr:           errors.New("unfiltered prefix read rejected"),
		},
		itemSlots: auctionHistoryProjectionEscrowSlots(t, ctx, repositories.Inventory, 6001),
	}
	repositories.Inventory = projection
	projectedOwner := mustAuctionHistoryOwner(t, repositories)
	gotAverage, gotSamples, err := projectedOwner.RecentSaleAverage(ctx, 6001)
	if err != nil || gotAverage != wantAverage || gotSamples != wantSamples {
		t.Fatalf("item projection average=%d samples=%d want=%d/%d err=%v", gotAverage, gotSamples, wantAverage, wantSamples, err)
	}
	if projection.itemCalls != 1 || projection.prefixCalls != 0 || projection.loadCalls != 0 {
		t.Fatalf("average selected wrong reads: item=%d prefix=%d load=%d", projection.itemCalls, projection.prefixCalls, projection.loadCalls)
	}
	if projection.itemOwner != EscrowCharacterID || projection.itemPrefix != saleHistoryPrefix || projection.itemID != 6001 {
		t.Fatalf("item scope owner=%q prefix=%q item=%d", projection.itemOwner, projection.itemPrefix, projection.itemID)
	}

	itemErr := errors.New("item projection unavailable")
	projection.itemErr = itemErr
	if _, _, err := projectedOwner.RecentSaleAverage(ctx, 6001); err != itemErr {
		t.Fatalf("item error was not returned unchanged: %v", err)
	}
	if projection.prefixCalls != 0 || projection.loadCalls != 0 {
		t.Fatal("item projection failure retried through a broader read")
	}
}

func TestHistoryProjectionFallsBackWithoutFailOpen(t *testing.T) {
	ctx, repositories, owner, now := historyFixture(t)
	completeAuctionHistoryProjectionSales(t, ctx, owner, now)
	wantRecords, err := owner.SaleHistory(ctx, "seller", "a-seller")
	if err != nil {
		t.Fatal(err)
	}
	wantAverage, wantSamples, err := owner.RecentSaleAverage(ctx, 6001)
	if err != nil {
		t.Fatal(err)
	}
	legacyInventory := repositories.Inventory

	prefix := &auctionHistoryProjectionPrefixReader{
		InventoryRepository: repositories.Inventory,
		prefixSlots:         auctionHistoryProjectionEscrowSlots(t, ctx, repositories.Inventory, 0),
		loadErr:             errors.New("complete inventory load rejected"),
	}
	repositories.Inventory = prefix
	prefixOwner := mustAuctionHistoryOwner(t, repositories)
	prefixRecords, err := prefixOwner.SaleHistory(ctx, "seller", "a-seller")
	if err != nil || !reflect.DeepEqual(prefixRecords, wantRecords) {
		t.Fatalf("prefix history differs: got=%+v want=%+v err=%v", prefixRecords, wantRecords, err)
	}
	prefixAverage, prefixSamples, err := prefixOwner.RecentSaleAverage(ctx, 6001)
	if err != nil || prefixAverage != wantAverage || prefixSamples != wantSamples {
		t.Fatalf("prefix average=%d/%d want=%d/%d err=%v", prefixAverage, prefixSamples, wantAverage, wantSamples, err)
	}
	if prefix.prefixCalls != 2 || prefix.loadCalls != 0 {
		t.Fatalf("prefix fallback reads: prefix=%d load=%d", prefix.prefixCalls, prefix.loadCalls)
	}

	prefixErr := errors.New("prefix projection unavailable")
	prefix.prefixErr = prefixErr
	if _, err := prefixOwner.SaleHistory(ctx, "seller", "a-seller"); err != prefixErr {
		t.Fatalf("sale history prefix error was not returned unchanged: %v", err)
	}
	if _, _, err := prefixOwner.RecentSaleAverage(ctx, 6001); err != prefixErr {
		t.Fatalf("prefix error was not returned unchanged: %v", err)
	}
	if prefix.loadCalls != 0 {
		t.Fatal("prefix projection failure retried through a complete load")
	}

	legacy := &auctionHistoryProjectionLegacyReader{InventoryRepository: legacyInventory}
	repositories.Inventory = legacy
	legacyOwner := mustAuctionHistoryOwner(t, repositories)
	legacyAverage, legacySamples, err := legacyOwner.RecentSaleAverage(ctx, 6001)
	if err != nil || legacyAverage != wantAverage || legacySamples != wantSamples || legacy.loadCalls != 1 {
		t.Fatalf("legacy average=%d/%d want=%d/%d calls=%d err=%v", legacyAverage, legacySamples, wantAverage, wantSamples, legacy.loadCalls, err)
	}
	if legacy.loadKey != EscrowCharacterID {
		t.Fatalf("legacy inventory key=%q", legacy.loadKey)
	}
}

func TestSaleHistoryRejectsOwnerBeforeLedgerRead(t *testing.T) {
	for _, tt := range []struct {
		name        string
		characterID string
		accountID   string
		deleteOwner bool
	}{
		{name: "cross account", characterID: "seller", accountID: "a-buyer1"},
		{name: "missing owner", characterID: "missing", accountID: "a-missing"},
		{name: "deleted owner", characterID: "seller", accountID: "a-seller", deleteOwner: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, repositories, _, _ := historyFixture(t)
			if tt.deleteOwner {
				character, found, err := repositories.Character.Load(ctx, tt.characterID)
				if err != nil || !found {
					t.Fatalf("load owner: found=%v err=%v", found, err)
				}
				character.Stats["delete_flag"] = 1
				if err := repositories.Character.Save(ctx, character); err != nil {
					t.Fatal(err)
				}
			}
			projection := &auctionHistoryProjectionItemReader{
				auctionHistoryProjectionPrefixReader: &auctionHistoryProjectionPrefixReader{
					InventoryRepository: repositories.Inventory,
					loadErr:             errors.New("complete inventory load rejected"),
					prefixErr:           errors.New("prefix read rejected"),
				},
				itemErr: errors.New("item read rejected"),
			}
			repositories.Inventory = projection
			_, err := mustAuctionHistoryOwner(t, repositories).SaleHistory(ctx, tt.characterID, tt.accountID)
			if !errors.Is(err, ErrNotOwner) {
				t.Fatalf("owner rejection=%v", err)
			}
			if projection.prefixCalls != 0 || projection.itemCalls != 0 || projection.loadCalls != 0 {
				t.Fatalf("ledger read before owner rejection: prefix=%d item=%d load=%d", projection.prefixCalls, projection.itemCalls, projection.loadCalls)
			}
		})
	}
}

func TestRecentSaleAverageUsesLatestTwentyAtEqualTime(t *testing.T) {
	ctx := context.Background()
	repositories := memory.NewMemoryGroup()
	now := time.Unix(1_800_000_000, 0).UTC()
	const (
		firstListing = uint64(100)
		saleCount    = 24
	)
	slots := make(map[string]repo.ItemStack, saleCount)
	for i := 0; i < saleCount; i++ {
		slots["0:"+strconv.Itoa(i)] = repo.ItemStack{ItemID: 6001, Count: 1}
	}
	seedAuctionCharacter(t, ctx, repositories, "seller", "a-seller", "seller", 1_000_000, slots)
	seedAuctionCharacter(t, ctx, repositories, "buyer", "a-buyer", "buyer", 10_000, nil)
	owner := mustAuctionHistoryOwner(t, repositories)
	owner.now = func() time.Time { return now }
	for i := 0; i < saleCount; i++ {
		listingID := firstListing + uint64(i)
		price := int64(listingID)
		if _, err := owner.Register(ctx, RegisterCommand{ListingID: listingID, SellerID: "seller", AccountID: "a-seller", ListType: 0, SourceSlot: int16(i), ExpectedID: 6001, Count: 1, UnitPrice: price, StartPrice: -1, BuyoutPrice: price, Duration: 24 * time.Hour, ListingLimit: 5}); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Buy(ctx, BuyCommand{ListingID: listingID, BuyerID: "buyer", AccountID: "a-buyer", ExpectedItemID: 6001, Count: 1, Money: price}); err != nil {
			t.Fatal(err)
		}
	}

	wantTotal := int64(0)
	for listingID := firstListing + saleCount - 20; listingID < firstListing+saleCount; listingID++ {
		wantTotal += int64(listingID)
	}
	wantAverage := wantTotal / 20
	projection := &auctionHistoryProjectionItemReader{
		auctionHistoryProjectionPrefixReader: &auctionHistoryProjectionPrefixReader{
			InventoryRepository: repositories.Inventory,
			prefixSlots:         auctionHistoryProjectionEscrowSlots(t, ctx, repositories.Inventory, 0),
			loadErr:             errors.New("complete inventory load rejected"),
		},
		itemSlots: auctionHistoryProjectionEscrowSlots(t, ctx, repositories.Inventory, 6001),
	}
	repositories.Inventory = projection
	projectedOwner := mustAuctionHistoryOwner(t, repositories)
	gotAverage, gotSamples, err := projectedOwner.RecentSaleAverage(ctx, 6001)
	if err != nil || gotAverage != wantAverage || gotSamples != 20 {
		t.Fatalf("latest twenty average=%d samples=%d want=%d/20 err=%v", gotAverage, gotSamples, wantAverage, err)
	}
	if projection.itemCalls != 1 || projection.prefixCalls != 0 || projection.loadCalls != 0 {
		t.Fatalf("latest twenty selected wrong reads: item=%d prefix=%d load=%d", projection.itemCalls, projection.prefixCalls, projection.loadCalls)
	}
	records, err := projectedOwner.SaleHistory(ctx, "seller", "a-seller")
	if err != nil {
		t.Fatalf("sale history: %v", err)
	}
	if len(records) != saleCount {
		t.Fatalf("sale history length=%d want=%d", len(records), saleCount)
	}
	if records[0].ListingID != firstListing+saleCount-1 || records[len(records)-1].ListingID != firstListing {
		t.Fatalf("equal-time stable order first=%d last=%d", records[0].ListingID, records[len(records)-1].ListingID)
	}
}

func TestRecentSaleAverageAccumulatesInBigInteger(t *testing.T) {
	ctx := context.Background()
	repositories := memory.NewMemoryGroup()
	seedAuctionCharacter(t, ctx, repositories, "seller", "a-seller", "seller", 0, nil)
	slots := make(map[string]repo.ItemStack)
	at := time.Unix(1_800_000_000, 0).UTC()
	for i := 0; i < 20; i++ {
		total := int64(math.MaxInt64)
		if i%2 == 1 {
			total--
		}
		listing := Listing{ID: uint64(100 + i), SellerID: "seller", Item: repo.ItemStack{ItemID: 6001, Count: 1}}
		recordAuctionSale(slots, listing, "buyer", 1, total, 0, at, false)
	}
	projection := &auctionHistoryProjectionPrefixReader{
		InventoryRepository: repositories.Inventory,
		prefixSlots:         slots,
		loadErr:             errors.New("complete inventory load rejected"),
	}
	repositories.Inventory = projection
	gotAverage, gotSamples, err := mustAuctionHistoryOwner(t, repositories).RecentSaleAverage(ctx, 6001)
	wantAverage := int64(math.MaxInt64 - 1)
	if err != nil || gotAverage != wantAverage || gotSamples != 20 {
		t.Fatalf("big integer average=%d samples=%d want=%d/20 err=%v", gotAverage, gotSamples, wantAverage, err)
	}
	if projection.prefixCalls != 1 || projection.loadCalls != 0 {
		t.Fatalf("big integer selected wrong reads: prefix=%d load=%d", projection.prefixCalls, projection.loadCalls)
	}
}

func mustAuctionHistoryOwner(t *testing.T, repositories repo.Group) *Owner {
	t.Helper()
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	return owner
}
