package auction

import (
	"context"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

const saleHistoryPrefix = "auction:history:"

// SaleRecord只由已提交成交产生；与发货及收款outbox在同一背包事务内保存。
// 流拍、撤拍、未成交出价和邮递重试均不能充当成交样本。
type SaleRecord struct {
	ID, SellerID, BuyerID             string
	ListingID                         uint64
	ItemID, Count, TotalGold, FeeGold int64
	SoldAt                            time.Time
	Bidding                           bool
}

func recordAuctionSale(slots map[string]repo.ItemStack, listing Listing, buyer string, count, total, remaining int64, at time.Time, bidding bool) {
	key := saleHistoryPrefix + strconv.FormatUint(listing.ID, 10) + ":" + strconv.FormatInt(remaining, 10)
	if _, exists := slots[key]; exists {
		return
	}
	if count <= 0 || total <= 0 || buyer == "" {
		return
	}
	slots[key] = repo.ItemStack{ItemID: listing.Item.ItemID, Count: count, Extra: map[string]string{
		"listing_id": strconv.FormatUint(listing.ID, 10), "seller_id": listing.SellerID, "buyer_id": buyer,
		"total_gold": strconv.FormatInt(total, 10), "fee_gold": strconv.FormatInt(listing.SaleFee(total), 10),
		"sold_at": strconv.FormatInt(at.UTC().UnixNano(), 10), "bidding": strconv.FormatBool(bidding),
	}}
}

func saleRecordFromStack(key string, stack repo.ItemStack) (SaleRecord, bool) {
	if !strings.HasPrefix(key, saleHistoryPrefix) {
		return SaleRecord{}, false
	}
	id, e1 := strconv.ParseUint(stack.Extra["listing_id"], 10, 64)
	total, e2 := strconv.ParseInt(stack.Extra["total_gold"], 10, 64)
	fee, e3 := strconv.ParseInt(stack.Extra["fee_gold"], 10, 64)
	at, e4 := strconv.ParseInt(stack.Extra["sold_at"], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || id == 0 || stack.ItemID <= 0 || stack.Count <= 0 || total <= 0 || fee < 0 || fee > total || at <= 0 || stack.Extra["seller_id"] == "" || stack.Extra["buyer_id"] == "" {
		return SaleRecord{}, false
	}
	return SaleRecord{ID: key, ListingID: id, ItemID: stack.ItemID, Count: stack.Count, TotalGold: total, FeeGold: fee, SellerID: stack.Extra["seller_id"], BuyerID: stack.Extra["buyer_id"], SoldAt: time.Unix(0, at).UTC(), Bidding: stack.Extra["bidding"] == "true"}, true
}

func auctionSaleRecords(slots map[string]repo.ItemStack) []SaleRecord {
	var rows []SaleRecord
	for key, stack := range slots {
		if row, ok := saleRecordFromStack(key, stack); ok {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].SoldAt.Equal(rows[j].SoldAt) {
			return rows[i].ID > rows[j].ID
		}
		return rows[i].SoldAt.After(rows[j].SoldAt)
	})
	return rows
}

// SaleHistory校验角色归属，只暴露该角色参与的已完成交易。
func (o *Owner) SaleHistory(ctx context.Context, characterID, accountID string) ([]SaleRecord, error) {
	if o == nil || strings.TrimSpace(characterID) == "" || strings.TrimSpace(accountID) == "" {
		return nil, ErrInvalidRequest
	}
	c, found, err := o.repositories.Character.Load(ctx, characterID)
	if err != nil {
		return nil, err
	}
	if !found || c.AccountID != accountID || c.Stats["delete_flag"] != 0 {
		return nil, ErrNotOwner
	}
	bag, found, err := o.repositories.Inventory.Load(ctx, EscrowCharacterID)
	if err != nil || !found {
		return nil, err
	}
	var rows []SaleRecord
	for _, row := range auctionSaleRecords(bag.Slots) {
		if row.SellerID == characterID || row.BuyerID == characterID {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// RecentSaleAverage按最近20笔实际成交的数量加权；大整数避免累计溢出。
// 无成交时返回0，由调用方继续显示在售参考价或PVF价格，不能制造历史成交。
func (o *Owner) RecentSaleAverage(ctx context.Context, itemID int64) (int64, int, error) {
	if o == nil || itemID <= 0 {
		return 0, 0, ErrInvalidRequest
	}
	bag, found, err := o.repositories.Inventory.Load(ctx, EscrowCharacterID)
	if err != nil || !found {
		return 0, 0, err
	}
	total, quantity := new(big.Int), new(big.Int)
	samples := 0
	for _, row := range auctionSaleRecords(bag.Slots) {
		if row.ItemID != itemID {
			continue
		}
		total.Add(total, big.NewInt(row.TotalGold))
		quantity.Add(quantity, big.NewInt(row.Count))
		samples++
		if samples == 20 {
			break
		}
	}
	if samples == 0 {
		return 0, 0, nil
	}
	average := new(big.Int).Quo(total, quantity)
	if !average.IsInt64() {
		return 0, 0, ErrInvalidRequest
	}
	return average.Int64(), samples, nil
}
