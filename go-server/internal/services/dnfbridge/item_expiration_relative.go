package dnfbridge

import (
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Only a recorded relative-period grant is eligible here. Absolute retired
// event dates, GM overrides, rentals and missing/conflicting provenance retain
// their existing owners. Projection alone never deletes an item.
func currentPVFRelativeGrantExpired(stack dnfrepo.ItemStack, definition dungeonDropItemDefinition, now time.Time) bool {
	if definition.UsablePeriodDays <= 0 || !definition.ExpirationDate.IsZero() ||
		stack.Extra["expiration_source"] != "runtime_pvf_usable_period_grant" ||
		stack.ExpireAt.IsZero() || now.IsZero() || stack.ExpireAt.After(now) {
		return false
	}
	days, err := strconv.ParseInt(strings.TrimSpace(stack.Extra["usable_period_days"]), 10, 64)
	if err != nil || days != definition.UsablePeriodDays {
		return false
	}
	deadline := stack.ExpireAt.Unix()
	if deadline <= 0 {
		return false
	}
	for _, key := range []string{"expire_time", "expire_unix"} {
		value, err := strconv.ParseInt(strings.TrimSpace(stack.Extra[key]), 10, 64)
		if err != nil || value != deadline {
			return false
		}
	}
	return true
}
