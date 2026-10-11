package itemexpiration

import (
	"fmt"
	"strconv"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const DailyDeleteDeadlineKey = "daily_delete_at"

var dailyDeleteLocation = time.FixedZone("CST", 8*60*60)

// NextDailyDeleteAt returns the first 06:00 service-day boundary strictly
// after acquisition. A material obtained at exactly 06:00 belongs to the new
// day. UTC persistence does not depend on the server operating-system zone.
func NextDailyDeleteAt(now time.Time) time.Time {
	local := now.In(dailyDeleteLocation)
	boundary := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, dailyDeleteLocation)
	if !now.Before(boundary) {
		boundary = boundary.AddDate(0, 0, 1)
	}
	return boundary.UTC()
}

// ReconcileDailyDelete is called only for a definition whose real PVF has
// [daily delete item] 1. Existing persistent deadlines never move on relogin.
// Legacy instances lack an acquisition timestamp; first observation assigns
// the next boundary rather than guessing that they were acquired yesterday.
func ReconcileDailyDelete(stack dnfrepo.ItemStack, now time.Time) (dnfrepo.ItemStack, bool, bool, error) {
	deadline := NextDailyDeleteAt(now)
	if raw := stack.Extra[DailyDeleteDeadlineKey]; raw != "" {
		unix, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || unix <= 0 || unix > 1<<32-1 {
			return stack, false, false, fmt.Errorf("invalid daily-delete deadline item=%d", stack.ItemID)
		}
		deadline = time.Unix(unix, 0).UTC()
		if !now.Before(deadline) {
			return stack, false, true, nil
		}
	}
	cloned := make(map[string]string, len(stack.Extra)+3)
	for key, value := range stack.Extra {
		cloned[key] = value
	}
	changed := false
	value := strconv.FormatInt(deadline.Unix(), 10)
	for _, key := range []string{DailyDeleteDeadlineKey, "expire_time", "expire_unix"} {
		if cloned[key] != value {
			cloned[key] = value
			changed = true
		}
	}
	if !stack.ExpireAt.Equal(deadline) {
		stack.ExpireAt = deadline
		changed = true
	}
	stack.Extra = cloned
	return stack, changed, false, nil
}
