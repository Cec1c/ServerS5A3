package raidcurrency

import (
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func fateTestTime(t *testing.T, value string) time.Time {
	t.Helper()
	now, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return now
}

func TestFateAccountCapSpendAndThursdayReset(t *testing.T) {
	before := fateTestTime(t, "2026-10-29T05:59:59+08:00")
	after := before.Add(time.Second)
	account := repo.AccountRecord{AccountID: "account-a"}
	if got, _, err := Credit(&account, 2499, before); err != nil || got != 2499 {
		t.Fatalf("got=%d err=%v", got, err)
	}
	if got, s, err := Credit(&account, 100, before); err != nil || got != 1 || s.Balance != 2500 || s.Earned != 2500 {
		t.Fatalf("got=%d s=%+v err=%v", got, s, err)
	}
	if _, err := Debit(&account, 40, before); err != nil {
		t.Fatal(err)
	}
	if got, s, err := Credit(&account, 100, before); err != nil || got != 0 || s.Balance != 2460 {
		t.Fatalf("spending must not reopen quota: got=%d s=%+v err=%v", got, s, err)
	}
	if got, s, err := Credit(&account, 100, after); err != nil || got != 100 || s.Balance != 2560 || s.Earned != 100 {
		t.Fatalf("reset: got=%d s=%+v err=%v", got, s, err)
	}
	other := repo.AccountRecord{AccountID: "account-b"}
	if s, err := Read(other, after); err != nil || s.Balance != 0 || s.Earned != 0 {
		t.Fatalf("other account=%+v err=%v", s, err)
	}
}

func TestFateConsolidationPreservesExistingMultipleCharactersAndRetries(t *testing.T) {
	now := fateTestTime(t, "2026-10-27T02:00:00+08:00")
	a := repo.AccountRecord{AccountID: "account-a"}
	first := repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:121": {ItemID: int64(FateItemID), Count: 3000}, "0:122": {ItemID: 42, Count: 9}}}
	second := repo.InventoryRecord{CharacterID: "2", Slots: map[string]repo.ItemStack{"0:130": {ItemID: int64(FateItemID), Count: 75}}}
	for _, inv := range []*repo.InventoryRecord{&first, &second} {
		if _, err := Consolidate(&a, inv, now); err != nil {
			t.Fatal(err)
		}
		if got, err := Consolidate(&a, inv, now); err != nil || got != 0 {
			t.Fatalf("retry got=%d err=%v", got, err)
		}
	}
	s, err := Read(a, now)
	if err != nil || s.Balance != 3075 || s.Earned != 0 || first.Slots["0:122"].Count != 9 || len(second.Slots) != 0 {
		t.Fatalf("s=%+v first=%+v second=%+v err=%v", s, first, second, err)
	}
}

func TestFateInvalidStateAndInsufficientFundsDoNotMutate(t *testing.T) {
	now := fateTestTime(t, "2026-10-27T02:00:00+08:00")
	for _, metadata := range []map[string]string{{balanceKey: "bad", weekKey: "1", earnedKey: "0"}, {balanceKey: "1"}, {balanceKey: "0", weekKey: "999999", earnedKey: "0"}, {balanceKey: "0", weekKey: "1", earnedKey: "2501"}} {
		a := repo.AccountRecord{AccountID: "a", Metadata: metadata}
		before := repo.CloneAccount(a)
		if _, _, err := Credit(&a, 100, now); err == nil || !reflect.DeepEqual(a, before) {
			t.Fatal("invalid state accepted or changed")
		}
	}
	a := repo.AccountRecord{AccountID: "a"}
	before := repo.CloneAccount(a)
	if _, err := Debit(&a, 1, now); err != ErrInsufficient || !reflect.DeepEqual(a, before) {
		t.Fatal("insufficient debit changed account")
	}
}
