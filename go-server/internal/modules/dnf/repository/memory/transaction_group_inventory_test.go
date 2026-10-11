package memory

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

type groupLateInventoryFailure struct {
	repository.InventoryRepository
	failID string
	failed bool
}

func (r *groupLateInventoryFailure) Save(ctx context.Context, record repository.InventoryRecord) error {
	if err := r.InventoryRepository.Save(ctx, record); err != nil {
		return err
	}
	if record.CharacterID == r.failID && !r.failed {
		r.failed = true
		return errors.New("failed after persisting last member")
	}
	return nil
}

func TestCharacterInventoryGroupScopeAndLateCommitRollback(t *testing.T) {
	ctx := context.Background()
	repos := NewMemoryGroup()
	ids := []string{"19", "20", "21"}
	for _, id := range ids {
		if err := repos.Character.Save(ctx, repository.CharacterRecord{CharacterID: id, AccountID: "a" + id}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory.Save(ctx, repository.InventoryRecord{CharacterID: id, Slots: map[string]repository.ItemStack{"0:50": {ItemID: 777, Count: 3}}}); err != nil {
			t.Fatal(err)
		}
	}
	u := repos.CharacterTrade.(*memoryCharacterTradeUnitOfWork)
	u.inventory = &groupLateInventoryFailure{InventoryRepository: repos.Inventory, failID: "21"}
	apply := func(c repository.CharacterRepository, i repository.InventoryRepository) error {
		if _, _, err := i.Load(ctx, "22"); err == nil {
			t.Fatal("out-of-scope inventory exposed")
		}
		if err := i.Save(ctx, repository.InventoryRecord{CharacterID: "22"}); err == nil {
			t.Fatal("out-of-scope save accepted")
		}
		for _, id := range ids {
			b, _, err := i.Load(ctx, id)
			if err != nil {
				return err
			}
			stack := b.Slots["0:50"]
			stack.Count--
			b.Slots["0:50"] = stack
			if err := i.Save(ctx, b); err != nil {
				return err
			}
		}
		return nil
	}
	if err := u.WithinCharacterInventoryGroup(ctx, []string{"21", "19", "20"}, apply); err == nil {
		t.Fatal("late failure ignored")
	}
	for _, id := range ids {
		b, _, _ := repos.Inventory.Load(ctx, id)
		if b.Slots["0:50"].Count != 3 {
			t.Fatalf("partial commit survived owner %s", id)
		}
	}
	if err := u.WithinCharacterInventoryGroup(ctx, ids, apply); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		b, _, _ := repos.Inventory.Load(ctx, id)
		if b.Slots["0:50"].Count != 2 {
			t.Fatalf("successful group missed owner %s", id)
		}
	}
	for _, bad := range [][]string{nil, {"19", "19"}, {"19", " "}} {
		if err := u.WithinCharacterInventoryGroup(ctx, bad, apply); err == nil {
			t.Fatalf("invalid scope accepted %v", bad)
		}
	}
}
