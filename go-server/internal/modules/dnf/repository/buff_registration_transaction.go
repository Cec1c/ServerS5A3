package repository

import (
	"context"
	"fmt"
	"strings"
)

// WithinBuffRegistration reuses the existing physical asset transaction, while
// exposing only this character and its exact native buff settings scope.
// Wallet repositories are deliberately excluded from the domain callback.
func WithinBuffRegistration(ctx context.Context, base Group, characterID, accountID string, apply func(Group) error) error {
	characterID, accountID = strings.TrimSpace(characterID), strings.TrimSpace(accountID)
	if characterID == "" || accountID == "" || apply == nil || base.CeraShopAssets == nil {
		return fmt.Errorf("buff registration transaction unavailable")
	}
	scope := "character:" + characterID + ":buff_swap_native"
	return base.CeraShopAssets.WithinCeraShopAssets(ctx, accountID, characterID, scope, func(_ AccountRepository, characters CharacterRepository, inventories InventoryRepository, equipment EquipmentRepository, settings SettingsRepository) error {
		character, found, err := characters.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != characterID || character.AccountID != accountID {
			return fmt.Errorf("buff registration owner mismatch")
		}
		return apply(Group{Character: characters, Inventory: inventories, Equipment: equipment, Settings: settings})
	})
}
