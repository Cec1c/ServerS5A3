package memory

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

var _ repository.AccountMailboxUnitOfWork = (*memoryMailboxAssetUnitOfWork)(nil)

func (u *memoryMailboxAssetUnitOfWork) WithinAccountMailbox(ctx context.Context, accountID, characterID string, apply func(repository.AccountRepository, repository.MailboxRepository) error) error {
	if apply == nil {
		return repository.ErrMailboxAssetTransactionUnavailable
	}
	return u.WithinAccountMailboxCharacter(ctx, accountID, characterID, func(a repository.AccountRepository, _ repository.CharacterRecord, m repository.MailboxRepository) error {
		return apply(a, m)
	})
}

var _ repository.AccountMailboxCharacterUnitOfWork = (*memoryMailboxAssetUnitOfWork)(nil)

func (u *memoryMailboxAssetUnitOfWork) WithinAccountMailboxCharacter(ctx context.Context, accountID, characterID string, apply func(repository.AccountRepository, repository.CharacterRecord, repository.MailboxRepository) error) error {
	accountID, characterID = strings.TrimSpace(accountID), strings.TrimSpace(characterID)
	if accountID == "" || characterID == "" || apply == nil || u == nil || u.account == nil || u.character == nil || u.mailbox == nil {
		return repository.ErrMailboxAssetTransactionUnavailable
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
	character, found, err := u.character.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found || character.CharacterID != characterID || character.AccountID != accountID {
		return repository.ErrMailboxAssetTransactionUnavailable
	}
	account, found, err := u.account.Load(ctx, accountID)
	if err != nil {
		return err
	}
	if !found || account.AccountID != accountID {
		return repository.ErrMailboxAssetTransactionUnavailable
	}
	mailbox, mailExists, err := u.mailbox.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if mailExists && mailbox.CharacterID != characterID {
		return repository.ErrMailboxAssetTransactionUnavailable
	}
	a := &memoryAccountTransaction{accountID: accountID, record: repository.CloneAccount(account), exists: true}
	m := &memoryMailboxTransaction{characterID: characterID, record: repository.CloneMailbox(mailbox), exists: mailExists}
	if err := apply(a, repository.CloneCharacter(character), m); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.dirty {
		if err := u.account.Save(ctx, a.record); err != nil {
			return errors.Join(err, restoreAccountRecord(u.account, accountID, account, true, true))
		}
	}
	if m.dirty {
		if err := u.mailbox.Save(ctx, m.record); err != nil {
			return errors.Join(err, restoreMailboxRecordIfDirty(u.mailbox, characterID, mailbox, mailExists, true), restoreAccountRecord(u.account, accountID, account, true, a.dirty))
		}
	}
	return nil
}
