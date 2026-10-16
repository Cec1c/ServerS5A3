// 本文件实现 DNF 背包仓储的 MySQL 字段化读写。
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"time"
)

const mysqlBagTable = "inventories"

type mysqlBagStore struct {
	mysqlStoreBase
	trackSlotChanges bool
	loadedSnapshot   *repository.InventoryRecord
}

// Load 按角色 ID 从 MySQL 读取 DNF 背包记录。
func (s *mysqlBagStore) Load(ctx context.Context, characterID string) (repository.InventoryRecord, bool, error) {
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
	record.Slots, err = loadItemStackCollection(
		ctx, s.router.db, itemsTable, extraTable, "character_id", characterID, "slots", true,
	)
	if err != nil {
		return repository.InventoryRecord{}, false, err
	}
	record.Warehouse, err = loadItemStackCollection(
		ctx, s.router.db, itemsTable, extraTable, "character_id", characterID, "warehouse", true,
	)
	if err != nil {
		return repository.InventoryRecord{}, false, err
	}
	record.UpdatedAt = scanTime(updatedAt)
	if s.trackSlotChanges {
		snapshot := repository.CloneInventory(record)
		s.loadedSnapshot = &snapshot
	}
	return repository.CloneInventory(record), true, nil
}

// Save 保存完整 DNF 背包记录到 MySQL。
func (s *mysqlBagStore) Save(ctx context.Context, record repository.InventoryRecord) error {
	return s.SaveFields(ctx, record, repository.AllInventoryFields()...)
}

// SaveFields 保存 DNF 背包指定字段到 MySQL。
// 它只更新 slots 或 warehouse dirty 字段，便于后续异步落库降低写放大。
func (s *mysqlBagStore) SaveFields(ctx context.Context, record repository.InventoryRecord, fields ...repository.InventoryField) error {
	characterID, err := requireRecordKey(repository.InventoryKey, record, "inventory")
	if err != nil {
		return err
	}
	fields = repository.InventoryFields.Normalize(fields)
	if len(fields) == 0 {
		return nil
	}
	table, err := s.router.writeTable(mysqlBagTable, characterID)
	if err != nil {
		return err
	}
	itemsTable, err := s.router.writeTable(mysqlInventoryItemsTable, characterID)
	if err != nil {
		return err
	}
	extraTable, err := s.router.writeTable(mysqlInventoryExtraTable, characterID)
	if err != nil {
		return err
	}
	err = withMySQLWriteExecutor(ctx, s.router.db, func(database SQLDB) error {
		if execErr := s.upsertRecord(ctx, database, table, characterID, record.UpdatedAt); execErr != nil {
			return execErr
		}
		for _, field := range fields {
			switch field {
			case repository.InventoryFieldSlots:
				if err := s.saveAccountTransactionCollection(ctx, database, itemsTable, extraTable, record, field); err != nil {
					return err
				}
			case repository.InventoryFieldWarehouse:
				if err := s.saveAccountTransactionCollection(ctx, database, itemsTable, extraTable, record, field); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil && s.trackSlotChanges {
		s.rememberAccountTransactionFields(record, fields)
	}
	return err
}

// SaveItemStackSlotDelta 是 O-B 的增量落点：只重写 record 里 keys 指定的 slot
// （连同该 slot 自己的 extra 行），其余 slot 一条 SQL 都不发。语义与
// SaveFields(ctx, record, field) 完全等价 —— 前提是未列出的 key 在库里已经等于
// record 的值（op19 的调用方在同一个事务里先 Load 完整记录、再按 delta 写回）。
//
// 安全口径（与 _dist/op19-commit-span-decomposition.md §7-O-B 的安全要求一致）：
//   - keys 为空 = "取不到增量"：**不报错也不静默不写**，而是整容器重写 + 记一条事件；
//   - collection 只认 "slots" / "warehouse"（= InventoryField 的字面量），
//     不认得的取值直接报错让事务回滚（宁可失败，也不写错容器）；
//   - 父表 upsert 与整容器口径逐字节相同。
func (s *mysqlBagStore) SaveItemStackSlotDelta(
	ctx context.Context,
	record repository.InventoryRecord,
	collection string,
	keys []string,
) error {
	characterID, err := requireRecordKey(repository.InventoryKey, record, "inventory")
	if err != nil {
		return err
	}
	field, values, err := inventoryCollectionValues(record, collection)
	if err != nil {
		return err
	}
	keys = sortedUniqueStrings(keys)
	if len(keys) == 0 {
		notifyWriteEvent(WriteEvent{
			Kind:       "item_stack_keys_unavailable",
			Table:      mysqlInventoryItemsTable,
			Collection: string(field),
			OwnerID:    characterID,
			Detail:     "fallback_to_collection",
		})
		return s.SaveFields(ctx, record, field)
	}
	table, err := s.router.writeTable(mysqlBagTable, characterID)
	if err != nil {
		return err
	}
	itemsTable, err := s.router.writeTable(mysqlInventoryItemsTable, characterID)
	if err != nil {
		return err
	}
	extraTable, err := s.router.writeTable(mysqlInventoryExtraTable, characterID)
	if err != nil {
		return err
	}
	return withMySQLWriteExecutor(ctx, s.router.db, func(database SQLDB) error {
		if execErr := s.upsertRecord(ctx, database, table, characterID, record.UpdatedAt); execErr != nil {
			return execErr
		}
		return replaceItemStackKeys(
			ctx, database, itemsTable, extraTable, "character_id", characterID, string(field), true, values, keys,
		)
	})
}

func (s *mysqlBagStore) upsertRecord(
	ctx context.Context,
	database SQLDB,
	table, characterID string,
	updatedAt time.Time,
) error {
	columns := []string{"character_id", "updated_at"}
	args := []any{characterID, timeOrNow(updatedAt, s.router.now)}
	updates := []string{updateValue("updated_at")}
	_, err := database.ExecContext(ctx, buildUpsert(table, columns, updates), args...)
	return err
}

// inventoryCollectionValues 把 collection 字面量映射回 record 的字段与 map。
// 只认 slots / warehouse（= repository.InventoryField 的两个字面量）；其它取值报错。
func inventoryCollectionValues(
	record repository.InventoryRecord,
	collection string,
) (repository.InventoryField, map[string]repository.ItemStack, error) {
	switch strings.TrimSpace(collection) {
	case string(repository.InventoryFieldSlots):
		return repository.InventoryFieldSlots, record.Slots, nil
	case string(repository.InventoryFieldWarehouse):
		return repository.InventoryFieldWarehouse, record.Warehouse, nil
	default:
		return "", nil, fmt.Errorf("%w: inventory collection=%q", ErrMySQLConfigInvalid, collection)
	}
}
