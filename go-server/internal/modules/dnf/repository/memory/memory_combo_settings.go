package memory

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"sort"
)

type memorySettlementSettingsTransaction struct {
	characterID    string
	base           repository.SettingsRepository
	records        map[string]repository.SettingsRecord
	exists         map[string]bool
	original       map[string]repository.SettingsRecord
	originalExists map[string]bool
	dirty          map[string]bool
	committed      []string
}

func (tx *memorySettlementSettingsTransaction) Load(ctx context.Context, scope string) (repository.SettingsRecord, bool, error) {
	if tx == nil || tx.base == nil || !repository.CharacterSkillSettingsScope(tx.characterID, scope) {
		return repository.SettingsRecord{}, false, repository.ErrCharacterSettlementTransactionUnavailable
	}
	if record, cached := tx.records[scope]; cached || tx.exists[scope] {
		return repository.CloneSettings(record), tx.exists[scope], nil
	}
	record, found, err := tx.base.Load(ctx, scope)
	if err != nil {
		return repository.SettingsRecord{}, false, err
	}
	tx.records[scope] = repository.CloneSettings(record)
	tx.exists[scope] = found
	tx.original[scope] = repository.CloneSettings(record)
	tx.originalExists[scope] = found
	return repository.CloneSettings(record), found, nil
}

func (tx *memorySettlementSettingsTransaction) Save(ctx context.Context, record repository.SettingsRecord) error {
	if tx == nil || tx.base == nil {
		return repository.ErrCharacterSettlementTransactionUnavailable
	}
	scope := repository.SettingsKey(record)
	if !repository.CharacterSkillSettingsScope(tx.characterID, scope) {
		return repository.ErrCharacterSettlementTransactionUnavailable
	}
	if _, captured := tx.original[scope]; !captured {
		original, found, err := tx.base.Load(ctx, scope)
		if err != nil {
			return err
		}
		tx.original[scope] = repository.CloneSettings(original)
		tx.originalExists[scope] = found
	}
	tx.records[scope] = repository.CloneSettings(record)
	tx.exists[scope] = true
	tx.dirty[scope] = true
	return nil
}

func (tx *memorySettlementSettingsTransaction) dirtyCount() int {
	if tx == nil {
		return 0
	}
	return len(tx.dirty)
}

func (tx *memorySettlementSettingsTransaction) commit(ctx context.Context) error {
	scopes := make([]string, 0, len(tx.dirty))
	for scope := range tx.dirty {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	for _, scope := range scopes {
		// A store is allowed to mutate and then report an error. Register the
		// scope before Save so the pre-image is restored in either outcome.
		tx.committed = append(tx.committed, scope)
		if err := tx.base.Save(ctx, tx.records[scope]); err != nil {
			return errors.Join(err, tx.rollback())
		}
	}
	return nil
}

func (tx *memorySettlementSettingsTransaction) rollback() error {
	if tx == nil || tx.base == nil {
		return nil
	}
	var rollbackErr error
	for index := len(tx.committed) - 1; index >= 0; index-- {
		scope := tx.committed[index]
		rollbackErr = errors.Join(rollbackErr, restoreSettingsRecord(
			tx.base,
			scope,
			tx.original[scope],
			tx.originalExists[scope],
			true,
		))
	}
	tx.committed = nil
	return rollbackErr
}
