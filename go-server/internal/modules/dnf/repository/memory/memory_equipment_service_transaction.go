package memory

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

var _ repository.EquipmentServiceUnitOfWork = (*memoryMailboxAssetUnitOfWork)(nil)

func (u *memoryMailboxAssetUnitOfWork) WithinEquipmentServiceAssets(ctx context.Context, accountID, sourceID, destinationID string, apply func(repository.Group, repository.CharacterRecord, repository.CharacterRecord) error) (err error) {
	accountID, sourceID, destinationID = strings.TrimSpace(accountID), strings.TrimSpace(sourceID), strings.TrimSpace(destinationID)
	if u == nil || u.account == nil || u.accountInventory == nil || u.character == nil || u.inventory == nil || u.mailbox == nil || accountID == "" || sourceID == "" || destinationID == "" || apply == nil {
		return repository.ErrMailboxAssetTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	lock := &u.mu
	if u.sharedMu != nil {
		lock = u.sharedMu
	}
	lock.Lock()
	defer lock.Unlock()
	accountStore := u.account
	// 与账号碎片自增共用锁；提交只使用锁下的底层存储，避免递归加锁。
	if raw, ok := u.account.(*memoryAccountStore); ok {
		raw.nameMu.Lock()
		defer raw.nameMu.Unlock()
		accountStore = raw.MemoryStore
	}
	a, af, e := accountStore.Load(ctx, accountID)
	if e != nil {
		return e
	}
	if !af || a.AccountID != accountID {
		return repository.ErrMailboxAssetTransactionUnavailable
	}
	source, sf, e := u.character.Load(ctx, sourceID)
	if e != nil {
		return e
	}
	if !sf || source.CharacterID != sourceID || source.AccountID != accountID || source.Stats["delete_flag"] != 0 || source.Slot < 0 {
		return repository.ErrMailboxAssetTransactionUnavailable
	}
	dest := source
	if destinationID != sourceID {
		var df bool
		dest, df, e = u.character.Load(ctx, destinationID)
		if e != nil {
			return e
		}
		if !df || dest.CharacterID != destinationID || dest.AccountID != accountID || dest.Stats["delete_flag"] != 0 || dest.Slot < 0 {
			return repository.ErrMailboxAssetTransactionUnavailable
		}
	}
	ai, aif, e := u.accountInventory.Load(ctx, accountID)
	if e != nil {
		return e
	}
	i, ifound, e := u.inventory.Load(ctx, sourceID)
	if e != nil {
		return e
	}
	m, mf, e := u.mailbox.Load(ctx, destinationID)
	if e != nil {
		return e
	}
	at := &memoryAccountTransaction{accountID: accountID, record: repository.CloneAccount(a), exists: af}
	ait := &memoryAccountInventoryTransaction{accountID: accountID, record: repository.CloneAccountInventory(ai), exists: aif}
	it := &memoryInventoryTransaction{characterID: sourceID, record: repository.CloneInventory(i), exists: ifound}
	mt := &memoryMailboxTransaction{characterID: destinationID, record: repository.CloneMailbox(m), exists: mf}
	g := repository.Group{Account: at, AccountInventory: ait, Inventory: it, Mailbox: mt}
	if err = apply(g, repository.CloneCharacter(source), repository.CloneCharacter(dest)); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	aa, ia, ma, aia, committed := false, false, false, false, false
	defer func() {
		if committed {
			return
		}
		err = errors.Join(err,
			restoreMailboxAssetAttempt(func() error { return restoreAccountRecord(accountStore, accountID, a, af, aa) }),
			restoreMailboxAssetAttempt(func() error { return restoreMailboxRecordIfDirty(u.mailbox, destinationID, m, mf, ma) }),
			restoreMailboxAssetAttempt(func() error { return restoreAccountInventoryRecordIfDirty(u.accountInventory, accountID, ai, aif, aia) }),
			restoreMailboxAssetAttempt(func() error { return restoreInventoryRecordIfDirty(u.inventory, sourceID, i, ifound, ia) }))
	}()
	if it.dirty {
		ia = true
		if err = u.inventory.Save(ctx, it.record); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if ait.dirty {
		aia = true
		if err = u.accountInventory.Save(ctx, ait.record); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if mt.dirty {
		ma = true
		if err = u.mailbox.Save(ctx, mt.record); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if at.dirty {
		aa = true
		if err = accountStore.Save(ctx, at.record); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	committed = true
	return nil
}
