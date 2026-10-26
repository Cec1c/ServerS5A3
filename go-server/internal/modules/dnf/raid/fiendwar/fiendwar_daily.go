package fiendwar

import (
	"fmt"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	"longheng.io/server/internal/modules/dnf/repository"
)

const FiendWarDailyScope = "raid:fiendwar:daily_entries"

func FiendWarDailyUsed(record repository.SettingsRecord, account string, now time.Time) (int, error) {
	if record.Values["day"] != strconv.FormatInt(premium.ServiceDayID(now), 10) {
		return 0, nil
	}
	value := record.Values["account:"+account]
	if value == "" {
		return 0, nil
	}
	used, err := strconv.Atoi(value)
	if err != nil || used < 0 {
		return 0, fmt.Errorf("invalid fiendwar daily entry record")
	}
	return used, nil
}

// Called inside the same roster inventory/settings transaction as entry tickets.
func ReserveFiendWarDailyEntries(record repository.SettingsRecord, accounts []string, limit int, now time.Time) (repository.SettingsRecord, error) {
	if limit <= 0 || len(accounts) == 0 {
		return record, fmt.Errorf("fiendwar daily entry configuration missing")
	}
	next := repository.CloneSettings(record)
	day := strconv.FormatInt(premium.ServiceDayID(now), 10)
	if next.Values["day"] != day {
		next.Values = map[string]string{"day": day}
	}
	for _, account := range accounts {
		if account == "" {
			return record, fmt.Errorf("fiendwar daily entry account missing")
		}
		used, err := FiendWarDailyUsed(next, account, now)
		if err != nil {
			return record, err
		}
		if used >= limit {
			return record, fmt.Errorf("fiendwar daily account entry limit reached")
		}
		next.Values["account:"+account] = strconv.Itoa(used + 1)
	}
	next.Scope, next.UpdatedAt = FiendWarDailyScope, now
	return next, nil
}
