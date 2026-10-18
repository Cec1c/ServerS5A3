package auction

import (
	"context"
	"errors"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func historyFixture(t *testing.T) (context.Context, repo.Group, *Owner, time.Time) {
	t.Helper()
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	now := time.Unix(1_800_000_000, 0).UTC()
	seedAuctionCharacter(t, ctx, r, "seller", "a-seller", "卖家", 20000, map[string]repo.ItemStack{"0:9": {ItemID: 6001, Count: 3}, "0:10": {ItemID: 6001, Count: 2}})
	for _, id := range []string{"buyer1", "buyer2"} {
		seedAuctionCharacter(t, ctx, r, id, "a-"+id, id, 1000, nil)
	}
	o, err := NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	o.now = func() time.Time { return now }
	_, err = o.Register(ctx, RegisterCommand{ListingID: 1, SellerID: "seller", AccountID: "a-seller", ListType: 0, SourceSlot: 9, ExpectedID: 6001, Count: 3, UnitPrice: 100, StartPrice: -1, BuyoutPrice: 100, Duration: 24 * time.Hour, ListingLimit: 5})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, r, o, now
}

func TestSaleHistoryUsesCommittedPartialAndBidSales(t *testing.T) {
	ctx, r, o, now := historyFixture(t)
	for _, cmd := range []BuyCommand{{ListingID: 1, BuyerID: "buyer1", AccountID: "a-buyer1", ExpectedItemID: 6001, Count: 1, Money: 100}, {ListingID: 1, BuyerID: "buyer2", AccountID: "a-buyer2", ExpectedItemID: 6001, Count: 2, Money: 200}} {
		if _, err := o.Buy(ctx, cmd); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.Register(ctx, RegisterCommand{ListingID: 2, SellerID: "seller", AccountID: "a-seller", ListType: 0, SourceSlot: 10, ExpectedID: 6001, Count: 2, UnitPrice: 50, StartPrice: 50, BuyoutPrice: -1, Duration: 24 * time.Hour, ListingLimit: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Bid(ctx, BidCommand{ListingID: 2, BidderID: "buyer1", AccountID: "a-buyer1", ExpectedItemID: 6001, Money: 300}); err != nil {
		t.Fatal(err)
	}
	o.now = func() time.Time { return now.Add(25 * time.Hour) }
	for range 3 {
		if _, err := o.SettleWonListings(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := o.SaleHistory(ctx, "seller", "a-seller")
	if err != nil || len(rows) != 3 {
		t.Fatal("成交遗漏或重复", rows, err)
	}
	if !rows[0].Bidding || rows[0].Count != 2 || rows[0].TotalGold != 300 {
		t.Fatal(rows)
	}
	buyer, err := o.SaleHistory(ctx, "buyer2", "a-buyer2")
	if err != nil || len(buyer) != 1 || buyer[0].Count != 2 {
		t.Fatal(buyer, err)
	}
	if _, err := o.SaleHistory(ctx, "seller", "a-buyer1"); !errors.Is(err, ErrNotOwner) {
		t.Fatal("跨账号读取成交历史", err)
	}
	other, err := NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	average, samples, err := other.RecentSaleAverage(ctx, 6001)
	if err != nil || average != 120 || samples != 3 {
		t.Fatal(average, samples, err)
	}
	if average, samples, err := other.RecentSaleAverage(ctx, 777); err != nil || average != 0 || samples != 0 {
		t.Fatal(average, samples, err)
	}
}

type historyFailTrade struct{ repo.CharacterTradeUnitOfWork }

func (f historyFailTrade) WithinCharacterTrade(ctx context.Context, a, b string, fn func(repo.CharacterRepository, repo.InventoryRepository) error) error {
	return f.CharacterTradeUnitOfWork.WithinCharacterTrade(ctx, a, b, func(c repo.CharacterRepository, i repo.InventoryRepository) error {
		return fn(c, historyFailInventory{i})
	})
}

type historyFailInventory struct{ repo.InventoryRepository }

func (f historyFailInventory) Save(ctx context.Context, v repo.InventoryRecord) error {
	if v.CharacterID == EscrowCharacterID && len(auctionSaleRecords(v.Slots)) > 0 {
		return errors.New("注入成交账本保存失败")
	}
	return f.InventoryRepository.Save(ctx, v)
}
func TestSaleHistoryFailureRollsBackMoneyItemAndRecord(t *testing.T) {
	ctx, r, o, _ := historyFixture(t)
	o.repositories.CharacterTrade = historyFailTrade{r.CharacterTrade}
	if _, err := o.Buy(ctx, BuyCommand{ListingID: 1, BuyerID: "buyer1", AccountID: "a-buyer1", ExpectedItemID: 6001, Count: 1, Money: 100}); err == nil {
		t.Fatal("故障未触发")
	}
	buyer, _, _ := r.Character.Load(ctx, "buyer1")
	if buyer.Stats["gold"] != 1000 {
		t.Fatal("失败后仍扣金币", buyer)
	}
	listing, err := o.LoadListing(ctx, 1)
	if err != nil || listing.Item.Count != 3 {
		t.Fatal("失败后商品减少", listing, err)
	}
	rows, err := o.SaleHistory(ctx, "seller", "a-seller")
	if err != nil || len(rows) != 0 {
		t.Fatal("失败交易写入成交历史", rows, err)
	}
}
