package memory

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

type memoryWarehouseTradeAccounts map[string]*memoryAccountInventoryTransaction

func (a memoryWarehouseTradeAccounts) Load(ctx context.Context, id string) (repo.AccountInventoryRecord, bool, error) {
	if tx := a[id]; tx != nil {
		return tx.Load(ctx, id)
	}
	return repo.AccountInventoryRecord{}, false, repo.ErrCharacterTradeTransactionUnavailable
}
func (a memoryWarehouseTradeAccounts) Save(ctx context.Context, record repo.AccountInventoryRecord) error {
	if tx := a[record.AccountID]; tx != nil {
		return tx.Save(ctx, record)
	}
	return repo.ErrCharacterTradeTransactionUnavailable
}

func (u *memoryCharacterTradeUnitOfWork) WithinCharacterWarehouseTrade(ctx context.Context, characters, accounts []string, apply func(repo.CharacterRepository, repo.InventoryRepository, repo.AccountInventoryRepository) error) error {
	ids, owners, err := repo.WarehouseTradeScope(characters, accounts)
	if err != nil || u == nil || apply == nil || u.character == nil || u.inventory == nil || u.accountInventory == nil {
		return repo.ErrCharacterTradeTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lock := &u.mu
	if u.sharedMu != nil {
		lock = u.sharedMu
	}
	lock.Lock()
	defer lock.Unlock()
	accountTx := memoryWarehouseTradeAccounts{}
	originalAccounts := map[string]repo.AccountInventoryRecord{}
	originalAccountExists := map[string]bool{}
	for _, id := range owners {
		record, found, err := u.accountInventory.Load(ctx, id)
		if err != nil {
			return err
		}
		originalAccounts[id] = repo.CloneAccountInventory(record)
		originalAccountExists[id] = found
		accountTx[id] = &memoryAccountInventoryTransaction{accountID: id, record: repo.CloneAccountInventory(record), exists: found}
	}
	allowed := map[string]struct{}{}
	c := &memoryCharacterTradeCharacterTransaction{allowed: allowed, records: map[string]repo.CharacterRecord{}, exists: map[string]bool{}, dirty: map[string]bool{}}
	i := &memoryCharacterTradeInventoryTransaction{allowed: allowed, records: map[string]repo.InventoryRecord{}, exists: map[string]bool{}, dirty: map[string]bool{}}
	originalCharacters := map[string]repo.CharacterRecord{}
	originalInventories := map[string]repo.InventoryRecord{}
	originalCharacterExists := map[string]bool{}
	originalInventoryExists := map[string]bool{}
	for _, id := range ids {
		allowed[id] = struct{}{}
		record, found, err := u.character.Load(ctx, id)
		if err != nil {
			return err
		}
		originalCharacters[id] = repo.CloneCharacter(record)
		c.records[id] = repo.CloneCharacter(record)
		c.exists[id] = found
		originalCharacterExists[id] = found
		inventory, found, err := u.inventory.Load(ctx, id)
		if err != nil {
			return err
		}
		originalInventories[id] = repo.CloneInventory(inventory)
		i.records[id] = repo.CloneInventory(inventory)
		i.exists[id] = found
		originalInventoryExists[id] = found
	}
	if err := apply(c, i, accountTx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Register compensation before each Save: a failing store may mutate first.
	undo := []func() error{}
	rollback := func(cause error) error {
		errs := []error{cause}
		for n := len(undo) - 1; n >= 0; n-- {
			errs = append(errs, undo[n]())
		}
		return errors.Join(errs...)
	}
	for _, id := range owners {
		if tx := accountTx[id]; tx.dirty {
			original, existed := originalAccounts[id], originalAccountExists[id]
			undo = append(undo, func() error { return restoreAccountInventoryRecord(u.accountInventory, id, original, existed) })
			if err := u.accountInventory.Save(ctx, tx.record); err != nil {
				return rollback(err)
			}
		}
	}
	for _, id := range ids {
		if c.dirty[id] {
			original, existed := originalCharacters[id], originalCharacterExists[id]
			undo = append(undo, func() error { return restoreCharacterRecord(u.character, id, original, existed, true) })
			if err := u.character.Save(ctx, c.records[id]); err != nil {
				return rollback(err)
			}
		}
		if i.dirty[id] {
			original, existed := originalInventories[id], originalInventoryExists[id]
			undo = append(undo, func() error { return restoreInventoryRecord(u.inventory, id, original, existed) })
			if err := u.inventory.Save(ctx, i.records[id]); err != nil {
				return rollback(err)
			}
		}
	}
	return nil
}
