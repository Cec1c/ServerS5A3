package itemlock

import (
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// A missing/malformed deadline never silently removes legacy protection.
func unlockDeadline(stack dnfrepo.ItemStack) (time.Time, bool) {
	n, err := strconv.ParseInt(stack.Extra["equipment_unlock_at"], 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}, false
	}
	return time.Unix(n, 0), true
}
func remainingUntil(deadline, now time.Time) int32 {
	seconds := deadline.Unix() - now.Unix()
	if seconds <= 0 {
		return 0
	}
	if seconds > 2147483647 {
		return 2147483647
	}
	return int32(seconds)
}
func IsLockedAt(stack dnfrepo.ItemStack, now time.Time) bool {
	switch strings.ToLower(strings.TrimSpace(stack.Extra["equipment_lock_state"])) {
	case "1", "active", "locked":
		return true
	case "2", "unlocking", "pending_unlock":
		deadline, ok := unlockDeadline(stack)
		return !ok || now.Before(deadline)
	default:
		return false
	}
}
func IsLocked(stack dnfrepo.ItemStack) bool { return IsLockedAt(stack, time.Now()) }

func isPendingState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "2", "unlocking", "pending_unlock":
		return true
	default:
		return false
	}
}
