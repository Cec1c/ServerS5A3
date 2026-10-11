package mysql

import (
	"context"
	"database/sql"
	"longheng.io/server/internal/modules/dnf/repository"
)

// Only the new BUFF UOW constructs this override. Promoted Save/SaveFields
// remain the existing physical inventory implementation in the same SQL tx.
type mysqlBuffInventoryStore struct{ mysqlBagStore }

func (s *mysqlBuffInventoryStore) Load(ctx context.Context, characterID string) (repository.InventoryRecord, bool, error) {
	if !s.router.lockReads {
		return repository.InventoryRecord{}, false, repository.ErrBuffOwnedSnapshotUnavailable
	}
	table, err := s.router.readTable(mysqlBagTable, characterID)
	if err != nil {
		return repository.InventoryRecord{}, false, err
	}
	itemsTable, err := s.router.readTable(mysqlInventoryItemsTable, characterID)
	if err != nil {
		return repository.InventoryRecord{}, false, err
	}
	extraTable, err := s.router.readTable(mysqlInventoryExtraTable, characterID)
	if err != nil {
		return repository.InventoryRecord{}, false, err
	}
	query := s.router.selectQuery("SELECT character_id, updated_at FROM " + table + " WHERE character_id = ?")
	var record repository.InventoryRecord
	var updatedAt sql.NullTime
	err = s.router.db.QueryRowContext(ctx, query, characterID).Scan(
		&record.CharacterID,
		&updatedAt,
	)
	if err != nil {
		ok, scanErr := scanErr(err)
		return repository.InventoryRecord{}, ok, scanErr
	}
	record.Slots, err = loadBuffLockedItemStackCollection(
		ctx, s.router.db, itemsTable, extraTable, "character_id", characterID, "slots", true,
	)
	if err != nil {
		return repository.InventoryRecord{}, false, err
	}
	record.Warehouse, err = loadBuffLockedItemStackCollection(
		ctx, s.router.db, itemsTable, extraTable, "character_id", characterID, "warehouse", true,
	)
	if err != nil {
		return repository.InventoryRecord{}, false, err
	}
	record.UpdatedAt = scanTime(updatedAt)
	return repository.CloneInventory(record), true, nil
}

func loadBuffLockedItemStackCollection(
	ctx context.Context,
	database SQLDB,
	itemTable, extraTable, ownerColumn, ownerID, collection string,
	withCollection bool,
) (map[string]repository.ItemStack, error) {
	where := quoteSQLIdentifier(ownerColumn) + " = ?"
	args := []any{ownerID}
	if withCollection {
		where += " AND collection_name = ?"
		args = append(args, collection)
	}
	query := "SELECT entry_key, item_id, item_count, bind_flag, expire_at, raw_entry FROM " + itemTable + " WHERE " + where + " ORDER BY entry_key FOR UPDATE"
	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var values map[string]repository.ItemStack
	for rows.Next() {
		var key string
		var stack repository.ItemStack
		var expireAt sql.NullTime
		var rawEntry []byte
		if err := rows.Scan(&key, &stack.ItemID, &stack.Count, &stack.Bind, &expireAt, &rawEntry); err != nil {
			rows.Close()
			return nil, err
		}
		stack.ExpireAt = scanTime(expireAt)
		stack.RawEntry = append([]byte(nil), rawEntry...)
		if values == nil {
			values = make(map[string]repository.ItemStack)
		}
		values[key] = stack
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	extraWhere := quoteSQLIdentifier(ownerColumn) + " = ?"
	extraArgs := []any{ownerID}
	if withCollection {
		extraWhere += " AND collection_name = ?"
		extraArgs = append(extraArgs, collection)
	}
	extraQuery := "SELECT entry_key, extra_key, extra_value FROM " + extraTable + " WHERE " + extraWhere + " ORDER BY entry_key, extra_key FOR UPDATE"
	extras, err := database.QueryContext(ctx, extraQuery, extraArgs...)
	if err != nil {
		return nil, err
	}
	for extras.Next() {
		var entryKey, extraKey, extraValue string
		if err := extras.Scan(&entryKey, &extraKey, &extraValue); err != nil {
			extras.Close()
			return nil, err
		}
		stack, ok := values[entryKey]
		if !ok {
			continue
		}
		if stack.Extra == nil {
			stack.Extra = make(map[string]string)
		}
		stack.Extra[extraKey] = extraValue
		values[entryKey] = stack
	}
	if err := extras.Err(); err != nil {
		extras.Close()
		return nil, err
	}
	if err := extras.Close(); err != nil {
		return nil, err
	}
	return values, nil
}
