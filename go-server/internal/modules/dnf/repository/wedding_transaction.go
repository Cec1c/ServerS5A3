package repository

import (
	"context"
	"sort"
	"strconv"
)

// Wedding assets belong to the exact reciprocal pair. The shared room and both
// character/inventory rows commit together, including purchase and placement.
type WeddingAssetUnitOfWork interface {
	WithinWeddingAssets(context.Context, []string, string, func(CharacterRepository, InventoryRepository, SettingsRepository) error) error
}

func WeddingPairScope(ids []string) (string, error) {
	if len(ids) != 2 || ids[0] == ids[1] {
		return "", ErrCharacterTradeTransactionUnavailable
	}
	ordered := append([]string(nil), ids...)
	for _, id := range ordered {
		n, err := strconv.ParseUint(id, 10, 16)
		if err != nil || n == 0 || strconv.FormatUint(n, 10) != id {
			return "", ErrCharacterTradeTransactionUnavailable
		}
	}
	sort.Strings(ordered)
	return "character-pair:wedding:v1:" + ordered[0] + ":" + ordered[1], nil
}
