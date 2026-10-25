package memory

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
)

// 独立团本入口事务，复用现有交易的限定角色快照及失败恢复。
func (u *memoryCharacterTradeUnitOfWork) WithinRaidEntryAssets(ctx context.Context, ids []string, scope string, apply func(repository.CharacterRepository, repository.InventoryRepository, repository.SettingsRepository) error) error {

	ids, err := repository.CharacterInventoryGroupIDs(ids)
	if err != nil || apply == nil || u == nil || u.character == nil || u.inventory == nil || u.settings == nil || strings.TrimSpace(scope) == "" {
		return repository.ErrCharacterTradeTransactionUnavailable
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

	characterRecords := make(map[string]repository.CharacterRecord, 2)
	characterExists := make(map[string]bool, 2)
	inventoryRecords := make(map[string]repository.InventoryRecord, 2)
	inventoryExists := make(map[string]bool, 2)
	for _, characterID := range ids {
		character, found, err := u.character.Load(ctx, characterID)
		if err != nil {
			return err
		}
		characterRecords[characterID] = repository.CloneCharacter(character)
		characterExists[characterID] = found
		inventory, found, err := u.inventory.Load(ctx, characterID)
		if err != nil {
			return err
		}
		inventoryRecords[characterID] = repository.CloneInventory(inventory)
		inventoryExists[characterID] = found
	}
	originalCharacters := make(map[string]repository.CharacterRecord, 2)
	originalInventories := make(map[string]repository.InventoryRecord, 2)
	originalCharacterExists := make(map[string]bool, 2)
	originalInventoryExists := make(map[string]bool, 2)
	for _, id := range ids {
		originalCharacters[id] = repository.CloneCharacter(characterRecords[id])
		originalInventories[id] = repository.CloneInventory(inventoryRecords[id])
		originalCharacterExists[id] = characterExists[id]
		originalInventoryExists[id] = inventoryExists[id]
	}
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	characterTx := &memoryCharacterTradeCharacterTransaction{
		allowed: allowed,
		records: characterRecords,
		exists:  characterExists,
		dirty:   make(map[string]bool, 2),
	}
	inventoryTx := &memoryCharacterTradeInventoryTransaction{
		allowed: allowed,
		records: inventoryRecords,
		exists:  inventoryExists,
		dirty:   make(map[string]bool, 2),
	}
	previousSettings, settingsExists, err := u.settings.Load(ctx, scope)
	if err != nil {
		return err
	}
	settingsTx := &memorySettingsTransaction{scope: scope, record: repository.CloneSettings(previousSettings), exists: settingsExists}

	if err := apply(characterTx, inventoryTx, settingsTx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	type committedTradeRecord struct {
		characterID string
		wallet      bool
	}
	committed := make([]committedTradeRecord, 0, 4)
	rollback := func(cause error) error {
		errs := []error{cause}
		for index := len(committed) - 1; index >= 0; index-- {
			row := committed[index]
			if row.wallet {
				errs = append(errs, restoreCharacterRecord(u.character, row.characterID, originalCharacters[row.characterID], originalCharacterExists[row.characterID], true))
			} else {
				errs = append(errs, restoreInventoryRecord(u.inventory, row.characterID, originalInventories[row.characterID], originalInventoryExists[row.characterID]))
			}
		}
		return errors.Join(errs...)
	}
	for _, characterID := range ids {
		if !characterTx.dirty[characterID] {
			continue
		}
		committed = append(committed, committedTradeRecord{characterID: characterID, wallet: true})
		if err := u.character.Save(ctx, characterTx.records[characterID]); err != nil {
			return rollback(err)
		}
	}
	for _, characterID := range ids {
		if !inventoryTx.dirty[characterID] {
			continue
		}
		committed = append(committed, committedTradeRecord{characterID: characterID})
		if err := u.inventory.Save(ctx, inventoryTx.records[characterID]); err != nil {
			return rollback(err)
		}
	}
	if settingsTx.dirty {
		if err := u.settings.Save(ctx, settingsTx.record); err != nil {
			return rollback(errors.Join(err, restoreSettingsRecord(u.settings, scope, previousSettings, settingsExists, true)))
		}
	}

	return nil
}
