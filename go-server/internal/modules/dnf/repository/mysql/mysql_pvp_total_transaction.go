package mysql

import (
	"context"
	"errors"

	"longheng.io/server/internal/modules/dnf/repository"
)

func (u *mysqlRentalAssetUnitOfWork) WithinPVPTotal(ctx context.Context, accountID string, roster [3]string, apply func(repository.AccountRepository, repository.CharacterRepository) error) error {
	ids, err := repository.PVPTotalTransactionIDs(accountID, roster)
	if err != nil {
		return err
	}
	if u == nil || apply == nil {
		return repository.ErrRentalAssetTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrRentalAssetTransactionUnavailable
	}
	tx, err := beginObservedSQLTransaction(ctx, beginner, nil)
	if err != nil {
		return err
	}
	router := u.router
	router.db = newSQLTransactionDB(tx.Tx, router.db)
	router.readDBs = append([]string(nil), u.router.writeDBs...)
	router.writeDBs = append([]string(nil), u.router.writeDBs...)
	router.lockReads = true
	group := newMySQLGroupFromRouter(router, false)
	accounts := &rentalScopedAccountRepository{accountID: accountID, base: group.Account}
	allowed := map[string]struct{}{}
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	characters := &characterTradeScopedCharacterRepository{allowed: allowed, base: group.Character}
	// Lock account first and characters in sorted order, including callers that
	// need to project in the user-selected order later.
	if _, _, err = accounts.Load(ctx, accountID); err == nil {
		for _, id := range ids {
			if _, _, err = characters.Load(ctx, id); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = apply(accounts, characters)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}
