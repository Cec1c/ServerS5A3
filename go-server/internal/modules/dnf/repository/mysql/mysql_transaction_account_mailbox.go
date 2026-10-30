package mysql

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

var _ repository.AccountMailboxUnitOfWork = (*mysqlMailboxAssetUnitOfWork)(nil)

func (u *mysqlMailboxAssetUnitOfWork) WithinAccountMailbox(ctx context.Context, accountID, characterID string, apply func(repository.AccountRepository, repository.MailboxRepository) error) error {
	if apply == nil {
		return repository.ErrMailboxAssetTransactionUnavailable
	}
	return u.WithinAccountMailboxCharacter(ctx, accountID, characterID, func(a repository.AccountRepository, _ repository.CharacterRecord, m repository.MailboxRepository) error {
		return apply(a, m)
	})
}

var _ repository.AccountMailboxCharacterUnitOfWork = (*mysqlMailboxAssetUnitOfWork)(nil)

func (u *mysqlMailboxAssetUnitOfWork) WithinAccountMailboxCharacter(ctx context.Context, accountID, characterID string, apply func(repository.AccountRepository, repository.CharacterRecord, repository.MailboxRepository) error) error {
	accountID, characterID = strings.TrimSpace(accountID), strings.TrimSpace(characterID)
	if accountID == "" || characterID == "" || apply == nil || u == nil {
		return repository.ErrMailboxAssetTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b, ok := u.router.db.(sqlTxBeginner)
	if !ok || len(u.router.writeDBs) == 0 {
		return repository.ErrMailboxAssetTransactionUnavailable
	}
	tx, err := b.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // Also covers a panicking callback; Commit marks it done.
	r := u.router
	r.db = newSQLTransactionDB(tx, r.db)
	r.readDBs = append([]string(nil), u.router.writeDBs...)
	r.writeDBs = append([]string(nil), u.router.writeDBs...)
	r.lockReads = true
	base := newMySQLGroupFromRouter(r, false)
	// Account-first matches the Fate ledger, durability, trade mailbox and
	// settlement writers. Loading the character first creates an account/
	// character deadlock cycle with those account-first transactions.
	accounts := &rentalScopedAccountRepository{accountID: accountID, base: base.Account}
	account, found, err := accounts.Load(ctx, accountID)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if !found || account.AccountID != accountID {
		return errors.Join(repository.ErrMailboxAssetTransactionUnavailable, tx.Rollback())
	}
	character, found, err := base.Character.Load(ctx, characterID)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if !found || character.CharacterID != characterID || character.AccountID != accountID {
		return errors.Join(repository.ErrMailboxAssetTransactionUnavailable, tx.Rollback())
	}
	mailboxes := &mailboxAssetScopedMailboxRepository{characterID: characterID, base: base.Mailbox}
	if err := apply(accounts, repository.CloneCharacter(character), mailboxes); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}
