package mysql

import (
	"context"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func (u *mysqlCharacterTradeUnitOfWork) WithinWeddingQuests(ctx context.Context, ids []string, apply func(r.CharacterRepository, r.QuestRepository) error) error {
	if _, err := r.WeddingPairScope(ids); err != nil || u == nil || apply == nil {
		return r.ErrCharacterTradeTransactionUnavailable
	}
	ids, _ = r.CharacterInventoryGroupIDs(ids)
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
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
	router.lockReads = true
	group := newMySQLGroupFromRouter(router, false)
	allowed := map[string]struct{}{}
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	chars := &characterTradeScopedCharacterRepository{allowed: allowed, base: group.Character}
	if err = apply(chars, r.ScopeWeddingQuests(group.Quest, ids)); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err = ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}
