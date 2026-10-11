// 本文件实现 DNF 穿戴装备仓储的 MySQL 字段化读写。
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"longheng.io/server/internal/modules/dnf/repository"
	"time"
)

const mysqlEquipmentTable = "equipments"

type mysqlEquipmentStore struct {
	mysqlStoreBase
}

// Load 按角色 ID 从 MySQL 读取穿戴装备记录。
func (s *mysqlEquipmentStore) Load(ctx context.Context, characterID string) (repository.EquipmentRecord, bool, error) {
	table, err := s.router.readTable(mysqlEquipmentTable, characterID)
	if err != nil {
		return repository.EquipmentRecord{}, false, err
	}
	entriesTable, err := s.router.readTable(mysqlEquipmentEntriesTable, characterID)
	if err != nil {
		return repository.EquipmentRecord{}, false, err
	}
	extraTable, err := s.router.readTable(mysqlEquipmentExtraTable, characterID)
	if err != nil {
		return repository.EquipmentRecord{}, false, err
	}
	query := s.router.selectQuery("SELECT character_id, updated_at FROM " + table + " WHERE character_id = ?")
	var record repository.EquipmentRecord
	var updatedAt sql.NullTime
	err = s.router.db.QueryRowContext(ctx, query, characterID).Scan(
		&record.CharacterID,
		&updatedAt,
	)
	if err != nil {
		ok, scanErr := scanErr(err)
		return repository.EquipmentRecord{}, ok, scanErr
	}
	record.Entries, err = loadEquipmentEntries(ctx, s.router, entriesTable, extraTable, characterID)
	if err != nil {
		return repository.EquipmentRecord{}, false, err
	}
	record.UpdatedAt = scanTime(updatedAt)
	return repository.CloneEquipment(record), true, nil
}

// Save 保存完整穿戴装备记录。
func (s *mysqlEquipmentStore) Save(ctx context.Context, record repository.EquipmentRecord) error {
	return s.SaveFields(ctx, record, repository.AllEquipmentFields()...)
}

// SaveFields 保存穿戴装备 dirty 字段。
func (s *mysqlEquipmentStore) SaveFields(ctx context.Context, record repository.EquipmentRecord, fields ...repository.EquipmentField) error {
	characterID, err := requireRecordKey(repository.EquipmentKey, record, "equipment")
	if err != nil {
		return err
	}
	fields = repository.EquipmentFields.Normalize(fields)
	if len(fields) == 0 {
		return nil
	}
	table, err := s.router.writeTable(mysqlEquipmentTable, characterID)
	if err != nil {
		return err
	}
	entriesTable, err := s.router.writeTable(mysqlEquipmentEntriesTable, characterID)
	if err != nil {
		return err
	}
	extraTable, err := s.router.writeTable(mysqlEquipmentExtraTable, characterID)
	if err != nil {
		return err
	}
	saveEntries := false
	for _, field := range fields {
		if field == repository.EquipmentFieldEntries {
			saveEntries = true
		}
	}
	return withMySQLWriteExecutor(ctx, s.router.db, func(database SQLDB) error {
		if execErr := s.upsertRecord(ctx, database, table, characterID, record.UpdatedAt); execErr != nil {
			return execErr
		}
		if !saveEntries {
			return nil
		}
		return replaceEquipmentEntries(ctx, database, entriesTable, extraTable, characterID, record.Entries)
	})
}

// SaveEquipmentEntryDelta 是 O-B 的增量落点：只重写 record.Entries 里 keys 指定的
// 穿戴 entry（连同各自的 extra 行），其余 entry 一条 SQL 都不发。语义与
// SaveFields(..., EquipmentFieldEntries) 完全等价 —— 前提是未列出的 key 在库里
// 已经等于 record 的值（op19 的调用方在同一事务里先 Load 完整记录再写回）。
//
// 安全口径（与 _dist/op19-commit-span-decomposition.md §7-O-B 的安全要求一致）：
//   - keys 为空 = "取不到增量"，**不报错也不静默不写**，而是整容器重写 + 记一条事件；
//   - 父表 upsert 与整容器口径完全一致（switch=entry 只省条目行的往返，不改任何语义）。
func (s *mysqlEquipmentStore) SaveEquipmentEntryDelta(
	ctx context.Context,
	record repository.EquipmentRecord,
	keys []string,
) error {
	characterID, err := requireRecordKey(repository.EquipmentKey, record, "equipment")
	if err != nil {
		return err
	}
	keys = sortedUniqueStrings(keys)
	if len(keys) == 0 {
		notifyWriteEvent(WriteEvent{
			Kind:    "equipment_entry_keys_unavailable",
			Table:   mysqlEquipmentEntriesTable,
			OwnerID: characterID,
			Detail:  "fallback_to_collection",
		})
		return s.SaveFields(ctx, record, repository.EquipmentFieldEntries)
	}
	table, err := s.router.writeTable(mysqlEquipmentTable, characterID)
	if err != nil {
		return err
	}
	entriesTable, err := s.router.writeTable(mysqlEquipmentEntriesTable, characterID)
	if err != nil {
		return err
	}
	extraTable, err := s.router.writeTable(mysqlEquipmentExtraTable, characterID)
	if err != nil {
		return err
	}
	return withMySQLWriteExecutor(ctx, s.router.db, func(database SQLDB) error {
		if execErr := s.upsertRecord(ctx, database, table, characterID, record.UpdatedAt); execErr != nil {
			return execErr
		}
		return replaceEquipmentEntryKeys(ctx, database, entriesTable, extraTable, characterID, record.Entries, keys)
	})
}

func (s *mysqlEquipmentStore) upsertRecord(
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

func loadEquipmentEntries(
	ctx context.Context,
	router mysqlRouter,
	entriesTable, extraTable, characterID string,
) (map[string]repository.EquipmentEntry, error) {
	query := router.selectQuery("SELECT entry_key, slot_index, item_id, bind_flag, expire_at, raw_entry FROM " + entriesTable + " WHERE character_id = ? ORDER BY entry_key")
	rows, err := router.db.QueryContext(ctx, query, characterID)
	if err != nil {
		return nil, err
	}
	var entries map[string]repository.EquipmentEntry
	for rows.Next() {
		var key string
		var entry repository.EquipmentEntry
		var expireAt sql.NullTime
		var rawEntry []byte
		if err := rows.Scan(&key, &entry.SlotIndex, &entry.ItemID, &entry.Bind, &expireAt, &rawEntry); err != nil {
			rows.Close()
			return nil, err
		}
		entry.ExpireAt = scanTime(expireAt)
		entry.RawEntry = append([]byte(nil), rawEntry...)
		if entries == nil {
			entries = make(map[string]repository.EquipmentEntry)
		}
		entries[key] = entry
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	extras, err := router.db.QueryContext(ctx, router.selectQuery("SELECT entry_key, extra_key, extra_value FROM "+extraTable+" WHERE character_id = ? ORDER BY entry_key, extra_key"), characterID)
	if err != nil {
		return nil, err
	}
	defer extras.Close()
	for extras.Next() {
		var entryKey, extraKey, extraValue string
		if err := extras.Scan(&entryKey, &extraKey, &extraValue); err != nil {
			return nil, err
		}
		entry, ok := entries[entryKey]
		if !ok {
			continue
		}
		if entry.Extra == nil {
			entry.Extra = make(map[string]string)
		}
		entry.Extra[extraKey] = extraValue
		entries[entryKey] = entry
	}
	if err := extras.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func replaceEquipmentEntries(
	ctx context.Context,
	database SQLDB,
	entriesTable, extraTable, characterID string,
	entries map[string]repository.EquipmentEntry,
) error {
	target := newEquipmentEntryWriteTarget(entriesTable, extraTable, characterID)
	if err := target.deleteCollection(ctx, database); err != nil {
		return err
	}
	return target.insert(ctx, database, entries, sortedStringKeys(entries))
}

// replaceEquipmentEntryKeys 是 O-B 的增量口径：只重写 keys 里列出的穿戴 entry
// （连同它自己的 extra 行），未列出的 entry 一条 SQL 都不发。
//
// 与整容器重写等价的前提：未列出的 key 在库里已经等于 entries 里的值。调用方
// （equip.Owner.saveMoveEquipmentEntries / swapEquipSlots）在同一个事务里先 Load
// 出完整 EquipmentRecord、再按 delta 写回，因此这条前提由"同一事务、先读后写"保证。
//
// 空 key 集合 = 静默不写，是最坏的失败形状，所以这里直接报错让事务回滚；
// "取不到 key 就整容器写并记事件"的责任留在上层入口
// （mysqlEquipmentStore.SaveEquipmentEntryDelta 与 equip.Owner.saveMoveEquipmentEntries）。
func replaceEquipmentEntryKeys(
	ctx context.Context,
	database SQLDB,
	entriesTable, extraTable, characterID string,
	entries map[string]repository.EquipmentEntry,
	keys []string,
) error {
	keys = sortedUniqueStrings(keys)
	if len(keys) == 0 {
		return fmt.Errorf("%w: equipment entry delta keys are empty", ErrMySQLConfigInvalid)
	}
	target := newEquipmentEntryWriteTarget(entriesTable, extraTable, characterID)
	if err := target.deleteKeys(ctx, database, keys); err != nil {
		return err
	}
	return target.insert(ctx, database, entries, keys)
}

// equipmentEntryWriteTarget 是穿戴表（entries 表 + extra 表）的语句形状。
// 整容器重写与增量重写共用它，保证两条路的行内容与行顺序完全一致。
type equipmentEntryWriteTarget struct {
	entriesTable string
	extraTable   string
	characterID  string
	entryShape   insertShape
	extraShape   insertShape
}

func newEquipmentEntryWriteTarget(entriesTable, extraTable, characterID string) equipmentEntryWriteTarget {
	return equipmentEntryWriteTarget{
		entriesTable: entriesTable,
		extraTable:   extraTable,
		characterID:  characterID,
		entryShape: insertShape{
			table:        entriesTable,
			prefix:       "INSERT INTO " + entriesTable + " (character_id, entry_key, slot_index, item_id, bind_flag, expire_at, raw_entry) VALUES ",
			placeholders: "(?, ?, ?, ?, ?, ?, ?)",
			columns:      7,
		},
		extraShape: insertShape{
			table:        extraTable,
			prefix:       "INSERT INTO " + extraTable + " (character_id, entry_key, extra_key, extra_value) VALUES ",
			placeholders: "(?, ?, ?, ?)",
			columns:      4,
		},
	}
}

// deleteCollection 是今天的两条整容器 DELETE（语句文本与改动前逐字节相同）。
func (t equipmentEntryWriteTarget) deleteCollection(ctx context.Context, database SQLDB) error {
	if _, err := database.ExecContext(ctx, "DELETE FROM "+t.extraTable+" WHERE character_id = ?", t.characterID); err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, "DELETE FROM "+t.entriesTable+" WHERE character_id = ?", t.characterID); err != nil {
		return err
	}
	return nil
}

// deleteKeys 只删列出的 entry（extra 表在前、entries 表在后，与整容器口径同序）。
func (t equipmentEntryWriteTarget) deleteKeys(ctx context.Context, database SQLDB, keys []string) error {
	for _, chunk := range chunkStrings(keys, mysqlKeyDeleteChunkRows) {
		predicate := "character_id = ? AND entry_key IN (" + placeholders(len(chunk)) + ")"
		args := append([]any{t.characterID}, stringsToAny(chunk)...)
		if _, err := database.ExecContext(ctx, "DELETE FROM "+t.extraTable+" WHERE "+predicate, args...); err != nil {
			return err
		}
		if _, err := database.ExecContext(ctx, "DELETE FROM "+t.entriesTable+" WHERE "+predicate, args...); err != nil {
			return err
		}
	}
	return nil
}

// insert 按 keys 的顺序写行。默认档（batch<=1）每条 add 立即执行，因此 entry 行与
// extra 行的**交错顺序与改动前逐字节相同**；批量档按表各用一个批量器，表内行顺序不变。
func (t equipmentEntryWriteTarget) insert(
	ctx context.Context,
	database SQLDB,
	entries map[string]repository.EquipmentEntry,
	keys []string,
) error {
	if len(keys) == 0 {
		return nil
	}
	entryBatcher := newRowInsertBatcher(ctx, database, t.characterID, t.entryShape)
	extraBatcher := newRowInsertBatcher(ctx, database, t.characterID, t.extraShape)
	for _, entryKey := range keys {
		entry, ok := entries[entryKey]
		if !ok {
			// key 不在 record 里 = 该穿戴槽已空：整容器口径下 DELETE 之后也不插任何行。
			continue
		}
		if err := entryBatcher.add(
			t.characterID, entryKey, entry.SlotIndex, entry.ItemID, entry.Bind, sqlTime(entry.ExpireAt), entry.RawEntry,
		); err != nil {
			return err
		}
		for _, extraKey := range sortedStringKeys(entry.Extra) {
			if err := extraBatcher.add(t.characterID, entryKey, extraKey, entry.Extra[extraKey]); err != nil {
				return err
			}
		}
	}
	if err := entryBatcher.flush(); err != nil {
		return err
	}
	return extraBatcher.flush()
}
