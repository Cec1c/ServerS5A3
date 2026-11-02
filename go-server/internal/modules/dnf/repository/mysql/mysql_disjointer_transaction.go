package mysql

import (
	"context"
	"errors"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

var _ repository.DisjointerAssetUnitOfWork = (*mysqlCharacterTradeUnitOfWork)(nil)

func (u *mysqlCharacterTradeUnitOfWork) WithinDisjointerAssets(ctx context.Context, accountID, requesterID, ownerID string,
	apply func(repository.AccountRepository, repository.CharacterRepository, repository.InventoryRepository, repository.AccountInventoryRepository) error,
) error {
	accountID, requesterID, ownerID = strings.TrimSpace(accountID), strings.TrimSpace(requesterID), strings.TrimSpace(ownerID)
	if u == nil || accountID == "" || requesterID == "" || ownerID == "" || apply == nil {
		return repository.ErrCharacterTradeTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrCharacterTradeTransactionUnavailable
	}
	tx, err := beginTimedObservedSQLTransaction(ctx, beginner)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	router := u.router
	router.db = newSQLTransactionDB(tx.Tx, router.db)
	router.readDBs = append([]string(nil), u.router.writeDBs...)
	router.writeDBs = append([]string(nil), u.router.writeDBs...)
	router.lockReads = true
	group := newMySQLGroupFromRouter(router, false)
	// Same account-before-character lock order as the epic book and pickup.
	a, found, err := group.Account.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !found || a.AccountID != accountID {
		return repository.ErrEpicBookOwnership
	}
	allowed := map[string]struct{}{requesterID: {}, ownerID: {}}
	characters := &characterTradeScopedCharacterRepository{allowed: allowed, base: group.Character, activity: group.Activity}
	ids := []string{requesterID}
	if requesterID != ownerID {
		ids = append(ids, ownerID)
	}
	ids, err = repository.CharacterInventoryGroupIDs(ids)
	if err != nil {
		return err
	}
	for _, id := range ids {
		c, exists, loadErr := characters.Load(ctx, id)
		if loadErr != nil {
			return loadErr
		}
		if !exists || c.CharacterID != id || (id == requesterID && c.AccountID != accountID) {
			return repository.ErrEpicBookOwnership
		}
	}
	ledger := &rentalScopedAccountRepository{accountID: accountID, base: group.Account}
	inventory := &characterTradeScopedInventoryRepository{allowed: allowed, base: group.Inventory}
	shared := &accountInventoryScopedRepository{accountID: accountID, base: group.AccountInventory}
	if err := apply(ledger, characters, inventory, shared); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}
