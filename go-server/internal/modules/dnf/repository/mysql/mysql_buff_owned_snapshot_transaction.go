package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

// This optional method does not change WithinCeraShopAssets or its queries.
// The domain callback must access Character, Settings, Inventory, Equipment,
// then optional Pet metadata, then Legacy in this order; LoadResolved may stop after absent Settings.
func (u *mysqlCeraShopAssetUnitOfWork) WithinBuffOwnedSnapshot(
	ctx context.Context, accountID, characterID, settingsScope string,
	apply func(repository.CharacterRepository, repository.InventoryRepository,
		repository.EquipmentRepository, repository.SettingsRepository,
		repository.BuffPetMetadataReader, repository.OwnedLegacyInventoryRepository) error,
) error {
	accountID, characterID, settingsScope = strings.TrimSpace(accountID), strings.TrimSpace(characterID), strings.TrimSpace(settingsScope)
	if u == nil || accountID == "" || characterID == "" || settingsScope != "character:"+characterID+":buff_swap_native" || apply == nil {
		return repository.ErrBuffOwnedSnapshotUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrBuffOwnedSnapshotUnavailable
	}
	tx, err := beginner.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	router := u.router
	router.db = newSQLTransactionDB(tx, router.db)
	router.readDBs = append([]string(nil), u.router.writeDBs...)
	router.writeDBs = append([]string(nil), u.router.writeDBs...)
	router.lockReads = true
	group := newMySQLGroupFromRouter(router, false)
	characters := &rentalScopedCharacterRepository{characterID: characterID, base: group.Character}
	inventories := &rentalScopedInventoryRepository{characterID: characterID, base: &mysqlBuffInventoryStore{mysqlBagStore{mysqlStoreBase: mysqlStoreBase{router: router}}}}
	equipment := &rentalScopedEquipmentRepository{characterID: characterID, base: group.Equipment}
	settings := &ceraShopScopedSettingsRepository{scope: settingsScope, base: group.Settings}
	petMetadata := &mysqlBuffPetMetadataStore{mysqlStoreBase: mysqlStoreBase{router: router}, characterID: characterID}
	legacy := &mysqlBuffOwnedLegacyStore{mysqlStoreBase: mysqlStoreBase{router: router}, characterID: characterID}
	character, found, err := characters.Load(ctx, characterID)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if !found || character.CharacterID != characterID || character.AccountID != accountID {
		return errors.Join(repository.ErrBuffLegacyOwnerMismatch, tx.Rollback())
	}
	// Lock/read exact Settings before any asset container. No snapshot is written.
	if _, _, err := settings.Load(ctx, settingsScope); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := apply(characters, inventories, equipment, settings, petMetadata, legacy); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}
