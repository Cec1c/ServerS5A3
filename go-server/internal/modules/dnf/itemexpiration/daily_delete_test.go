package itemexpiration

import (
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestDailyDeleteServiceBoundaryAndAcquisitionAtSix(t *testing.T) {
	for _, tc := range []struct {
		local, want string
	}{
		{"2026-10-06T05:59:59+08:00", "2026-10-06T06:00:00+08:00"},
		{"2026-10-06T06:00:00+08:00", "2026-10-07T06:00:00+08:00"},
		{"2026-10-06T23:59:59+08:00", "2026-10-07T06:00:00+08:00"},
		{"2026-12-31T23:59:59+08:00", "2027-01-01T06:00:00+08:00"},
	} {
		now, _ := time.Parse(time.RFC3339, tc.local)
		want, _ := time.Parse(time.RFC3339, tc.want)
		if got := NextDailyDeleteAt(now.UTC()); !got.Equal(want) || got.Location() != time.UTC {
			t.Fatalf("at %s deadline=%s want=%s", tc.local, got, want)
		}
	}
}

func TestDailyDeleteReloginDoesNotRenewAndExpiresAtExactBoundary(t *testing.T) {
	now := time.Date(2026, 10, 6, 13, 0, 0, 0, dailyDeleteLocation)
	original := dnfrepo.ItemStack{ItemID: 10096296, Count: 3, Extra: map[string]string{"source": "quest", "unique": "x"}, RawEntry: []byte{1, 2, 3}}
	got, changed, expired, err := ReconcileDailyDelete(original, now)
	if err != nil || !changed || expired || original.Extra[DailyDeleteDeadlineKey] != "" || got.Count != original.Count || !reflect.DeepEqual(got.RawEntry, original.RawEntry) {
		t.Fatalf("first projection=%+v changed=%t expired=%t err=%v", got, changed, expired, err)
	}
	deadline := got.ExpireAt
	again, changed, expired, err := ReconcileDailyDelete(got, deadline.Add(-time.Nanosecond))
	if err != nil || changed || expired || !reflect.DeepEqual(again, got) {
		t.Fatalf("relogin changed deadline: %+v changed=%t expired=%t err=%v", again, changed, expired, err)
	}
	for _, at := range []time.Time{deadline, deadline.Add(72 * time.Hour)} {
		again, changed, expired, err := ReconcileDailyDelete(got, at)
		if err != nil || changed || !expired || !reflect.DeepEqual(again, got) {
			t.Fatalf("overdue at %s changed=%t expired=%t err=%v", at, changed, expired, err)
		}
	}
}

func TestDailyDeleteInvalidPersistentDeadlinePreservesInstance(t *testing.T) {
	for _, raw := range []string{"garbage", "-1", "4294967296"} {
		stack := dnfrepo.ItemStack{ItemID: 10096296, Count: 3, Extra: map[string]string{DailyDeleteDeadlineKey: raw}}
		got, changed, expired, err := ReconcileDailyDelete(stack, time.Now())
		if err == nil || changed || expired || !reflect.DeepEqual(got, stack) {
			t.Fatalf("invalid marker %q changed=%t expired=%t err=%v", raw, changed, expired, err)
		}
	}
}
