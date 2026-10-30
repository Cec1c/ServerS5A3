package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/repository"
	"sort"
)

func withMySQLWriteExecutor(ctx context.Context, database SQLDB, apply func(SQLDB) error) error {
	if apply == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	beginner, ok := database.(sqlTxBeginner)
	if !ok {
		return apply(database)
	}
	tx, err := beginObservedSQLTransaction(ctx, beginner, nil)
	if err != nil {
		return err
	}
	txDB := newSQLTransactionDB(tx.Tx, database)
	if err := apply(txDB); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

func sortedStringKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func loadStringMap(
	ctx context.Context,
	database SQLDB,
	query string,
	ownerID string,
) (map[string]string, error) {
	rows, err := database.QueryContext(ctx, query, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values map[string]string
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if values == nil {
			values = make(map[string]string)
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func replaceStringMap(
	ctx context.Context,
	database SQLDB,
	table, ownerColumn, ownerID string,
	values map[string]string,
) error {
	if _, err := database.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+quoteSQLIdentifier(ownerColumn)+" = ?", ownerID); err != nil {
		return err
	}
	query := "INSERT INTO " + table + " (" + quoteSQLIdentifier(ownerColumn) + ", entry_key, entry_value) VALUES (?, ?, ?)"
	for _, key := range sortedStringKeys(values) {
		if _, err := database.ExecContext(ctx, query, ownerID, key, values[key]); err != nil {
			return err
		}
	}
	return nil
}

func loadItemStackCollection(
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
	query := "SELECT entry_key, item_id, item_count, bind_flag, expire_at, raw_entry FROM " + itemTable + " WHERE " + where + " ORDER BY entry_key"
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
	extraQuery := "SELECT entry_key, extra_key, extra_value FROM " + extraTable + " WHERE " + extraWhere + " ORDER BY entry_key, extra_key"
	extras, err := database.QueryContext(ctx, extraQuery, extraArgs...)
	if err != nil {
		return nil, err
	}
	defer extras.Close()
	for extras.Next() {
		var entryKey, extraKey, extraValue string
		if err := extras.Scan(&entryKey, &extraKey, &extraValue); err != nil {
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
		return nil, err
	}
	return values, nil
}

func replaceItemStackCollection(
	ctx context.Context,
	database SQLDB,
	itemTable, extraTable, ownerColumn, ownerID, collection string,
	withCollection bool,
	values map[string]repository.ItemStack,
) error {
	target := newItemStackWriteTarget(itemTable, extraTable, ownerColumn, ownerID, collection, withCollection)
	if err := target.deleteCollection(ctx, database); err != nil {
		return err
	}
	return target.insert(ctx, database, values, sortedStringKeys(values))
}

// replaceItemStackKeys 是 O-B 的增量口径：只重写 keys 里列出的 slot（连同它自己的
// extra 行），未列出的 key 一条 SQL 都不发。
//
// 与整容器重写等价的前提：未列出的 key 在库里已经等于 values 里的值。调用方
// （equip.Owner.saveMoveInventoryFields）在同一个事务里先 Load 出完整 record、
// 再按 delta 写回，因此这条前提由"同一事务、先读后写"保证。
//
// 空 key 集合 = 一条 SQL 都不发 = 静默不写，这是本项最坏的失败形状，所以这里直接
// 报错让事务回滚；"取不到 key 就整容器写并记事件"的责任留在两个上层入口
// （mysqlBagStore.SaveItemStackSlotDelta 与 equip.Owner.saveMoveInventoryFields）。
func replaceItemStackKeys(
	ctx context.Context,
	database SQLDB,
	itemTable, extraTable, ownerColumn, ownerID, collection string,
	withCollection bool,
	values map[string]repository.ItemStack,
	keys []string,
) error {
	keys = sortedUniqueStrings(keys)
	if len(keys) == 0 {
		return fmt.Errorf("%w: item stack delta keys are empty", ErrMySQLConfigInvalid)
	}
	target := newItemStackWriteTarget(itemTable, extraTable, ownerColumn, ownerID, collection, withCollection)
	if err := target.deleteKeys(ctx, database, keys); err != nil {
		return err
	}
	return target.insert(ctx, database, values, keys)
}

// itemStackWriteTarget 是 ItemStack 容器（items 表 + extra 表 + 归属列 + collection）
// 的语句形状。整容器重写与增量重写共用它，保证两条路拼出来的行内容与行顺序完全一致。
type itemStackWriteTarget struct {
	itemTable      string
	extraTable     string
	ownerColumn    string
	ownerID        string
	collection     string
	withCollection bool
	where          string
	whereArgs      []any
	itemShape      insertShape
	extraShape     insertShape
}

func newItemStackWriteTarget(
	itemTable, extraTable, ownerColumn, ownerID, collection string,
	withCollection bool,
) itemStackWriteTarget {
	where := quoteSQLIdentifier(ownerColumn) + " = ?"
	whereArgs := []any{ownerID}
	if withCollection {
		where += " AND collection_name = ?"
		whereArgs = append(whereArgs, collection)
	}
	itemColumns := quoteSQLIdentifier(ownerColumn) + ", entry_key, item_id, item_count, bind_flag, expire_at, raw_entry"
	itemPlaceholders := "?, ?, ?, ?, ?, ?, ?"
	itemColumnCount := 7
	if withCollection {
		itemColumns = quoteSQLIdentifier(ownerColumn) + ", collection_name, entry_key, item_id, item_count, bind_flag, expire_at, raw_entry"
		itemPlaceholders = "?, ?, ?, ?, ?, ?, ?, ?"
		itemColumnCount = 8
	}
	extraColumns := quoteSQLIdentifier(ownerColumn) + ", entry_key, extra_key, extra_value"
	extraPlaceholders := "?, ?, ?, ?"
	extraColumnCount := 4
	if withCollection {
		extraColumns = quoteSQLIdentifier(ownerColumn) + ", collection_name, entry_key, extra_key, extra_value"
		extraPlaceholders = "?, ?, ?, ?, ?"
		extraColumnCount = 5
	}
	return itemStackWriteTarget{
		itemTable:      itemTable,
		extraTable:     extraTable,
		ownerColumn:    ownerColumn,
		ownerID:        ownerID,
		collection:     collection,
		withCollection: withCollection,
		where:          where,
		whereArgs:      whereArgs,
		itemShape: insertShape{
			table:        itemTable,
			collection:   collection,
			prefix:       "INSERT INTO " + itemTable + " (" + itemColumns + ") VALUES ",
			placeholders: "(" + itemPlaceholders + ")",
			columns:      itemColumnCount,
		},
		extraShape: insertShape{
			table:        extraTable,
			collection:   collection,
			prefix:       "INSERT INTO " + extraTable + " (" + extraColumns + ") VALUES ",
			placeholders: "(" + extraPlaceholders + ")",
			columns:      extraColumnCount,
		},
	}
}

// deleteCollection 是今天的两条整容器 DELETE（语句文本与改动前逐字节相同）。
func (t itemStackWriteTarget) deleteCollection(ctx context.Context, database SQLDB) error {
	if _, err := database.ExecContext(ctx, "DELETE FROM "+t.extraTable+" WHERE "+t.where, t.whereArgs...); err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, "DELETE FROM "+t.itemTable+" WHERE "+t.where, t.whereArgs...); err != nil {
		return err
	}
	return nil
}

// deleteKeys 只删列出的 key（extra 表在前、items 表在后，与整容器口径同序）。
func (t itemStackWriteTarget) deleteKeys(ctx context.Context, database SQLDB, keys []string) error {
	for _, chunk := range chunkStrings(keys, mysqlKeyDeleteChunkRows) {
		predicate := t.where + " AND entry_key IN (" + placeholders(len(chunk)) + ")"
		args := append(append([]any(nil), t.whereArgs...), stringsToAny(chunk)...)
		if _, err := database.ExecContext(ctx, "DELETE FROM "+t.extraTable+" WHERE "+predicate, args...); err != nil {
			return err
		}
		if _, err := database.ExecContext(ctx, "DELETE FROM "+t.itemTable+" WHERE "+predicate, args...); err != nil {
			return err
		}
	}
	return nil
}

// insert 按 keys 的顺序写行。默认档（batch<=1）每条 add 立即执行，因此 items 行与
// extra 行的**交错顺序与改动前逐字节相同**；批量档按表各用一个批量器，表内行顺序不变。
func (t itemStackWriteTarget) insert(
	ctx context.Context,
	database SQLDB,
	values map[string]repository.ItemStack,
	keys []string,
) error {
	if len(keys) == 0 {
		return nil
	}
	itemBatcher := newRowInsertBatcher(ctx, database, t.ownerID, t.itemShape)
	extraBatcher := newRowInsertBatcher(ctx, database, t.ownerID, t.extraShape)
	for _, entryKey := range keys {
		stack, ok := values[entryKey]
		if !ok {
			// key 不在 record 里 = 这一格已空：整容器口径下 DELETE 之后也不插任何行。
			continue
		}
		itemArgs := []any{t.ownerID, entryKey, stack.ItemID, stack.Count, stack.Bind, sqlTime(stack.ExpireAt), stack.RawEntry}
		if t.withCollection {
			itemArgs = []any{t.ownerID, t.collection, entryKey, stack.ItemID, stack.Count, stack.Bind, sqlTime(stack.ExpireAt), stack.RawEntry}
		}
		if err := itemBatcher.add(itemArgs...); err != nil {
			return err
		}
		for _, extraKey := range sortedStringKeys(stack.Extra) {
			extraArgs := []any{t.ownerID, entryKey, extraKey, stack.Extra[extraKey]}
			if t.withCollection {
				extraArgs = []any{t.ownerID, t.collection, entryKey, extraKey, stack.Extra[extraKey]}
			}
			if err := extraBatcher.add(extraArgs...); err != nil {
				return err
			}
		}
	}
	if err := itemBatcher.flush(); err != nil {
		return err
	}
	return extraBatcher.flush()
}
