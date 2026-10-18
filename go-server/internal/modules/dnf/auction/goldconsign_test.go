package auction

import (
	"context"
	"errors"
	shop "longheng.io/server/internal/modules/dnf/cerashop"
	mail "longheng.io/server/internal/modules/dnf/mail"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"strconv"
	"sync"
	"testing"
	"time"
)

func goldFixture(t *testing.T) (context.Context, repo.Group, *GoldConsignOwner, time.Time) {
	t.Helper()
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	now := time.Unix(1800000000, 0).UTC()
	for _, id := range []string{"seller", "buyer", "other"} {
		account := repo.AccountRecord{AccountID: "a-" + id}
		shop.SetBalance(&account, 1000)
		if e := r.Account.Save(ctx, account); e != nil {
			t.Fatal(e)
		}
		if e := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: account.AccountID, Name: id, Level: 90, Stats: map[string]int64{"gold": 10000000}}); e != nil {
			t.Fatal(e)
		}
		if e := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: id, Slots: map[string]repo.ItemStack{}}); e != nil {
			t.Fatal(e)
		}
	}
	o, e := NewGoldConsignOwner(r)
	if e != nil {
		t.Fatal(e)
	}
	o.now = func() time.Time { return now }
	if _, e = o.Register(ctx, GoldConsignRegisterCommand{ListingID: 1, SellerID: "seller", AccountID: "a-seller", GoldItemID: 2681725, PriceCera: 111, DurationHour: 24}); e != nil {
		t.Fatal(e)
	}
	return ctx, r, o, now
}
func goldBuy(id string) GoldConsignBuyCommand {
	return GoldConsignBuyCommand{ListingID: 1, BuyerID: id, AccountID: "a-" + id, GoldItemID: 2681725, ExpectedCera: 111}
}
func goldMailCount(t *testing.T, ctx context.Context, r repo.Group, id string) int {
	t.Helper()
	m, _, e := r.Mailbox.Load(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	return len(m.Mails)
}
func TestGoldConsignAtomicSaleClaimSpendAndNoReplayAfterMailDeletion(t *testing.T) {
	ctx, r, o, now := goldFixture(t)
	result, e := o.Buy(ctx, goldBuy("buyer"))
	if e != nil {
		t.Fatal(e)
	}
	if result.CeraAfter != 889 || result.Listing.ProceedsCera != 105 {
		t.Fatal(result)
	}
	for range 3 {
		copy, e := NewGoldConsignOwner(r)
		if e != nil {
			t.Fatal(e)
		}
		copy.now = func() time.Time { return now }
		if pending, e := copy.Maintain(ctx); e != nil || pending {
			t.Fatal(pending, e)
		}
	}
	if goldMailCount(t, ctx, r, "buyer") != 1 || goldMailCount(t, ctx, r, "seller") != 1 {
		t.Fatal("成交邮件重复或遗漏")
	}
	if _, e = mail.NewOwner(r).Claim(ctx, "buyer", mail.ExtractRequest{MailIDs: []uint32{1}}); e != nil {
		t.Fatal(e)
	}
	buyer, _, _ := r.Character.Load(ctx, "buyer")
	if buyer.Stats["gold"] != 11000000 {
		t.Fatal(buyer.Stats)
	}
	claim, e := mail.NewOwner(r).Claim(ctx, "seller", mail.ExtractRequest{MailIDs: []uint32{1}})
	if e != nil || !claim.WalletChanged {
		t.Fatal(claim, e)
	}
	seller, _, _ := r.Account.Load(ctx, "a-seller")
	if shop.TokenCeraBalance(seller) != 105 {
		t.Fatal(seller)
	}
	checkout, e := shop.NewOwner(r)
	if e != nil {
		t.Fatal(e)
	}
	paid, e := checkout.Checkout(ctx, shop.CheckoutCommand{AccountID: "a-seller", CharacterID: "seller", SettingsScope: "test:containers", Cost: 100, PayPolicy: shop.PayPolicyWaterfall, Project: func(*shop.CheckoutAssets) (shop.CheckoutChanges, error) { return shop.CheckoutChanges{}, nil }})
	if e != nil || paid.CeraAfter != 1000 || paid.WalletAfter.TokenCera != 5 {
		t.Fatal(paid, e)
	}
	if _, e = mail.NewOwner(r).Claim(ctx, "seller", mail.ExtractRequest{MailIDs: []uint32{1}}); e != nil && !errors.Is(e, mail.ErrMailboxAlreadyClaimed) {
		t.Fatal(e)
	}
	if e = r.Mailbox.Save(ctx, repo.MailboxRecord{CharacterID: "seller", Mails: map[string]repo.MailRecord{}}); e != nil {
		t.Fatal(e)
	}
	replay, e := o.Buy(ctx, goldBuy("buyer"))
	if e != nil || !replay.Repeated {
		t.Fatal(replay, e)
	}
	if _, e = o.Maintain(ctx); e != nil {
		t.Fatal(e)
	}
	if goldMailCount(t, ctx, r, "seller") != 0 || goldMailCount(t, ctx, r, "buyer") != 1 {
		t.Fatal("删除邮件后补发了成交资产")
	}
	a, _, _ := r.Account.Load(ctx, "a-buyer")
	if shop.Balance(a) != 889 {
		t.Fatal("重试重复扣款")
	}
	avg, n, e := o.AverageCeraPrice(ctx, 2681725)
	if e != nil || avg != 111 || n != 1 {
		t.Fatal(avg, n, e)
	}
	escrow, _, _ := r.Character.Load(ctx, EscrowCharacterID)
	if escrow.Stats["gold"] != 0 {
		t.Fatal("寄售金币未完成出账")
	}
}
func TestGoldConsignOnlyOneConcurrentBuyerAndValidations(t *testing.T) {
	ctx, r, o, _ := goldFixture(t)
	tests := []struct {
		q    GoldConsignBuyCommand
		want error
	}{{GoldConsignBuyCommand{ListingID: 1, BuyerID: "seller", AccountID: "a-seller", ExpectedCera: 111, GoldItemID: 2681725}, ErrGoldConsignBuyerIsSeller}, {GoldConsignBuyCommand{ListingID: 1, BuyerID: "buyer", AccountID: "a-buyer", ExpectedCera: 112, GoldItemID: 2681725}, ErrPriceMismatch}, {GoldConsignBuyCommand{ListingID: 1, BuyerID: "buyer", AccountID: "a-buyer", ExpectedCera: 111, GoldItemID: 2681726}, ErrPriceMismatch}}
	for _, tt := range tests {
		if _, e := o.Buy(ctx, tt.q); !errors.Is(e, tt.want) {
			t.Fatal(e, tt.want)
		}
	}
	a, _, _ := r.Account.Load(ctx, "a-buyer")
	shop.SetTokenCeraBalance(&a, 100000)
	shop.SetBalance(&a, 0)
	r.Account.Save(ctx, a)
	if _, e := o.Buy(ctx, goldBuy("buyer")); !errors.Is(e, shop.ErrCeraInsufficient) {
		t.Fatal("金币寄售错误地允许代币券付款", e)
	}
	shop.SetBalance(&a, 1000)
	r.Account.Save(ctx, a)
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := []string{}
	for _, id := range []string{"buyer", "other"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			q := goldBuy(id)
			q.GoldItemID = 0
			_, e := o.Buy(ctx, q)
			if e == nil {
				mu.Lock()
				won = append(won, id)
				mu.Unlock()
			} else if !errors.Is(e, ErrListingMissing) {
				t.Error(e)
			}
		}(id)
	}
	wg.Wait()
	if len(won) != 1 {
		t.Fatal(won)
	}
	for _, id := range []string{"buyer", "other"} {
		a, _, _ := r.Account.Load(ctx, "a-"+id)
		want := int64(1000)
		if id == won[0] {
			want = 889
		}
		if shop.Balance(a) != want {
			t.Fatal(id, shop.Balance(a), want)
		}
	}
}

type goldFailUOW struct {
	repo.MailboxAssetUnitOfWork
	tx    repo.AccountTradeMailboxUnitOfWork
	stage string
}

func (u goldFailUOW) WithinAccountTradeMailbox(ctx context.Context, a string, ids []string, m string, fn func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.MailboxRepository) error) error {
	return u.tx.WithinAccountTradeMailbox(ctx, a, ids, m, func(a repo.AccountRepository, c repo.CharacterRepository, i repo.InventoryRepository, m repo.MailboxRepository) error {
		if u.stage == "inventory" {
			i = goldFailInventory{i}
		} else {
			m = goldFailMailbox{m}
		}
		return fn(a, c, i, m)
	})
}

type goldFailInventory struct{ repo.InventoryRepository }

func (i goldFailInventory) Save(context.Context, repo.InventoryRecord) error {
	return errors.New("注入托管保存故障")
}

type goldFailMailbox struct{ repo.MailboxRepository }

func (m goldFailMailbox) Save(context.Context, repo.MailboxRecord) error {
	return errors.New("注入邮件保存故障")
}
func TestGoldConsignBuyFailuresRollBackWalletEscrowHistoryAndMail(t *testing.T) {
	for _, stage := range []string{"inventory", "mail"} {
		t.Run(stage, func(t *testing.T) {
			ctx, r, o, _ := goldFixture(t)
			o.repositories.MailboxAssets = goldFailUOW{r.MailboxAssets, r.MailboxAssets.(repo.AccountTradeMailboxUnitOfWork), stage}
			if _, e := o.Buy(ctx, goldBuy("buyer")); e == nil {
				t.Fatal("未触发故障")
			}
			a, _, _ := r.Account.Load(ctx, "a-buyer")
			if shop.Balance(a) != 1000 || goldMailCount(t, ctx, r, "buyer") != 0 {
				t.Fatal("失败后残留扣款或邮件")
			}
			inv, _, _ := r.Inventory.Load(ctx, EscrowCharacterID)
			if _, yes := inv.Slots[goldConsignKey(goldConsignListingPrefix, 1)]; !yes || len(inv.Slots) != 1 {
				t.Fatal(inv)
			}
			escrow, _, _ := r.Character.Load(ctx, EscrowCharacterID)
			if escrow.Stats["gold"] != 1000000 {
				t.Fatal(escrow.Stats)
			}
		})
	}
}
func TestGoldConsignCancelExpiryAndRestartRefundExactlyOnce(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(strconv.FormatBool(expired), func(t *testing.T) {
			ctx, r, o, now := goldFixture(t)
			if expired {
				o.now = func() time.Time { return now.Add(25 * time.Hour) }
				if _, e := o.Buy(ctx, goldBuy("buyer")); !errors.Is(e, ErrListingExpired) {
					t.Fatal(e)
				}
				if pending, e := o.Maintain(ctx); e != nil || pending {
					t.Fatal(pending, e)
				}
			} else {
				if _, e := o.Cancel(ctx, "seller", "a-seller", 1); e != nil {
					t.Fatal(e)
				}
			}
			restarted, _ := NewGoldConsignOwner(r)
			for range 3 {
				if pending, e := restarted.Maintain(ctx); e != nil || pending {
					t.Fatal(pending, e)
				}
			}
			if goldMailCount(t, ctx, r, "seller") != 1 {
				t.Fatal("退款重复")
			}
			if _, e := mail.NewOwner(r).Claim(ctx, "seller", mail.ExtractRequest{MailIDs: []uint32{1}}); e != nil {
				t.Fatal(e)
			}
			c, _, _ := r.Character.Load(ctx, "seller")
			if c.Stats["gold"] != 10000000 {
				t.Fatal(c.Stats)
			}
		})
	}
}
func TestGoldConsignRecentAverageUsesTimeRatherThanHighestPrices(t *testing.T) {
	slots := map[string]repo.ItemStack{}
	now := time.Unix(1800000000, 0)
	for n := 1; n <= 25; n++ {
		price := int64(100)
		if n <= 5 {
			price = 10000
		}
		l := GoldConsignListing{ID: uint64(n), SellerID: "s", SellerAccountID: "a", BuyerID: "b", GoldItemID: 2681725, PriceCera: price, FeePercent: 5, CreatedAt: now, ExpiresAt: now.Add(time.Hour), SoldAt: now.Add(time.Duration(n) * time.Second)}
		slots[goldConsignKey(goldConsignSoldPrefix, l.ID)] = goldConsignStack(l)
	}
	avg, n := goldConsignAverage(slots, 2681725)
	if avg != 100 || n != 20 {
		t.Fatal(avg, n)
	}
	for _, tc := range []struct {
		p, want int64
		ok      bool
	}{{39, 0, false}, {40, 5, true}, {150, 5, true}, {151, 30, true}, {161, 40, true}, {171, 50, true}, {181, 95, true}} {
		fee, ok := ResolveGoldConsignFee(tc.p, 100, false)
		if fee != tc.want || ok != tc.ok {
			t.Fatal(tc, fee, ok)
		}
	}
}
