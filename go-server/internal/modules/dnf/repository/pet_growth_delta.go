package repository

import "context"

// PetGrowthWriter updates one existing creature read under the enclosing pet
// transaction. The complete record is retained for adapters without this API.
type PetGrowthWriter interface {
	SavePetGrowth(context.Context, PetRecord, string, PetEntry) error
}

func SavePetGrowth(ctx context.Context, repo PetRepository, after PetRecord, key string, before PetEntry) error {
	if writer, ok := repo.(PetGrowthWriter); ok {
		return writer.SavePetGrowth(ctx, after, key, before)
	}
	return SavePetFields(ctx, repo, after, PetFieldEntries)
}
