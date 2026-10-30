package mysql

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"time"
)

var _ repository.EquipmentServiceUnitOfWork = (*mysqlMailboxAssetUnitOfWork)(nil)

func (u *mysqlMailboxAssetUnitOfWork) WithinEquipmentServiceAssets(ctx context.Context, accountID, sourceID, destinationID string, apply func(repository.Group, repository.CharacterRecord, repository.CharacterRecord) error) error {
	accountID, sourceID, destinationID = strings.TrimSpace(accountID), strings.TrimSpace(sourceID), strings.TrimSpace(destinationID)
	if u == nil || accountID == "" || sourceID == "" || destinationID == "" || apply == nil {
		return repository.ErrMailboxAssetTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b, ok := u.router.db.(sqlTxBeginner)
	if !ok || len(u.router.writeDBs) == 0 {
		return repository.ErrMailboxAssetTransactionUnavailable
	}
	tx, err := b.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r := u.router
	r.db = newSQLTransactionDB(tx, r.db)
	r.readDBs = append([]string(nil), r.writeDBs...)
	r.lockReads = true
	base := newMySQLGroupFromRouter(r, false)
	// 先锁账号，与碎片原子自增所锁账号行保持同一顺序，避免旧快照覆盖进账。
	a, found, err := base.Account.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !found || a.AccountID != accountID {
		return repository.ErrMailboxAssetTransactionUnavailable
	}
	source, found, err := base.Character.Load(ctx, sourceID)
	if err != nil {
		return err
	}
	if !found || source.CharacterID != sourceID || source.AccountID != accountID || source.Stats["delete_flag"] != 0 || source.Slot < 0 {
		return repository.ErrMailboxAssetTransactionUnavailable
	}
	destination := source
	if destinationID != sourceID {
		destination, found, err = base.Character.Load(ctx, destinationID)
		if err != nil {
			return err
		}
		if !found || destination.CharacterID != destinationID || destination.AccountID != accountID || destination.Stats["delete_flag"] != 0 || destination.Slot < 0 {
			return repository.ErrMailboxAssetTransactionUnavailable
		}
	}
	g := repository.Group{
		Account:          &equipmentServiceAccountRepository{AccountRepository: &rentalScopedAccountRepository{accountID: accountID, base: base.Account}, accountID: accountID, base: base.Account},
		AccountInventory: &accountInventoryScopedRepository{accountID: accountID, base: base.AccountInventory},
		Inventory:        &characterSettlementScopedInventoryRepository{characterID: sourceID, base: base.Inventory},
		Mailbox:          &mailboxAssetScopedMailboxRepository{characterID: destinationID, base: base.Mailbox},
	}
	if err = apply(g, repository.CloneCharacter(source), repository.CloneCharacter(destination)); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err = ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

type equipmentServiceAccountRepository struct {
	repository.AccountRepository
	accountID string
	base      repository.AccountRepository
}

func (r *equipmentServiceAccountRepository) SaveMetadataEntry(ctx context.Context, id, key, value string, at time.Time) error {
	if err := repository.TransactionKeyError(ctx, r.accountID, id); err != nil {
		return err
	}
	w, ok := r.base.(repository.AccountMetadataEntryWriter)
	if !ok {
		return repository.ErrAccountMetadataUnavailable
	}
	return w.SaveMetadataEntry(ctx, id, key, value, at)
}
