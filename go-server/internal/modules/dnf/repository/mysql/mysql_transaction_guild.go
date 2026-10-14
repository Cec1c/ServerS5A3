package mysql

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
)

type mysqlGuildUnitOfWork struct{ router mysqlRouter }

func (u *mysqlGuildUnitOfWork) WithinGuild(ctx context.Context, owners []repository.GuildOwner, apply func(repository.Group) error) error {
	ordered, err := repository.GuildOwners(owners)
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
	group := repository.GuildScopedGroup(newMySQLGroupFromRouter(router, false), ordered)
	if _, _, err = group.Settings.Load(ctx, repository.GuildRegistryScope); err != nil {
		return err
	}
	for _, owner := range ordered {
		if _, _, err = group.Character.Load(ctx, owner.CharacterID); err != nil {
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
