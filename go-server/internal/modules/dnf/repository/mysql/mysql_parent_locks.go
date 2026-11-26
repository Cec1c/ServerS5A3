package mysql

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

// Parent lock only: callbacks populate complete delta before-images with their
// own Load, while Save without Load retains the full replacement path.
func (r mysqlRouter) lockAccountRow(ctx context.Context, id string) (bool, error) {
	if err := ctxErr(ctx); err != nil {
		return false, err
	}
	table, err := r.readTable(mysqlAccountTable, id)
	if err != nil {
		return false, err
	}
	var stored string
	if err = r.db.QueryRowContext(ctx, r.selectQuery("SELECT account_id FROM "+table+" WHERE account_id = ?"), id).Scan(&stored); err != nil {
		return scanErr(err)
	}
	return stored == id, nil
}
func (r *rentalScopedCharacterRepository) LoadCharacterIdentity(ctx context.Context, id string) (repository.CharacterIdentity, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.characterID, id); err != nil {
		return repository.CharacterIdentity{}, false, err
	}
	return repository.LoadCharacterIdentity(ctx, r.base, id)
}
func (r *characterTradeScopedCharacterRepository) LoadCharacterIdentity(ctx context.Context, id string) (repository.CharacterIdentity, bool, error) {
	id = strings.TrimSpace(id)
	if _, ok := r.allowed[id]; !ok {
		return repository.CharacterIdentity{}, false, repository.ErrCharacterTradeTransactionUnavailable
	}
	return repository.LoadCharacterIdentity(ctx, r.ownerStore(id), id)
}

func (r *rentalScopedCharacterRepository) LoadCharacterMirrorStats(ctx context.Context, id string, keys ...string) (repository.CharacterMirrorStats, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.characterID, id); err != nil {
		return repository.CharacterMirrorStats{}, false, err
	}
	return repository.LoadCharacterMirrorStats(ctx, r.base, id, keys...)
}
func (r *characterTradeScopedCharacterRepository) LoadCharacterMirrorStats(ctx context.Context, id string, keys ...string) (repository.CharacterMirrorStats, bool, error) {
	id = strings.TrimSpace(id)
	if _, ok := r.allowed[id]; !ok {
		return repository.CharacterMirrorStats{}, false, repository.ErrCharacterTradeTransactionUnavailable
	}
	return repository.LoadCharacterMirrorStats(ctx, r.ownerStore(id), id, keys...)
}
