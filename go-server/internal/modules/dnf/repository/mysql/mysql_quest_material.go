package mysql

import (
	"context"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type mysqlQuestMaterialPreparation struct{ router mysqlRouter }

func (u *mysqlQuestMaterialPreparation) WithinQuestMaterialPreparation(ctx context.Context, ids []string, apply func(r.Group) error) error {
	if u == nil || apply == nil || len(ids) == 0 || len(ids) > 4 {
		return r.ErrCharacterSettlementTransactionUnavailable
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return r.ErrCharacterSettlementTransactionUnavailable
	}
	tx, err := beginObservedSQLTransaction(ctx, beginner, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	router := u.router
	router.db = newSQLTransactionDB(tx.Tx, router.db)
	router.readDBs = append([]string(nil), router.writeDBs...)
	router.lockReads = true
	group := r.QuestMaterialPreparationGroup(newMySQLGroupFromRouter(router, false), ids)
	if err = apply(group); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	// An ambiguous commit deliberately returns an error. The caller must retry
	// this same event; immutable journal entries suppress re-planning.
	return tx.Commit()
}
