package npcshop

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrPurchaseLimit = errors.New("NPC shop purchase limit exceeded")

type PurchaseLimit struct {
	ItemID uint32
	Scope  string
	// Period is the purchase window period: "", "daily", or "weekly". An empty
	// period is the legacy daily form.
	Period     string
	Maximum    int64
	ResetDaily bool
	Count      int64
}

func purchaseLimitKeys(itemID uint32) (string, string) {
	stem := "npc_shop_purchase_" + strconv.FormatUint(uint64(itemID), 10)
	return stem + "_day", stem + "_count"
}

// validPurchaseLimitPeriod reports whether a period is representable. An empty
// period is the legacy daily form.
func validPurchaseLimitPeriod(period string) bool {
	switch strings.ToLower(strings.TrimSpace(period)) {
	case "", "daily", "weekly":
		return true
	}
	return false
}

// purchaseLimitBucket returns the persisted window bucket for a policy.
//
// Window policy: this is an explicit LOCAL OPERATING POLICY, not something the
// PVF proves. The weekly shop resource (itemshop/2018_epicroad_kairyu.shp ->
// 490010214) declares "account weekly 70": period and count are
// resource-derived, the weekday is not. This server fixes the weekly reset at
// Thursday 06:00 China time, the same declaration used by
// onlineevent.weeklySeasonStart and by the raid weekly policy default.
//
// premium.ServiceDayID already buckets by the 06:00 Beijing service day, and
// its day 0 starts at 1970-01-01 06:00 Beijing, a Thursday, so ServiceDayID/7
// buckets are exactly Thursday-06:00 Chinese weeks. npcshop intentionally does
// not import the onlineevent or raid packages.
//
//   - weekly  -> Thursday-06:00 Chinese week bucket
//   - daily   -> 06:00 Beijing service day bucket, regardless of the
//     optional reset marker: the explicit period is authoritative
//   - legacy (empty period) without reset -> bucket 0, never resets
//   - legacy (empty period) with reset    -> 06:00 Beijing service day bucket
func purchaseLimitBucket(policy PurchaseLimit, now time.Time) int64 {
	switch strings.ToLower(strings.TrimSpace(policy.Period)) {
	case "weekly":
		return premium.ServiceDayID(now) / 7
	case "daily":
		// The explicit three-token form [purchase limit] <scope> daily <count>
		// declares a daily window on its own and must reset at the 06:00
		// Beijing service day even when the optional reset marker is absent.
		return premium.ServiceDayID(now)
	}
	// Legacy two-token [daily purchase limit] <scope> <count> keeps its
	// historical ResetDaily semantics: no marker means the cumulative bucket 0
	// that never resets.
	if policy.ResetDaily {
		return premium.ServiceDayID(now)
	}
	return 0
}

// PurchaseCount reads the persisted limit without resetting or writing records.
func PurchaseCount(account dnfrepo.AccountRecord, character dnfrepo.CharacterRecord, policy PurchaseLimit, now time.Time) int64 {
	if !validPurchaseLimitPeriod(policy.Period) {
		return 0
	}
	dayKey, countKey := purchaseLimitKeys(policy.ItemID)
	day, count := character.Stats[dayKey], character.Stats[countKey]
	if policy.Scope == "account" {
		day, _ = strconv.ParseInt(account.Metadata[dayKey], 10, 64)
		count, _ = strconv.ParseInt(account.Metadata[countKey], 10, 64)
	}
	if day != purchaseLimitBucket(policy, now) || count < 0 {
		return 0
	}
	return count
}

func consumePurchaseLimit(assets *Assets, policy *PurchaseLimit, now time.Time) (Changes, error) {
	if policy == nil {
		return Changes{}, nil
	}
	if assets == nil || assets.Account == nil || assets.Character == nil || policy.ItemID == 0 || policy.Maximum <= 0 || policy.Count <= 0 ||
		(policy.Scope != "account" && policy.Scope != "character") || !validPurchaseLimitPeriod(policy.Period) {
		return Changes{}, ErrPurchaseLimit
	}
	count := PurchaseCount(*assets.Account, *assets.Character, *policy, now)
	if count > policy.Maximum || policy.Count > policy.Maximum-count {
		return Changes{}, fmt.Errorf("%w: item=%d scope=%s maximum=%d purchased=%d requested=%d", ErrPurchaseLimit, policy.ItemID, policy.Scope, policy.Maximum, count, policy.Count)
	}
	dayKey, countKey := purchaseLimitKeys(policy.ItemID)
	day := purchaseLimitBucket(*policy, now)
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
