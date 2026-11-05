package wedding

import (
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemlock"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// usableAssetAt reads every persisted expiration representation. A missing
// field means no deadline; malformed or expired metadata cannot be normalized
// into a usable asset by moving it between the shared room and its inventory.
func usableAssetAt(stack r.ItemStack, now time.Time) bool {
	if itemlock.IsLockedAt(stack, now) || !stack.ExpireAt.IsZero() && !now.Before(stack.ExpireAt) {
		return false
	}
	for _, key := range []string{"expire_time", "expire_unix"} {
		text := strings.TrimSpace(stack.Extra[key])
		if text == "" {
			continue
		}
		deadline, err := strconv.ParseInt(text, 10, 64)
		if err != nil || deadline < 0 || deadline > 0 && deadline <= now.Unix() {
			return false
		}
	}
	return true
}
