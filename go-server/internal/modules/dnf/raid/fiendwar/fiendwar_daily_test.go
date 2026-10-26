package fiendwar

import (
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestFiendWarDailyEntryBoundsResetAndIsolation(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	r, err := ReserveFiendWarDailyEntries(repository.SettingsRecord{}, []string{"a", "a", "b"}, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	before := repository.CloneSettings(r)
	if _, err := ReserveFiendWarDailyEntries(r, []string{"b", "a"}, 2, now); err == nil {
		t.Fatal("over limit accepted")
	}
	if !reflect.DeepEqual(before, r) {
		t.Fatal("rejected group consumed an earlier member's entry")
	}
	if got, _ := FiendWarDailyUsed(r, "a", now); got != 2 {
		t.Fatal(got)
	}
	if got, _ := FiendWarDailyUsed(r, "b", now); got != 1 {
		t.Fatal(got)
	}
	if got, _ := FiendWarDailyUsed(r, "a", now.Add(24*time.Hour)); got != 0 {
		t.Fatal("service day did not reset")
	}
	if _, err := ReserveFiendWarDailyEntries(r, []string{"a"}, 2, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
}
