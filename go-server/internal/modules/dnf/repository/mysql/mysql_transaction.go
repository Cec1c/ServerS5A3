package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"time"
)

type sqlTxBeginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

type sqlTransactionDB struct {
	tx       *sql.Tx
	prepared *PreparedSQLDB
	local    *transactionStatements
}

func (db sqlTransactionDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	s, err := db.preparedStatement(ctx, query)
	if err != nil {
		return nil, err
	}
	if s != nil {
		result, err := s.ExecContext(ctx, args...)
		db.forgetFailedStatement(query, s, err)
		return result, err
	}
	return db.tx.ExecContext(ctx, query, args...)
}

func (db sqlTransactionDB) QueryContext(ctx context.Context, query string, args ...any) (SQLRows, error) {
	s, err := db.preparedStatement(ctx, query)
	if err != nil {
		return nil, err
	}
	if s != nil {
		rows, err := s.QueryContext(ctx, args...)
		db.forgetFailedStatement(query, s, err)
		return rows, err
	}
	return db.tx.QueryContext(ctx, query, args...)
}

func (db sqlTransactionDB) QueryRowContext(ctx context.Context, query string, args ...any) SQLRow {
	s, err := db.preparedStatement(ctx, query)
	if err != nil {
		return errorRow{err}
	}
	if s != nil {
		return transactionPreparedRow{row: s.QueryRowContext(ctx, args...), database: db, query: query, statement: s}
	}
	return db.tx.QueryRowContext(ctx, query, args...)
}

func (db sqlTransactionDB) PingContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

type mysqlCharacterItemUnitOfWork struct {
	router mysqlRouter
}

type mysqlCharacterTradeUnitOfWork struct {
	router mysqlRouter
}

type mysqlCharacterAssetUnitOfWork struct {
	router mysqlRouter
}

type mysqlRentalAssetUnitOfWork struct {
	router mysqlRouter
}

type mysqlCeraShopAssetUnitOfWork struct {
	router mysqlRouter
}

type mysqlCharacterSkillUnitOfWork struct {
	router mysqlRouter
}

type mysqlCharacterProgressionUnitOfWork struct {
	router mysqlRouter
}

type mysqlCharacterCreationUnitOfWork struct {
	router mysqlRouter
}

func (u *mysqlCharacterCreationUnitOfWork) WithinCharacterNameMutation(ctx context.Context, characterID string, base repository.Group, apply func(repository.Group) error) error {
	return u.WithinCharacterCreation(ctx, characterID, base, apply)
}

func (u *mysqlCharacterCreationUnitOfWork) WithinCharacterCreation(ctx context.Context, characterID string, _ repository.Group, apply func(repository.Group) error) error {
	characterID = strings.TrimSpace(characterID)
	if characterID == "" || apply == nil {
		return repository.ErrCharacterCreationTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrCharacterCreationTransactionUnavailable
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	txRouter := u.router
	txRouter.db = newSQLTransactionDB(tx, txRouter.db)
	txRouter.readDBs = append([]string(nil), u.router.writeDBs...)
	txRouter.writeDBs = append([]string(nil), u.router.writeDBs...)
	txRouter.lockReads = true
	// All writers use the same coordinator key, independent of the character's
	// shard. INSERT IGNORE handles the first concurrent use; SELECT FOR UPDATE
	// holds an InnoDB row lock until the character/name transaction commits.
	table, err := txRouter.writeTable(mysqlSettingsTable, repository.CharacterNameMutationScope)
	if err != nil {
		return err
	}
	if _, err := txRouter.db.ExecContext(ctx, "INSERT IGNORE INTO "+table+" (scope, updated_at) VALUES (?, ?)", repository.CharacterNameMutationScope, timeOrNow(time.Time{}, txRouter.now)); err != nil {
		return err
	}
	var lockedScope string
	if err := txRouter.db.QueryRowContext(ctx, "SELECT scope FROM "+table+" WHERE scope = ? FOR UPDATE", repository.CharacterNameMutationScope).Scan(&lockedScope); err != nil {
		return err
	}
	if lockedScope != repository.CharacterNameMutationScope {
		return fmt.Errorf("character name namespace lock was not acquired")
	}
	txGroup := newMySQLGroupFromRouter(txRouter, false)
	if err := apply(txGroup); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

func (u *mysqlCharacterSkillUnitOfWork) WithinCharacterSkill(ctx context.Context, characterID string, apply func(repository.SkillRepository) error) error {
	characterID = strings.TrimSpace(characterID)
	if characterID == "" || apply == nil {
		return repository.ErrCharacterSkillTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrCharacterSkillTransactionUnavailable
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	writeDatabase := u.router.writeDBs[pickDatabase(characterID, len(u.router.writeDBs))]
	txRouter := u.router
	txRouter.db = newSQLTransactionDB(tx, txRouter.db)
	txRouter.readDBs = []string{writeDatabase}
	txRouter.writeDBs = []string{writeDatabase}
	txRouter.lockReads = true
	txGroup := newMySQLGroupFromRouter(txRouter, false)
	if err := apply(txGroup.Skill); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

func (u *mysqlCharacterProgressionUnitOfWork) WithinCharacterProgression(
	ctx context.Context,
	characterID string,
	apply func(repository.CharacterRepository, repository.SkillRepository) error,
) error {
	characterID = strings.TrimSpace(characterID)
	if characterID == "" || apply == nil {
		return repository.ErrCharacterProgressionTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrCharacterProgressionTransactionUnavailable
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	writeDatabase := u.router.writeDBs[pickDatabase(characterID, len(u.router.writeDBs))]
	txRouter := u.router
	txRouter.db = newSQLTransactionDB(tx, txRouter.db)
	txRouter.readDBs = []string{writeDatabase}
	txRouter.writeDBs = []string{writeDatabase}
	txRouter.lockReads = true
	txGroup := newMySQLGroupFromRouter(txRouter, false)
	characters := &characterProgressionScopedCharacterRepository{
		characterID: characterID,
		base:        txGroup.Character,
	}
	skills := &characterProgressionScopedSkillRepository{
		characterID: characterID,
		base:        txGroup.Skill,
	}
	if err := apply(characters, skills); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

type characterProgressionScopedCharacterRepository struct {
	characterID string
	base        repository.CharacterRepository
}

func (r *characterProgressionScopedCharacterRepository) Load(ctx context.Context, characterID string) (repository.CharacterRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.characterID, characterID); err != nil {
		return repository.CharacterRecord{}, false, err
	}
	return r.base.Load(ctx, characterID)
}

func (r *characterProgressionScopedCharacterRepository) Save(ctx context.Context, record repository.CharacterRecord) error {
	if err := repository.TransactionKeyError(ctx, r.characterID, record.CharacterID); err != nil {
		return err
	}
	return r.base.Save(ctx, record)
}

func (r *characterProgressionScopedCharacterRepository) SaveFields(ctx context.Context, record repository.CharacterRecord, fields ...repository.CharacterField) error {
	if err := repository.TransactionKeyError(ctx, r.characterID, record.CharacterID); err != nil {
		return err
	}
	return repository.SaveCharacterFields(ctx, r.base, record, fields...)
}

func (r *characterProgressionScopedCharacterRepository) ListByAccount(context.Context, string, int) ([]repository.CharacterRecord, error) {
	return nil, repository.ErrCharacterProgressionTransactionUnavailable
}

func (r *characterProgressionScopedCharacterRepository) FindIDByName(context.Context, string) (string, bool, error) {
	return "", false, repository.ErrCharacterProgressionTransactionUnavailable
}

func (r *characterProgressionScopedCharacterRepository) NextNumericID(context.Context) (int, error) {
	return 0, repository.ErrCharacterProgressionTransactionUnavailable
}

type characterProgressionScopedSkillRepository struct {
	characterID string
	base        repository.SkillRepository
}

func (r *characterProgressionScopedSkillRepository) Load(ctx context.Context, characterID string) (repository.SkillRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.characterID, characterID); err != nil {
		return repository.SkillRecord{}, false, err
	}
	return r.base.Load(ctx, characterID)
}

func (r *characterProgressionScopedSkillRepository) Save(ctx context.Context, record repository.SkillRecord) error {
	if err := repository.TransactionKeyError(ctx, r.characterID, record.CharacterID); err != nil {
		return err
	}
	return r.base.Save(ctx, record)
}

func (r *characterProgressionScopedSkillRepository) SaveFields(ctx context.Context, record repository.SkillRecord, fields ...repository.SkillField) error {
	if err := repository.TransactionKeyError(ctx, r.characterID, record.CharacterID); err != nil {
		return err
	}
	return repository.SaveSkillFields(ctx, r.base, record, fields...)
}

func (u *mysqlCharacterItemUnitOfWork) WithinCharacterItems(ctx context.Context, characterID string, apply func(repository.InventoryRepository, repository.EquipmentRepository) error) error {
	characterID = strings.TrimSpace(characterID)
	if characterID == "" || apply == nil {
		return repository.ErrCharacterItemTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrCharacterItemTransactionUnavailable
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	writeDatabase := u.router.writeDBs[pickDatabase(characterID, len(u.router.writeDBs))]
	txRouter := u.router
	txRouter.db = newSQLTransactionDB(tx, txRouter.db)
	txRouter.readDBs = []string{writeDatabase}
	txRouter.writeDBs = []string{writeDatabase}
	txRouter.lockReads = true
	txGroup := newMySQLGroupFromRouter(txRouter, false)
	if err := apply(txGroup.Inventory, txGroup.Equipment); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

func (u *mysqlCharacterTradeUnitOfWork) WithinCharacterTrade(ctx context.Context, first, second string, apply func(repository.CharacterRepository, repository.InventoryRepository) error) error {
	if apply == nil {
		return repository.ErrCharacterTradeTransactionUnavailable
	}
	return u.withinCharacterTrade(ctx, first, second, false, func(c repository.CharacterRepository, i repository.InventoryRepository, _ repository.AccountInventoryRepository) error {
		return apply(c, i)
	})
}

func (u *mysqlCharacterTradeUnitOfWork) WithinCharacterTradeAssets(ctx context.Context, first, second string, apply func(repository.CharacterRepository, repository.InventoryRepository, repository.AccountInventoryRepository) error) error {
	return u.withinCharacterTrade(ctx, first, second, true, apply)
}

func (u *mysqlCharacterTradeUnitOfWork) WithinCharacterInventoryGroup(ctx context.Context, ids []string, apply func(repository.CharacterRepository, repository.InventoryRepository) error) error {
	if apply == nil {
		return repository.ErrCharacterTradeTransactionUnavailable
	}
	return u.withinCharacterInventoryGroup(ctx, ids, "", func(c repository.CharacterRepository, i repository.InventoryRepository, _ repository.AccountInventoryRepository) error {
		return apply(c, i)
	})
}

func (u *mysqlCharacterTradeUnitOfWork) withinCharacterTrade(
	ctx context.Context,
	firstCharacterID string,
	secondCharacterID string,
	withWarehouse bool,
	apply func(repository.CharacterRepository, repository.InventoryRepository, repository.AccountInventoryRepository) error,
) error {
	firstCharacterID = strings.TrimSpace(firstCharacterID)
	secondCharacterID = strings.TrimSpace(secondCharacterID)
	if firstCharacterID == "" || secondCharacterID == "" || firstCharacterID == secondCharacterID || apply == nil {
		return repository.ErrCharacterTradeTransactionUnavailable
	}
	warehouseOwner := ""
	if withWarehouse {
		warehouseOwner = firstCharacterID
	}
	return u.withinCharacterInventoryGroup(ctx, []string{firstCharacterID, secondCharacterID}, warehouseOwner, apply)
}

func (u *mysqlCharacterTradeUnitOfWork) withinCharacterInventoryGroup(ctx context.Context, ids []string, warehouseOwner string, apply func(repository.CharacterRepository, repository.InventoryRepository, repository.AccountInventoryRepository) error) error {
	ids, err := repository.CharacterInventoryGroupIDs(ids)
	if err != nil || u == nil || apply == nil {
		return repository.ErrCharacterTradeTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrCharacterTradeTransactionUnavailable
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // Also release the SQL transaction if the callback panics.
	txRouter := u.router
	txRouter.db = newSQLTransactionDB(tx, txRouter.db)
	// Character IDs can route to different logical schemas. They remain on
	// one physical MySQL connection, so keep the full qualified write plan.
	txRouter.readDBs = append([]string(nil), u.router.writeDBs...)
	txRouter.writeDBs = append([]string(nil), u.router.writeDBs...)
	txRouter.lockReads = true
	txGroup := newMySQLGroupFromRouter(txRouter, false)
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	characters := &characterTradeScopedCharacterRepository{allowed: allowed, base: txGroup.Character}
	inventories := &characterTradeScopedInventoryRepository{allowed: allowed, base: txGroup.Inventory}
	var warehouse repository.AccountInventoryRepository
	if warehouseOwner != "" {
		warehouse = &characterSettlementAccountInventoryRepository{characterID: warehouseOwner, characters: characters, accounts: txGroup.AccountInventory}
	}
	if err := apply(characters, inventories, warehouse); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

type characterTradeScopedCharacterRepository struct {
	allowed map[string]struct{}
	base    repository.CharacterRepository
}

func (r *characterTradeScopedCharacterRepository) Load(ctx context.Context, characterID string) (repository.CharacterRecord, bool, error) {
	characterID = strings.TrimSpace(characterID)
	if _, allowed := r.allowed[characterID]; !allowed {
		return repository.CharacterRecord{}, false, repository.ErrCharacterTradeTransactionUnavailable
	}
	return r.base.Load(ctx, characterID)
}

func (r *characterTradeScopedCharacterRepository) Save(ctx context.Context, record repository.CharacterRecord) error {
	if _, allowed := r.allowed[strings.TrimSpace(record.CharacterID)]; !allowed {
		return repository.ErrCharacterTradeTransactionUnavailable
	}
	return r.base.Save(ctx, record)
}

func (r *characterTradeScopedCharacterRepository) ListByAccount(context.Context, string, int) ([]repository.CharacterRecord, error) {
	return nil, repository.ErrCharacterTradeTransactionUnavailable
}

func (r *characterTradeScopedCharacterRepository) FindIDByName(context.Context, string) (string, bool, error) {
	return "", false, repository.ErrCharacterTradeTransactionUnavailable
}

func (r *characterTradeScopedCharacterRepository) NextNumericID(context.Context) (int, error) {
	return 0, repository.ErrCharacterTradeTransactionUnavailable
}

type characterTradeScopedInventoryRepository struct {
	allowed map[string]struct{}
	base    repository.InventoryRepository
}

func (r *characterTradeScopedInventoryRepository) Load(ctx context.Context, characterID string) (repository.InventoryRecord, bool, error) {
	characterID = strings.TrimSpace(characterID)
	if _, allowed := r.allowed[characterID]; !allowed {
		return repository.InventoryRecord{}, false, repository.ErrCharacterTradeTransactionUnavailable
	}
	return r.base.Load(ctx, characterID)
}

func (r *characterTradeScopedInventoryRepository) Save(ctx context.Context, record repository.InventoryRecord) error {
	if _, allowed := r.allowed[strings.TrimSpace(record.CharacterID)]; !allowed {
		return repository.ErrCharacterTradeTransactionUnavailable
	}
	return r.base.Save(ctx, record)
}

func (r *characterTradeScopedInventoryRepository) SaveFields(ctx context.Context, record repository.InventoryRecord, fields ...repository.InventoryField) error {
	if _, allowed := r.allowed[strings.TrimSpace(record.CharacterID)]; !allowed {
		return repository.ErrCharacterTradeTransactionUnavailable
	}
	return repository.SaveInventoryFields(ctx, r.base, record, fields...)
}

func (u *mysqlCharacterAssetUnitOfWork) WithinCharacterAssets(ctx context.Context, characterID string, apply func(repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository) error) error {
	characterID = strings.TrimSpace(characterID)
	if characterID == "" || apply == nil {
		return repository.ErrCharacterAssetTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrCharacterAssetTransactionUnavailable
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	writeDatabase := u.router.writeDBs[pickDatabase(characterID, len(u.router.writeDBs))]
	txRouter := u.router
	txRouter.db = newSQLTransactionDB(tx, txRouter.db)
	txRouter.readDBs = []string{writeDatabase}
	txRouter.writeDBs = []string{writeDatabase}
	txRouter.lockReads = true
	txGroup := newMySQLGroupFromRouter(txRouter, false)
	if err := apply(txGroup.Character, txGroup.Inventory, txGroup.Equipment); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

func (u *mysqlCharacterAssetUnitOfWork) WithinCharacterAssetsAndQuest(ctx context.Context, characterID string, apply func(repository.CharacterRepository, repository.QuestReader) error) error {
	if u == nil || apply == nil {
		return repository.ErrCharacterAssetTransactionUnavailable
	}
	// Reuse the scoped transaction router and FOR UPDATE reads already used by
	// quest settlement; the callback only receives wallet and read-only quests.
	return (&mysqlCharacterSettlementUnitOfWork{router: u.router}).WithinCharacterSettlement(ctx, characterID, func(tx repository.Group) error {
		return apply(tx.Character, tx.Quest)
	})
}

func (u *mysqlRentalAssetUnitOfWork) WithinRentalAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(repository.AccountRepository, repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository) error,
) error {
	accountID = strings.TrimSpace(accountID)
	characterID = strings.TrimSpace(characterID)
	if accountID == "" || characterID == "" || apply == nil {
		return repository.ErrRentalAssetTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrRentalAssetTransactionUnavailable
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Account and character keys can hash to different logical databases. Keep
	// the complete write plan while using one physical MySQL transaction.
	txRouter := u.router
	txRouter.db = newSQLTransactionDB(tx, txRouter.db)
	txRouter.readDBs = append([]string(nil), u.router.writeDBs...)
	txRouter.writeDBs = append([]string(nil), u.router.writeDBs...)
	txRouter.lockReads = true
	txGroup := newMySQLGroupFromRouter(txRouter, false)
	accounts := &rentalScopedAccountRepository{accountID: accountID, base: txGroup.Account}
	characters := &rentalScopedCharacterRepository{characterID: characterID, base: txGroup.Character}
	inventories := &rentalScopedInventoryRepository{characterID: characterID, base: txGroup.Inventory}
	equipment := &rentalScopedEquipmentRepository{characterID: characterID, base: txGroup.Equipment}
	if err := apply(accounts, characters, inventories, equipment); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

func (u *mysqlCeraShopAssetUnitOfWork) WithinCeraShopAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	settingsScope string,
	apply func(repository.AccountRepository, repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository, repository.SettingsRepository) error,
) error {
	if apply == nil {
		return repository.ErrCeraShopAssetTransactionUnavailable
	}
	return u.withinCeraShopSkillAssets(ctx, accountID, characterID, settingsScope,
		func(a repository.AccountRepository, c repository.CharacterRepository, i repository.InventoryRepository, e repository.EquipmentRepository, s repository.SettingsRepository, _ repository.SkillRepository) error {
			return apply(a, c, i, e, s)
		})
}

func (u *mysqlCeraShopAssetUnitOfWork) WithinCeraShopSkillAssets(ctx context.Context, accountID, characterID, settingsScope string,
	apply func(repository.AccountRepository, repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository, repository.SettingsRepository, repository.SkillRepository) error) error {
	return u.withinCeraShopSkillAssets(ctx, accountID, characterID, settingsScope, apply)
}

func (u *mysqlCeraShopAssetUnitOfWork) withinCeraShopSkillAssets(ctx context.Context, accountID, characterID, settingsScope string,
	apply func(repository.AccountRepository, repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository, repository.SettingsRepository, repository.SkillRepository) error) error {
	accountID = strings.TrimSpace(accountID)
	characterID = strings.TrimSpace(characterID)
	settingsScope = strings.TrimSpace(settingsScope)
	if accountID == "" || characterID == "" || settingsScope == "" || apply == nil {
		return repository.ErrCeraShopAssetTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	beginner, ok := u.router.db.(sqlTxBeginner)
	if !ok {
		return repository.ErrCeraShopAssetTransactionUnavailable
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// The active local profile uses one physical MySQL server. Keep the full
	// qualified write plan so account, character, and settings keys remain in
	// one SQL transaction even when their logical database hashes differ.
	txRouter := u.router
	txRouter.db = newSQLTransactionDB(tx, txRouter.db)
	txRouter.readDBs = append([]string(nil), u.router.writeDBs...)
	txRouter.writeDBs = append([]string(nil), u.router.writeDBs...)
	txRouter.lockReads = true
	txGroup := newMySQLGroupFromRouter(txRouter, false)
	accounts := &rentalScopedAccountRepository{accountID: accountID, base: txGroup.Account}
	characters := &rentalScopedCharacterRepository{characterID: characterID, base: txGroup.Character}
	inventories := &rentalScopedInventoryRepository{characterID: characterID, base: txGroup.Inventory}
	equipment := &rentalScopedEquipmentRepository{characterID: characterID, base: txGroup.Equipment}
	settings := &ceraShopScopedSettingsRepository{scope: settingsScope, base: txGroup.Settings}
	skills := &characterProgressionScopedSkillRepository{characterID: characterID, base: txGroup.Skill}
	if err := apply(accounts, characters, inventories, equipment, settings, skills); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

type ceraShopScopedSettingsRepository struct {
	scope string
	base  repository.SettingsRepository
}

func (r *ceraShopScopedSettingsRepository) Load(ctx context.Context, scope string) (repository.SettingsRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.scope, scope); err != nil {
		return repository.SettingsRecord{}, false, err
	}
	return r.base.Load(ctx, scope)
}

func (r *ceraShopScopedSettingsRepository) Save(ctx context.Context, record repository.SettingsRecord) error {
	if err := repository.TransactionKeyError(ctx, r.scope, record.Scope); err != nil {
		return err
	}
	return r.base.Save(ctx, record)
}

type rentalScopedAccountRepository struct {
	accountID string
	base      repository.AccountRepository
}

func (r *rentalScopedAccountRepository) Load(ctx context.Context, accountID string) (repository.AccountRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.accountID, accountID); err != nil {
		return repository.AccountRecord{}, false, err
	}
	return r.base.Load(ctx, accountID)
}

func (r *rentalScopedAccountRepository) Save(ctx context.Context, record repository.AccountRecord) error {
	if err := repository.TransactionKeyError(ctx, r.accountID, record.AccountID); err != nil {
		return err
	}
	return r.base.Save(ctx, record)
}

// SaveMetadataEntry forwards a single-key metadata write so callers do not
// fall back to the scoped repository's full account Save, which replaces every
// metadata row. The account key and the underlying transaction stay intact.
func (r *rentalScopedAccountRepository) SaveMetadataEntry(ctx context.Context, accountID, key, value string, at time.Time) error {
	if err := repository.TransactionKeyError(ctx, r.accountID, accountID); err != nil {
		return err
	}
	writer, ok := r.base.(repository.AccountMetadataEntryWriter)
	if !ok {
		return repository.ErrAccountMetadataUnavailable
	}
	return writer.SaveMetadataEntry(ctx, accountID, key, value, at)
}

type rentalScopedCharacterRepository struct {
	characterID string
	base        repository.CharacterRepository
}

func (r *rentalScopedCharacterRepository) Load(ctx context.Context, characterID string) (repository.CharacterRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.characterID, characterID); err != nil {
		return repository.CharacterRecord{}, false, err
	}
	return r.base.Load(ctx, characterID)
}

func (r *rentalScopedCharacterRepository) Save(ctx context.Context, record repository.CharacterRecord) error {
	if err := repository.TransactionKeyError(ctx, r.characterID, record.CharacterID); err != nil {
		return err
	}
	return r.base.Save(ctx, record)
}

func (r *rentalScopedCharacterRepository) SaveFields(ctx context.Context, record repository.CharacterRecord, fields ...repository.CharacterField) error {
	if err := repository.TransactionKeyError(ctx, r.characterID, record.CharacterID); err != nil {
		return err
	}
	return repository.SaveCharacterFields(ctx, r.base, record, fields...)
}

func (r *rentalScopedCharacterRepository) ListByAccount(context.Context, string, int) ([]repository.CharacterRecord, error) {
	return nil, repository.ErrRentalAssetTransactionUnavailable
}

func (r *rentalScopedCharacterRepository) FindIDByName(context.Context, string) (string, bool, error) {
	return "", false, repository.ErrRentalAssetTransactionUnavailable
}

func (r *rentalScopedCharacterRepository) NextNumericID(context.Context) (int, error) {
	return 0, repository.ErrRentalAssetTransactionUnavailable
}

type rentalScopedInventoryRepository struct {
	characterID string
	base        repository.InventoryRepository
}

func (r *rentalScopedInventoryRepository) Load(ctx context.Context, characterID string) (repository.InventoryRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.characterID, characterID); err != nil {
		return repository.InventoryRecord{}, false, err
	}
	return r.base.Load(ctx, characterID)
}

func (r *rentalScopedInventoryRepository) Save(ctx context.Context, record repository.InventoryRecord) error {
	if err := repository.TransactionKeyError(ctx, r.characterID, record.CharacterID); err != nil {
		return err
	}
	return r.base.Save(ctx, record)
}

func (r *rentalScopedInventoryRepository) SaveFields(ctx context.Context, record repository.InventoryRecord, fields ...repository.InventoryField) error {
	if err := repository.TransactionKeyError(ctx, r.characterID, record.CharacterID); err != nil {
		return err
	}
	return repository.SaveInventoryFields(ctx, r.base, record, fields...)
}

type rentalScopedEquipmentRepository struct {
	characterID string
	base        repository.EquipmentRepository
}

func (r *rentalScopedEquipmentRepository) Load(ctx context.Context, characterID string) (repository.EquipmentRecord, bool, error) {
	if err := repository.TransactionKeyError(ctx, r.characterID, characterID); err != nil {
		return repository.EquipmentRecord{}, false, err
	}
	return r.base.Load(ctx, characterID)
}

func (r *rentalScopedEquipmentRepository) Save(ctx context.Context, record repository.EquipmentRecord) error {
	if err := repository.TransactionKeyError(ctx, r.characterID, record.CharacterID); err != nil {
		return err
	}
	return r.base.Save(ctx, record)
}

func (r *rentalScopedEquipmentRepository) SaveFields(ctx context.Context, record repository.EquipmentRecord, fields ...repository.EquipmentField) error {
	if err := repository.TransactionKeyError(ctx, r.characterID, record.CharacterID); err != nil {
		return err
	}
	return repository.SaveEquipmentFields(ctx, r.base, record, fields...)
}
