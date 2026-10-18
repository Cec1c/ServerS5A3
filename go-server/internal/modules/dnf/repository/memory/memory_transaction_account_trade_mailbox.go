package memory

import (
	"context"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
	"slices"
	"strings"
)

var _ r.AccountTradeMailboxUnitOfWork = (*memoryMailboxAssetUnitOfWork)(nil)

func (u *memoryMailboxAssetUnitOfWork) WithinAccountTradeMailbox(ctx context.Context, accountID string, ids []string, mailID string, apply func(r.AccountRepository, r.CharacterRepository, r.InventoryRepository, r.MailboxRepository) error) (err error) {
	accountID, mailID = strings.TrimSpace(accountID), strings.TrimSpace(mailID)
	ids, err = r.CharacterInventoryGroupIDs(ids)
	if err != nil {
		return err
	}
	if u == nil || u.account == nil || u.character == nil || u.inventory == nil || u.mailbox == nil || accountID == "" || mailID == "" || !slices.Contains(ids, mailID) || apply == nil {
		return r.ErrMailboxAssetTransactionUnavailable
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
	c := &memoryCharacterTradeCharacterTransaction{allowed: map[string]struct{}{}, records: map[string]r.CharacterRecord{}, exists: map[string]bool{}, dirty: map[string]bool{}}
	i := &memoryCharacterTradeInventoryTransaction{allowed: c.allowed, records: map[string]r.InventoryRecord{}, exists: map[string]bool{}, dirty: map[string]bool{}}
	originalsC := map[string]r.CharacterRecord{}
	originalsI := map[string]r.InventoryRecord{}
	originalsIExists := map[string]bool{}
	for _, id := range ids {
		record, found, e := u.character.Load(ctx, id)
		if e != nil {
			return e
		}
		if !found || record.CharacterID != id {
			return r.ErrMailboxAssetTransactionUnavailable
		}
		if id == mailID && (record.AccountID != accountID || record.Stats["delete_flag"] != 0) {
			return r.ErrAccountCharacterMailboxOwnership
		}
		c.allowed[id] = struct{}{}
		c.records[id] = r.CloneCharacter(record)
		c.exists[id] = true
		originalsC[id] = r.CloneCharacter(record)
		inv, exists, e := u.inventory.Load(ctx, id)
		if e != nil {
			return e
		}
		if exists && inv.CharacterID != id {
			return r.ErrMailboxAssetTransactionUnavailable
		}
		i.records[id] = r.CloneInventory(inv)
		i.exists[id] = exists
		originalsI[id] = r.CloneInventory(inv)
		originalsIExists[id] = exists
	}
	account, found, err := u.account.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !found || account.AccountID != accountID {
		return r.ErrMailboxAssetTransactionUnavailable
	}
	mail, mailExists, err := u.mailbox.Load(ctx, mailID)
	if err != nil {
		return err
	}
	if mailExists && mail.CharacterID != mailID {
		return r.ErrMailboxAssetTransactionUnavailable
	}
	a := &memoryAccountTransaction{accountID: accountID, record: r.CloneAccount(account), exists: true}
	m := &memoryMailboxTransaction{characterID: mailID, record: r.CloneMailbox(mail), exists: mailExists}
	if err = apply(a, c, i, m); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	accountTouched, mailTouched, committed := false, false, false
	charTouched, invTouched := []string{}, []string{}
	defer func() {
		if committed {
			return
		}
		var failures []error
		if mailTouched {
			failures = append(failures, restoreMailboxAssetAttempt(func() error { return restoreMailboxRecordIfDirty(u.mailbox, mailID, mail, mailExists, true) }))
		}
		for n := len(invTouched) - 1; n >= 0; n-- {
			id := invTouched[n]
			failures = append(failures, restoreMailboxAssetAttempt(func() error {
				return restoreInventoryRecordIfDirty(u.inventory, id, originalsI[id], originalsIExists[id], true)
			}))
		}
		for n := len(charTouched) - 1; n >= 0; n-- {
			id := charTouched[n]
			failures = append(failures, restoreMailboxAssetAttempt(func() error { return restoreCharacterRecord(u.character, id, originalsC[id], true, true) }))
		}
		if accountTouched {
			failures = append(failures, restoreMailboxAssetAttempt(func() error { return restoreAccountRecord(u.account, accountID, account, true, true) }))
		}
		err = errors.Join(append([]error{err}, failures...)...)
	}()
	if a.dirty {
		accountTouched = true
		if err = u.account.Save(ctx, a.record); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	for _, id := range ids {
		if c.dirty[id] {
			charTouched = append(charTouched, id)
			if err = u.character.Save(ctx, c.records[id]); err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
		}
	}
	for _, id := range ids {
		if i.dirty[id] {
			invTouched = append(invTouched, id)
			if err = u.inventory.Save(ctx, i.records[id]); err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
		}
	}
	if m.dirty {
		mailTouched = true
		if err = u.mailbox.Save(ctx, m.record); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	committed = true
	return nil
}
