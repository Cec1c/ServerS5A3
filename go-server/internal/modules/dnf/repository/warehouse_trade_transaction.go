package repository

import "context"

// CharacterWarehouseTradeUnitOfWork keeps both account warehouses and both
// characters on one transaction. Account keys are locked before character keys.
type CharacterWarehouseTradeUnitOfWork interface {
	WithinCharacterWarehouseTrade(context.Context, []string, []string, func(CharacterRepository, InventoryRepository, AccountInventoryRepository) error) error
}

func WarehouseTradeScope(characters, accounts []string) ([]string, []string, error) {
	c, err := CharacterInventoryGroupIDs(characters)
	if err != nil || len(c) != 2 {
		return nil, nil, ErrCharacterTradeTransactionUnavailable
	}
	a, err := CharacterInventoryGroupIDs(accounts)
	if err != nil || len(a) > 2 {
		return nil, nil, ErrCharacterTradeTransactionUnavailable
	}
	return c, a, nil
}
