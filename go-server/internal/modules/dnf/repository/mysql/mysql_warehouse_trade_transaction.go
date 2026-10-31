package mysql

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"time"
)

func (u *mysqlCharacterTradeUnitOfWork) WithinCharacterWarehouseTrade(ctx context.Context, characters, accounts []string, apply func(repo.CharacterRepository, repo.InventoryRepository, repo.AccountInventoryRepository) error) error {
	c, a, err := repo.WarehouseTradeScope(characters, accounts)
	if err != nil || u == nil || apply == nil {
		return repo.ErrCharacterTradeTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repo.ErrCharacterTradeTransactionUnavailable
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
	group := newMySQLGroupFromRouter(router, false)
	// A first-time recipient has no warehouse parent row. Materialize and lock
	// each parent before loading balances so correctness does not depend on gap
	// locks or the database's transaction isolation setting.
	for _, id := range a {
		table, err := router.writeTable(mysqlAccountInventoryTable, id)
		if err != nil {
			return err
		}
		query := "INSERT INTO " + table + " (account_id, updated_at) VALUES (?, ?) ON DUPLICATE KEY UPDATE account_id = account_id"
		if _, err := router.db.ExecContext(ctx, query, id, timeOrNow(time.Time{}, router.now)); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	allowed := make(map[string]struct{}, len(c))
	for _, id := range c {
		allowed[id] = struct{}{}
	}
	allowedAccounts := make(map[string]struct{}, len(a))
	for _, id := range a {
		allowedAccounts[id] = struct{}{}
	}
	chars := &characterTradeScopedCharacterRepository{allowed: allowed, base: group.Character}
	inventory := &characterTradeScopedInventoryRepository{allowed: allowed, base: group.Inventory}
	warehouse := &warehouseTradeScopedAccountRepository{allowed: allowedAccounts, base: group.AccountInventory}
	if err := apply(chars, inventory, warehouse); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

type warehouseTradeScopedAccountRepository struct {
	allowed map[string]struct{}
	base    repo.AccountInventoryRepository
}

func (r *warehouseTradeScopedAccountRepository) Load(ctx context.Context, id string) (repo.AccountInventoryRecord, bool, error) {
	id = strings.TrimSpace(id)
	if _, ok := r.allowed[id]; !ok {
		return repo.AccountInventoryRecord{}, false, repo.ErrCharacterTradeTransactionUnavailable
	}
	return r.base.Load(ctx, id)
}
func (r *warehouseTradeScopedAccountRepository) Save(ctx context.Context, record repo.AccountInventoryRecord) error {
	if _, ok := r.allowed[strings.TrimSpace(record.AccountID)]; !ok {
		return repo.ErrCharacterTradeTransactionUnavailable
	}
	return r.base.Save(ctx, record)
}
