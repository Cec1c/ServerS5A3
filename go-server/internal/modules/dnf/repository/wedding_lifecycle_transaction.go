package repository

import (
	"context"
	"strconv"
)

const WeddingGuestIndexScope = "wedding:guest-index:v1"

// WeddingLifecycleUnitOfWork commits the reciprocal pair, their shared room,
// preparation quests and both mailboxes in one transaction. The callback only
// receives these scoped repositories, never an unscoped nested unit of work.
type WeddingLifecycleUnitOfWork interface {
	WithinWeddingLifecycle(context.Context, []string, string, func(Group) error) error
}

// WeddingGuestLifecycleUnitOfWork includes invited guests in the same physical
// transaction as the host pair's ledger. It must not emulate a multi-owner
// transaction with independent host or guest commits.
type WeddingGuestLifecycleUnitOfWork interface {
	WithinWeddingGuestLifecycle(context.Context, []string, []string, string, func(Group) error) error
}

func WeddingLifecycleIDs(hosts, guests []string, scope string) ([]string, error) {
	want, err := WeddingPairScope(hosts)
	if err != nil || scope != want {
		return nil, ErrCharacterTradeTransactionUnavailable
	}
	ids := append(append([]string(nil), hosts...), guests...)
	for _, id := range ids {
		n, err := strconv.ParseUint(id, 10, 16)
		if err != nil || n == 0 || strconv.FormatUint(n, 10) != id {
			return nil, ErrCharacterTradeTransactionUnavailable
		}
	}
	return CharacterInventoryGroupIDs(ids)
}

func ScopeWeddingMailboxes(base MailboxRepository, ids []string) MailboxRepository {
	allowed := make(map[string]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	return &weddingMailboxes{base: base, allowed: allowed}
}

// Container upgrades are read-only inputs for inventory slot validation. They
// do not grant the wedding owner a second writable settings namespace.
func ScopeWeddingLifecycleSettings(primary, readOnly SettingsRepository, scope string, ids []string, extra ...string) SettingsRepository {
	allowed := map[string]bool{}
	for _, id := range ids {
		allowed[CharacterContainerStateScope(id)] = true
	}
	writes := map[string]bool{scope: true}
	for _, key := range extra {
		if key == WeddingGuestIndexScope {
			writes[key] = true
		}
	}
	return &weddingLifecycleSettings{primary: primary, readOnly: readOnly, writes: writes, allowed: allowed}
}

type weddingLifecycleSettings struct {
	primary, readOnly SettingsRepository
	writes            map[string]bool
	allowed           map[string]bool
}

func (s *weddingLifecycleSettings) Load(ctx context.Context, scope string) (SettingsRecord, bool, error) {
	if s.writes[scope] {
		return s.primary.Load(ctx, scope)
	}
	if s.allowed[scope] {
		return s.readOnly.Load(ctx, scope)
	}
	return SettingsRecord{}, false, ErrCharacterTradeTransactionUnavailable
}
func (s *weddingLifecycleSettings) Save(ctx context.Context, record SettingsRecord) error {
	if !s.writes[record.Scope] {
		return ErrCharacterTradeTransactionUnavailable
	}
	return s.primary.Save(ctx, record)
}

type weddingMailboxes struct {
	base    MailboxRepository
	allowed map[string]bool
}

func (m *weddingMailboxes) Load(ctx context.Context, id string) (MailboxRecord, bool, error) {
	if !m.allowed[id] {
		return MailboxRecord{}, false, ErrCharacterTradeTransactionUnavailable
	}
	return m.base.Load(ctx, id)
}
func (m *weddingMailboxes) Save(ctx context.Context, record MailboxRecord) error {
	if !m.allowed[record.CharacterID] {
		return ErrCharacterTradeTransactionUnavailable
	}
	return m.base.Save(ctx, record)
}
func (m *weddingMailboxes) SaveFields(ctx context.Context, record MailboxRecord, fields ...MailboxField) error {
	if !m.allowed[record.CharacterID] {
		return ErrCharacterTradeTransactionUnavailable
	}
	return SaveMailboxFields(ctx, m.base, record, fields...)
}
