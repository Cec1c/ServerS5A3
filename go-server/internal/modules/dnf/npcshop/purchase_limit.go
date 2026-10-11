package npcshop

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrPurchaseLimit = errors.New("NPC shop purchase limit exceeded")

type PurchaseLimit struct {
	ItemID     uint32
	Scope      string
	Maximum    int64
	ResetDaily bool
	Count      int64
}

func purchaseLimitKeys(itemID uint32) (string, string) {
	stem := "npc_shop_purchase_" + strconv.FormatUint(uint64(itemID), 10)
	return stem + "_day", stem + "_count"
}

func purchaseLimitDay(policy PurchaseLimit, now time.Time) int64 {
	if policy.ResetDaily {
		return premium.ServiceDayID(now)
	}
	return 0
}

// PurchaseCount reads the persisted limit without resetting or writing records.
func PurchaseCount(account dnfrepo.AccountRecord, character dnfrepo.CharacterRecord, policy PurchaseLimit, now time.Time) int64 {
	dayKey, countKey := purchaseLimitKeys(policy.ItemID)
	day, count := character.Stats[dayKey], character.Stats[countKey]
	if policy.Scope == "account" {
		day, _ = strconv.ParseInt(account.Metadata[dayKey], 10, 64)
		count, _ = strconv.ParseInt(account.Metadata[countKey], 10, 64)
	}
	if day != purchaseLimitDay(policy, now) || count < 0 {
		return 0
	}
	return count
}

func consumePurchaseLimit(assets *Assets, policy *PurchaseLimit, now time.Time) (Changes, error) {
	if policy == nil {
		return Changes{}, nil
	}
	if assets == nil || assets.Account == nil || assets.Character == nil || policy.ItemID == 0 || policy.Maximum <= 0 || policy.Count <= 0 || (policy.Scope != "account" && policy.Scope != "character") {
		return Changes{}, ErrPurchaseLimit
	}
	count := PurchaseCount(*assets.Account, *assets.Character, *policy, now)
	if count > policy.Maximum || policy.Count > policy.Maximum-count {
		return Changes{}, fmt.Errorf("%w: item=%d scope=%s maximum=%d purchased=%d requested=%d", ErrPurchaseLimit, policy.ItemID, policy.Scope, policy.Maximum, count, policy.Count)
	}
	dayKey, countKey := purchaseLimitKeys(policy.ItemID)
	day := purchaseLimitDay(*policy, now)
	if policy.Scope == "account" {
		if assets.Account.Metadata == nil {
			assets.Account.Metadata = make(map[string]string)
		}
		assets.Account.Metadata[dayKey] = strconv.FormatInt(day, 10)
		assets.Account.Metadata[countKey] = strconv.FormatInt(count+policy.Count, 10)
		return Changes{Account: true}, nil
	}
	if assets.Character.Stats == nil {
		assets.Character.Stats = make(map[string]int64)
	}
	assets.Character.Stats[dayKey], assets.Character.Stats[countKey] = day, count+policy.Count
	return Changes{Character: true}, nil
}
