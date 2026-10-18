package auction

import (
	"context"
	"errors"
	shop "longheng.io/server/internal/modules/dnf/cerashop"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	goldConsignListingPrefix     = "goldconsign:listing:"
	goldConsignSoldPrefix        = "goldconsign:sold:"
	goldConsignPayoutPrefix      = "goldconsign:payout:"
	GoldConsignSellerLevel       = 30
	GoldConsignBuyerLevel        = 20
	GoldConsignMaxActiveListings = 10
	GoldConsignMaxVisibleRows    = 100
	GoldConsignMinDurationHours  = 24
	GoldConsignMaxDurationHours  = 48
	GoldConsignBaseFeePercent    = 5
	GoldConsignCouponFeePercent  = 1
)

var (
	ErrGoldConsignPriceRejected = errors.New("金币寄售价格低于允许范围")
	ErrGoldConsignSellerLevel   = errors.New("金币寄售卖家等级不足")
	ErrGoldConsignBuyerLevel    = errors.New("金币寄售买家等级不足")
	ErrGoldConsignNotTier       = errors.New("不是现用客户端的金币寄售档位")
	ErrGoldConsignBuyerIsSeller = errors.New("不能购买同账号寄售的金币")
	ErrGoldConsignNotActive     = errors.New("金币寄售单已不可交易")
	ErrGoldConsignLimit         = errors.New("金币寄售数量达到上限")
)

type GoldConsignTier struct{ ItemID, Amount int64 }

var goldConsignTiers = []GoldConsignTier{
	{ItemID: 2681725, Amount: 1_000_000},
	{ItemID: 2681726, Amount: 2_000_000},
	{ItemID: 2681727, Amount: 3_000_000},
	{ItemID: 2681728, Amount: 4_000_000},
	{ItemID: 2681729, Amount: 5_000_000},
	{ItemID: 2681730, Amount: 6_000_000},
	{ItemID: 2681731, Amount: 7_000_000},
	{ItemID: 2681732, Amount: 8_000_000},
	{ItemID: 2681733, Amount: 9_000_000},
	{ItemID: 2681734, Amount: 10_000_000},
	{ItemID: 2681735, Amount: 20_000_000},
	{ItemID: 2681736, Amount: 30_000_000},
	{ItemID: 2683069, Amount: 40_000_000},
	{ItemID: 2683070, Amount: 50_000_000},
	{ItemID: 2683071, Amount: 100_000_000},
	{ItemID: 2683072, Amount: 150_000_000},
	{ItemID: 2683073, Amount: 200_000_000},
}

func GoldConsignTiers() []GoldConsignTier {
	out := make([]GoldConsignTier, len(goldConsignTiers))
	copy(out, goldConsignTiers)
	return out
}

func GoldConsignTierAmount(itemID int64) (int64, bool) {
	for _, tier := range goldConsignTiers {
		if tier.ItemID == itemID {
			return tier.Amount, true
		}
	}
	return 0, false
}

func IsGoldConsignTier(itemID int64) bool {
	_, ok := GoldConsignTierAmount(itemID)
	return ok
}

func ResolveGoldConsignFee(price, average int64, hasAuctionCoupon bool) (feePercent int64, listable bool) {
	if price <= 0 {
		return 0, false
	}
	if average <= 0 {
		return goldConsignBaseFee(hasAuctionCoupon), true
	}
	// Compare with integer maths throughout: price*100 against average*threshold
	// avoids both floating point and an overflow on the 2亿 tier.
	switch {
	case price*100 < average*40:
		return 0, false
	case price*100 <= average*150:
		return goldConsignBaseFee(hasAuctionCoupon), true
	case price*100 <= average*160:
		return 30, true
	case price*100 <= average*170:
		return 40, true
	case price*100 <= average*180:
		return 50, true
	default:
		return 95, true
	}
}

func goldConsignBaseFee(hasAuctionCoupon bool) int64 {
	if hasAuctionCoupon {
		return GoldConsignCouponFeePercent
	}
	return GoldConsignBaseFeePercent
}

func GoldConsignProceeds(priceCera, feePercent int64) int64 {
	if priceCera <= 0 {
		return 0
	}
	if feePercent <= 0 {
		return priceCera
	}
	if feePercent >= 100 {
		return 0
	}
	return priceCera * (100 - feePercent) / 100
}

func GoldConsignFee(priceCera, feePercent int64) int64 {
	proceeds := GoldConsignProceeds(priceCera, feePercent)
	if priceCera <= 0 {
		return 0
	}
	return priceCera - proceeds
}

type GoldConsignListing struct {
	ID                                                          uint64
	SellerID, SellerName, SellerAccountID, BuyerID              string
	GoldItemID, GoldAmount, PriceCera, FeePercent, ProceedsCera int64
	CreatedAt, ExpiresAt, SoldAt                                time.Time
}
type GoldConsignRegisterCommand struct {
	ListingID                           uint64
	SellerID, AccountID, SellerName     string
	GoldItemID, PriceCera, DurationHour int64
	HasCoupon                           bool
}
type GoldConsignBuyCommand struct {
	ListingID                uint64
	BuyerID, AccountID       string
	ExpectedCera, GoldItemID int64
}
type GoldConsignResult struct {
	Listing              GoldConsignListing
	GoldAfter, CeraAfter int64
	Repeated             bool
}
type GoldConsignOwner struct {
	owner        *Owner
	repositories dnfrepo.Group
	now          func() time.Time
}

func NewGoldConsignOwner(r dnfrepo.Group) (*GoldConsignOwner, error) {
	o, err := NewOwner(r)
	if err != nil {
		return nil, err
	}
	if _, ok := r.MailboxAssets.(dnfrepo.AccountTradeMailboxUnitOfWork); !ok {
		return nil, ErrUnavailable
	}
	return &GoldConsignOwner{owner: o, repositories: r}, nil
}
func (o *GoldConsignOwner) clock() time.Time {
	if o.now != nil {
		return o.now().UTC()
	}
	return time.Now().UTC()
}
func (o *GoldConsignOwner) EnsureEscrow(ctx context.Context) error { return o.owner.EnsureEscrow(ctx) }
func goldConsignKey(prefix string, id uint64) string               { return prefix + strconv.FormatUint(id, 10) }

func goldConsignStack(l GoldConsignListing) dnfrepo.ItemStack {
	return dnfrepo.ItemStack{ItemID: l.GoldItemID, Count: 1, Extra: map[string]string{
		"listing_id": strconv.FormatUint(l.ID, 10), "seller_id": l.SellerID, "seller_name": l.SellerName, "seller_account_id": l.SellerAccountID, "buyer_id": l.BuyerID,
		"price_cera": strconv.FormatInt(l.PriceCera, 10), "fee_percent": strconv.FormatInt(l.FeePercent, 10),
		"created_at": strconv.FormatInt(l.CreatedAt.Unix(), 10), "expires_at": strconv.FormatInt(l.ExpiresAt.Unix(), 10), "sold_at": strconv.FormatInt(l.SoldAt.UnixNano(), 10),
	}}
}
func goldConsignListingFromStack(stack dnfrepo.ItemStack) (GoldConsignListing, error) {
	l := GoldConsignListing{GoldItemID: stack.ItemID, SellerID: strings.TrimSpace(stack.Extra["seller_id"]), SellerName: stack.Extra["seller_name"], SellerAccountID: strings.TrimSpace(stack.Extra["seller_account_id"]), BuyerID: stack.Extra["buyer_id"]}
	amount, ok := GoldConsignTierAmount(l.GoldItemID)
	if !ok || stack.Count != 1 || l.SellerID == "" || l.SellerAccountID == "" {
		return l, ErrListingMissing
	}
	l.GoldAmount = amount
	id, e := strconv.ParseUint(stack.Extra["listing_id"], 10, 64)
	if e != nil || id == 0 {
		return l, ErrListingMissing
	}
	l.ID = id
	p, e := strconv.ParseInt(stack.Extra["price_cera"], 10, 64)
	if e != nil || p <= 0 || p > math.MaxInt32 {
		return l, ErrListingMissing
	}
	l.PriceCera = p
	fee, e := strconv.ParseInt(stack.Extra["fee_percent"], 10, 64)
	if e != nil || fee < 0 || fee >= 100 {
		return l, ErrListingMissing
	}
	l.FeePercent = fee
	l.ProceedsCera = GoldConsignProceeds(p, fee)
	if l.ProceedsCera <= 0 {
		return l, ErrListingMissing
	}
	created, e := strconv.ParseInt(stack.Extra["created_at"], 10, 64)
	if e != nil || created <= 0 {
		return l, ErrListingMissing
	}
	expires, e := strconv.ParseInt(stack.Extra["expires_at"], 10, 64)
	if e != nil || expires <= created {
		return l, ErrListingMissing
	}
	l.CreatedAt = time.Unix(created, 0).UTC()
	l.ExpiresAt = time.Unix(expires, 0).UTC()
	if l.BuyerID != "" {
		sold, e := strconv.ParseInt(stack.Extra["sold_at"], 10, 64)
		if e != nil || sold <= 0 {
			return l, ErrListingMissing
		}
		l.SoldAt = time.Unix(0, sold).UTC()
	}
	return l, nil
}
func goldConsignAverage(slots map[string]dnfrepo.ItemStack, itemID int64) (int64, int64) {
	sales := []GoldConsignListing{}
	for k, s := range slots {
		if !strings.HasPrefix(k, goldConsignSoldPrefix) || s.ItemID != itemID {
			continue
		}
		l, e := goldConsignListingFromStack(s)
		if e == nil && !l.SoldAt.IsZero() {
			sales = append(sales, l)
		}
	}
	sort.Slice(sales, func(i, j int) bool {
		if sales[i].SoldAt.Equal(sales[j].SoldAt) {
			return sales[i].ID > sales[j].ID
		}
		return sales[i].SoldAt.After(sales[j].SoldAt)
	})
	if len(sales) > 20 {
		sales = sales[:20]
	}
	var sum int64
	for _, l := range sales {
		sum += l.PriceCera
	}
	if len(sales) == 0 {
		return 0, 0
	}
	return sum / int64(len(sales)), int64(len(sales))
}
func (o *GoldConsignOwner) AverageCeraPrice(ctx context.Context, itemID int64) (int64, int64, error) {
	if !IsGoldConsignTier(itemID) {
		return 0, 0, ErrGoldConsignNotTier
	}
	inv, found, e := o.repositories.Inventory.Load(ctx, EscrowCharacterID)
	if e != nil || !found {
		return 0, 0, e
	}
	avg, n := goldConsignAverage(inv.Slots, itemID)
	return avg, n, nil
}
func (o *GoldConsignOwner) Register(ctx context.Context, q GoldConsignRegisterCommand) (GoldConsignResult, error) {
	amount, ok := GoldConsignTierAmount(q.GoldItemID)
	if !ok {
		return GoldConsignResult{}, ErrGoldConsignNotTier
	}
	if q.ListingID == 0 || q.SellerID == "" || q.SellerID == EscrowCharacterID || q.AccountID == "" || q.PriceCera <= 0 || q.PriceCera > math.MaxInt32 || (q.DurationHour != 24 && q.DurationHour != 48) {
		return GoldConsignResult{}, ErrInvalidRequest
	}
	if e := o.EnsureEscrow(ctx); e != nil {
		return GoldConsignResult{}, e
	}
	var result GoldConsignResult
	err := o.repositories.CharacterTrade.WithinCharacterTrade(ctx, q.SellerID, EscrowCharacterID, func(c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository) error {
		seller, found, e := c.Load(ctx, q.SellerID)
		if e != nil {
			return e
		}
		if !found || seller.AccountID != q.AccountID || seller.Stats["delete_flag"] != 0 {
			return ErrNotOwner
		}
		if seller.Level < GoldConsignSellerLevel {
			return ErrGoldConsignSellerLevel
		}
		escrow, found, e := c.Load(ctx, EscrowCharacterID)
		if e != nil || !found {
			return errors.Join(ErrUnavailable, e)
		}
		inv, found, e := i.Load(ctx, EscrowCharacterID)
		if e != nil || !found {
			return errors.Join(ErrUnavailable, e)
		}
		key := goldConsignKey(goldConsignListingPrefix, q.ListingID)
		if _, yes := inv.Slots[key]; yes {
			return ErrListingIDExists
		}
		if _, yes := inv.Slots[goldConsignKey(goldConsignSoldPrefix, q.ListingID)]; yes {
			return ErrListingIDExists
		}
		active := 0
		for k, s := range inv.Slots {
			if strings.HasPrefix(k, goldConsignListingPrefix) && s.Extra["seller_id"] == q.SellerID {
				active++
			}
		}
		if active >= GoldConsignMaxActiveListings {
			return ErrGoldConsignLimit
		}
		avg, _ := goldConsignAverage(inv.Slots, q.GoldItemID)
		fee, allowed := ResolveGoldConsignFee(q.PriceCera, avg, q.HasCoupon)
		if !allowed {
			return ErrGoldConsignPriceRejected
		}
		proceeds := GoldConsignProceeds(q.PriceCera, fee)
		if proceeds <= 0 {
			return ErrInvalidRequest
		}
		if seller.Stats["gold"] < amount {
			return ErrGoldUnavailable
		}
		if escrow.Stats == nil || escrow.Stats["gold"] < 0 || escrow.Stats["gold"] > math.MaxInt64-amount {
			return ErrInvalidRequest
		}
		seller.Stats["gold"] -= amount
		escrow.Stats["gold"] += amount
		now := o.clock()
		seller.UpdatedAt = now
		escrow.UpdatedAt = now
		l := GoldConsignListing{ID: q.ListingID, SellerID: q.SellerID, SellerName: seller.Name, SellerAccountID: q.AccountID, GoldItemID: q.GoldItemID, GoldAmount: amount, PriceCera: q.PriceCera, FeePercent: fee, ProceedsCera: proceeds, CreatedAt: now, ExpiresAt: now.Add(time.Duration(q.DurationHour) * time.Hour)}
		if inv.Slots == nil {
			inv.Slots = map[string]dnfrepo.ItemStack{}
		}
		inv.Slots[key] = goldConsignStack(l)
		inv.UpdatedAt = now
		if e = c.Save(ctx, seller); e != nil {
			return e
		}
		if e = c.Save(ctx, escrow); e != nil {
			return e
		}
		if e = i.Save(ctx, inv); e != nil {
			return e
		}
		result = GoldConsignResult{Listing: l, GoldAfter: seller.Stats["gold"]}
		return nil
	})
	if err != nil {
		return GoldConsignResult{}, err
	}
	return result, nil
}
func (o *GoldConsignOwner) Search(ctx context.Context, ids []int64) ([]GoldConsignListing, error) {
	inv, found, e := o.repositories.Inventory.Load(ctx, EscrowCharacterID)
	if e != nil || !found {
		return nil, e
	}
	out := []GoldConsignListing{}
	now := o.clock()
	for key, s := range inv.Slots {
		if !strings.HasPrefix(key, goldConsignListingPrefix) {
			continue
		}
		l, e := goldConsignListingFromStack(s)
		if e != nil || !l.ExpiresAt.After(now) {
			continue
		}
		keep := len(ids) == 0
		for _, id := range ids {
			keep = keep || id == l.GoldItemID
		}
		if keep {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PriceCera == out[j].PriceCera {
			return out[i].ID < out[j].ID
		}
		return out[i].PriceCera < out[j].PriceCera
	})
	return out, nil
}
func (o *GoldConsignOwner) ListOwned(ctx context.Context, sellerID string) ([]GoldConsignListing, error) {
	all, e := o.Search(ctx, nil)
	out := []GoldConsignListing{}
	for _, l := range all {
		if l.SellerID == sellerID {
			out = append(out, l)
		}
	}
	return out, e
}
func (o *GoldConsignOwner) Buy(ctx context.Context, q GoldConsignBuyCommand) (GoldConsignResult, error) {
	if q.ListingID == 0 || q.BuyerID == "" || q.BuyerID == EscrowCharacterID || q.AccountID == "" || q.ExpectedCera <= 0 || (q.GoldItemID != 0 && !IsGoldConsignTier(q.GoldItemID)) {
		return GoldConsignResult{}, ErrInvalidRequest
	}
	tx, ok := o.repositories.MailboxAssets.(dnfrepo.AccountTradeMailboxUnitOfWork)
	if !ok {
		return GoldConsignResult{}, ErrUnavailable
	}
	var result GoldConsignResult
	err := tx.WithinAccountTradeMailbox(ctx, q.AccountID, []string{q.BuyerID, EscrowCharacterID}, q.BuyerID, func(a dnfrepo.AccountRepository, c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, m dnfrepo.MailboxRepository) error {
		buyer, found, e := c.Load(ctx, q.BuyerID)
		if e != nil {
			return e
		}
		if !found || buyer.AccountID != q.AccountID || buyer.Stats["delete_flag"] != 0 {
			return ErrNotOwner
		}
		if buyer.Level < GoldConsignBuyerLevel {
			return ErrGoldConsignBuyerLevel
		}
		escrow, found, e := c.Load(ctx, EscrowCharacterID)
		if e != nil || !found {
			return errors.Join(ErrUnavailable, e)
		}
		inv, found, e := i.Load(ctx, EscrowCharacterID)
		if e != nil || !found {
			return errors.Join(ErrUnavailable, e)
		}
		key := goldConsignKey(goldConsignListingPrefix, q.ListingID)
		stack, found := inv.Slots[key]
		if !found {
			sold, yes := inv.Slots[goldConsignKey(goldConsignSoldPrefix, q.ListingID)]
			l, e := goldConsignListingFromStack(sold)
			if yes && e == nil && l.BuyerID == q.BuyerID && (q.GoldItemID == 0 || l.GoldItemID == q.GoldItemID) && l.PriceCera == q.ExpectedCera {
				result = GoldConsignResult{Listing: l, Repeated: true}
				return nil
			}
			return ErrListingMissing
		}
		l, e := goldConsignListingFromStack(stack)
		if e != nil || l.ID != q.ListingID {
			return ErrListingMissing
		}
		if l.SellerAccountID == q.AccountID {
			return ErrGoldConsignBuyerIsSeller
		}
		if !l.ExpiresAt.After(o.clock()) {
			return ErrListingExpired
		}
		if l.PriceCera != q.ExpectedCera || (q.GoldItemID != 0 && l.GoldItemID != q.GoldItemID) {
			return ErrPriceMismatch
		}
		account, found, e := a.Load(ctx, q.AccountID)
		if e != nil {
			return e
		}
		if !found {
			return ErrNotOwner
		}
		balance := shop.Balance(account)
		if balance < l.PriceCera {
			return shop.ErrCeraInsufficient
		}
		if escrow.Stats["gold"] < l.GoldAmount {
			return ErrGoldUnavailable
		}
		now := o.clock()
		shop.SetBalance(&account, balance-l.PriceCera)
		account.UpdatedAt = now
		escrow.Stats["gold"] -= l.GoldAmount
		escrow.UpdatedAt = now
		l.BuyerID = q.BuyerID
		l.SoldAt = now
		delete(inv.Slots, key)
		inv.Slots[goldConsignKey(goldConsignSoldPrefix, l.ID)] = goldConsignStack(l)
		inv.Slots[goldConsignKey(goldConsignPayoutPrefix, l.ID)] = goldConsignStack(l)
		inv.UpdatedAt = now
		_, e = dnfrepo.AppendSystemMail(ctx, m, dnfrepo.SystemMailDelivery{RecipientCharacterID: q.BuyerID, RecipientName: buyer.Name, Source: "金币寄售", Title: "金币寄售购买成功", Body: "您购买的金币已通过邮件送达。", Gold: l.GoldAmount, CompletionKey: goldConsignKey("goldconsign:buyer:", l.ID), CreatedAt: now})
		if e != nil {
			return e
		}
		if e = a.Save(ctx, account); e != nil {
			return e
		}
		if e = c.Save(ctx, escrow); e != nil {
			return e
		}
		if e = i.Save(ctx, inv); e != nil {
			return e
		}
		result = GoldConsignResult{Listing: l, CeraAfter: balance - l.PriceCera}
		return nil
	})
	if err != nil {
		return GoldConsignResult{}, err
	}
	return result, nil
}

func (o *GoldConsignOwner) Cancel(ctx context.Context, sellerID, accountID string, id uint64) (GoldConsignResult, error) {
	return o.returnGold(ctx, sellerID, accountID, id, false)
}
func (o *GoldConsignOwner) returnGold(ctx context.Context, sellerID, accountID string, id uint64, expiredOnly bool) (GoldConsignResult, error) {
	tx, ok := o.repositories.MailboxAssets.(dnfrepo.AccountTradeMailboxUnitOfWork)
	if !ok || id == 0 || sellerID == "" || sellerID == EscrowCharacterID || accountID == "" {
		return GoldConsignResult{}, ErrInvalidRequest
	}
	var result GoldConsignResult
	err := tx.WithinAccountTradeMailbox(ctx, accountID, []string{sellerID, EscrowCharacterID}, sellerID, func(_ dnfrepo.AccountRepository, c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, m dnfrepo.MailboxRepository) error {
		seller, found, e := c.Load(ctx, sellerID)
		if e != nil {
			return e
		}
		if !found || seller.AccountID != accountID || seller.Stats["delete_flag"] != 0 {
			return ErrNotOwner
		}
		escrow, found, e := c.Load(ctx, EscrowCharacterID)
		if e != nil || !found {
			return errors.Join(ErrUnavailable, e)
		}
		inv, found, e := i.Load(ctx, EscrowCharacterID)
		if e != nil || !found {
			return errors.Join(ErrUnavailable, e)
		}
		key := goldConsignKey(goldConsignListingPrefix, id)
		s, found := inv.Slots[key]
		if !found {
			return ErrListingMissing
		}
		l, e := goldConsignListingFromStack(s)
		if e != nil || l.ID != id {
			return ErrListingMissing
		}
		if l.SellerID != sellerID || l.SellerAccountID != accountID {
			return ErrNotOwner
		}
		now := o.clock()
		if expiredOnly && l.ExpiresAt.After(now) {
			return ErrGoldConsignNotActive
		}
		if escrow.Stats["gold"] < l.GoldAmount {
			return ErrGoldUnavailable
		}
		title := "金币寄售撤销返还"
		if expiredOnly {
			title = "金币寄售到期返还"
		}
		_, e = dnfrepo.AppendSystemMail(ctx, m, dnfrepo.SystemMailDelivery{RecipientCharacterID: sellerID, RecipientName: seller.Name, Source: "金币寄售", Title: title, Body: "未成交的寄售金币已全额返还。", Gold: l.GoldAmount, CompletionKey: goldConsignKey("goldconsign:return:", id), CreatedAt: now})
		if e != nil {
			return e
		}
		escrow.Stats["gold"] -= l.GoldAmount
		escrow.UpdatedAt = now
		delete(inv.Slots, key)
		inv.UpdatedAt = now
		if e = c.Save(ctx, escrow); e != nil {
			return e
		}
		if e = i.Save(ctx, inv); e != nil {
			return e
		}
		result = GoldConsignResult{Listing: l, GoldAfter: seller.Stats["gold"]}
		return nil
	})
	if err != nil {
		return GoldConsignResult{}, err
	}
	return result, nil
}

// Maintain 只处理持久活动单和待付款邮件。成交历史不会触发空轮询。
func (o *GoldConsignOwner) Maintain(ctx context.Context) (bool, error) {
	inv, found, e := o.repositories.Inventory.Load(ctx, EscrowCharacterID)
	if e != nil || !found {
		return false, e
	}
	var failures []error
	for key, s := range inv.Slots {
		if !strings.HasPrefix(key, goldConsignListingPrefix) && !strings.HasPrefix(key, goldConsignPayoutPrefix) {
			continue
		}
		l, e := goldConsignListingFromStack(s)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if strings.HasPrefix(key, goldConsignListingPrefix) {
			if !l.ExpiresAt.After(o.clock()) {
				_, e = o.returnGold(ctx, l.SellerID, l.SellerAccountID, l.ID, true)
			}
		} else {
			e = o.deliverSeller(ctx, key, l)
		}
		if e != nil && !errors.Is(e, ErrListingMissing) {
			failures = append(failures, e)
		}
	}
	inv, _, e = o.repositories.Inventory.Load(ctx, EscrowCharacterID)
	pending := false
	for key := range inv.Slots {
		pending = pending || strings.HasPrefix(key, goldConsignListingPrefix) || strings.HasPrefix(key, goldConsignPayoutPrefix)
	}
	return pending, errors.Join(append(failures, e)...)
}
func (o *GoldConsignOwner) deliverSeller(ctx context.Context, key string, l GoldConsignListing) error {
	tx, ok := o.repositories.MailboxAssets.(dnfrepo.AccountTradeMailboxUnitOfWork)
	if !ok {
		return ErrUnavailable
	}
	return tx.WithinAccountTradeMailbox(ctx, l.SellerAccountID, []string{EscrowCharacterID, l.SellerID}, l.SellerID, func(_ dnfrepo.AccountRepository, c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, m dnfrepo.MailboxRepository) error {
		seller, found, e := c.Load(ctx, l.SellerID)
		if e != nil {
			return e
		}
		if !found || seller.AccountID != l.SellerAccountID || seller.Stats["delete_flag"] != 0 {
			return ErrNotOwner
		}
		inv, found, e := i.Load(ctx, EscrowCharacterID)
		if e != nil || !found {
			return errors.Join(ErrUnavailable, e)
		}
		stack, pending := inv.Slots[key]
		if !pending {
			return nil
		}
		current, e := goldConsignListingFromStack(stack)
		if e != nil || current.ID != l.ID || current.SellerID != l.SellerID || current.ProceedsCera != l.ProceedsCera {
			return ErrInvalidRequest
		}
		_, e = dnfrepo.AppendSystemMail(ctx, m, dnfrepo.SystemMailDelivery{RecipientCharacterID: l.SellerID, RecipientName: seller.Name, Source: "金币寄售", Title: "金币寄售成交款", Body: "寄售金币已售出，扣除手续费后的代币券请领取入账。", Attachments: []dnfrepo.MailAttachment{{ItemID: shop.TokenCeraVoucherItemID, Count: l.ProceedsCera}}, CompletionKey: key, CreatedAt: o.clock()})
		if e != nil {
			return e
		}
		delete(inv.Slots, key)
		inv.UpdatedAt = o.clock()
		return i.Save(ctx, inv)
	})
}
