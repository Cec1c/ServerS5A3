package mysql

import (
	"context"
	r "longheng.io/server/internal/modules/dnf/repository"
	"slices"
	"strings"
)

var _ r.AccountTradeMailboxUnitOfWork = (*mysqlMailboxAssetUnitOfWork)(nil)

func (u *mysqlMailboxAssetUnitOfWork) WithinAccountTradeMailbox(ctx context.Context, accountID string, ids []string, mailID string, apply func(r.AccountRepository, r.CharacterRepository, r.InventoryRepository, r.MailboxRepository) error) error {
	accountID, mailID = strings.TrimSpace(accountID), strings.TrimSpace(mailID)
	ids, err := r.CharacterInventoryGroupIDs(ids)
	if err != nil {
		return err
	}
	if u == nil || accountID == "" || mailID == "" || !slices.Contains(ids, mailID) || apply == nil {
		return r.ErrMailboxAssetTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b, ok := u.router.db.(sqlTxBeginner)
	if !ok || len(u.router.writeDBs) == 0 {
		return r.ErrMailboxAssetTransactionUnavailable
	}
	tx, err := b.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	router := u.router
	router.db = sqlTransactionDB{tx: tx}
	router.readDBs = append([]string(nil), u.router.writeDBs...)
	router.writeDBs = append([]string(nil), u.router.writeDBs...)
	router.lockReads = true
	base := newMySQLGroupFromRouter(router, false)
	scope := map[string]struct{}{}
	for _, id := range ids {
		scope[id] = struct{}{}
	}
	c := &characterTradeScopedCharacterRepository{allowed: scope, base: base.Character}
	i := &characterTradeScopedInventoryRepository{allowed: scope, base: base.Inventory}
	// 先按稳定顺序锁角色，避免同一托管记录被并发购买。
	for _, id := range ids {
		ch, found, e := c.Load(ctx, id)
		if e != nil {
			return e
		}
		if !found || ch.CharacterID != id {
			return r.ErrMailboxAssetTransactionUnavailable
		}
		if id == mailID && (ch.AccountID != accountID || ch.Stats["delete_flag"] != 0) {
			return r.ErrAccountCharacterMailboxOwnership
		}
	}
	a := &rentalScopedAccountRepository{accountID: accountID, base: base.Account}
	account, found, err := a.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !found || account.AccountID != accountID {
		return r.ErrMailboxAssetTransactionUnavailable
	}
	m := &mailboxAssetScopedMailboxRepository{characterID: mailID, base: base.Mailbox}
	if err = apply(a, c, i, m); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return tx.Commit()
}
