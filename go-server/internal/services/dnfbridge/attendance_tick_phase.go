package dnfbridge

import (
	"hash/fnv"
	"time"
)

// Four reconciliation timers get distinct phases within the existing 30s
// period, spread by account. Claim handlers remain immediate and authoritative;
// the spend-time persistence timer and its crash-loss interval are unchanged.
func currentAttendanceTickDelay(now time.Time, accountID string, kind int) time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(accountID))
	phase := time.Duration(h.Sum32()%6000)*time.Millisecond + time.Duration(kind%4)*6*time.Second
	next := now.Truncate(30 * time.Second).Add(phase)
	if !next.After(now) {
		next = next.Add(30 * time.Second)
	}
	return next.Sub(now)
}
