package mysql

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
)

type mysqlSemesterClearUnitOfWork struct{ router mysqlRouter }

func (u *mysqlSemesterClearUnitOfWork) WithinSemesterClear(ctx context.Context, source repository.SemesterClearOwner, owners []repository.SemesterClearOwner, scope string, apply func(repository.Group) error) error {
	ordered, err := repository.SemesterClearOwners(source, owners, scope)
	if err != nil {
		return err
	}
	if u == nil || apply == nil {
		return repository.ErrSemesterClearTransaction
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrSemesterClearTransaction
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// A callback panic or a definite pre-commit error releases the SQL tx. Commit
	// error remains unknown; database/sql may already consider the tx finished.
	defer tx.Rollback()
	router := u.router
	router.db = newSQLTransactionDB(tx, router.db)
	router.readDBs = append([]string(nil), u.router.writeDBs...)
	router.writeDBs = append([]string(nil), u.router.writeDBs...)
	router.lockReads = true
	raw := newMySQLGroupFromRouter(router, false)
	scoped := repository.SemesterClearScopedGroup(raw, source, ordered, scope)
	if _, _, err = scoped.Settings.Load(ctx, scope); err != nil {
		return err
	}
	for _, owner := range ordered {
		if _, _, err = scoped.Character.Load(ctx, owner.CharacterID); err != nil {
			return err
		}
	}
	if _, _, err = scoped.Quest.Load(ctx, source.CharacterID); err != nil {
		return err
	}
	if err = apply(scoped); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errors.Join(repository.ErrSemesterClearCommitUnknown, err)
	}
	return nil
}
