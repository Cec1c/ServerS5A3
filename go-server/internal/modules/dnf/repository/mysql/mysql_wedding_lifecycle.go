package mysql

import (
	"context"
	r "longheng.io/server/internal/modules/dnf/repository"
	"time"
)

var _ r.WeddingLifecycleUnitOfWork = (*mysqlCharacterTradeUnitOfWork)(nil)
var _ r.WeddingGuestLifecycleUnitOfWork = (*mysqlCharacterTradeUnitOfWork)(nil)

func (u *mysqlCharacterTradeUnitOfWork) WithinWeddingLifecycle(ctx context.Context, ids []string, scope string, apply func(r.Group) error) error {
	return u.withinWeddingLifecycle(ctx, ids, nil, scope, false, apply)
}

func (u *mysqlCharacterTradeUnitOfWork) WithinWeddingGuestLifecycle(ctx context.Context, hosts, guests []string, scope string, apply func(r.Group) error) error {
	return u.withinWeddingLifecycle(ctx, hosts, guests, scope, true, apply)
}

func (u *mysqlCharacterTradeUnitOfWork) withinWeddingLifecycle(ctx context.Context, hosts, guests []string, scope string, index bool, apply func(r.Group) error) error {
	ids, err := r.WeddingLifecycleIDs(hosts, guests, scope)
	if err != nil || u == nil || apply == nil {
		return r.ErrCharacterTradeTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return r.ErrCharacterTradeTransactionUnavailable
	}
	tx, err := beginObservedSQLTransaction(ctx, beginner, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	router := u.router
	router.db = newSQLTransactionDB(tx.Tx, router.db)
	router.readDBs = append([]string(nil), u.router.writeDBs...)
	router.writeDBs = append([]string(nil), u.router.writeDBs...)
	router.lockReads = true
	base := newMySQLGroupFromRouter(router, false)
	allowed := map[string]struct{}{}
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	// Match the shared room transaction's scope-first lock order. All wedding
	// guest writes serialize on this parent even when the ledger is initially absent.
	scopes := []string{scope}
	var extra []string
	if index {
		scopes = append([]string{r.WeddingGuestIndexScope}, scopes...)
		extra = []string{r.WeddingGuestIndexScope}
	}
	for _, key := range scopes {
		table, err := router.writeTable(mysqlSettingsTable, key)
		if err != nil {
			return err
		}
		// A duplicate no-op UPDATE takes an exclusive parent-row lock. INSERT
		// IGNORE would leave concurrent readers with shared locks which deadlock
		// when both subsequently promote to SELECT FOR UPDATE.
		if _, err = router.db.ExecContext(ctx, "INSERT INTO "+table+" (scope,updated_at) VALUES (?, ?) ON DUPLICATE KEY UPDATE scope=VALUES(scope)", key, timeOrNow(time.Time{}, router.now)); err != nil {
			return err
		}
		if _, _, err = base.Settings.Load(ctx, key); err != nil {
			return err
		}
	}
	chars := &characterTradeScopedCharacterRepository{allowed: allowed, base: base.Character}
	inv := &characterTradeScopedInventoryRepository{allowed: allowed, base: base.Inventory}
	for _, id := range ids {
		c, found, e := chars.Load(ctx, id)
		if e != nil {
			return e
		}
		if !found || c.CharacterID != id {
			return r.ErrCharacterTradeTransactionUnavailable
		}
	}
	group := r.Group{Character: chars, Inventory: inv, Quest: r.ScopeWeddingQuests(base.Quest, ids), Settings: r.ScopeWeddingLifecycleSettings(base.Settings, base.Settings, scope, ids, extra...), Mailbox: r.ScopeWeddingMailboxes(base.Mailbox, ids)}
	if err = apply(group); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return tx.Commit()
}
