package auction

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	EscrowCharacterID = "system:auction:escrow"
	EscrowAccountID   = "system:auction"
	listingPrefix     = "auction:listing:"
	payoutPrefix      = "auction:payout:"
	deliveryPrefix    = "auction:delivery:"
)

// Auction charges, following 86JP's DefaultAuctionListingPolicy: a flat
// refundable deposit taken when the listing is registered, plus a commission
// on the sale price. The 拍卖行优惠券 (premium type 29) advertises
// "减少使用拍卖行所需的手续费" in its own PVF explain text without naming a
// number, so the discounted rate is an operator setting rather than a decoded
// constant. The deposit is not discounted: it is returned in full on a cancel,
// on expiry, and with the proceeds once a listing sells out.
const (
	ListingDepositGold       int64 = 10000
	SaleFeePercent           int64 = 5
	DiscountedSaleFeePercent int64 = 3
)

// SaleFeePercentFor picks the commission a registration freezes into its
// listing. Freezing it beats reading the seller's coupon when the sale
// settles: the buyer's transaction never has to reach across into the
// seller's account, and a coupon expiring mid-listing cannot silently change
// terms the seller already accepted.
func SaleFeePercentFor(hasAuctionCoupon bool) int64 {
	if hasAuctionCoupon {
		return DiscountedSaleFeePercent
	}
	return SaleFeePercent
}

var (
	ErrUnavailable     = errors.New("auction repository is unavailable")
	ErrInvalidRequest  = errors.New("auction request is invalid")
	ErrListingMissing  = errors.New("auction listing is missing")
	ErrNotOwner        = errors.New("auction listing owner mismatch")
	ErrItemUnavailable = errors.New("auction item is unavailable")
	ErrInventoryFull   = errors.New("auction destination inventory is full")
	ErrGoldUnavailable = errors.New("auction gold is unavailable")
	ErrListingExpired  = errors.New("auction listing is expired")
	ErrListingIDExists = errors.New("auction listing id already exists")
	ErrListingLimit    = errors.New("auction listing limit reached")
	// ErrPriceMismatch is a payment that is neither the buyout total nor a
	// sub-buyout bid - an overpayment, or a per-piece purchase whose money
	// field does not match the count it asked for.
	ErrPriceMismatch = errors.New("auction payment does not match the listing price")
	// ErrBuyerIsSeller is the seller trying to buy or bid on their own
	// consignment. 86JP separates it (AuctionApplicationError.BuyerIsSeller)
	// because the client has its own text for it.
	ErrBuyerIsSeller = errors.New("auction buyer is the seller")
	// ErrBiddingNotAllowed is a bid on a listing the seller registered
	// buyout-only: with no opening bid there is nothing to bid against.
	ErrBiddingNotAllowed = errors.New("auction listing does not accept bids")
	// ErrBidTooLow is a bid that does not beat what the listing already holds:
	// at or below the standing bid, below the opening bid when there is none
	// yet, or at/above the buyout - that last one has to go through the buyout
	// instead, exactly as 86JP's AuctionBidService.TryBid refuses it.
	ErrBidTooLow = errors.New("auction bid does not beat the standing bid")
)

type Listing struct {
	ID         uint64
	SellerID   string
	SellerName string
	Item       dnfrepo.ItemStack
	ListType   byte
	SourceSlot int16
	UnitPrice  int64
	// StartPrice is the seller's per-item opening bid, or -1 for a listing
	// registered buyout-only. The client sends it in the op183 registration and
	// renders it as 当前竞拍价; dropping it made that control read back the
	// buyout instead. Every capture so far carries -1, so -1 is also what a row
	// written before this field existed decodes to.
	StartPrice int64
	// BuyoutPrice is the seller's per-item 一口价, or -1 when the listing was
	// registered with an opening bid only. The client treats both price
	// controls as independently optional and refuses just the both-empty case,
	// so a listing can legitimately have no buyout; UnitPrice then holds the
	// opening bid, which is the price the listing actually sells at. A row
	// escrowed before this field existed always had a buyout, so a missing key
	// decodes to UnitPrice.
	BuyoutPrice int64
	// Deposit is the refundable gold this registration escrowed, and
	// FeePercent the sale commission it froze. Both are zero on a row written
	// before auction charges existed, which is exactly right: those listings
	// were free to register and must stay free to settle.
	Deposit    int64
	FeePercent int64
	// BidTotal is the standing top bid for the whole listing in gold, 0 when
	// nobody has bid yet, and BidderID/BidderAccountID who placed it. The gold
	// is already out of the bidder's wallet and sitting in the escrow wallet,
	// so every path that ends this listing has to either pay it to the seller
	// (the bidder won) or refund it (outbid, bought out from under, cancelled,
	// or the listing was pulled).
	BidTotal        int64
	BidderID        string
	BidderAccountID string
	BidAt           time.Time
	CreatedAt       time.Time
	ExpiresAt       time.Time
}

// HasBidding reports whether the seller offered this listing for bidding at
// all. 86JP gates its whole bid path on the same term (`StartBidPrice > 0`):
// a buyout-only registration has no opening bid, so there is nothing to bid
// against and the buyout is the only way to take it.
func (l Listing) HasBidding() bool {
	return l.StartPrice > 0
}

// OpeningBidTotal is the least a first bid may be, and BuyoutTotal what the
// buyout costs for the whole listing (0 when there is none). Both prices are
// stored per item while every wire amount is a total, so the multiplication
// belongs here rather than at each caller.
func (l Listing) OpeningBidTotal() int64 {
	return listingTotal(l.BidUnitPrice(), l.Item.Count)
}

func (l Listing) BuyoutTotal() int64 {
	if !l.HasBuyout() {
		return 0
	}
	return listingTotal(l.BuyoutUnitPrice(), l.Item.Count)
}

// DisplayBidTotal is what the client's 当前竞拍价 slot shows: the standing bid
// once there is one, otherwise the seller's opening bid. Same term as 86JP's
// AuctionListingRecord.DisplayBidPrice.
func (l Listing) DisplayBidTotal() int64 {
	if l.BidTotal > 0 {
		return l.BidTotal
	}
	return l.OpeningBidTotal()
}

func listingTotal(unitPrice, count int64) int64 {
	if unitPrice <= 0 || count <= 0 {
		return 0
	}
	if unitPrice > math.MaxInt64/count {
		return math.MaxInt64
	}
	return unitPrice * count
}

// SaleFee is the commission this listing owes on a given sale price, and
// SellerProceeds what is left for the seller once it is taken. The deposit
// comes back only when the listing is fully consumed, matching 86JP.
func (l Listing) SaleFee(salePrice int64) int64 {
	if salePrice <= 0 || l.FeePercent <= 0 {
		return 0
	}
	if salePrice > math.MaxInt64/l.FeePercent {
		// Only reachable on a price no wallet can hold; keep the rounding
		// direction rather than overflowing.
		return salePrice / 100 * l.FeePercent
	}
	return salePrice * l.FeePercent / 100
}

func (l Listing) SellerProceeds(salePrice int64, listingCompleted bool) int64 {
	proceeds := salePrice - l.SaleFee(salePrice)
	if listingCompleted {
		proceeds += l.Deposit
	}
	return proceeds
}

// SaleStatement is the money breakdown the seller's 成交 letter prints, frozen
// at the moment the sale settles:
//
//	[已售出 金刚石 1个。]
//	出售价 : + 10000 金币
//	保证金 : + 10000 金币
//	手续费 : - 500 金币
//	已售出 金刚石 并获得 19500 金币。
//
// It is stored on the payout row rather than recomputed at mail time: the
// listing row is gone once the last piece sells, and the letter has to read the
// same on the first attempt and on a deferred retry.
type SaleStatement struct {
	// GrossGold is what the item sold for, before the commission.
	GrossGold int64
	// FeeGold is the commission taken out of it.
	FeeGold int64
	// DepositGold is the registration deposit that came home with the proceeds,
	// and is zero on a partial sale - the deposit only returns once the whole
	// listing is gone.
	DepositGold int64
	// NetGold is what the letter actually pays: gross - fee + deposit.
	NetGold int64
}

// SaleStatementFor freezes the breakdown for one settlement.
func (l Listing) SaleStatementFor(salePrice int64, listingCompleted bool) SaleStatement {
	deposit := int64(0)
	if listingCompleted {
		deposit = l.Deposit
	}
	return SaleStatement{
		GrossGold:   salePrice,
		FeeGold:     l.SaleFee(salePrice),
		DepositGold: deposit,
		NetGold:     l.SellerProceeds(salePrice, listingCompleted),
	}
}

// HasBuyout reports whether the seller set a 一口价. Only -1 means there is
// none: the struct's zero value is what every construction site written before
// the buyout became optional leaves behind, and those listings all had one.
func (l Listing) HasBuyout() bool {
	return l.BuyoutPrice != -1
}

// BuyoutUnitPrice is the per-item 一口价, or 0 when the listing has none.
func (l Listing) BuyoutUnitPrice() int64 {
	if !l.HasBuyout() {
		return 0
	}
	if l.BuyoutPrice > 0 {
		return l.BuyoutPrice
	}
	return l.UnitPrice
}

// BidUnitPrice is the per-item price the client shows as the current bid. With
// no bid ledger on this server it is the seller's opening bid, and the buyout
// for a listing registered buyout-only. There is deliberately no top-bid term:
// Buy refuses every payment below the buyout total, so no bid is ever recorded
// and a top-bid field would only ever hold zero.
//
// Any StartPrice at or below zero means buyout-only, not "an opening bid of
// zero". That covers the -1 the client sends, and it keeps the struct's zero
// value - what every caller written before StartPrice existed leaves behind -
// from quietly turning into a free item on the current-bid control.
func (l Listing) BidUnitPrice() int64 {
	if l.StartPrice > 0 {
		return l.StartPrice
	}
	return l.UnitPrice
}

type RegisterCommand struct {
	ListingID  uint64
	SellerID   string
	AccountID  string
	SellerName string
	ListType   byte
	SourceSlot int16
	ExpectedID int64
	Count      int64
	UnitPrice  int64
	// SourceAccountOwned marks a listing whose stack lives in the account-scoped
	// crystal/soul warehouse. The current client addresses those entries as
	// ordinary list-0 slots 354..365 but marks the container as 36 on op183, and
	// the durable owner of that range is the account, not the character, so the
	// debit has to run against the account inventory. Everything after the
	// debit is unchanged: the consignment rides the same escrow row, and both
	// the buyer's delivery mail and the seller's cancel/expiry mail already
	// route crystal and soul attachments back into the account warehouse
	// (mailboxAttachmentUsesAccountInventory).
	SourceAccountOwned bool
	// StartPrice is the per-item opening bid, or -1 for buyout-only. Anything
	// else must fall in [0, UnitPrice]: the client's own op183 encoder cannot
	// produce an opening bid above the buyout.
	StartPrice int64
	// BuyoutPrice is the per-item 一口价, or -1 for a listing registered with
	// an opening bid and no buyout. Zero means "unset" and is read as a buyout
	// equal to UnitPrice, which is what every caller written before the buyout
	// became optional intends.
	BuyoutPrice int64
	// Deposit is the refundable gold to take from the seller now, and
	// FeePercent the commission to freeze into the listing. Both default to
	// zero so a caller that predates auction charges registers a free listing
	// rather than silently taking gold.
	Deposit      int64
	FeePercent   int64
	Duration     time.Duration
	ListingLimit int
	// ExpectedBind freezes the source row's seal/bind byte between the bridge
	// preview and the transactional escrow move. AllowSealed is set only for
	// PVF equipment whose rarity requires a sealed row; ordinary stackables and
	// unsealed common/uncommon equipment keep both values false.
	ExpectedBind bool
	AllowSealed  bool
}

type BuyCommand struct {
	BuyerID        string
	AccountID      string
	ListingID      uint64
	ExpectedItemID int64
	Count          int64
	Money          int64
	WholeListing   bool
}

type CancelCommand struct {
	SellerID  string
	AccountID string
	ListingID uint64
}

// BidCommand is one 竞拍 on the bidding opcode: Money is the whole-listing
// amount the bidder typed, not a per-item price.
type BidCommand struct {
	BidderID       string
	AccountID      string
	ListingID      uint64
	ExpectedItemID int64
	Money          int64
}

type Result struct {
	Listing      Listing
	Inventory    dnfrepo.InventoryRecord
	Character    dnfrepo.CharacterRecord
	Destination  int16
	GoldAfter    int64
	ClaimedGold  int64
	ClaimedItems []dnfrepo.ItemStack
	ReturnedRows []ReturnedRow
	Payout       Payout
	Delivery     Delivery
	// Refund is the released-bid row a bid, buyout or cancel wrote for a
	// bidder who lost their standing bid. Like Payout it is gold in the escrow
	// wallet waiting on an idempotent mail, so the caller has to send it.
	Refund Payout
	// Settlements are the won-listing rows an expiry sweep produced: one
	// delivery to the winning bidder plus the seller's proceeds, per listing.
	Settlements []Settlement
	// ExpiredReturns are the outbox rows written for listings that ran out
	// their duration. Like a cancel, an expired consignment leaves by system
	// mail, so the caller has to send each one and confirm it.
	ExpiredReturns []Delivery
}

// Payout is the durable gold outbox. SellerID is the recipient rather than
// strictly the seller: a refunded bid rides the same rows so that a bidder who
// was outbid is paid back through the same idempotent mail path a sale uses,
// with Refund telling the bridge which wording to send.
type Payout struct {
	ID         string
	ListingID  uint64
	SellerID   string
	SellerName string
	ItemID     int64
	SoldCount  int64
	Gold       int64
	Refund     bool
	// Statement is the seller's 成交 breakdown. HasStatement is false for a
	// refunded bid - announced with its own wording - and for a sale row written
	// before the breakdown was persisted, which keeps the older one-line letter.
	Statement    SaleStatement
	HasStatement bool
}

type ConfirmPayoutCommand struct {
	PayoutID string
	SellerID string
	Gold     int64
}

// Delivery is the durable buyer-item outbox. The item stays in auction escrow
// until the idempotent system-mail write has committed and ConfirmDeliveryMail
// removes this row.
type Delivery struct {
	ID        string
	ListingID uint64
	BuyerID   string
	Item      dnfrepo.ItemStack
	// Gold rides along with a returned consignment: the registration deposit,
	// handed back in the same mail as the item.
	Gold int64
	// Return marks a consignment going back to its seller rather than a
	// purchase going to its buyer, and Expired distinguishes a listing that
	// ran out its duration from one the seller cancelled. Every kind rides the
	// same outbox and a deferred row is re-sent from the key alone, so the
	// kind has to survive in the key - see deliveryKeyForReturn/ForExpiry.
	Return  bool
	Expired bool
}

type ConfirmDeliveryCommand struct {
	DeliveryID string
	BuyerID    string
	ListingID  uint64
	ItemID     int64
	Count      int64
	// Gold is the deposit refund the mail carried. It leaves the escrow
	// wallet here, not when the outbox row was written, so a deferred mail
	// never double-spends it.
	Gold int64
}

type ReturnedRow struct {
	ListType byte
	Slot     int16
	Stack    dnfrepo.ItemStack
}

type Owner struct {
	repositories dnfrepo.Group
	now          func() time.Time
}

func NewOwner(repositories dnfrepo.Group) (*Owner, error) {
	if repositories.CharacterTrade == nil || repositories.Character == nil || repositories.Inventory == nil {
		return nil, ErrUnavailable
	}
	return &Owner{repositories: repositories, now: time.Now}, nil
}

func (o *Owner) EnsureEscrow(ctx context.Context) error {
	if o == nil || o.repositories.CharacterCreate == nil {
		return ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// 首次创建在同一账户锁/SQL 事务中完成，已有托管快照只读。
	// 不能在买卖事务外重新保存旧钱包，否则并发上架会覆盖最新托管余额。
	return o.repositories.CharacterCreate.WithinCharacterCreation(ctx, EscrowCharacterID, o.repositories, func(tx dnfrepo.Group) error {
		c, i := tx.Character, tx.Inventory
		character, found, err := c.Load(ctx, EscrowCharacterID)
		if err != nil {
			return err
		}
		if found {
			if character.AccountID != EscrowAccountID || character.Stats == nil {
				return ErrUnavailable
			}
		} else {
			now := o.clock()
			character = dnfrepo.CharacterRecord{CharacterID: EscrowCharacterID, AccountID: EscrowAccountID, Slot: -1, Name: "Auction Escrow", Stats: map[string]int64{"gold": 0}, CreatedAt: now, UpdatedAt: now}
			if err := c.Save(ctx, character); err != nil {
				return err
			}
		}
		_, found, err = i.Load(ctx, EscrowCharacterID)
		if err != nil {
			return err
		}
		if found {
			return nil
		}
		return i.Save(ctx, dnfrepo.InventoryRecord{CharacterID: EscrowCharacterID, Slots: map[string]dnfrepo.ItemStack{}, UpdatedAt: o.clock()})
	})
}

func (o *Owner) Register(ctx context.Context, cmd RegisterCommand) (Result, error) {
	cmd.SellerID = strings.TrimSpace(cmd.SellerID)
	cmd.AccountID = strings.TrimSpace(cmd.AccountID)
	if o == nil || cmd.ListingID == 0 || cmd.SellerID == "" || cmd.AccountID == "" || cmd.SellerID == EscrowCharacterID || cmd.SourceSlot < 0 || (cmd.ListType != 0 && cmd.ListType != 1) || cmd.ExpectedID <= 0 || cmd.Count <= 0 || cmd.UnitPrice <= 0 || cmd.Duration <= 0 || cmd.ListingLimit <= 0 {
		return Result{}, ErrInvalidRequest
	}
	if cmd.StartPrice < -1 || cmd.StartPrice > cmd.UnitPrice {
		return Result{}, ErrInvalidRequest
	}
	buyoutPrice := cmd.BuyoutPrice
	if buyoutPrice == 0 {
		buyoutPrice = cmd.UnitPrice
	}
	// UnitPrice is the asking price either way: the buyout when there is one,
	// and the opening bid when the seller set no 一口价. Anything else would
	// let a listing sell at a price neither control on the client shows.
	if buyoutPrice == -1 {
		if cmd.StartPrice != cmd.UnitPrice {
			return Result{}, ErrInvalidRequest
		}
	} else if buyoutPrice != cmd.UnitPrice {
		return Result{}, ErrInvalidRequest
	}
	if cmd.Deposit < 0 || cmd.FeePercent < 0 || cmd.FeePercent > 100 {
		return Result{}, ErrInvalidRequest
	}
	if err := o.EnsureEscrow(ctx); err != nil {
		return Result{}, err
	}
	if cmd.SourceAccountOwned && !dnfrepo.IsAccountSharedInventorySlot(cmd.ListType, cmd.SourceSlot) {
		return Result{}, ErrInvalidRequest
	}
	var result Result
	// The account warehouse only exists inside an account-aware transaction, so
	// the two source containers take different transaction entry points and
	// otherwise share one body. The character path is byte-for-byte the trade
	// it has always been.
	var err error
	if cmd.SourceAccountOwned {
		assets, ok := o.repositories.CharacterTrade.(dnfrepo.CharacterTradeAssetsUnitOfWork)
		if !ok {
			return Result{}, ErrUnavailable
		}
		err = assets.WithinCharacterTradeAssets(
			ctx, cmd.SellerID, EscrowCharacterID,
			func(
				characters dnfrepo.CharacterRepository,
				inventories dnfrepo.InventoryRepository,
				accountInventories dnfrepo.AccountInventoryRepository,
			) error {
				return o.registerWithin(ctx, cmd, characters, inventories, accountInventories, &result)
			})
	} else {
		err = o.repositories.CharacterTrade.WithinCharacterTrade(
			ctx, cmd.SellerID, EscrowCharacterID,
			func(characters dnfrepo.CharacterRepository, inventories dnfrepo.InventoryRepository) error {
				return o.registerWithin(ctx, cmd, characters, inventories, nil, &result)
			})
	}
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// currentAuctionSourceInventory loads the durable inventory a registration
// draws its stack from. Container 36 is the account-scoped crystal/soul
// warehouse, whose wire slots are still list-0 354..365.
func currentAuctionSourceInventory(
	ctx context.Context,
	cmd RegisterCommand,
	inventories dnfrepo.InventoryRepository,
	accountInventories dnfrepo.AccountInventoryRepository,
) (dnfrepo.InventoryRecord, bool, error) {
	if !cmd.SourceAccountOwned {
		return inventories.Load(ctx, cmd.SellerID)
	}
	if accountInventories == nil {
		return dnfrepo.InventoryRecord{}, false, nil
	}
	account, found, err := accountInventories.Load(ctx, cmd.AccountID)
	if err != nil {
		return dnfrepo.InventoryRecord{}, false, err
	}
	if !found {
		// An account that has never stored anything in the warehouse has an
		// empty warehouse rather than a missing seller.
		return dnfrepo.InventoryRecord{CharacterID: cmd.AccountID}, false, nil
	}
	return dnfrepo.InventoryRecord{
		CharacterID: cmd.AccountID,
		Slots:       account.Slots,
		UpdatedAt:   account.UpdatedAt,
	}, true, nil
}

// saveAuctionSourceInventory writes the post-debit stack back to the container
// it came from, so a warehouse listing never falls back into a character's own
// list 0 - which would leave the same stack visible twice.
func saveAuctionSourceInventory(
	ctx context.Context,
	cmd RegisterCommand,
	inventories dnfrepo.InventoryRepository,
	accountInventories dnfrepo.AccountInventoryRepository,
	record dnfrepo.InventoryRecord,
) error {
	if !cmd.SourceAccountOwned {
		return inventories.Save(ctx, record)
	}
	if accountInventories == nil {
		return ErrUnavailable
	}
	if record.Slots == nil {
		record.Slots = map[string]dnfrepo.ItemStack{}
	}
	return accountInventories.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: cmd.AccountID,
		Slots:     record.Slots,
		UpdatedAt: record.UpdatedAt,
	})
}

// registerWithin is the transaction body of Register. accountInventories is nil
// on the character-inventory path.
func (o *Owner) registerWithin(
	ctx context.Context,
	cmd RegisterCommand,
	characters dnfrepo.CharacterRepository,
	inventories dnfrepo.InventoryRepository,
	accountInventories dnfrepo.AccountInventoryRepository,
	result *Result,
) error {
	// Same derivation Register validated, repeated here because the body no
	// longer shares its scope.
	buyoutPrice := cmd.BuyoutPrice
	if buyoutPrice == 0 {
		buyoutPrice = cmd.UnitPrice
	}
	seller, found, err := characters.Load(ctx, cmd.SellerID)
	if err != nil || !found {
		return errors.Join(ErrInvalidRequest, err)
	}
	if strings.TrimSpace(seller.AccountID) != cmd.AccountID {
		return ErrInvalidRequest
	}
	sellerInventory, found, err := currentAuctionSourceInventory(ctx, cmd, inventories, accountInventories)
	if err != nil || !found {
		return errors.Join(ErrItemUnavailable, err)
	}
	escrow, _, err := inventories.Load(ctx, EscrowCharacterID)
	if err != nil {
		return err
	}
	if escrow.Slots == nil {
		escrow.Slots = make(map[string]dnfrepo.ItemStack)
	}
	if _, exists := escrow.Slots[listingKey(cmd.ListingID)]; exists {
		return ErrListingIDExists
	}
	now := o.clock()
	active := 0
	for key, stack := range escrow.Slots {
		if !strings.HasPrefix(key, listingPrefix) || stack.Extra["seller_id"] != cmd.SellerID {
			continue
		}
		expires, parseErr := strconv.ParseInt(stack.Extra["expires_at"], 10, 64)
		if parseErr == nil && expires > now.Unix() {
			active++
		}
	}
	if active >= cmd.ListingLimit {
		return ErrListingLimit
	}
	sourceKey := inventoryKey(cmd.ListType, cmd.SourceSlot)
	stack, exists := sellerInventory.Slots[sourceKey]
	if !exists || stack.ItemID != cmd.ExpectedID || stack.Count < cmd.Count ||
		stack.Bind != cmd.ExpectedBind || (stack.Bind && !cmd.AllowSealed) ||
		(!stack.ExpireAt.IsZero() && !stack.ExpireAt.After(now)) {
		return ErrItemUnavailable
	}
	listed := cloneStack(stack)
	listed.Count = cmd.Count
	listed.Extra = cloneExtra(listed.Extra)
	listed.Extra["seller_id"] = cmd.SellerID
	listed.Extra["seller_name"] = strings.TrimSpace(cmd.SellerName)
	listed.Extra["list_type"] = strconv.Itoa(int(cmd.ListType))
	listed.Extra["source_slot"] = strconv.Itoa(int(cmd.SourceSlot))
	if cmd.SourceAccountOwned {
		listed.Extra["source_account_owned"] = "1"
	}
	listed.Extra["unit_price"] = strconv.FormatInt(cmd.UnitPrice, 10)
	listed.Extra["start_price"] = strconv.FormatInt(cmd.StartPrice, 10)
	listed.Extra["buyout_price"] = strconv.FormatInt(buyoutPrice, 10)
	listed.Extra["deposit"] = strconv.FormatInt(cmd.Deposit, 10)
	listed.Extra["fee_percent"] = strconv.FormatInt(cmd.FeePercent, 10)
	listed.Extra["created_at"] = strconv.FormatInt(now.Unix(), 10)
	listed.Extra["expires_at"] = strconv.FormatInt(now.Add(cmd.Duration).Unix(), 10)
	if stack.Count == cmd.Count {
		delete(sellerInventory.Slots, sourceKey)
	} else {
		stack.Count -= cmd.Count
		sellerInventory.Slots[sourceKey] = stack
	}
	escrow.Slots[listingKey(cmd.ListingID)] = listed
	// The deposit moves into the escrow wallet with the item, so gold is
	// conserved: it comes back out on a cancel, on expiry, or folded into
	// the proceeds when the listing sells out.
	if cmd.Deposit > 0 {
		escrowCharacter, escrowFound, escrowErr := characters.Load(ctx, EscrowCharacterID)
		if escrowErr != nil || !escrowFound {
			return errors.Join(ErrUnavailable, escrowErr)
		}
		if seller.Stats == nil || seller.Stats["gold"] < cmd.Deposit {
			return ErrGoldUnavailable
		}
		if escrowCharacter.Stats == nil {
			escrowCharacter.Stats = make(map[string]int64)
		}
		if escrowCharacter.Stats["gold"] > math.MaxInt64-cmd.Deposit {
			return ErrInvalidRequest
		}
		seller.Stats["gold"] -= cmd.Deposit
		escrowCharacter.Stats["gold"] += cmd.Deposit
		seller.UpdatedAt, escrowCharacter.UpdatedAt = now, now
		if err := characters.Save(ctx, seller); err != nil {
			return err
		}
		if err := characters.Save(ctx, escrowCharacter); err != nil {
			return err
		}
	}
	sellerInventory.UpdatedAt = now
	escrow.UpdatedAt = now
	if err := saveAuctionSourceInventory(ctx, cmd, inventories, accountInventories, sellerInventory); err != nil {
		return err
	}
	if err := inventories.Save(ctx, escrow); err != nil {
		return err
	}
	listing, err := listingFromStack(cmd.ListingID, listed)
	if err != nil {
		return err
	}
	*result = Result{
		Listing: listing, Inventory: dnfrepo.CloneInventory(sellerInventory),
		Character: dnfrepo.CloneCharacter(seller), GoldAfter: seller.Stats["gold"],
	}
	return nil
}

func (o *Owner) Search(ctx context.Context, itemIDs []uint32) ([]Listing, error) {
	if o == nil {
		return nil, ErrUnavailable
	}
	if err := o.EnsureEscrow(ctx); err != nil {
		return nil, err
	}
	record, found, err := o.repositories.Inventory.Load(ctx, EscrowCharacterID)
	if err != nil || !found {
		return nil, err
	}
	wanted := make(map[int64]struct{}, len(itemIDs))
	for _, itemID := range itemIDs {
		if itemID != 0 {
			wanted[int64(itemID)] = struct{}{}
		}
	}
	now := o.clock()
	result := make([]Listing, 0)
	for key, stack := range record.Slots {
		if !strings.HasPrefix(key, listingPrefix) {
			continue
		}
		if len(wanted) != 0 {
			if _, ok := wanted[stack.ItemID]; !ok {
				continue
			}
		}
		id, err := strconv.ParseUint(strings.TrimPrefix(key, listingPrefix), 10, 64)
		if err != nil {
			continue
		}
		listing, err := listingFromStack(id, stack)
		if err == nil && listing.ExpiresAt.After(now) {
			result = append(result, listing)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UnitPrice == result[j].UnitPrice {
			return result[i].ID < result[j].ID
		}
		return result[i].UnitPrice < result[j].UnitPrice
	})
	return result, nil
}

func (o *Owner) Cancel(ctx context.Context, cmd CancelCommand) (Result, error) {
	cmd.SellerID = strings.TrimSpace(cmd.SellerID)
	cmd.AccountID = strings.TrimSpace(cmd.AccountID)
	if o == nil || cmd.SellerID == "" || cmd.AccountID == "" || cmd.ListingID == 0 {
		return Result{}, ErrInvalidRequest
	}
	if err := o.EnsureEscrow(ctx); err != nil {
		return Result{}, err
	}
	var result Result
	err := o.repositories.CharacterTrade.WithinCharacterTrade(ctx, cmd.SellerID, EscrowCharacterID, func(characters dnfrepo.CharacterRepository, inventories dnfrepo.InventoryRepository) error {
		seller, found, err := characters.Load(ctx, cmd.SellerID)
		if err != nil || !found || strings.TrimSpace(seller.AccountID) != cmd.AccountID {
			return errors.Join(ErrNotOwner, err)
		}
		escrow, found, err := inventories.Load(ctx, EscrowCharacterID)
		if err != nil || !found {
			return errors.Join(ErrListingMissing, err)
		}
		stack, exists := escrow.Slots[listingKey(cmd.ListingID)]
		if !exists {
			return ErrListingMissing
		}
		listing, err := listingFromStack(cmd.ListingID, stack)
		if err != nil {
			return err
		}
		if listing.SellerID != cmd.SellerID {
			return ErrNotOwner
		}
		// A cancelled consignment goes back by system mail, the same channel a
		// sale already uses for the buyer's item and the seller's gold. Writing
		// it straight into the seller's bag was both a different experience
		// from the game - 86JP's AuctionReturnService mails the cancelled and
		// the expired listing through one shared transition - and a failure
		// mode of its own: with no free slot the cancel simply errored and the
		// item stayed stuck in escrow.
		deliveryID := deliveryKeyForReturn(cmd.ListingID)
		if _, exists := escrow.Slots[deliveryID]; exists {
			return ErrInvalidRequest
		}
		// A cancelled listing releases whatever bid was standing on it, the
		// same way 86JP's shared cancel/expire transition refunds the current
		// bid before returning the item.
		refund := releaseListingBid(escrow.Slots, listing)
		returned := cloneStack(stack)
		stripListingExtra(&returned)
		deliveryStack := cloneStack(returned)
		deliveryStack.Extra = cloneExtra(deliveryStack.Extra)
		deliveryStack.Extra["buyer_id"] = cmd.SellerID
		deliveryStack.Extra["listing_id"] = strconv.FormatUint(cmd.ListingID, 10)
		// The registration deposit is FORFEITED on a manual cancel and no
		// refund_gold key is written, so ConfirmDeliveryMail moves nothing out of
		// the escrow wallet and the 10000 stays there as a gold sink - exactly
		// like the sale commission. The client's own confirmation dialog is the
		// authority: dstr 60149 reads
		//   确定取消上架吗？
		//   取消上架后， 信誉度会下降， 且无法获得缴纳的押金。
		//   取消的拍卖品将以邮件的形式发放。
		// - "you cannot get back the deposit you paid". Only an expiry (流拍)
		// still refunds it, and that is the sibling path in ReturnExpiredListings.
		escrow.Slots[deliveryID] = deliveryStack
		delete(escrow.Slots, listingKey(cmd.ListingID))
		escrow.UpdatedAt = o.clock()
		if err := inventories.Save(ctx, escrow); err != nil {
			return err
		}
		listing.Item = returned
		result = Result{
			Listing: listing, Character: dnfrepo.CloneCharacter(seller), Destination: -1,
			Delivery: Delivery{ID: deliveryID, ListingID: cmd.ListingID, BuyerID: cmd.SellerID, Item: cloneStack(returned), Return: true},
			Refund:   refund,
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// LoadListing reads one live listing without locking anything. It exists for
// the bridge's read-only decisions - a bidding request has to know the
// listing's buyout total before it can tell a 竞拍 from a buyout - and must
// never be used to gate a mutation: only the transactional paths re-read the
// row under their own boundary.
func (o *Owner) LoadListing(ctx context.Context, id uint64) (Listing, error) {
	if o == nil || id == 0 {
		return Listing{}, ErrInvalidRequest
	}
	if err := o.EnsureEscrow(ctx); err != nil {
		return Listing{}, err
	}
	escrow, found, err := o.repositories.Inventory.Load(ctx, EscrowCharacterID)
	if err != nil || !found {
		return Listing{}, errors.Join(ErrUnavailable, err)
	}
	stack, exists := escrow.Slots[listingKey(id)]
	if !exists {
		return Listing{}, ErrListingMissing
	}
	return listingFromStack(id, stack)
}

// Settlement is one expired listing that had a standing bid and therefore
// sold to that bidder rather than going back to its seller.
type Settlement struct {
	ListingID uint64
	Delivery  Delivery
	Payout    Payout
}

// releaseListingBid queues the standing bid back to whoever placed it. The
// gold is already in the escrow wallet, so this only writes the outbox row;
// ConfirmPayoutMail is what actually moves it out, which keeps a deferred or
// retried mail from paying twice. Writing the row twice is a no-op: the key
// carries the bidder and the amount.
func releaseListingBid(slots map[string]dnfrepo.ItemStack, listing Listing) Payout {
	if listing.BidTotal <= 0 || listing.BidderID == "" {
		return Payout{}
	}
	key := refundKeyForBid(listing.ID, listing.BidderID, listing.BidTotal)
	if _, exists := slots[key]; exists {
		return Payout{}
	}
	slots[key] = dnfrepo.ItemStack{
		ItemID: listing.Item.ItemID,
		Count:  listing.BidTotal,
		Extra: map[string]string{
			"seller_id":  listing.BidderID,
			"listing_id": strconv.FormatUint(listing.ID, 10),
			"sold_count": "0",
			"refund":     "1",
		},
	}
	return Payout{
		ID: key, ListingID: listing.ID, SellerID: listing.BidderID,
		ItemID: listing.Item.ItemID, Gold: listing.BidTotal, Refund: true,
	}
}

// consumeListingBidRefunds takes back the gold this bidder already has parked
// in escrow for this listing - releases from earlier rounds that have not been
// mailed out yet - and returns the total, deleting the rows in the same
// transaction so the same gold can never be both credited here and mailed.
//
// It exists because a released bid is only *queued* for return: it leaves
// escrow when the refund mail commits, and reaches the wallet only once the
// player empties their mailbox. Without this, being outbid locked your own
// money away from the very listing you were bidding on - with 10,000 gold and
// an 8,000 bid outstanding you could not answer a 8,500 raise, even though the
// 8,000 is yours and sitting right there. Applying it is not a discount: it is
// the same escrow gold, moving from "owed back to you" to "committed to your
// new bid", which is exactly what would happen if you had already collected it
// and spent it again.
func consumeListingBidRefunds(slots map[string]dnfrepo.ItemStack, listingID uint64, bidderID string) int64 {
	if len(slots) == 0 || bidderID == "" {
		return 0
	}
	prefix := refundKeyPrefixForBid(listingID, bidderID)
	credit := int64(0)
	for key, stack := range slots {
		if !strings.HasPrefix(key, prefix) || stack.Count <= 0 {
			continue
		}
		if strings.TrimSpace(stack.Extra["refund"]) != "1" ||
			strings.TrimSpace(stack.Extra["seller_id"]) != bidderID {
			continue
		}
		if credit > math.MaxInt64-stack.Count {
			continue
		}
		credit += stack.Count
		delete(slots, key)
	}
	return credit
}

// Bid records one 竞拍 against a listing, following 86JP's
// AuctionBidService.TryBid: the amount has to beat the standing bid (or reach
// the opening bid when there is none) and stay under the buyout, the bidder's
// gold moves into escrow immediately, and whoever held the previous bid gets
// it back by mail. A bidder raising their own bid only pays the difference,
// because the earlier amount is already escrowed under their name.
func (o *Owner) Bid(ctx context.Context, cmd BidCommand) (Result, error) {
	cmd.BidderID = strings.TrimSpace(cmd.BidderID)
	cmd.AccountID = strings.TrimSpace(cmd.AccountID)
	if o == nil || cmd.BidderID == "" || cmd.AccountID == "" || cmd.BidderID == EscrowCharacterID ||
		cmd.ListingID == 0 || cmd.ExpectedItemID <= 0 || cmd.Money <= 0 {
		return Result{}, ErrInvalidRequest
	}
	if err := o.EnsureEscrow(ctx); err != nil {
		return Result{}, err
	}
	var result Result
	err := o.repositories.CharacterTrade.WithinCharacterTrade(ctx, cmd.BidderID, EscrowCharacterID, func(characters dnfrepo.CharacterRepository, inventories dnfrepo.InventoryRepository) error {
		bidder, found, err := characters.Load(ctx, cmd.BidderID)
		if err != nil || !found || strings.TrimSpace(bidder.AccountID) != cmd.AccountID {
			return errors.Join(ErrInvalidRequest, err)
		}
		if bidder.Stats == nil {
			bidder.Stats = make(map[string]int64)
		}
		escrowCharacter, found, err := characters.Load(ctx, EscrowCharacterID)
		if err != nil || !found {
			return errors.Join(ErrUnavailable, err)
		}
		if escrowCharacter.Stats == nil {
			escrowCharacter.Stats = make(map[string]int64)
		}
		escrow, found, err := inventories.Load(ctx, EscrowCharacterID)
		if err != nil || !found {
			return errors.Join(ErrListingMissing, err)
		}
		stack, exists := escrow.Slots[listingKey(cmd.ListingID)]
		if !exists {
			return ErrListingMissing
		}
		listing, err := listingFromStack(cmd.ListingID, stack)
		if err != nil {
			return err
		}
		if !listing.ExpiresAt.After(o.clock()) {
			return ErrListingExpired
		}
		if listing.Item.ItemID != cmd.ExpectedItemID {
			return ErrItemUnavailable
		}
		if listing.SellerID == cmd.BidderID {
			return ErrBuyerIsSeller
		}
		if !listing.HasBidding() {
			return ErrBiddingNotAllowed
		}
		// Three refusals, all "your number does not take this listing": at or
		// below the standing bid, under the opening bid, or at/above the
		// buyout - the last one is a buyout, not a bid.
		switch {
		case listing.BidTotal > 0 && cmd.Money <= listing.BidTotal:
			return ErrBidTooLow
		case listing.BidTotal == 0 && cmd.Money < listing.OpeningBidTotal():
			return ErrBidTooLow
		case listing.HasBuyout() && cmd.Money >= listing.BuyoutTotal():
			return ErrBidTooLow
		}
		sameBidder := listing.BidTotal > 0 && listing.BidderID == cmd.BidderID
		var refund Payout
		if !sameBidder {
			refund = releaseListingBid(escrow.Slots, listing)
		}
		// Everything of this bidder's that is already escrowed against this
		// listing counts towards the new bid: the standing bid when they are
		// raising their own, plus any earlier release still waiting on its
		// mail. Only the difference leaves the wallet.
		credit := consumeListingBidRefunds(escrow.Slots, cmd.ListingID, cmd.BidderID)
		if sameBidder {
			credit += listing.BidTotal
		}
		required := cmd.Money - credit
		// A bid always beats the standing one, which is at least as large as
		// anything this bidder had escrowed here, so the credit can never
		// cover the whole bid; a negative charge would mean the ledger is
		// inconsistent rather than that the bid is free.
		if required < 0 {
			return ErrInvalidRequest
		}
		if bidder.Stats["gold"] < required {
			return ErrGoldUnavailable
		}
		if escrowCharacter.Stats["gold"] > math.MaxInt64-required {
			return ErrInvalidRequest
		}
		bidder.Stats["gold"] -= required
		escrowCharacter.Stats["gold"] += required
		now := o.clock()
		writeListingBid(&stack, cmd.BidderID, cmd.AccountID, cmd.Money, now)
		escrow.Slots[listingKey(cmd.ListingID)] = stack
		bidder.UpdatedAt, escrowCharacter.UpdatedAt, escrow.UpdatedAt = now, now, now
		if err := characters.Save(ctx, bidder); err != nil {
			return err
		}
		if err := characters.Save(ctx, escrowCharacter); err != nil {
			return err
		}
		if err := inventories.Save(ctx, escrow); err != nil {
			return err
		}
		listing.BidTotal, listing.BidderID, listing.BidderAccountID, listing.BidAt = cmd.Money, cmd.BidderID, cmd.AccountID, now
		result = Result{
			Listing: listing, Character: dnfrepo.CloneCharacter(bidder),
			Destination: -1, GoldAfter: bidder.Stats["gold"], Refund: refund,
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func (o *Owner) Buy(ctx context.Context, cmd BuyCommand) (Result, error) {
	cmd.BuyerID = strings.TrimSpace(cmd.BuyerID)
	cmd.AccountID = strings.TrimSpace(cmd.AccountID)
	if o == nil || cmd.BuyerID == "" || cmd.AccountID == "" || cmd.BuyerID == EscrowCharacterID || cmd.ListingID == 0 || cmd.ExpectedItemID <= 0 || (!cmd.WholeListing && cmd.Count <= 0) || cmd.Money <= 0 {
		return Result{}, ErrInvalidRequest
	}
	if err := o.EnsureEscrow(ctx); err != nil {
		return Result{}, err
	}
	var result Result
	err := o.repositories.CharacterTrade.WithinCharacterTrade(ctx, cmd.BuyerID, EscrowCharacterID, func(characters dnfrepo.CharacterRepository, inventories dnfrepo.InventoryRepository) error {
		buyer, found, err := characters.Load(ctx, cmd.BuyerID)
		if err != nil || !found || strings.TrimSpace(buyer.AccountID) != cmd.AccountID {
			return errors.Join(ErrInvalidRequest, err)
		}
		if buyer.Stats == nil {
			buyer.Stats = make(map[string]int64)
		}
		escrowCharacter, found, err := characters.Load(ctx, EscrowCharacterID)
		if err != nil || !found {
			return errors.Join(ErrUnavailable, err)
		}
		if escrowCharacter.Stats == nil {
			escrowCharacter.Stats = make(map[string]int64)
		}
		escrow, found, err := inventories.Load(ctx, EscrowCharacterID)
		if err != nil || !found {
			return errors.Join(ErrListingMissing, err)
		}
		stack, exists := escrow.Slots[listingKey(cmd.ListingID)]
		if !exists {
			return ErrListingMissing
		}
		listing, err := listingFromStack(cmd.ListingID, stack)
		if err != nil {
			return err
		}
		if !listing.ExpiresAt.After(o.clock()) {
			return ErrListingExpired
		}
		if listing.Item.ItemID != cmd.ExpectedItemID {
			return ErrItemUnavailable
		}
		count := cmd.Count
		if cmd.WholeListing {
			count = stack.Count
		}
		if listing.SellerID == cmd.BuyerID {
			return ErrBuyerIsSeller
		}
		if count <= 0 || stack.Count < count || listing.UnitPrice > math.MaxInt64/count {
			return ErrInvalidRequest
		}
		total := listing.UnitPrice * count
		if cmd.Money != total {
			return ErrPriceMismatch
		}
		// A listing under bid can only be bought out whole: leaving a partial
		// row behind would strand the standing bid on an item the bidder can
		// no longer win in full. 86JP's TryBuy refuses the same case with
		// InvalidQuantity.
		if listing.BidTotal > 0 && count != stack.Count {
			return ErrInvalidRequest
		}
		// The buyer's own standing bid is already escrowed under their name,
		// so it counts towards the buyout and only the difference is charged -
		// the same netting 86JP does with buyerOwnsCurrentBid - and so does
		// any earlier release of theirs on this listing that has not been
		// mailed out yet, for the reason consumeListingBidRefunds explains.
		buyerHoldsBid := listing.BidTotal > 0 && listing.BidderID == cmd.BuyerID
		var refund Payout
		if listing.BidTotal > 0 && !buyerHoldsBid {
			refund = releaseListingBid(escrow.Slots, listing)
		}
		credit := consumeListingBidRefunds(escrow.Slots, cmd.ListingID, cmd.BuyerID)
		if buyerHoldsBid {
			credit += listing.BidTotal
		}
		if credit > total {
			return ErrInvalidRequest
		}
		charged := total - credit
		if buyer.Stats["gold"] < charged {
			return ErrGoldUnavailable
		}
		purchased := cloneStack(stack)
		purchased.Count = count
		stripListingExtra(&purchased)
		remaining := stack.Count - count
		if remaining == 0 {
			delete(escrow.Slots, listingKey(cmd.ListingID))
		} else {
			stack.Count = remaining
			// A partial buy can only happen on a listing with no standing bid
			// (refused above), so there is no bid state left to carry - clear
			// it anyway so a row can never keep a stale bidder.
			clearListingBid(&stack)
			escrow.Slots[listingKey(cmd.ListingID)] = stack
		}
		// Only the difference leaves the wallet when the buyer's own bid is
		// being applied; the rest of the sale price is already in escrow.
		buyer.Stats["gold"] -= charged
		if escrowCharacter.Stats["gold"] > math.MaxInt64-charged {
			return ErrInvalidRequest
		}
		escrowCharacter.Stats["gold"] += charged
		payoutID := payoutKeyForSale(cmd.ListingID, remaining)
		if _, exists := escrow.Slots[payoutID]; exists {
			return ErrInvalidRequest
		}
		deliveryID := deliveryKeyForSale(cmd.ListingID, remaining)
		if _, exists := escrow.Slots[deliveryID]; exists {
			return ErrInvalidRequest
		}
		// The commission stays behind in the escrow wallet - it is the gold
		// sink - and the registration deposit only comes back once the last
		// piece is gone, so a partially bought listing keeps it riding until
		// it sells out, is cancelled, or expires.
		proceeds := listing.SellerProceeds(total, remaining == 0)
		if proceeds < 0 {
			return ErrInvalidRequest
		}
		statement := listing.SaleStatementFor(total, remaining == 0)
		payout := dnfrepo.ItemStack{
			ItemID: listing.Item.ItemID,
			Count:  proceeds,
			Extra: map[string]string{
				"seller_id":    listing.SellerID,
				"seller_name":  listing.SellerName,
				"listing_id":   strconv.FormatUint(cmd.ListingID, 10),
				"sold_count":   strconv.FormatInt(count, 10),
				"gross_gold":   strconv.FormatInt(statement.GrossGold, 10),
				"fee_gold":     strconv.FormatInt(statement.FeeGold, 10),
				"deposit_gold": strconv.FormatInt(statement.DepositGold, 10),
			},
		}
		escrow.Slots[payoutID] = payout
		deliveryStack := cloneStack(purchased)
		deliveryStack.Extra = cloneExtra(deliveryStack.Extra)
		deliveryStack.Extra["buyer_id"] = cmd.BuyerID
		deliveryStack.Extra["listing_id"] = strconv.FormatUint(cmd.ListingID, 10)
		escrow.Slots[deliveryID] = deliveryStack
		now := o.clock()
		recordAuctionSale(escrow.Slots, listing, cmd.BuyerID, count, total, remaining, now, false)
		buyer.UpdatedAt, escrowCharacter.UpdatedAt = now, now
		escrow.UpdatedAt = now
		if err := characters.Save(ctx, buyer); err != nil {
			return err
		}
		if err := characters.Save(ctx, escrowCharacter); err != nil {
			return err
		}
		if err := inventories.Save(ctx, escrow); err != nil {
			return err
		}
		listing.Item = purchased
		result = Result{
			Listing: listing, Character: dnfrepo.CloneCharacter(buyer),
			Destination: -1, GoldAfter: buyer.Stats["gold"],
			Payout:   Payout{ID: payoutID, ListingID: cmd.ListingID, SellerID: listing.SellerID, SellerName: listing.SellerName, ItemID: listing.Item.ItemID, SoldCount: count, Gold: proceeds, Statement: statement, HasStatement: true},
			Delivery: Delivery{ID: deliveryID, ListingID: cmd.ListingID, BuyerID: cmd.BuyerID, Item: cloneStack(purchased)},
			Refund:   refund,
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// PendingDeliveries returns durable buyer-item outbox rows. These rows are
// independent from the active listing and survive response/mail write errors.
func (o *Owner) PendingDeliveries(ctx context.Context, buyerID string) ([]Delivery, error) {
	buyerID = strings.TrimSpace(buyerID)
	if o == nil || buyerID == "" {
		return nil, ErrInvalidRequest
	}
	if err := o.EnsureEscrow(ctx); err != nil {
		return nil, err
	}
	escrow, found, err := o.repositories.Inventory.Load(ctx, EscrowCharacterID)
	if err != nil || !found {
		return nil, errors.Join(ErrUnavailable, err)
	}
	result := make([]Delivery, 0)
	for key, stack := range escrow.Slots {
		if !strings.HasPrefix(key, deliveryPrefix) || strings.TrimSpace(stack.Extra["buyer_id"]) != buyerID || stack.ItemID <= 0 || stack.Count <= 0 {
			continue
		}
		listingID, parseErr := strconv.ParseUint(strings.TrimSpace(stack.Extra["listing_id"]), 10, 64)
		if parseErr != nil || listingID == 0 {
			continue
		}
		item := cloneStack(stack)
		refund, _ := strconv.ParseInt(strings.TrimSpace(stack.Extra["refund_gold"]), 10, 64)
		if refund < 0 {
			refund = 0
		}
		delete(item.Extra, "buyer_id")
		delete(item.Extra, "listing_id")
		delete(item.Extra, "refund_gold")
		expired := key == deliveryKeyForExpiry(listingID)
		result = append(result, Delivery{
			ID: key, ListingID: listingID, BuyerID: buyerID, Item: item, Gold: refund,
			Return: expired || key == deliveryKeyForReturn(listingID), Expired: expired,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// ConfirmDeliveryMail consumes one escrowed buyer item only after its matching
// idempotent system mail exists.
func (o *Owner) ConfirmDeliveryMail(ctx context.Context, cmd ConfirmDeliveryCommand) error {
	cmd.DeliveryID, cmd.BuyerID = strings.TrimSpace(cmd.DeliveryID), strings.TrimSpace(cmd.BuyerID)
	if o == nil || !strings.HasPrefix(cmd.DeliveryID, deliveryPrefix) || cmd.BuyerID == "" || cmd.ListingID == 0 || cmd.ItemID <= 0 || cmd.Count <= 0 {
		return ErrInvalidRequest
	}
	if err := o.EnsureEscrow(ctx); err != nil {
		return err
	}
	return o.repositories.CharacterTrade.WithinCharacterTrade(ctx, cmd.BuyerID, EscrowCharacterID, func(characters dnfrepo.CharacterRepository, inventories dnfrepo.InventoryRepository) error {
		escrow, found, err := inventories.Load(ctx, EscrowCharacterID)
		if err != nil || !found {
			return errors.Join(ErrUnavailable, err)
		}
		stack, exists := escrow.Slots[cmd.DeliveryID]
		if !exists {
			return nil
		}
		listingID, parseErr := strconv.ParseUint(strings.TrimSpace(stack.Extra["listing_id"]), 10, 64)
		if parseErr != nil || listingID != cmd.ListingID || strings.TrimSpace(stack.Extra["buyer_id"]) != cmd.BuyerID || stack.ItemID != cmd.ItemID || stack.Count != cmd.Count {
			return ErrInvalidRequest
		}
		refund, _ := strconv.ParseInt(strings.TrimSpace(stack.Extra["refund_gold"]), 10, 64)
		if refund < 0 || cmd.Gold != refund {
			return ErrInvalidRequest
		}
		now := o.clock()
		// The deposit refund leaves the escrow wallet only now, together with
		// the outbox row, so the mail and the accounting commit or fail as one.
		if refund > 0 {
			escrowCharacter, escrowFound, escrowErr := characters.Load(ctx, EscrowCharacterID)
			if escrowErr != nil || !escrowFound {
				return errors.Join(ErrUnavailable, escrowErr)
			}
			if escrowCharacter.Stats == nil || escrowCharacter.Stats["gold"] < refund {
				return ErrGoldUnavailable
			}
			escrowCharacter.Stats["gold"] -= refund
			escrowCharacter.UpdatedAt = now
			if err := characters.Save(ctx, escrowCharacter); err != nil {
				return err
			}
		}
		delete(escrow.Slots, cmd.DeliveryID)
		escrow.UpdatedAt = now
		return inventories.Save(ctx, escrow)
	})
}

// PendingPayouts returns the durable auction-sale outbox rows. Gold remains
// owned by the escrow character until the matching mailbox delivery has been
// committed and ConfirmPayoutMail removes the row.
func (o *Owner) PendingPayouts(ctx context.Context, sellerID string) ([]Payout, error) {
	sellerID = strings.TrimSpace(sellerID)
	if o == nil || sellerID == "" {
		return nil, ErrInvalidRequest
	}
	if err := o.EnsureEscrow(ctx); err != nil {
		return nil, err
	}
	escrow, found, err := o.repositories.Inventory.Load(ctx, EscrowCharacterID)
	if err != nil || !found {
		return nil, errors.Join(ErrUnavailable, err)
	}
	result := make([]Payout, 0)
	for key, stack := range escrow.Slots {
		if !strings.HasPrefix(key, payoutPrefix) || strings.TrimSpace(stack.Extra["seller_id"]) != sellerID || stack.Count <= 0 {
			continue
		}
		listingID, parseErr := strconv.ParseUint(strings.TrimSpace(stack.Extra["listing_id"]), 10, 64)
		if parseErr != nil || listingID == 0 {
			continue
		}
		soldCount, _ := strconv.ParseInt(strings.TrimSpace(stack.Extra["sold_count"]), 10, 64)
		payout := Payout{
			ID: key, ListingID: listingID, SellerID: sellerID, SellerName: strings.TrimSpace(stack.Extra["seller_name"]),
			ItemID: stack.ItemID, SoldCount: soldCount, Gold: stack.Count,
			Refund: strings.TrimSpace(stack.Extra["refund"]) == "1",
		}
		// The seller's 成交 breakdown rides the row, so a deferred retry prints
		// the same letter after the listing row itself is long gone. Rows written
		// before the breakdown existed keep the older one-line wording.
		if raw, present := stack.Extra["gross_gold"]; present {
			gross, grossErr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			fee, feeErr := strconv.ParseInt(strings.TrimSpace(stack.Extra["fee_gold"]), 10, 64)
			deposit, depositErr := strconv.ParseInt(strings.TrimSpace(stack.Extra["deposit_gold"]), 10, 64)
			if grossErr == nil && feeErr == nil && depositErr == nil && gross >= 0 && fee >= 0 && deposit >= 0 {
				payout.Statement = SaleStatement{
					GrossGold:   gross,
					FeeGold:     fee,
					DepositGold: deposit,
					NetGold:     stack.Count,
				}
				payout.HasStatement = true
			}
		}
		result = append(result, payout)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// ConfirmPayoutMail consumes one escrow payout only after the idempotent
// mailbox delivery exists. A failed or outcome-unknown mail write leaves this
// row intact so the bridge can safely retry the same completion key.
func (o *Owner) ConfirmPayoutMail(ctx context.Context, cmd ConfirmPayoutCommand) error {
	cmd.PayoutID, cmd.SellerID = strings.TrimSpace(cmd.PayoutID), strings.TrimSpace(cmd.SellerID)
	if o == nil || !strings.HasPrefix(cmd.PayoutID, payoutPrefix) || cmd.SellerID == "" || cmd.Gold <= 0 {
		return ErrInvalidRequest
	}
	if err := o.EnsureEscrow(ctx); err != nil {
		return err
	}
	return o.repositories.CharacterTrade.WithinCharacterTrade(ctx, cmd.SellerID, EscrowCharacterID, func(characters dnfrepo.CharacterRepository, inventories dnfrepo.InventoryRepository) error {
		escrowCharacter, found, err := characters.Load(ctx, EscrowCharacterID)
		if err != nil || !found {
			return errors.Join(ErrUnavailable, err)
		}
		escrow, found, err := inventories.Load(ctx, EscrowCharacterID)
		if err != nil || !found {
			return errors.Join(ErrUnavailable, err)
		}
		payout, exists := escrow.Slots[cmd.PayoutID]
		if !exists {
			return nil
		}
		if strings.TrimSpace(payout.Extra["seller_id"]) != cmd.SellerID || payout.Count != cmd.Gold ||
			escrowCharacter.Stats == nil || escrowCharacter.Stats["gold"] < cmd.Gold {
			return ErrInvalidRequest
		}
		delete(escrow.Slots, cmd.PayoutID)
		escrowCharacter.Stats["gold"] -= cmd.Gold
		now := o.clock()
		escrowCharacter.UpdatedAt, escrow.UpdatedAt = now, now
		if err := characters.Save(ctx, escrowCharacter); err != nil {
			return err
		}
		return inventories.Save(ctx, escrow)
	})
}

// RefundBidToBidder pays a released bid straight back into the bidder's wallet
// instead of mailing it, and returns their balance afterwards.
//
// Sale proceeds keep going by mail - that is the authentic channel and nobody
// is waiting on them. A released bid is different: the current client refuses
// to *send* a bid larger than the wallet balance at all (sub_2FFB0C0 compares
// the amount against the wallet and answers 所持金币不足%d金币 without touching
// the socket), so an outbid player whose gold is sitting in a mail cannot
// answer the raise until they walk to the mailbox and collect it. Returning it
// to the wallet is what makes being outbid recoverable in the same breath.
//
// The row is deleted in the same transaction that credits the wallet, so a
// retry after a partial failure cannot pay twice.
func (o *Owner) RefundBidToBidder(ctx context.Context, payout Payout) (int64, error) {
	payout.ID, payout.SellerID = strings.TrimSpace(payout.ID), strings.TrimSpace(payout.SellerID)
	if o == nil || !payout.Refund || !strings.HasPrefix(payout.ID, payoutPrefix) ||
		payout.SellerID == "" || payout.SellerID == EscrowCharacterID || payout.Gold <= 0 {
		return 0, ErrInvalidRequest
	}
	if err := o.EnsureEscrow(ctx); err != nil {
		return 0, err
	}
	var goldAfter int64
	err := o.repositories.CharacterTrade.WithinCharacterTrade(ctx, payout.SellerID, EscrowCharacterID, func(characters dnfrepo.CharacterRepository, inventories dnfrepo.InventoryRepository) error {
		bidder, found, err := characters.Load(ctx, payout.SellerID)
		if err != nil || !found {
			return errors.Join(ErrInvalidRequest, err)
		}
		if bidder.Stats == nil {
			bidder.Stats = make(map[string]int64)
		}
		escrowCharacter, found, err := characters.Load(ctx, EscrowCharacterID)
		if err != nil || !found {
			return errors.Join(ErrUnavailable, err)
		}
		escrow, found, err := inventories.Load(ctx, EscrowCharacterID)
		if err != nil || !found {
			return errors.Join(ErrUnavailable, err)
		}
		row, exists := escrow.Slots[payout.ID]
		if !exists {
			// Already consumed - by a re-bid that netted it, or by an earlier
			// retry. The caller's copy is stale, not wrong.
			goldAfter = bidder.Stats["gold"]
			return nil
		}
		if strings.TrimSpace(row.Extra["refund"]) != "1" ||
			strings.TrimSpace(row.Extra["seller_id"]) != payout.SellerID || row.Count != payout.Gold ||
			escrowCharacter.Stats == nil || escrowCharacter.Stats["gold"] < payout.Gold ||
			bidder.Stats["gold"] > math.MaxInt64-payout.Gold {
			return ErrInvalidRequest
		}
		delete(escrow.Slots, payout.ID)
		escrowCharacter.Stats["gold"] -= payout.Gold
		bidder.Stats["gold"] += payout.Gold
		now := o.clock()
		bidder.UpdatedAt, escrowCharacter.UpdatedAt, escrow.UpdatedAt = now, now, now
		if err := characters.Save(ctx, bidder); err != nil {
			return err
		}
		if err := characters.Save(ctx, escrowCharacter); err != nil {
			return err
		}
		if err := inventories.Save(ctx, escrow); err != nil {
			return err
		}
		goldAfter = bidder.Stats["gold"]
		return nil
	})
	if err != nil {
		return 0, err
	}
	return goldAfter, nil
}

func (o *Owner) ClaimProceeds(ctx context.Context, sellerID, accountID string) (Result, error) {
	sellerID, accountID = strings.TrimSpace(sellerID), strings.TrimSpace(accountID)
	if o == nil || sellerID == "" || accountID == "" {
		return Result{}, ErrInvalidRequest
	}
	if err := o.EnsureEscrow(ctx); err != nil {
		return Result{}, err
	}
	var result Result
	err := o.repositories.CharacterTrade.WithinCharacterTrade(ctx, sellerID, EscrowCharacterID, func(characters dnfrepo.CharacterRepository, inventories dnfrepo.InventoryRepository) error {
		seller, found, err := characters.Load(ctx, sellerID)
		if err != nil || !found || strings.TrimSpace(seller.AccountID) != accountID {
			return errors.Join(ErrNotOwner, err)
		}
		if seller.Stats == nil {
			seller.Stats = make(map[string]int64)
		}
		escrowCharacter, found, err := characters.Load(ctx, EscrowCharacterID)
		if err != nil || !found {
			return errors.Join(ErrUnavailable, err)
		}
		if escrowCharacter.Stats == nil {
			escrowCharacter.Stats = make(map[string]int64)
		}
		escrow, found, err := inventories.Load(ctx, EscrowCharacterID)
		if err != nil || !found {
			return errors.Join(ErrUnavailable, err)
		}
		sellerInventory, _, err := inventories.Load(ctx, sellerID)
		if err != nil {
			return err
		}
		if sellerInventory.Slots == nil {
			sellerInventory.Slots = make(map[string]dnfrepo.ItemStack)
		}
		var total int64
		expiredReturns := make([]Delivery, 0)
		now := o.clock()
		listingKeys := make([]string, 0)
		for key := range escrow.Slots {
			if strings.HasPrefix(key, listingPrefix) {
				listingKeys = append(listingKeys, key)
			}
		}
		sort.Strings(listingKeys)
		for _, key := range listingKeys {
			stack := escrow.Slots[key]
			if stack.Extra["seller_id"] != sellerID {
				continue
			}
			id, parseErr := strconv.ParseUint(strings.TrimPrefix(key, listingPrefix), 10, 64)
			if parseErr != nil {
				continue
			}
			listing, parseErr := listingFromStack(id, stack)
			if parseErr != nil || listing.ExpiresAt.After(now) {
				continue
			}
			// An expired listing carrying a bid sold to that bidder and must
			// not go home to its seller. SettleWonListings owns those rows.
			if listing.BidTotal > 0 {
				continue
			}
			// An expired consignment goes home the same way a cancelled one
			// does: by system mail, out of the durable outbox. 86JP's
			// AuctionReturnService runs TryCancel and TryExpire through one
			// shared transition for exactly this reason, and a bag with no
			// free slot used to abort the whole claim sweep here.
			deliveryID := deliveryKeyForExpiry(id)
			if _, exists := escrow.Slots[deliveryID]; exists {
				continue
			}
			returnedStack := cloneStack(stack)
			stripListingExtra(&returnedStack)
			deliveryStack := cloneStack(returnedStack)
			deliveryStack.Extra = cloneExtra(deliveryStack.Extra)
			deliveryStack.Extra["buyer_id"] = sellerID
			deliveryStack.Extra["listing_id"] = strconv.FormatUint(id, 10)
			deliveryStack.Extra["refund_gold"] = strconv.FormatInt(listing.Deposit, 10)
			escrow.Slots[deliveryID] = deliveryStack
			delete(escrow.Slots, key)
			expiredReturns = append(expiredReturns, Delivery{
				ID: deliveryID, ListingID: id, BuyerID: sellerID,
				Item: cloneStack(returnedStack), Gold: listing.Deposit, Return: true, Expired: true,
			})
		}
		for key, stack := range escrow.Slots {
			if strings.HasPrefix(key, payoutPrefix) && stack.Extra["seller_id"] == sellerID {
				if stack.Count <= 0 || total > math.MaxInt64-stack.Count {
					return ErrInvalidRequest
				}
				total += stack.Count
				delete(escrow.Slots, key)
			}
		}
		if total == 0 && len(expiredReturns) == 0 {
			result = Result{Character: dnfrepo.CloneCharacter(seller), Inventory: dnfrepo.CloneInventory(sellerInventory), GoldAfter: seller.Stats["gold"]}
			return nil
		}
		if escrowCharacter.Stats["gold"] < total || seller.Stats["gold"] > math.MaxInt64-total {
			return ErrGoldUnavailable
		}
		escrowCharacter.Stats["gold"] -= total
		seller.Stats["gold"] += total
		seller.UpdatedAt, escrowCharacter.UpdatedAt, escrow.UpdatedAt = now, now, now
		sellerInventory.UpdatedAt = now
		if err := characters.Save(ctx, seller); err != nil {
			return err
		}
		if err := characters.Save(ctx, escrowCharacter); err != nil {
			return err
		}
		if err := inventories.Save(ctx, escrow); err != nil {
			return err
		}
		if err := inventories.Save(ctx, sellerInventory); err != nil {
			return err
		}
		claimedItems := make([]dnfrepo.ItemStack, len(expiredReturns))
		for index := range expiredReturns {
			claimedItems[index] = cloneStack(expiredReturns[index].Item)
		}
		result = Result{Character: dnfrepo.CloneCharacter(seller), Inventory: dnfrepo.CloneInventory(sellerInventory), GoldAfter: seller.Stats["gold"], ClaimedGold: total, ClaimedItems: claimedItems, ExpiredReturns: expiredReturns}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// ReturnExpiredListings returns only expired escrow items. Sale proceeds are
// deliberately excluded because the current auction pays sellers through
// idempotent system mail instead of crediting the wallet when op188 opens.
func (o *Owner) ReturnExpiredListings(ctx context.Context, sellerID, accountID string) (Result, error) {
	sellerID, accountID = strings.TrimSpace(sellerID), strings.TrimSpace(accountID)
	if o == nil || sellerID == "" || accountID == "" {
		return Result{}, ErrInvalidRequest
	}
	if err := o.EnsureEscrow(ctx); err != nil {
		return Result{}, err
	}
	var result Result
	err := o.repositories.CharacterTrade.WithinCharacterTrade(ctx, sellerID, EscrowCharacterID, func(characters dnfrepo.CharacterRepository, inventories dnfrepo.InventoryRepository) error {
		seller, found, err := characters.Load(ctx, sellerID)
		if err != nil || !found || strings.TrimSpace(seller.AccountID) != accountID {
			return errors.Join(ErrNotOwner, err)
		}
		sellerInventory, _, err := inventories.Load(ctx, sellerID)
		if err != nil {
			return err
		}
		if sellerInventory.Slots == nil {
			sellerInventory.Slots = make(map[string]dnfrepo.ItemStack)
		}
		escrow, found, err := inventories.Load(ctx, EscrowCharacterID)
		if err != nil || !found {
			return errors.Join(ErrUnavailable, err)
		}
		now := o.clock()
		keys := make([]string, 0)
		for key := range escrow.Slots {
			if strings.HasPrefix(key, listingPrefix) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		expiredReturns := make([]Delivery, 0)
		for _, key := range keys {
			stack := escrow.Slots[key]
			if strings.TrimSpace(stack.Extra["seller_id"]) != sellerID {
				continue
			}
			id, parseErr := strconv.ParseUint(strings.TrimPrefix(key, listingPrefix), 10, 64)
			if parseErr != nil {
				continue
			}
			listing, parseErr := listingFromStack(id, stack)
			if parseErr != nil || listing.ExpiresAt.After(now) {
				continue
			}
			// An expired listing carrying a bid sold to that bidder and must
			// not go home to its seller. SettleWonListings owns those rows.
			if listing.BidTotal > 0 {
				continue
			}
			// Expired listings leave by system mail, exactly like a cancel:
			// 86JP's AuctionReturnService runs TryCancel and TryExpire through
			// one shared transition. A full inventory tab used to strand the
			// item here; the outbox has no such failure mode.
			deliveryID := deliveryKeyForExpiry(id)
			if _, exists := escrow.Slots[deliveryID]; exists {
				continue
			}
			returnedStack := cloneStack(stack)
			stripListingExtra(&returnedStack)
			deliveryStack := cloneStack(returnedStack)
			deliveryStack.Extra = cloneExtra(deliveryStack.Extra)
			deliveryStack.Extra["buyer_id"] = sellerID
			deliveryStack.Extra["listing_id"] = strconv.FormatUint(id, 10)
			deliveryStack.Extra["refund_gold"] = strconv.FormatInt(listing.Deposit, 10)
			escrow.Slots[deliveryID] = deliveryStack
			delete(escrow.Slots, key)
			expiredReturns = append(expiredReturns, Delivery{
				ID: deliveryID, ListingID: id, BuyerID: sellerID,
				Item: cloneStack(returnedStack), Gold: listing.Deposit, Return: true, Expired: true,
			})
		}
		if len(expiredReturns) != 0 {
			escrow.UpdatedAt = now
			if err := inventories.Save(ctx, escrow); err != nil {
				return err
			}
		}
		items := make([]dnfrepo.ItemStack, len(expiredReturns))
		for index := range expiredReturns {
			items[index] = cloneStack(expiredReturns[index].Item)
		}
		result = Result{Character: dnfrepo.CloneCharacter(seller), Inventory: dnfrepo.CloneInventory(sellerInventory), ClaimedItems: items, ExpiredReturns: expiredReturns, GoldAfter: seller.Stats["gold"]}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// SettleWonListings closes every listing whose duration ran out while a bid
// was standing on it: the item goes to that bidder and the proceeds to the
// seller. It is deliberately seller-agnostic and escrow-only - the winner's
// gold left their wallet when they bid, and the seller is paid through the
// payout outbox - so it needs neither party present, which is what lets the
// sweep run off any auction request instead of a scheduler. 86JP drives the
// same transition from AuctionExpirationScanner + AuctionReturnService.TryExpire.
//
// A listing with no bid is left alone: those go back to their seller through
// ReturnExpiredListings, which is where the seller's own bag and deposit are
// already in scope.
func (o *Owner) SettleWonListings(ctx context.Context) (Result, error) {
	if o == nil {
		return Result{}, ErrUnavailable
	}
	if o.repositories.CharacterItems == nil {
		return Result{}, ErrUnavailable
	}
	if err := o.EnsureEscrow(ctx); err != nil {
		return Result{}, err
	}
	var result Result
	// One escrow-scoped item transaction is the whole boundary: no wallet
	// moves here, so neither the winner nor the seller has to be locked.
	err := o.repositories.CharacterItems.WithinCharacterItems(ctx, EscrowCharacterID, func(inventories dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
		escrow, found, err := inventories.Load(ctx, EscrowCharacterID)
		if err != nil || !found {
			return errors.Join(ErrUnavailable, err)
		}
		now := o.clock()
		keys := make([]string, 0)
		for key := range escrow.Slots {
			if strings.HasPrefix(key, listingPrefix) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		settlements := make([]Settlement, 0)
		for _, key := range keys {
			stack := escrow.Slots[key]
			id, parseErr := strconv.ParseUint(strings.TrimPrefix(key, listingPrefix), 10, 64)
			if parseErr != nil {
				continue
			}
			listing, parseErr := listingFromStack(id, stack)
			if parseErr != nil || listing.ExpiresAt.After(now) || listing.BidTotal <= 0 {
				continue
			}
			deliveryID := deliveryKeyForBidWin(id)
			payoutID := payoutKeyForSale(id, 0)
			if _, exists := escrow.Slots[deliveryID]; exists {
				continue
			}
			if _, exists := escrow.Slots[payoutID]; exists {
				continue
			}
			// The winning bid is the sale price, so the commission comes off
			// it and the deposit rides home with the proceeds - the listing is
			// fully consumed either way.
			proceeds := listing.SellerProceeds(listing.BidTotal, true)
			if proceeds < 0 {
				continue
			}
			settled := listing.SaleStatementFor(listing.BidTotal, true)
			won := cloneStack(stack)
			stripListingExtra(&won)
			deliveryStack := cloneStack(won)
			deliveryStack.Extra = cloneExtra(deliveryStack.Extra)
			deliveryStack.Extra["buyer_id"] = listing.BidderID
			deliveryStack.Extra["listing_id"] = strconv.FormatUint(id, 10)
			escrow.Slots[deliveryID] = deliveryStack
			escrow.Slots[payoutID] = dnfrepo.ItemStack{
				ItemID: listing.Item.ItemID,
				Count:  proceeds,
				Extra: map[string]string{
					"seller_id":    listing.SellerID,
					"seller_name":  listing.SellerName,
					"listing_id":   strconv.FormatUint(id, 10),
					"sold_count":   strconv.FormatInt(won.Count, 10),
					"gross_gold":   strconv.FormatInt(settled.GrossGold, 10),
					"fee_gold":     strconv.FormatInt(settled.FeeGold, 10),
					"deposit_gold": strconv.FormatInt(settled.DepositGold, 10),
				},
			}
			recordAuctionSale(escrow.Slots, listing, listing.BidderID, won.Count, listing.BidTotal, 0, now, true)
			delete(escrow.Slots, key)
			settlements = append(settlements, Settlement{
				ListingID: id,
				Delivery:  Delivery{ID: deliveryID, ListingID: id, BuyerID: listing.BidderID, Item: cloneStack(won)},
				Payout: Payout{
					ID: payoutID, ListingID: id, SellerID: listing.SellerID, SellerName: listing.SellerName,
					ItemID: listing.Item.ItemID, SoldCount: won.Count, Gold: proceeds,
					Statement: settled, HasStatement: true,
				},
			})
		}
		if len(settlements) != 0 {
			escrow.UpdatedAt = now
			if err := inventories.Save(ctx, escrow); err != nil {
				return err
			}
		}
		result = Result{Settlements: settlements}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func listingFromStack(id uint64, stack dnfrepo.ItemStack) (Listing, error) {
	listType, err := strconv.ParseUint(stack.Extra["list_type"], 10, 8)
	if err != nil {
		return Listing{}, ErrListingMissing
	}
	slot, err := strconv.ParseInt(stack.Extra["source_slot"], 10, 16)
	if err != nil {
		return Listing{}, ErrListingMissing
	}
	price, err := strconv.ParseInt(stack.Extra["unit_price"], 10, 64)
	if err != nil || price <= 0 {
		return Listing{}, ErrListingMissing
	}
	// A listing escrowed before start_price existed carries no such key. It was
	// registered through a decoder that already required buyout-only or an
	// opening bid inside [0, unit_price], so -1 restores exactly what the row
	// used to project, rather than rejecting a live listing over a missing key.
	startPrice := int64(-1)
	if raw, present := stack.Extra["start_price"]; present {
		parsed, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || parsed < -1 || parsed > price {
			return Listing{}, ErrListingMissing
		}
		startPrice = parsed
	}
	// A listing escrowed before the buyout became optional always had one, and
	// it was always the unit price, so a missing key restores exactly what the
	// row used to project instead of retiring a live listing.
	buyoutPrice := price
	if raw, present := stack.Extra["buyout_price"]; present {
		parsed, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || (parsed != -1 && parsed != price) {
			return Listing{}, ErrListingMissing
		}
		buyoutPrice = parsed
	}
	// Charges arrived after these two keys, so a row without them is a
	// listing registered while the auction was free. Reading it as deposit 0 /
	// fee 0 settles it on the terms its seller actually accepted.
	deposit, err := listingExtraInt(stack, "deposit", 0, math.MaxInt64)
	if err != nil {
		return Listing{}, err
	}
	feePercent, err := listingExtraInt(stack, "fee_percent", 0, 100)
	if err != nil {
		return Listing{}, err
	}
	created, err := strconv.ParseInt(stack.Extra["created_at"], 10, 64)
	if err != nil {
		return Listing{}, ErrListingMissing
	}
	expires, err := strconv.ParseInt(stack.Extra["expires_at"], 10, 64)
	if err != nil {
		return Listing{}, ErrListingMissing
	}
	// Bid state arrived last of all, so a row without these keys is simply a
	// listing nobody has bid on - which is also exactly what every listing
	// escrowed before bidding existed is.
	bidTotal, err := listingExtraInt(stack, "bid_total", 0, math.MaxInt64)
	if err != nil {
		return Listing{}, err
	}
	bidAt, err := listingExtraInt(stack, "bid_at", 0, math.MaxInt64)
	if err != nil {
		return Listing{}, err
	}
	bidderID := strings.TrimSpace(stack.Extra["bidder_id"])
	bidderAccountID := strings.TrimSpace(stack.Extra["bidder_account_id"])
	// A half-written bid would let the gold in escrow belong to nobody, so it
	// retires the row instead of being read as "no bid".
	if (bidTotal > 0) != (bidderID != "" && bidderAccountID != "") {
		return Listing{}, ErrListingMissing
	}
	item := cloneStack(stack)
	stripListingExtra(&item)
	listing := Listing{
		ID: id, SellerID: stack.Extra["seller_id"], SellerName: stack.Extra["seller_name"],
		Item: item, ListType: byte(listType), SourceSlot: int16(slot),
		UnitPrice: price, StartPrice: startPrice, BuyoutPrice: buyoutPrice,
		Deposit: deposit, FeePercent: feePercent,
		BidTotal: bidTotal, BidderID: bidderID, BidderAccountID: bidderAccountID,
		CreatedAt: time.Unix(created, 0).UTC(), ExpiresAt: time.Unix(expires, 0).UTC(),
	}
	if bidAt > 0 {
		listing.BidAt = time.Unix(bidAt, 0).UTC()
	}
	return listing, nil
}

// writeListingBid stores the standing bid on the escrowed listing row, and
// clearListingBid removes it. Both operate on the row's Extra because the
// listing has no record of its own: the escrow stack *is* the listing.
func writeListingBid(stack *dnfrepo.ItemStack, bidderID, bidderAccountID string, total int64, at time.Time) {
	if stack == nil {
		return
	}
	stack.Extra = cloneExtra(stack.Extra)
	stack.Extra["bid_total"] = strconv.FormatInt(total, 10)
	stack.Extra["bidder_id"] = bidderID
	stack.Extra["bidder_account_id"] = bidderAccountID
	stack.Extra["bid_at"] = strconv.FormatInt(at.Unix(), 10)
}

func clearListingBid(stack *dnfrepo.ItemStack) {
	if stack == nil {
		return
	}
	stack.Extra = cloneExtra(stack.Extra)
	for _, key := range []string{"bid_total", "bidder_id", "bidder_account_id", "bid_at"} {
		delete(stack.Extra, key)
	}
}

func stripListingExtra(stack *dnfrepo.ItemStack) {
	if stack == nil {
		return
	}
	stack.Extra = cloneExtra(stack.Extra)
	for _, key := range []string{
		"seller_id", "seller_name", "list_type", "source_slot", "unit_price",
		"start_price", "buyout_price", "deposit", "fee_percent", "created_at", "expires_at",
		"bid_total", "bidder_id", "bidder_account_id", "bid_at",
	} {
		delete(stack.Extra, key)
	}
}

func allocateSlot(slots map[string]dnfrepo.ItemStack, listType byte, preferred int16) (int16, error) {
	ranges := [][2]int16{{9, 64}, {65, 120}, {121, 176}, {177, 232}, {233, 288}, {289, 344}, {345, 353}}
	if listType == 1 {
		ranges = [][2]int16{{0, 500}}
	}
	for _, bounds := range ranges {
		if preferred >= bounds[0] && preferred <= bounds[1] {
			if _, occupied := slots[inventoryKey(listType, preferred)]; !occupied {
				return preferred, nil
			}
			break
		}
	}
	for _, bounds := range ranges {
		for slot := bounds[0]; slot <= bounds[1]; slot++ {
			if _, occupied := slots[inventoryKey(listType, slot)]; !occupied {
				return slot, nil
			}
		}
	}
	return 0, ErrInventoryFull
}

func inventoryKey(listType byte, slot int16) string { return fmt.Sprintf("%d:%d", listType, slot) }
func listingKey(id uint64) string                   { return listingPrefix + strconv.FormatUint(id, 10) }
func payoutKey(id uint64) string                    { return payoutPrefix + strconv.FormatUint(id, 10) }
func payoutKeyForSale(id uint64, remaining int64) string {
	return payoutKey(id) + ":" + strconv.FormatInt(remaining, 10)
}
func deliveryKeyForSale(id uint64, remaining int64) string {
	return deliveryPrefix + strconv.FormatUint(id, 10) + ":" + strconv.FormatInt(remaining, 10)
}

// deliveryKeyForReturn names the outbox row for a cancelled listing, and
// deliveryKeyForExpiry the row for one that ran out its duration. A sale key
// ends in the remaining count, so neither suffix can collide with one.
func deliveryKeyForReturn(id uint64) string {
	return deliveryPrefix + strconv.FormatUint(id, 10) + ":return"
}

func deliveryKeyForExpiry(id uint64) string {
	return deliveryPrefix + strconv.FormatUint(id, 10) + ":expired"
}

// deliveryKeyForBidWin names the outbox row that hands an expired listing to
// the bidder who won it, and refundKeyForBid the row that pays a released bid
// back. A refund is keyed by the amount it releases so that the same listing
// can outbid several bidders in turn without two rows colliding, and so a
// retried release is idempotent.
func deliveryKeyForBidWin(id uint64) string {
	return deliveryPrefix + strconv.FormatUint(id, 10) + ":bid-win"
}

func refundKeyForBid(id uint64, bidderID string, total int64) string {
	return refundKeyPrefixForBid(id, bidderID) + strconv.FormatInt(total, 10)
}

func refundKeyPrefixForBid(id uint64, bidderID string) string {
	return payoutKey(id) + ":refund:" + bidderID + ":"
}

// listingExtraInt reads one optional numeric listing term. A missing key is
// the pre-charges default; a present but unparseable or out-of-range one is
// data corruption and retires the row rather than guessing.
func listingExtraInt(stack dnfrepo.ItemStack, key string, low, high int64) (int64, error) {
	raw, present := stack.Extra[key]
	if !present {
		return 0, nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < low || value > high {
		return 0, ErrListingMissing
	}
	return value, nil
}

func cloneStack(stack dnfrepo.ItemStack) dnfrepo.ItemStack {
	stack.RawEntry = append([]byte(nil), stack.RawEntry...)
	stack.Extra = cloneExtra(stack.Extra)
	return stack
}

func cloneExtra(extra map[string]string) map[string]string {
	result := make(map[string]string, len(extra)+8)
	for key, value := range extra {
		result[key] = value
	}
	return result
}

func (o *Owner) clock() time.Time {
	if o != nil && o.now != nil {
		return o.now().UTC()
	}
	return time.Now().UTC()
}
