package mysql

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

var _ repository.AccountCharacterMailboxAssetUnitOfWork = (*mysqlAccountCharacterAssetUnitOfWork)(nil)

func (u *mysqlAccountCharacterAssetUnitOfWork) WithinAccountCharacterMailboxAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(repository.AccountInventoryRepository, repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository, repository.MailboxRepository) error,
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
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer tx.Rollback() // Also rolls back a panicking projector.

	// Account inventory and character rows can hash to different logical
	// databases. They still share one physical MySQL transaction, so keep the
	// full write plan while forcing locked reads through the transaction.
	txRouter := u.router
	txRouter.db = sqlTransactionDB{tx: tx}
	txRouter.readDBs = append([]string(nil), u.router.writeDBs...)
	txRouter.writeDBs = append([]string(nil), u.router.writeDBs...)
	txRouter.lockReads = true
	txGroup := newMySQLGroupFromRouter(txRouter, false)
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
	if err := apply(accounts, characters, inventories, equipment, mailboxes); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}
