package fatigue

import (
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"time"
)

const DayKey = "fatigue_service_day_id"

func Limit(account dnfrepo.AccountRecord, now time.Time) int64 {
	for _, kind := range []int64{17} {
		if premium.Active(account, kind, now) {
			return 188
		}
	}
	return 156
}

// The first migration preserves a known balance. Subsequent 06:00 UTC+8
// service days refill it; an entitlement increase adds only the new allowance.
func Refresh(character *dnfrepo.CharacterRecord, account dnfrepo.AccountRecord, now time.Time) bool {
	if character.Stats == nil {
		character.Stats = make(map[string]int64)
	}
	stats := character.Stats
	day, limit := premium.ServiceDayID(now), Limit(account, now)
	oldLimit, hadLimit := stats["fatigue_limit"]
	remaining, known := stats["fatigue"]
	oldDay, dayKnown := stats[DayKey]
	changed := !dayKnown || oldDay != day || oldLimit != limit || !known
	originalRemaining := remaining
	if !known || (dayKnown && oldDay != day) {
		remaining = limit
		for key := range stats {
			if strings.HasPrefix(key, "fatigue_potion_used_") || strings.HasPrefix(key, "fatigue_potion_time_") {
				stats[key] = 0
			}
			if strings.HasPrefix(key, "fatigue_potion_day_") {
				stats[key] = day
			}
		}
	} else if hadLimit && limit > oldLimit {
		remaining += limit - oldLimit
	}
	if remaining < 0 {
		remaining = 0
	}
	if remaining > limit {
		remaining = limit
	}
	changed = changed || originalRemaining != remaining || stats["fatigue_used"] != limit-remaining || stats["fatigue_display_update"] != limit-remaining
	stats[DayKey], stats["fatigue_limit"], stats["fatigue"] = day, limit, remaining
	stats["fatigue_used"], stats["fatigue_display_update"] = limit-remaining, limit-remaining
	return changed
}

func Debit(character *dnfrepo.CharacterRecord) {
	remaining := character.Stats["fatigue"]
	if remaining > 0 {
		remaining--
	}
	character.Stats["fatigue"] = remaining
	character.Stats["fatigue_used"] = character.Stats["fatigue_limit"] - remaining
	character.Stats["fatigue_display_update"] = character.Stats["fatigue_used"]
}
