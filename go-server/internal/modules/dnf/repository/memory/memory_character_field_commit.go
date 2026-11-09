package memory

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
)

func (tx *memoryCharacterTransaction) commit(ctx context.Context, repo repository.CharacterRepository) error {
	return commitCharacterFields(ctx, repo, tx.record, tx.weddingDirty)
}

func (tx *memoryCharacterTradeCharacterTransaction) commit(ctx context.Context, repo repository.CharacterRepository, id string) error {
	return commitCharacterFields(ctx, repo, tx.records[id], tx.weddingDirty[id])
}

func commitCharacterFields(ctx context.Context, repo repository.CharacterRepository, record repository.CharacterRecord, wedding bool) error {
	if !wedding {
		return repo.Save(ctx, record)
	}
	return repository.SaveCharacterFields(ctx, repo, record, append(repository.AllCharacterFields(), repository.CharacterFieldWedding)...)
}
