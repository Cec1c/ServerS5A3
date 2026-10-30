package mysql

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"time"
)

func (u *mysqlCharacterTradeUnitOfWork) WithinRaidEntryAssets(ctx context.Context, ids []string, scope string, apply func(repository.CharacterRepository, repository.InventoryRepository, repository.SettingsRepository) error) error {

	ids, err := repository.CharacterInventoryGroupIDs(ids)
	if err != nil || u == nil || apply == nil || strings.TrimSpace(scope) == "" {
		return repository.ErrCharacterTradeTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrCharacterTradeTransactionUnavailable
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // Also release the SQL transaction if the callback panics.
	txRouter := u.router
	txRouter.db = newSQLTransactionDB(tx, txRouter.db)
	// Character IDs can route to different logical schemas. They remain on
	// one physical MySQL connection, so keep the full qualified write plan.
	txRouter.readDBs = append([]string(nil), u.router.writeDBs...)
	txRouter.writeDBs = append([]string(nil), u.router.writeDBs...)
	txRouter.lockReads = true
	txGroup := newMySQLGroupFromRouter(txRouter, false)
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	characters := &characterTradeScopedCharacterRepository{allowed: allowed, base: txGroup.Character}
	inventories := &characterTradeScopedInventoryRepository{allowed: allowed, base: txGroup.Inventory}
	table, err := txRouter.writeTable(mysqlSettingsTable, scope)
	if err != nil {
		return err
	}
	if _, err := txRouter.db.ExecContext(ctx, "INSERT IGNORE INTO "+table+" (scope,updated_at) VALUES (?, ?)", scope, timeOrNow(time.Time{}, txRouter.now)); err != nil {
		return err
	}
	settings := &raidEntrySettings{scope: scope, base: txGroup.Settings}
	if _, _, err := settings.Load(ctx, scope); err != nil {
		return err
	}

	if err := apply(characters, inventories, settings); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

type raidEntrySettings struct {
	scope string
	base  repository.SettingsRepository
}

func (s *raidEntrySettings) Load(ctx context.Context, key string) (repository.SettingsRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, s.scope, key); err != nil {
		return repository.SettingsRecord{}, false, err
	}
	return s.base.Load(ctx, key)
}
func (s *raidEntrySettings) Save(ctx context.Context, r repository.SettingsRecord) error {
	if err := repository.TransactionKeyError(ctx, s.scope, r.Scope); err != nil {
		return err
	}
	return s.base.Save(ctx, r)
}
