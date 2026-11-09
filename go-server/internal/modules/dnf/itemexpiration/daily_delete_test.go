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

// TestDailyDeleteBoundaryAtOrAfterSnapsUsablePeriod 锁住 2026-11-07 的修复：
// PVF [usable period] 算出来的绝对到期时间必须吸附到服务日 06:00，
// 否则同一服务日连开的同一限期道具各得到不同秒，堆叠永远合并不上。
func TestDailyDeleteBoundaryAtOrAfterSnapsUsablePeriod(t *testing.T) {
	for _, tc := range []struct {
		local, want string
	}{
		{"2026-12-06T03:00:00+08:00", "2026-12-06T06:00:00+08:00"},
		{"2026-12-06T06:00:00+08:00", "2026-12-06T06:00:00+08:00"},
		{"2026-12-06T15:43:43+08:00", "2026-12-07T06:00:00+08:00"},
		{"2026-12-06T23:59:59+08:00", "2026-12-07T06:00:00+08:00"},
		{"2026-12-31T20:00:00+08:00", "2027-01-01T06:00:00+08:00"},
	} {
		deadline, _ := time.Parse(time.RFC3339, tc.local)
		want, _ := time.Parse(time.RFC3339, tc.want)
		got := DailyDeleteBoundaryAtOrAfter(deadline.UTC())
		if !got.Equal(want) || got.Location() != time.UTC {
			t.Fatalf("deadline=%s got=%s want=%s", tc.local, got, want)
		}
	}
}

// TestDailyDeleteBoundaryAtOrAfterSameServiceDayIsIdentical 是这次修复的判据本身：
// 同一服务日内不同秒发放的限期道具，必须得到同一个到期时间（堆叠合并要求整数秒相等）。
func TestDailyDeleteBoundaryAtOrAfterSameServiceDayIsIdentical(t *testing.T) {
	base := time.Date(2026, 12, 6, 15, 43, 43, 0, dailyDeleteLocation)
	var first time.Time
	for i := 0; i < 60; i++ {
		at := DailyDeleteBoundaryAtOrAfter(base.Add(time.Duration(i*7) * time.Second))
		if i == 0 {
			first = at
			continue
		}
		if !at.Equal(first) {
			t.Fatalf("同服务日不同秒得到不同到期时间：%s vs %s", first, at)
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
