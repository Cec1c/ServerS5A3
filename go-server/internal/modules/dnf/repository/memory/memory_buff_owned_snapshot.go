package memory

import (
	"context"
	"errors"
	"sort"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

// Explicit caller-provided memory fixtures, never an import or fabricated
// production asset. The default group does not acquire any Legacy capability.
func NewMemoryGroupWithBuffLegacyItems(items []repository.OwnedLegacyItem) repository.Group {
	group := NewMemoryGroup()
	u := group.CeraShopAssets.(*memoryCeraShopAssetUnitOfWork)
	u.hasOwnedLegacy = true
	for _, item := range items {
		u.ownedLegacy = append(u.ownedLegacy, repository.CloneOwnedLegacyItem(item))
	}
	return group
}

func (u *memoryCeraShopAssetUnitOfWork) WithinBuffOwnedSnapshot(ctx context.Context, accountID, characterID, scope string,
	apply func(repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository, repository.SettingsRepository, repository.BuffPetMetadataReader, repository.OwnedLegacyInventoryRepository) error,
) error {
	accountID, characterID, scope = strings.TrimSpace(accountID), strings.TrimSpace(characterID), strings.TrimSpace(scope)
	if u == nil || u.character == nil || u.settings == nil || accountID == "" || characterID == "" || scope != "character:"+characterID+":buff_swap_native" || apply == nil {
		return repository.ErrBuffOwnedSnapshotUnavailable
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
	if err := ctx.Err(); err != nil {
		return err
	}
	character, found, err := u.character.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found || character.CharacterID != characterID || character.AccountID != accountID {
		return repository.ErrBuffLegacyOwnerMismatch
	}
	settings, settingsFound, err := u.settings.Load(ctx, scope)
	if err != nil {
		return err
	}
	characterTx := &memoryBuffReadCharacter{owner: characterID, record: repository.CloneCharacter(character)}
	settingsTx := &memorySettingsTransaction{scope: scope, record: repository.CloneSettings(settings), exists: settingsFound}
	inventory := &memoryBuffLazyInventory{owner: characterID, base: u.inventory}
	equipment := &memoryBuffLazyEquipment{owner: characterID, base: u.equipment}
	var pet repository.BuffPetMetadataReader
	if u.pet != nil {
		pet = &memoryBuffPetMetadata{owner: characterID, base: u.pet}
	}
	var legacy repository.OwnedLegacyInventoryRepository
	if u.hasOwnedLegacy {
		legacy = &memoryBuffLegacySnapshot{owner: characterID, rows: u.ownedLegacy}
	}
	if err := apply(characterTx, inventory, equipment, settingsTx, pet, legacy); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	invDirty := inventory.tx != nil && inventory.tx.dirty
	equipDirty := equipment.tx != nil && equipment.tx.dirty
	if invDirty {
		if err := u.inventory.Save(ctx, inventory.tx.record); err != nil {
			return errors.Join(err, restoreInventoryRecordIfDirty(u.inventory, characterID, inventory.original, inventory.existed, true))
		}
	}
	if equipDirty {
		if err := u.equipment.Save(ctx, equipment.tx.record); err != nil {
			return errors.Join(err, restoreEquipmentRecordIfDirty(u.equipment, characterID, equipment.original, equipment.existed, true), restoreInventoryRecordIfDirty(u.inventory, characterID, inventory.original, inventory.existed, invDirty))
		}
	}
	if settingsTx.dirty {
		if err := u.settings.Save(ctx, settingsTx.record); err != nil {
			return errors.Join(err, restoreSettingsRecord(u.settings, scope, settings, settingsFound, true), restoreEquipmentRecordIfDirty(u.equipment, characterID, equipment.original, equipment.existed, equipDirty), restoreInventoryRecordIfDirty(u.inventory, characterID, inventory.original, inventory.existed, invDirty))
		}
	}
	return nil
}

type memoryBuffReadCharacter struct {
	owner  string
	record repository.CharacterRecord
}

func (r *memoryBuffReadCharacter) Load(ctx context.Context, key string) (repository.CharacterRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.owner, key); err != nil {
		return repository.CharacterRecord{}, false, err
	}
	return repository.CloneCharacter(r.record), true, nil
}
func (r *memoryBuffReadCharacter) Save(context.Context, repository.CharacterRecord) error {
	return repository.ErrBuffOwnedSnapshotUnavailable
}

// This owner-only snapshot cannot answer account-wide or global identity queries.
// Fail closed rather than accessing the base repository outside the locked view.
func (r *memoryBuffReadCharacter) ListByAccount(ctx context.Context, _ string, _ int) ([]repository.CharacterRecord, error) {
	if err := memoryBuffUnsupportedCharacterContext(ctx); err != nil {
		return nil, err
	}
	return nil, repository.ErrBuffOwnedSnapshotUnavailable
}
func (r *memoryBuffReadCharacter) FindIDByName(ctx context.Context, _ string) (string, bool, error) {
	if err := memoryBuffUnsupportedCharacterContext(ctx); err != nil {
		return "", false, err
	}
	return "", false, repository.ErrBuffOwnedSnapshotUnavailable
}
func (r *memoryBuffReadCharacter) NextNumericID(ctx context.Context) (int, error) {
	if err := memoryBuffUnsupportedCharacterContext(ctx); err != nil {
		return 0, err
	}
	return 0, repository.ErrBuffOwnedSnapshotUnavailable
}
func memoryBuffUnsupportedCharacterContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return ctx.Err()
}

var _ repository.CharacterRepository = (*memoryBuffReadCharacter)(nil)

type memoryBuffLazyInventory struct {
	owner    string
	base     repository.InventoryRepository
	tx       *memoryInventoryTransaction
	original repository.InventoryRecord
	existed  bool
}

func (r *memoryBuffLazyInventory) Load(ctx context.Context, key string) (repository.InventoryRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.owner, key); err != nil {
		return repository.InventoryRecord{}, false, err
	}
	if r.tx == nil {
		if r.base == nil {
			return repository.InventoryRecord{}, false, repository.ErrBuffOwnedSnapshotUnavailable
		}
		value, found, err := r.base.Load(ctx, key)
		if err != nil {
			return repository.InventoryRecord{}, false, err
		}
		r.original, r.existed = repository.CloneInventory(value), found
		r.tx = &memoryInventoryTransaction{characterID: key, record: repository.CloneInventory(value), exists: found}
	}
	return r.tx.Load(ctx, key)
}
func (r *memoryBuffLazyInventory) Save(ctx context.Context, value repository.InventoryRecord) error {
	if _, _, err := r.Load(ctx, value.CharacterID); err != nil {
		return err
	}
	return r.tx.Save(ctx, value)
}

type memoryBuffLazyEquipment struct {
	owner    string
	base     repository.EquipmentRepository
	tx       *memoryEquipmentTransaction
	original repository.EquipmentRecord
	existed  bool
}

func (r *memoryBuffLazyEquipment) Load(ctx context.Context, key string) (repository.EquipmentRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.owner, key); err != nil {
		return repository.EquipmentRecord{}, false, err
	}
	if r.tx == nil {
		if r.base == nil {
			return repository.EquipmentRecord{}, false, repository.ErrBuffOwnedSnapshotUnavailable
		}
		value, found, err := r.base.Load(ctx, key)
		if err != nil {
			return repository.EquipmentRecord{}, false, err
		}
		r.original, r.existed = repository.CloneEquipment(value), found
		r.tx = &memoryEquipmentTransaction{characterID: key, record: repository.CloneEquipment(value), exists: found}
	}
	return r.tx.Load(ctx, key)
}
func (r *memoryBuffLazyEquipment) Save(ctx context.Context, value repository.EquipmentRecord) error {
	if _, _, err := r.Load(ctx, value.CharacterID); err != nil {
		return err
	}
	return r.tx.Save(ctx, value)
}

type memoryBuffPetMetadata struct {
	owner string
	base  repository.PetRepository
}

func (r *memoryBuffPetMetadata) LoadBuffPetEntries(ctx context.Context, key string) (repository.PetRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.owner, key); err != nil {
		return repository.PetRecord{}, false, err
	}
	value, found, err := r.base.Load(ctx, key)
	if err != nil {
		return repository.PetRecord{}, false, err
	}
	if !found || value.CharacterID != key {
		return repository.PetRecord{}, false, nil
	}
	// Generic memory Load returns a detached record. Only Entries are retained;
	// artifacts and clear-token data never leave this read-only metadata view.
	result := repository.ClonePet(repository.PetRecord{CharacterID: key, Entries: value.Entries})
	for k, entry := range result.Entries {
		entry.AppliedClearTokens = nil
		entry.AppliedClearTokenOrder = nil
		result.Entries[k] = entry
	}
	return result, true, nil
}

type memoryBuffLegacySnapshot struct {
	owner string
	rows  []repository.OwnedLegacyItem
}

func (r *memoryBuffLegacySnapshot) SelectOwnedBuffItems(ctx context.Context, key string, lists []byte) ([]repository.OwnedLegacyItem, error) {
	if err := repository.TransactionKeyError(ctx, r.owner, key); err != nil {
		return nil, err
	}
	wanted := map[byte]bool{}
	for _, list := range lists {
		if list != 0 && list != 1 && list != 7 {
			return nil, repository.ErrBuffOwnedSnapshotUnavailable
		}
		wanted[list] = true
	}
	seen := map[int64]bool{}
	out := []repository.OwnedLegacyItem{}
	for _, row := range r.rows {
		if !wanted[row.Item.ListType] || row.OwnerScope != "character" || row.OwnerID != key {
			continue
		}
		if row.CharacterID != key || row.ItemUID <= 0 || row.Item.SlotIndex < 0 {
			return nil, repository.ErrBuffLegacyOwnerMismatch
		}
		if seen[row.ItemUID] {
			return nil, repository.ErrBuffLegacyAmbiguous
		}
		seen[row.ItemUID] = true
		out = append(out, repository.CloneOwnedLegacyItem(row))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Item.ListType != out[j].Item.ListType {
			return out[i].Item.ListType < out[j].Item.ListType
		}
		if out[i].Item.SlotIndex != out[j].Item.SlotIndex {
			return out[i].Item.SlotIndex < out[j].Item.SlotIndex
		}
		return out[i].ItemUID < out[j].ItemUID
	})
	return out, nil
}
