package mysql

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
)

type mysqlGuildUnitOfWork struct{ router mysqlRouter }

func (u *mysqlGuildUnitOfWork) WithinGuild(ctx context.Context, owners []repository.GuildOwner, apply func(repository.Group) error) error {
	return u.withinGuild(ctx, owners, false, false, false, false, "", apply)
}

func (u *mysqlGuildUnitOfWork) WithinGuildPresence(ctx context.Context, owner repository.GuildOwner, apply func(repository.Group) error) error {
	return u.withinGuild(ctx, []repository.GuildOwner{owner}, false, false, false, true, "", apply)
}

func (u *mysqlGuildUnitOfWork) WithinGuildTravel(ctx context.Context, owners []repository.GuildOwner, apply func(repository.Group) error) error {
	return u.withinGuild(ctx, owners, false, true, false, false, "", apply)
}

func (u *mysqlGuildUnitOfWork) WithinGuildDungeonEntry(ctx context.Context, owners []repository.GuildOwner, receiptScope string, apply func(repository.Group) error) error {
	if !repository.ValidGuildDungeonEntryReceiptScope(receiptScope) {
		return repository.ErrGuildTransaction
	}
	return u.withinGuild(ctx, owners, false, false, false, false, receiptScope, apply)
}

func (u *mysqlGuildUnitOfWork) WithinGuildInventory(ctx context.Context, owner repository.GuildOwner, apply func(repository.Group) error) error {
	return u.withinGuild(ctx, []repository.GuildOwner{owner}, true, false, false, false, "", apply)
}

func (u *mysqlGuildUnitOfWork) withinGuild(ctx context.Context, owners []repository.GuildOwner, withInventory, travel, raidClear, presenceOnly bool, receiptScope string, apply func(repository.Group) error) error {
	var ordered []repository.GuildOwner
	var err error
	switch {
	case raidClear:
		ordered, err = repository.GuildRaidClearOwners(owners)
	case receiptScope != "":
		ordered, err = repository.GuildDungeonEntryOwners(owners)
	case travel:
		ordered, err = repository.GuildTravelOwners(owners)
	default:
		ordered, err = repository.GuildOwners(owners)
	}
	if err != nil {
		return err
	}
	if u == nil || apply == nil {
		return repository.ErrGuildTransaction
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrGuildTransaction
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
	table, err := router.writeTable(mysqlSettingsTable, repository.GuildRegistryScope)
	if err != nil {
		return err
	}
	// Lock a durable registry row even on the first ever creation. A SELECT of
	// a missing row alone is not a portable mutual-exclusion boundary. The
	// duplicate branch changes no data, including the existing update time.
	if _, err := router.db.ExecContext(ctx, "INSERT INTO "+table+" (scope, updated_at) VALUES (?, ?) ON DUPLICATE KEY UPDATE scope = VALUES(scope)", repository.GuildRegistryScope, router.now()); err != nil {
		return err
	}
	base := newMySQLGroupFromRouter(router, false)
	if store, ok := base.Settings.(*mysqlSettingsStore); ok {
		store.cacheLockedReads = true
	}
	group := repository.GuildScopedGroup(base, ordered)
	if withInventory {
		group = repository.GuildInventoryScopedGroup(base, ordered[0])
	}
	if receiptScope != "" {
		group.Settings = repository.GuildDungeonEntrySettings(base.Settings, receiptScope)
	}
	if _, _, err = group.Settings.Load(ctx, repository.GuildRegistryScope); err != nil {
		return err
	}
	if receiptScope != "" {
		if _, _, err = group.Settings.Load(ctx, receiptScope); err != nil {
			return err
		}
	}
	for _, owner := range ordered {
		if presenceOnly {
			charTable, tableErr := router.readTable(mysqlCharTable, owner.CharacterID)
			if tableErr != nil {
				return tableErr
			}
			var charID, accountID string
			var deleteFlag int64
			scanErr := router.db.QueryRowContext(
				ctx,
				router.selectQuery("SELECT character_id, account_id, delete_flag FROM "+charTable+" WHERE character_id = ?"),
				owner.CharacterID,
			).Scan(&charID, &accountID, &deleteFlag)
			if scanErr != nil {
				found, mappedErr := scanErrOrNotFound(scanErr)
				if mappedErr != nil {
					return mappedErr
				}
				if !found {
					return repository.ErrGuildTransaction
				}
			}
			if charID != owner.CharacterID || accountID != owner.AccountID || deleteFlag != 0 {
				return repository.ErrGuildTransaction
			}
			continue
		}
		if _, _, err = group.Character.Load(ctx, owner.CharacterID); err != nil {
			return err
		}
	}
	if withInventory {
		if _, _, err = group.Inventory.Load(ctx, ordered[0].CharacterID); err != nil {
			return err
		}
	}
	if err = apply(group); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return errors.Join(repository.ErrGuildCommitUnknown, err)
	}
	return nil
}

func scanErrOrNotFound(err error) (bool, error) {
	return scanErr(err)
}

func (u *mysqlGuildUnitOfWork) WithinGuildRaidClear(ctx context.Context, owners []repository.GuildOwner, apply func(repository.Group) error) error {
	return u.withinGuild(ctx, owners, false, false, true, false, "", apply)
}

// WithinGuildArchive 在主库写事务内先加锁 GuildRegistryScope，再将归档读写与主注册表更新
// 放入同一原子 SQL 事务（读路由固定指向写库且启用 FOR UPDATE），避免读延迟与非原子覆盖。
func (u *mysqlGuildUnitOfWork) WithinGuildArchive(ctx context.Context, apply func(repository.SettingsRepository) error) error {
	if u == nil || apply == nil {
		return repository.ErrGuildTransaction
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrGuildTransaction
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
	table, err := router.writeTable(mysqlSettingsTable, repository.GuildRegistryScope)
	if err != nil {
		return err
	}
	if _, err := router.db.ExecContext(ctx, "INSERT INTO "+table+" (scope, updated_at) VALUES (?, ?) ON DUPLICATE KEY UPDATE scope = VALUES(scope)", repository.GuildRegistryScope, router.now()); err != nil {
		return err
	}
	base := newMySQLGroupFromRouter(router, false)
	if store, ok := base.Settings.(*mysqlSettingsStore); ok {
		store.cacheLockedReads = true
	}
	scoped := repository.GuildArchiveSettings(base.Settings)
	if _, _, err = scoped.Load(ctx, repository.GuildRegistryScope); err != nil {
		return err
	}
	if err = apply(scoped); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return errors.Join(repository.ErrGuildCommitUnknown, err)
	}
	return nil
}
