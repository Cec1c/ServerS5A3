package mysql

import (
	"context"
	"database/sql"
	"longheng.io/server/internal/modules/dnf/repository"
)

const mysqlAccountInventoryTable = "account_inventories"

type mysqlAccountInventoryStore struct {
	mysqlStoreBase
	trackSlotChanges bool
	loadedSnapshot   *repository.AccountInventoryRecord
}

func (s *mysqlAccountInventoryStore) Load(ctx context.Context, accountID string) (repository.AccountInventoryRecord, bool, error) {
	table, err := s.router.readTable(mysqlAccountInventoryTable, accountID)
	if err != nil {
		return repository.AccountInventoryRecord{}, false, err
	}
	itemsTable, err := s.router.readTable(mysqlAccountInventoryItemsTable, accountID)
	if err != nil {
		return repository.AccountInventoryRecord{}, false, err
	}
	extraTable, err := s.router.readTable(mysqlAccountInventoryExtraTable, accountID)
	if err != nil {
		return repository.AccountInventoryRecord{}, false, err
	}
	query := s.router.selectQuery("SELECT account_id, updated_at FROM " + table + " WHERE account_id = ?")
	var record repository.AccountInventoryRecord
	var updatedAt sql.NullTime
	err = s.router.db.QueryRowContext(ctx, query, accountID).Scan(&record.AccountID, &updatedAt)
	if err != nil {
		ok, scanErr := scanErr(err)
		return repository.AccountInventoryRecord{}, ok, scanErr
	}
	record.Slots, err = loadItemStackCollection(
		ctx, s.router.db, itemsTable, extraTable, "account_id", accountID, "", false,
	)
	if err != nil {
		return repository.AccountInventoryRecord{}, false, err
	}
	record.UpdatedAt = scanTime(updatedAt)
	if s.trackSlotChanges {
		snapshot := repository.CloneAccountInventory(record)
		s.loadedSnapshot = &snapshot
	}
	return repository.CloneAccountInventory(record), true, nil
}

func (s *mysqlAccountInventoryStore) Save(ctx context.Context, record repository.AccountInventoryRecord) error {
	accountID, err := requireRecordKey(repository.AccountInventoryKey, record, "account inventory")
	if err != nil {
		return err
	}
	table, err := s.router.writeTable(mysqlAccountInventoryTable, accountID)
	if err != nil {
		return err
	}
	itemsTable, err := s.router.writeTable(mysqlAccountInventoryItemsTable, accountID)
	if err != nil {
		return err
	}
	extraTable, err := s.router.writeTable(mysqlAccountInventoryExtraTable, accountID)
	if err != nil {
		return err
	}
	columns := []string{"account_id", "updated_at"}
	args := []any{accountID, timeOrNow(record.UpdatedAt, s.router.now)}
	updates := []string{updateValue("updated_at")}
	err = withMySQLWriteExecutor(ctx, s.router.db, func(database SQLDB) error {
		if _, execErr := database.ExecContext(ctx, buildUpsert(table, columns, updates), args...); execErr != nil {
			return execErr
		}
		if s.trackSlotChanges && s.loadedSnapshot != nil && s.loadedSnapshot.AccountID == accountID {
			keys := changedAccountItemKeys(s.loadedSnapshot.Slots, record.Slots)
			if len(keys) == 0 {
				return nil
			}
			return replaceItemStackKeys(ctx, database, itemsTable, extraTable, "account_id", accountID, "", false, record.Slots, keys)
		}
		return replaceItemStackCollection(
			ctx, database, itemsTable, extraTable, "account_id", accountID, "", false, record.Slots,
		)
	})
	if err == nil && s.trackSlotChanges {
		snapshot := repository.CloneAccountInventory(record)
		s.loadedSnapshot = &snapshot
	}
	return err
}
