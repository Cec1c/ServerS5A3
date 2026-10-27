package raidcurrency

import (
	"context"
	"sync"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestFateMigrationAccountIsolationAndConcurrentReplay(t *testing.T) {
	ctx := context.Background()
	group := memory.NewMemoryGroup()
	now := fateTestTime(t, "2026-10-27T02:00:00+08:00")
	for _, a := range []string{"a", "b"} {
		if err := group.Account.Save(ctx, repo.AccountRecord{AccountID: a}); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range []string{"1", "2"} {
		if err := group.Character.Save(ctx, repo.CharacterRecord{CharacterID: c, AccountID: "a"}); err != nil {
			t.Fatal(err)
		}
		if err := group.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: c, Slots: map[string]repo.ItemStack{"0:121": {ItemID: int64(FateItemID), Count: int64((i + 1) * 75)}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := MigrateCharacter(ctx, group, "b", "1", now); err == nil {
		t.Fatal("foreign account migrated source")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for _, c := range []string{"1", "2", "1", "2", "1", "2", "1", "2"} {
		wg.Add(1)
		go func(id string) { defer wg.Done(); _, err := MigrateCharacter(ctx, group, "a", id, now); errs <- err }(c)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	a, _, err := group.Account.Load(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Read(a, now)
	if err != nil || s.Balance != 225 {
		t.Fatalf("balance=%+v err=%v", s, err)
	}
	b, _, err := group.Account.Load(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	s, err = Read(b, now)
	if err != nil || s.Balance != 0 {
		t.Fatalf("foreign balance=%+v err=%v", s, err)
	}
	for _, c := range []string{"1", "2"} {
		inv, _, err := group.Inventory.Load(ctx, c)
		if err != nil || len(inv.Slots) != 0 {
			t.Fatalf("source=%+v err=%v", inv, err)
		}
	}
}
