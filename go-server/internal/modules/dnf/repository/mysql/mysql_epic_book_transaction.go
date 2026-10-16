package mysql

import (
	"context"
	"errors"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

var _ repository.EpicBookAssetUnitOfWork = (*mysqlAccountCharacterAssetUnitOfWork)(nil)

type mysqlEpicBookDebit struct {
	ctx      context.Context
	accounts repository.AccountRepository
	record   repository.AccountRecord
}

func (d *mysqlEpicBookDebit) DebitPiece(ctx context.Context, pieceID, amount uint32, now time.Time) (uint32, error) {
	if err := d.ctx.Err(); err != nil {
		return 0, err
	}
	if ctx == nil {
		ctx = d.ctx
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if pieceID == 0 || amount == 0 {
		return 0, repository.ErrAccountEpicPieceIDRequired
	}
	key := repository.AccountEpicPieceMetadataKey(pieceID)
	old := repository.ParseAccountEpicPieceBalance(d.record.Metadata[key])
	if old < amount {
		return 0, repository.ErrEpicBookPieceInsufficient
	}
	value := repository.FormatAccountEpicPieceBalance(old - amount)
	// Account.Load already locked the account row before metadata. Credits
	// acquire the same row first, so this conditional decision cannot go stale.
	writer, ok := d.accounts.(repository.AccountMetadataEntryWriter)
	if !ok {
		return 0, repository.ErrEpicBookTransactionUnavailable
	}
	if err := writer.SaveMetadataEntry(ctx, d.record.AccountID, key, value, now); err != nil {
		return 0, err
	}
	if d.record.Metadata == nil {
		d.record.Metadata = map[string]string{}
	}
	d.record.Metadata[key] = value
	return old - amount, nil
}

func (u *mysqlAccountCharacterAssetUnitOfWork) WithinEpicBookAssets(ctx context.Context, accountID, characterID string,
	apply func(repository.EpicBookPieceDebitor, repository.CharacterRecord, repository.AccountInventoryRepository, repository.InventoryRepository) error,
) error {
	accountID, characterID = strings.TrimSpace(accountID), strings.TrimSpace(characterID)
	if u == nil || accountID == "" || characterID == "" || apply == nil || len(u.router.writeDBs) == 0 {
		return repository.ErrEpicBookTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrEpicBookTransactionUnavailable
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	router := u.router
	router.db = sqlTransactionDB{tx: tx}
	router.readDBs = append([]string(nil), u.router.writeDBs...)
	router.writeDBs = append([]string(nil), u.router.writeDBs...)
	router.lockReads = true
	group := newMySQLGroupFromRouter(router, false)
	// Match AddAccountEpicPieceBalance's account-before-metadata lock order.
	account, found, err := group.Account.Load(ctx, accountID)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if !found || account.AccountID != accountID {
		return repository.ErrEpicBookOwnership
	}
	character, found, err := group.Character.Load(ctx, characterID)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if !found || character.CharacterID != characterID || character.AccountID != accountID {
		return repository.ErrEpicBookOwnership
	}
	debit := &mysqlEpicBookDebit{ctx: ctx, accounts: group.Account, record: account}
	shared := &accountInventoryScopedRepository{accountID: accountID, base: group.AccountInventory}
	inventory := &rentalScopedInventoryRepository{characterID: characterID, base: group.Inventory}
	if err := apply(debit, repository.CloneCharacter(character), shared, inventory); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}
