package memory

import (
	"context"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

// WithinAccountCharacterPetAssets commits account inventory, the character
// wallet, both character item aggregates, and pets under one aggregate lock.
// The pet repository is written last, so a late pet failure must restore all
// previously attempted containers from their captured preimages. It must not
// invoke CharacterPets because both entry points share the same aggregate lock.
func (u *memoryAccountCharacterAssetUnitOfWork) WithinAccountCharacterPetAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(repository.AccountInventoryRepository, repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository, repository.PetRepository) error,
) error {
	accountID = strings.TrimSpace(accountID)
	characterID = strings.TrimSpace(characterID)
	if accountID == "" || characterID == "" || apply == nil || u == nil ||
		u.accountInventory == nil || u.character == nil || u.inventory == nil || u.equipment == nil || u.pet == nil {
		return repository.ErrAccountCharacterAssetTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	lock := &u.mu
	if u.sharedMu != nil {
		lock = u.sharedMu
	}
	lock.Lock()
	defer lock.Unlock()

	accountRecord, accountExists, err := u.accountInventory.Load(ctx, accountID)
	if err != nil {
		return err
	}
	characterRecord, characterExists, err := u.character.Load(ctx, characterID)
	if err != nil {
		return err
	}
	inventoryRecord, inventoryExists, err := u.inventory.Load(ctx, characterID)
	if err != nil {
		return err
	}
	equipmentRecord, equipmentExists, err := u.equipment.Load(ctx, characterID)
	if err != nil {
		return err
	}
	petRecord, petExists, err := u.pet.Load(ctx, characterID)
	if err != nil {
		return err
	}

	accountTx := &memoryAccountInventoryTransaction{
		accountID: accountID,
		record:    repository.CloneAccountInventory(accountRecord),
		exists:    accountExists,
	}
	characterTx := &memoryCharacterTransaction{
		characterID: characterID,
		record:      repository.CloneCharacter(characterRecord),
		exists:      characterExists,
	}
	inventoryTx := &memoryInventoryTransaction{
		characterID: characterID,
		record:      repository.CloneInventory(inventoryRecord),
		exists:      inventoryExists,
	}
	equipmentTx := &memoryEquipmentTransaction{
		characterID: characterID,
		record:      repository.CloneEquipment(equipmentRecord),
		exists:      equipmentExists,
	}
	petTx := &memoryPetTransaction{
		characterID: characterID,
		record:      repository.ClonePet(petRecord),
		exists:      petExists,
	}
	if err := apply(accountTx, characterTx, inventoryTx, equipmentTx, petTx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// All preimages are captured before any physical write. The helper marks
	// each attempted write before calling Save, restores them in reverse order
	// on errors, cancellation, or a Save panic, and holds sharedMu throughout.
	return commitMemoryLedgerAssets(ctx,
		memoryLedgerWrite{dirty: accountTx.dirty, save: func() error { return u.accountInventory.Save(ctx, accountTx.record) }, restore: func() error {
			return restoreAccountInventoryRecord(u.accountInventory, accountID, accountRecord, accountExists)
		}},
		memoryLedgerWrite{dirty: characterTx.dirty, save: func() error { return u.character.Save(ctx, characterTx.record) }, restore: func() error {
			return restoreCharacterRecord(u.character, characterID, characterRecord, characterExists, true)
		}},
		memoryLedgerWrite{dirty: inventoryTx.dirty, save: func() error { return u.inventory.Save(ctx, inventoryTx.record) }, restore: func() error {
			return restoreInventoryRecordIfDirty(u.inventory, characterID, inventoryRecord, inventoryExists, true)
		}},
		memoryLedgerWrite{dirty: equipmentTx.dirty, save: func() error { return u.equipment.Save(ctx, equipmentTx.record) }, restore: func() error {
			return restoreEquipmentRecordIfDirty(u.equipment, characterID, equipmentRecord, equipmentExists, true)
		}},
		memoryLedgerWrite{dirty: petTx.dirty, save: func() error { return u.pet.Save(ctx, petTx.record) }, restore: func() error {
			return restorePetRecordIfDirty(u.pet, characterID, petRecord, petExists, true)
		}},
	)
}
