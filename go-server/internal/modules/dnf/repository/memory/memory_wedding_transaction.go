package memory

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
)

func (u *memoryCharacterTradeUnitOfWork) WithinWeddingAssets(ctx context.Context, ids []string, scope string, apply func(repository.CharacterRepository, repository.InventoryRepository, repository.SettingsRepository) error) error {
	want, err := repository.WeddingPairScope(ids)
	if err != nil || scope != want {
		return repository.ErrCharacterTradeTransactionUnavailable
	}
	return u.withinScopedCharacterAssets(ctx, ids, scope, apply)
}
