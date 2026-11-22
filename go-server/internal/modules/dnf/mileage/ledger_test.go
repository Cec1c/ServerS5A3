package mileage

import (
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestMileageSpendRemainderCapAndRedemption(t *testing.T) {
	a := r.AccountRecord{}
	for i := 0; i < 100; i++ {
		if err := AccrueSpend(&a, 1, MaximumCashEarned); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := Load(a)
	if s.Balance != 3 || s.CashEarned != 3 || s.Remainder != 0 {
		t.Fatal(s)
	}
	if err := AccrueSpend(&a, 1000000, MaximumCashEarned); err != nil {
		t.Fatal(err)
	}
	s, _ = Load(a)
	if s.Balance != 20000 || s.CashEarned != 20000 || s.Remainder != 0 {
		t.Fatal(s)
	}
	if err := Redeem(&a, "19", "nonce", 1, []Product{{Commodity: 1, Price: 10, Count: 1, Limit: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := AccrueSpend(&a, 1000000, MaximumCashEarned); err != nil {
		t.Fatal(err)
	}
	s, _ = Load(a)
	if s.Balance != 19990 || s.CashEarned != 20000 {
		t.Fatal(s)
	}
	before := a.Metadata[MetadataKey]
	if err := Redeem(&a, "19", "nonce", 1, []Product{{Commodity: 2, Price: 1, Count: 1}}); !errors.Is(err, ErrReplay) {
		t.Fatal(err)
	}
	if err := Redeem(&a, "20", "other", 2, []Product{{Commodity: 1, Price: 10, Count: 1, Limit: 1}}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := Redeem(&a, "20", "other", 2, []Product{{Commodity: 2, Price: 20000, Count: 1}}); !errors.Is(err, ErrInsufficient) {
		t.Fatal(err)
	}
	if a.Metadata[MetadataKey] != before {
		t.Fatal("rejected cart mutated ledger")
	}
}

func TestMileageClearAccountDailyBoundaryAndRollback(t *testing.T) {
	a := r.AccountRecord{}
	now := time.Date(2026, 11, 17, 21, 0, 0, 0, time.FixedZone("CST", 8*3600))
	for i := 0; i < 70; i++ {
		if err := AccrueClear(&a, now, true); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := Load(a)
	if s.Balance != 50 || s.DailyEarned != 50 {
		t.Fatal(s)
	}
	if err := AccrueClear(&a, now.Add(8*time.Hour+59*time.Minute), true); err != nil {
		t.Fatal(err)
	}
	s, _ = Load(a)
	if s.Balance != 50 {
		t.Fatal(s)
	}
	if err := AccrueClear(&a, now.Add(9*time.Hour), true); err != nil {
		t.Fatal(err)
	}
	s, _ = Load(a)
	if s.Balance != 51 || s.DailyEarned != 1 {
		t.Fatal(s)
	}
	before := a.Metadata[MetadataKey]
	if err := AccrueClear(&a, now, true); !errors.Is(err, ErrInvalid) || a.Metadata[MetadataKey] != before {
		t.Fatal(err)
	}
	if err := AccrueClear(&a, now.Add(24*time.Hour), false); err != nil {
		t.Fatal(err)
	}
	s, _ = Load(a)
	if s.Balance != 51 {
		t.Fatal(s)
	}
}
