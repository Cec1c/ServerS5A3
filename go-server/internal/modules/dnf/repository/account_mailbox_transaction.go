package repository

import "context"

// AccountMailboxUnitOfWork is an optional extension of MailboxAssets. It
// validates the selected character's account inside the same transaction and
// exposes only that account and that character's mailbox. Activity receipts
// must not commit separately from their reward mail.
type AccountMailboxUnitOfWork interface {
	WithinAccountMailbox(context.Context, string, string, func(AccountRepository, MailboxRepository) error) error
}

// The character snapshot is validated and locked by the same transaction.
// It permits eligibility checks, not character writes.
type AccountMailboxCharacterUnitOfWork interface {
	WithinAccountMailboxCharacter(context.Context, string, string, func(AccountRepository, CharacterRecord, MailboxRepository) error) error
}
