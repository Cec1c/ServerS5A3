package raidcurrency

import (
	"context"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestFateAccountMigrationIncludesOfflineCharactersAndRetries(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	now := time.Now()
	for _, id := range []string{"a", "b"} {
		if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: id}); err != nil {
			t.Fatal(err)
		}
	}
	for slot, row := range []struct {
		id, account string
		amount      int64
	}{{"1", "a", 75}, {"2", "a", 100}, {"3", "b", 500}} {
		if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: row.id, AccountID: row.account, Slot: slot}); err != nil {
			t.Fatal(err)
		}
		if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: row.id, Slots: map[string]repo.ItemStack{"0:121": {ItemID: int64(FateItemID), Count: row.amount}, "0:122": {ItemID: 123, Count: 9}}}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := MigrateAccount(ctx, g, "a", now); err != nil || got != 175 {
		t.Fatalf("moved=%d err=%v", got, err)
	}
	if got, err := MigrateAccount(ctx, g, "a", now); err != nil || got != 0 {
		t.Fatalf("retry moved=%d err=%v", got, err)
	}
	a, _, _ := g.Account.Load(ctx, "a")
	s, err := Read(a, now)
	if err != nil || s.Balance != 175 || s.Earned != 0 {
		t.Fatalf("state=%+v err=%v", s, err)
	}
	for _, id := range []string{"1", "2"} {
		inv, _, _ := g.Inventory.Load(ctx, id)
		if len(inv.Slots) != 1 || inv.Slots["0:122"].Count != 9 {
			t.Fatalf("source not conserved: %+v", inv)
		}
	}
	inv, _, _ := g.Inventory.Load(ctx, "3")
	if inv.Slots["0:121"].Count != 500 {
		t.Fatal("another account was migrated")
	}
}
