package memory

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

type lateTradeWarehouseFailure struct {
	repository.AccountInventoryRepository
	fail bool
}

func (s *lateTradeWarehouseFailure) Save(ctx context.Context, r repository.AccountInventoryRecord) error {
	if err := s.AccountInventoryRepository.Save(ctx, r); err != nil {
		return err
	}
	if s.fail {
		s.fail = false
		return errors.New("late warehouse failure")
	}
	return nil
}

func TestTradeAssetsScopeAndLateFailureRestoreAllOwners(t *testing.T) {
	ctx := context.Background()
	repos := NewMemoryGroup()
	// Requester's ID sorts AFTER the shop owner. Sorting locks must not change
	// whose account warehouse receives the products.
	for _, id := range []string{"20", "19"} {
		if err := repos.Character.Save(ctx, repository.CharacterRecord{CharacterID: id, AccountID: "account-" + id, Stats: map[string]int64{"gold": 1000, "expert_job_exp": 10}}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory.Save(ctx, repository.InventoryRecord{CharacterID: id, Slots: map[string]repository.ItemStack{"0:10": {ItemID: 100300212, Count: 1}}}); err != nil {
			t.Fatal(err)
		}
		if err := repos.AccountInventory.Save(ctx, repository.AccountInventoryRecord{AccountID: "account-" + id, Slots: map[string]repository.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
	}
	u := repos.CharacterTrade.(*memoryCharacterTradeUnitOfWork)
	u.accountInventory = &lateTradeWarehouseFailure{AccountInventoryRepository: repos.AccountInventory, fail: true}
	err := u.WithinCharacterTradeAssets(ctx, "20", "19", func(chars repository.CharacterRepository, bags repository.InventoryRepository, warehouse repository.AccountInventoryRepository) error {
		if _, _, err := warehouse.Load(ctx, "account-19"); err == nil {
			return errors.New("shop owner warehouse was exposed")
		}
		for _, id := range []string{"19", "20"} {
			c, _, _ := chars.Load(ctx, id)
			c.Stats["gold"] = 900
			c.Stats["expert_job_exp"] = 11
			if err := chars.Save(ctx, c); err != nil {
				return err
			}
			b, _, _ := bags.Load(ctx, id)
			delete(b.Slots, "0:10")
			if err := bags.Save(ctx, b); err != nil {
				return err
			}
		}
		return warehouse.Save(ctx, repository.AccountInventoryRecord{AccountID: "account-20", Slots: map[string]repository.ItemStack{"0:365": {ItemID: 10158124, Count: 1}}})
	})
	if err == nil {
		t.Fatal("late failure did not propagate")
	}
	for _, id := range []string{"19", "20"} {
		c, _, _ := repos.Character.Load(ctx, id)
		b, _, _ := repos.Inventory.Load(ctx, id)
		a, _, _ := repos.AccountInventory.Load(ctx, "account-"+id)
		if c.Stats["gold"] != 1000 || c.Stats["expert_job_exp"] != 10 || b.Slots["0:10"].Count != 1 || len(a.Slots) != 0 {
			t.Fatalf("partial commit survived for %s: stats=%+v bag=%+v warehouse=%+v", id, c.Stats, b.Slots, a.Slots)
		}
	}
	if err := u.WithinCharacterTradeAssets(ctx, "20", "19", func(_ repository.CharacterRepository, _ repository.InventoryRepository, warehouse repository.AccountInventoryRepository) error {
		return warehouse.Save(ctx, repository.AccountInventoryRecord{AccountID: "account-20", Slots: map[string]repository.ItemStack{"0:365": {ItemID: 10158124, Count: 1}}})
	}); err != nil {
		t.Fatal(err)
	}
	a, _, _ := repos.AccountInventory.Load(ctx, "account-20")
	other, _, _ := repos.AccountInventory.Load(ctx, "account-19")
	if a.Slots["0:365"].Count != 1 || len(other.Slots) != 0 {
		t.Fatalf("successful grant used wrong owner: %+v %+v", a, other)
	}
}
