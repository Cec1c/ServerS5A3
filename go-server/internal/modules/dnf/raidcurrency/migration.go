package raidcurrency

import (
	"context"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// MigrateCharacter keeps the account ledger and source rows under the same
// account lock as inventory grants. It is safe to retry after a lost response.
func MigrateCharacter(ctx context.Context, group repo.Group, accountID, characterID string, now time.Time) (uint32, error) {
	owner, ok := group.AccountAssets.(repo.AccountCharacterLedgerAssetUnitOfWork)
	if !ok || accountID == "" || characterID == "" {
		return 0, ErrState
	}
	var moved uint32
	err := owner.WithinAccountCharacterLedgerAssets(ctx, accountID, characterID, func(accounts repo.AccountRepository, _ repo.AccountInventoryRepository, characters repo.CharacterRepository, inventories repo.InventoryRepository, _ repo.EquipmentRepository) error {
		if accounts == nil || characters == nil || inventories == nil {
			return ErrState
		}
		character, found, err := characters.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != characterID || character.AccountID != accountID {
			return ErrState
		}
		account, found, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found || account.AccountID != accountID {
			return ErrState
		}
		inventory, found, err := inventories.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if inventory.CharacterID != characterID {
			return ErrState
		}
		account = repo.CloneAccount(account)
		inventory = repo.CloneInventory(inventory)
		moved, err = Consolidate(&account, &inventory, now)
		if err != nil || moved == 0 {
			return err
		}
		account.UpdatedAt, inventory.UpdatedAt = now.UTC(), now.UTC()
		if err := repo.SaveInventoryFields(ctx, inventories, inventory, repo.InventoryFieldSlots); err != nil {
			return err
		}
		return accounts.Save(ctx, account)
	})
	if err != nil {
		return 0, err
	}
	return moved, nil
}
