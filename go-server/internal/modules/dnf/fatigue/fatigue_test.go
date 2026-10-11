package fatigue

import (
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestServiceDayRefillAndPremiumAllowance(t *testing.T) {
	before := time.Date(2026, 10, 1, 21, 59, 59, 0, time.UTC)
	after := before.Add(time.Second)
	account := dnfrepo.AccountRecord{AccountID: "a"}
	character := dnfrepo.CharacterRecord{Stats: map[string]int64{"fatigue": 1, "fatigue_limit": 156}}
	Refresh(&character, account, before)
	Debit(&character)
	Debit(&character)
	if character.Stats["fatigue"] != 0 {
		t.Fatal("fatigue underflow")
	}
	Refresh(&character, account, before)
	if character.Stats["fatigue"] != 0 {
		t.Fatal("same service day refilled")
	}
	Refresh(&character, account, after)
	if character.Stats["fatigue"] != 156 {
		t.Fatal("06:00 did not refill")
	}
	premium.Upsert(&account, 17, 30*86400, 1, after)
	Refresh(&character, account, after)
	if character.Stats["fatigue"] != 188 {
		t.Fatal("black diamond allowance missing")
	}
	Debit(&character)
	Refresh(&character, account, after)
	if character.Stats["fatigue"] != 187 {
		t.Fatal("premium relog refilled")
	}
	Refresh(&character, account, after.Add(31*24*time.Hour))
	if character.Stats["fatigue"] != 156 {
		t.Fatal("expired premium refill")
	}
}
