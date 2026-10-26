package memory

import (
	"context"
	"errors"

	"longheng.io/server/internal/modules/dnf/repository"
)

func (u *memoryRentalAssetUnitOfWork) WithinPVPTotal(ctx context.Context, accountID string, roster [3]string, apply func(repository.AccountRepository, repository.CharacterRepository) error) error {
	ids, err := repository.PVPTotalTransactionIDs(accountID, roster)
	if err != nil {
		return err
	}
	if u == nil || apply == nil || u.account == nil || u.character == nil {
		return repository.ErrRentalAssetTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lock := &u.mu
	if u.sharedMu != nil {
		lock = u.sharedMu
	}
	lock.Lock()
	defer lock.Unlock()
	account, accountExists, err := u.account.Load(ctx, accountID)
	if err != nil {
		return err
	}
	a := &memoryAccountTransaction{accountID: accountID, record: repository.CloneAccount(account), exists: accountExists}
	c := &memoryCharacterTradeCharacterTransaction{allowed: map[string]struct{}{}, records: map[string]repository.CharacterRecord{}, exists: map[string]bool{}, dirty: map[string]bool{}}
	original := map[string]repository.CharacterRecord{}
	for _, id := range ids {
		v, found, err := u.character.Load(ctx, id)
		if err != nil {
			return err
		}
		c.allowed[id] = struct{}{}
		c.records[id], c.exists[id], original[id] = repository.CloneCharacter(v), found, repository.CloneCharacter(v)
	}
	if err := apply(a, c); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var committed []string
	accountCommitted := false
	rollback := func(cause error) error {
		errs := []error{cause}
		for _, id := range committed {
			errs = append(errs, restoreCharacterRecord(u.character, id, original[id], c.exists[id], true))
		}
		if accountCommitted {
			errs = append(errs, restoreAccountRecord(u.account, accountID, account, accountExists, true))
		}
		return errors.Join(errs...)
	}
	if a.dirty {
		accountCommitted = true
		if err := u.account.Save(ctx, a.record); err != nil {
			return rollback(err)
		}
	}
	for _, id := range ids {
		if c.dirty[id] {
			committed = append(committed, id)
			if err := u.character.Save(ctx, c.records[id]); err != nil {
				return rollback(err)
			}
		}
	}
	return nil
}
