package adventuregroup

import (
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestRecordedDailyLoginDaysDoesNotObserveOrRepair(t *testing.T) {
	for _, tc := range []struct {
		value   string
		days    uint32
		invalid bool
	}{
		{"", 0, false}, {"2026-07-16|7", 7, false}, {"invalid", 0, true}, {"2026-07-16|0", 0, true},
	} {
		a := dnfrepo.AccountRecord{Metadata: map[string]string{DailyLoginStateMetadataKey: tc.value}}
		days, err := RecordedDailyLoginDays(a)
		if days != tc.days || (err != nil) != tc.invalid || a.Metadata[DailyLoginStateMetadataKey] != tc.value {
			t.Fatalf("%q days=%d err=%v", tc.value, days, err)
		}
	}
}
