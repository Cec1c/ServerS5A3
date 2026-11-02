package memory

import (
	"context"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

var _ repository.DisjointerAssetUnitOfWork = (*memoryCharacterTradeUnitOfWork)(nil)

func (u *memoryCharacterTradeUnitOfWork) WithinDisjointerAssets(ctx context.Context, accountID, requesterID, ownerID string,
	apply func(repository.AccountRepository, repository.CharacterRepository, repository.InventoryRepository, repository.AccountInventoryRepository) error,
) error {
	accountID, requesterID, ownerID = strings.TrimSpace(accountID), strings.TrimSpace(requesterID), strings.TrimSpace(ownerID)
	if u == nil || u.account == nil || u.character == nil || u.inventory == nil || u.accountInventory == nil || accountID == "" || requesterID == "" || ownerID == "" || apply == nil {
		return repository.ErrCharacterTradeTransactionUnavailable
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
	u.account.nameMu.Lock()
	defer u.account.nameMu.Unlock()
	a, found, err := u.account.MemoryStore.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !found || a.AccountID != accountID {
		return repository.ErrEpicBookOwnership
	}
	ledger := &memoryAccountTransaction{accountID: accountID, record: repository.CloneAccount(a), exists: true}
	ids := []string{requesterID}
	if ownerID != requesterID {
		ids = append(ids, ownerID)
	}
	ids, err = repository.CharacterInventoryGroupIDs(ids)
	if err != nil {
		return err
	}
	chars := &memoryCharacterTradeCharacterTransaction{allowed: map[string]struct{}{}, records: map[string]repository.CharacterRecord{}, exists: map[string]bool{}, dirty: map[string]bool{}}
	bags := &memoryCharacterTradeInventoryTransaction{allowed: map[string]struct{}{}, records: map[string]repository.InventoryRecord{}, exists: map[string]bool{}, dirty: map[string]bool{}}
	var writes []memoryLedgerWrite
	for _, id := range ids {
		c, exists, e := u.character.Load(ctx, id)
		if e != nil {
			return e
		}
		if !exists || c.CharacterID != id || (id == requesterID && c.AccountID != accountID) {
			return repository.ErrEpicBookOwnership
		}
		i, inventoryExists, e := u.inventory.Load(ctx, id)
		if e != nil {
			return e
		}
		chars.allowed[id], bags.allowed[id] = struct{}{}, struct{}{}
		chars.records[id], chars.exists[id] = repository.CloneCharacter(c), true
		bags.records[id], bags.exists[id] = repository.CloneInventory(i), inventoryExists
		writes = append(writes,
			memoryLedgerWrite{save: func() error { return u.character.Save(ctx, chars.records[id]) }, restore: func() error { return restoreCharacterRecord(u.character, id, c, true, true) }},
			memoryLedgerWrite{save: func() error { return u.inventory.Save(ctx, bags.records[id]) }, restore: func() error { return restoreInventoryRecord(u.inventory, id, i, inventoryExists) }})
	}
	w, warehouseExists, err := u.accountInventory.Load(ctx, accountID)
	if err != nil {
		return err
	}
	warehouse := &memoryAccountInventoryTransaction{accountID: accountID, record: repository.CloneAccountInventory(w), exists: warehouseExists}
	if u.activity != nil {
		chars.activity = u.activity.transaction()
		chars.activityOwners = make(map[string]repository.CharacterRecord, len(ids))
		for _, id := range ids {
			chars.activityOwners[id] = repository.CloneCharacter(chars.records[id])
		}
	}
	if err := apply(ledger, chars, bags, warehouse); err != nil {
		return err
	}
	for n, id := range ids {
		writes[n*2].dirty, writes[n*2+1].dirty = chars.dirty[id], bags.dirty[id]
	}
	writes = append(writes,
		memoryLedgerWrite{dirty: warehouse.dirty, save: func() error { return u.accountInventory.Save(ctx, warehouse.record) }, restore: func() error { return restoreAccountInventoryRecord(u.accountInventory, accountID, w, warehouseExists) }},
		memoryLedgerWrite{dirty: ledger.dirty, save: func() error { return u.account.MemoryStore.Save(ctx, ledger.record) }, restore: func() error { return u.account.MemoryStore.Save(context.Background(), a) }})
	err = commitMemoryLedgerAssets(ctx, writes...)
	if err == nil && chars.activity != nil {
		chars.activity.commit()
	}
	return err
}
