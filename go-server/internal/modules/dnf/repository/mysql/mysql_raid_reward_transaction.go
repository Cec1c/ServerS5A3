package mysql

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

var (
	_ repository.AccountCharacterMailboxAssetUnitOfWork       = (*mysqlAccountCharacterAssetUnitOfWork)(nil)
	_ repository.AccountCharacterLedgerMailboxAssetUnitOfWork = (*mysqlAccountCharacterAssetUnitOfWork)(nil)
)

func (u *mysqlAccountCharacterAssetUnitOfWork) WithinAccountCharacterMailboxAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(repository.AccountInventoryRepository, repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository, repository.MailboxRepository) error,
) error {
	if apply == nil {
		return repository.ErrAccountCharacterAssetTransactionUnavailable
	}
	return u.withinAccountCharacterMailboxAssets(ctx, accountID, characterID, false, func(_ repository.AccountRepository, accounts repository.AccountInventoryRepository, characters repository.CharacterRepository, inventories repository.InventoryRepository, equipment repository.EquipmentRepository, mailboxes repository.MailboxRepository) error {
		return apply(accounts, characters, inventories, equipment, mailboxes)
	})
}

func (u *mysqlAccountCharacterAssetUnitOfWork) WithinAccountCharacterLedgerMailboxAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(repository.AccountRepository, repository.AccountInventoryRepository, repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository, repository.MailboxRepository) error,
) error {
	if apply == nil {
		return repository.ErrAccountCharacterAssetTransactionUnavailable
	}
	return u.withinAccountCharacterMailboxAssets(ctx, accountID, characterID, true, apply)
}

func (u *mysqlAccountCharacterAssetUnitOfWork) withinAccountCharacterMailboxAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	includeLedger bool,
	apply func(repository.AccountRepository, repository.AccountInventoryRepository, repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository, repository.MailboxRepository) error,
) error {
	accountID = strings.TrimSpace(accountID)
	characterID = strings.TrimSpace(characterID)
	if accountID == "" || characterID == "" || apply == nil || u == nil {
		return repository.ErrAccountCharacterAssetTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok || len(u.router.writeDBs) == 0 {
		return repository.ErrAccountCharacterAssetTransactionUnavailable
	}
	tx, err := beginObservedSQLTransaction(ctx, beginner, nil)
	if err != nil {
		return err
	}

	defer tx.Rollback() // Also rolls back a panicking projector.

	// Account inventory and character rows can hash to different logical
	// databases. They still share one physical MySQL transaction, so keep the
	// full write plan while forcing locked reads through the transaction.
	txRouter := u.router
	txRouter.db = newSQLTransactionDB(tx.Tx, txRouter.db)
	txRouter.readDBs = append([]string(nil), u.router.writeDBs...)
	txRouter.writeDBs = append([]string(nil), u.router.writeDBs...)
	txRouter.lockReads = true
	txGroup := newMySQLGroupFromRouter(txRouter, false)
	var ledger repository.AccountRepository
	if includeLedger {
		ledger = &rentalScopedAccountRepository{accountID: accountID, base: txGroup.Account}
		// Keep the same account-first lock order used by the ledger asset
		// transaction and account material/pickup writers.
		record, found, err := ledger.Load(ctx, accountID)
		if err != nil {
			return errors.Join(err, tx.Rollback())
		}
		if !found || record.AccountID != accountID {
			return errors.Join(repository.ErrAccountCharacterAssetTransactionUnavailable, tx.Rollback())
		}
	}
	accounts := &accountInventoryScopedRepository{accountID: accountID, base: txGroup.AccountInventory}
	characters := &rentalScopedCharacterRepository{characterID: characterID, base: txGroup.Character}
	inventories := &rentalScopedInventoryRepository{characterID: characterID, base: txGroup.Inventory}
	equipment := &rentalScopedEquipmentRepository{characterID: characterID, base: txGroup.Equipment}
	character, found, err := characters.Load(ctx, characterID)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if !found || character.CharacterID != characterID || character.AccountID != accountID {
		return errors.Join(repository.ErrAccountCharacterMailboxOwnership, tx.Rollback())
	}

	mailboxes := &mailboxAssetScopedMailboxRepository{characterID: characterID, base: txGroup.Mailbox}
	if err := apply(ledger, accounts, characters, inventories, equipment, mailboxes); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}
