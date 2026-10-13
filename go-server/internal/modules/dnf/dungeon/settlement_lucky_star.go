package dungeon

import (
	"context"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"strconv"
	"time"
)

// Values are resolved from the paired rental-system PVF before settlement.
type LuckyStarPolicy struct{ Limit, Amount uint32 }
type LuckyStarResult struct {
	Resolved             bool
	Before, After, Limit uint32
}

const luckyStarReceiptPrefix = "dungeon_settlement_lucky_star_"

func commitSettlementLuckyStar(ctx context.Context, tx dnfrepo.Group, character *dnfrepo.CharacterRecord, policy *LuckyStarPolicy, now time.Time) (LuckyStarResult, error) {
	for _, key := range []string{"resolved", "before", "after", "limit"} {
		delete(character.Stats, luckyStarReceiptPrefix+key)
	}
	if policy == nil {
		return LuckyStarResult{}, nil
	}
	if policy.Limit == 0 || policy.Amount == 0 || policy.Amount > policy.Limit || tx.Account == nil {
		return LuckyStarResult{}, ErrSettlementStateInvalid
	}
	account, found, err := tx.Account.Load(ctx, character.AccountID)
	if err != nil {
		return LuckyStarResult{}, err
	}
	if !found || account.AccountID != character.AccountID {
		return LuckyStarResult{}, ErrSettlementStateInvalid
	}
	raw := account.Metadata[LuckyStarMetadataKey]
	before := uint64(0)
	if raw != "" {
		before, err = strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return LuckyStarResult{}, ErrSettlementStateInvalid
		}
	}
	// Do not silently destroy a balance above a changed PVF cap.
	if before > uint64(policy.Limit) {
		return LuckyStarResult{}, ErrSettlementStateInvalid
	}
	after := before + uint64(policy.Amount)
	if after > uint64(policy.Limit) {
		after = uint64(policy.Limit)
	}
	result := LuckyStarResult{Resolved: true, Before: uint32(before), After: uint32(after), Limit: policy.Limit}
	if after != before {
		account = dnfrepo.CloneAccount(account)
		if err := dnfrepo.SaveAccountMetadataEntry(ctx, tx.Account, account, LuckyStarMetadataKey, strconv.FormatUint(after, 10), now); err != nil {
			return LuckyStarResult{}, err
		}
	}
	character.Stats[luckyStarReceiptPrefix+"resolved"] = 1
	character.Stats[luckyStarReceiptPrefix+"before"] = int64(result.Before)
	character.Stats[luckyStarReceiptPrefix+"after"] = int64(result.After)
	character.Stats[luckyStarReceiptPrefix+"limit"] = int64(result.Limit)
	return result, nil
}

func readSettlementLuckyStarReceipt(stats map[string]int64) (LuckyStarResult, error) {
	flag, present := stats[luckyStarReceiptPrefix+"resolved"]
	if !present {
		return LuckyStarResult{}, nil
	} // Older receipts cannot be awarded retroactively.
	if flag != 1 {
		return LuckyStarResult{}, ErrSettlementStateInvalid
	}
	values := make([]uint32, 3)
	for i, key := range []string{"before", "after", "limit"} {
		value, ok := stats[luckyStarReceiptPrefix+key]
		if !ok || value < 0 || value > math.MaxUint32 {
			return LuckyStarResult{}, ErrSettlementStateInvalid
		}
		values[i] = uint32(value)
	}
	if values[2] == 0 || values[0] > values[1] || values[1] > values[2] {
		return LuckyStarResult{}, ErrSettlementStateInvalid
	}
	return LuckyStarResult{Resolved: true, Before: values[0], After: values[1], Limit: values[2]}, nil
}
