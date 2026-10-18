package memory

import (
	"context"
	"errors"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

type failGuildSettingsCommit struct {
	r.SettingsRepository
	failed bool
}

func (f *failGuildSettingsCommit) Save(ctx context.Context, record r.SettingsRecord) error {
	if !f.failed {
		f.failed = true
		return errors.New("injected late memory commit failure")
	}
	return f.SettingsRepository.Save(ctx, record)
}

func TestGuildInventoryTransactionScopeAndCommitRollback(t *testing.T) {
	ctx := context.Background()
	repos := NewMemoryGroup()
	owner := r.GuildOwner{CharacterID: "1", AccountID: "a1"}
	c := r.CharacterRecord{CharacterID: "1", AccountID: "a1", Stats: map[string]int64{"gold": 42}}
	beforeInv := r.InventoryRecord{CharacterID: "1", Slots: map[string]r.ItemStack{"0:87": {ItemID: 10153530, Count: 2}}}
	beforeSettings := r.SettingsRecord{Scope: r.GuildRegistryScope, Values: map[string]string{"test": "before"}}
	for _, err := range []error{repos.Character.Save(ctx, c), repos.Inventory.Save(ctx, beforeInv), repos.Settings.Save(ctx, beforeSettings)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	u := repos.Guild.(*memoryGuildUnitOfWork)
	if err := u.WithinGuild(ctx, []r.GuildOwner{owner}, func(tx r.Group) error {
		if tx.Inventory != nil {
			t.Fatal("ordinary guild scope gained inventory")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := u.WithinGuildInventory(ctx, owner, func(tx r.Group) error {
		if tx.Account != nil || tx.Equipment != nil || tx.Guild != nil || tx.AccountInventory != nil {
			t.Fatal("unexpected repository capability")
		}
		if _, _, err := tx.Inventory.Load(ctx, "2"); !errors.Is(err, r.ErrGuildTransaction) {
			t.Fatal("foreign inventory readable")
		}
		if err := tx.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "2"}); !errors.Is(err, r.ErrGuildTransaction) {
			t.Fatal("foreign inventory writable")
		}
		if _, _, err := tx.Settings.Load(ctx, "other"); !errors.Is(err, r.ErrGuildTransaction) {
			t.Fatal("foreign settings readable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	u.settings = &failGuildSettingsCommit{SettingsRepository: repos.Settings}
	if err := u.WithinGuildInventory(ctx, owner, func(tx r.Group) error {
		inv, _, _ := tx.Inventory.Load(ctx, "1")
		delete(inv.Slots, "0:87")
		if err := tx.Inventory.Save(ctx, inv); err != nil {
			return err
		}
		return tx.Settings.Save(ctx, r.SettingsRecord{Scope: r.GuildRegistryScope, Values: map[string]string{"test": "after"}})
	}); err == nil {
		t.Fatal("late commit failure ignored")
	}
	inv, _, _ := repos.Inventory.Load(ctx, "1")
	settings, _, _ := repos.Settings.Load(ctx, r.GuildRegistryScope)
	if !reflect.DeepEqual(inv, beforeInv) || !reflect.DeepEqual(settings, beforeSettings) {
		t.Fatal("late memory failure did not restore inventory and settings")
	}
}
