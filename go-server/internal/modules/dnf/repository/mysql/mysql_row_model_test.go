// 本文件是 _dist/op19-commit-span-decomposition.md §7-O-B 的等价性证据底座：
// 一个只认本仓这几张表、这几条语句形状的最小**行模型**（内存表 + 最小 SQL 解释器）。
//
// 为什么要它：O-B 的判据不是"语句看起来对"，而是"落库之后库里的行逐行相同"。
// 行模型让"整容器重写"与"只写脏 key"两条路可以在同一个初值上各跑一次，再把
// 六张表的最终行集合做 DeepEqual。未覆盖的语句形状一律报错（防止生产 SQL 漂移后
// 测试假通过）。
package mysql

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

// rowModelPrimaryKeys 是参与比对的六张表的主键列。
var rowModelPrimaryKeys = map[string][]string{
	"dnf_settings":              {"scope"},
	"dnf_setting_values":        {"scope", "entry_key"},
	"dnf_inventories":           {"character_id"},
	"dnf_inventory_items":       {"character_id", "collection_name", "entry_key"},
	"dnf_inventory_item_extra":  {"character_id", "collection_name", "entry_key", "extra_key"},
	"dnf_equipments":            {"character_id"},
	"dnf_equipment_entries":     {"character_id", "entry_key"},
	"dnf_equipment_entry_extra": {"character_id", "entry_key", "extra_key"},
}

// rowModel 是六张表的内存镜像：表 -> 主键串 -> 列 -> 值。
type rowModel struct {
	tables map[string]map[string]map[string]any
}

func newRowModel() *rowModel {
	model := &rowModel{tables: map[string]map[string]map[string]any{}}
	for table := range rowModelPrimaryKeys {
		model.tables[table] = map[string]map[string]any{}
	}
	return model
}

func (m *rowModel) upsert(table string, row map[string]any) error {
	primary, ok := rowModelPrimaryKeys[table]
	if !ok {
		return fmt.Errorf("row model: unknown table %q", table)
	}
	key := rowModelPrimaryKeyText(primary, row)
	if key == "" {
		return fmt.Errorf("row model: row without primary key for %q: %v", table, row)
	}
	if m.tables[table] == nil {
		m.tables[table] = map[string]map[string]any{}
	}
	m.tables[table][key] = row
	return nil
}

// deleteWhere 删除满足 SQL 谓词的行（谓词只支持 character_id/collection_name/entry_key IN）。
func (m *rowModel) deleteWhere(table, predicate string, args []any) error {
	matcher, err := rowModelMatcher(predicate, args)
	if err != nil {
		return err
	}
	for key, row := range m.tables[table] {
		if matcher(row) {
			delete(m.tables[table], key)
		}
	}
	return nil
}

// snapshot 输出可用于 DeepEqual 的确定性文本视图（表 -> 排序后的行）。
//
// 唯一被排除的列是 updated_at：saveMove 每次都写 time.Now()，两条路各跑一次必然
// 不同时刻，它不参与"行内容是否等价"的判据（父表行本身的存在与其它列仍然比对）。
func (m *rowModel) snapshot() map[string][]string {
	out := make(map[string][]string, len(m.tables))
	for table, rows := range m.tables {
		lines := make([]string, 0, len(rows))
		for _, row := range rows {
			names := make([]string, 0, len(row))
			for name := range row {
				if name == "updated_at" {
					continue
				}
				names = append(names, name)
			}
			sort.Strings(names)
			parts := make([]string, 0, len(names))
			for _, name := range names {
				parts = append(parts, name+"="+rowModelValueText(row[name]))
			}
			lines = append(lines, strings.Join(parts, ", "))
		}
		sort.Strings(lines)
		out[table] = lines
	}
	return out
}

func rowModelValueText(value any) string {
	switch typed := value.(type) {
	case nil:
		return "<null>"
	case []byte:
		return "0x" + hex.EncodeToString(typed)
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func rowModelPrimaryKeyText(columns []string, row map[string]any) string {
	parts := make([]string, 0, len(columns))
	for _, column := range columns {
		value := rowModelValueText(row[column])
		if value == "<null>" {
			return ""
		}
		parts = append(parts, value)
	}
	return strings.Join(parts, "|")
}

// rowModelCondition 是谓词里的一个条件（= ? 或 IN (...)）。
type rowModelCondition struct {
	column string
	value  any
	list   []any
}

func rowModelMatcher(predicate string, args []any) (func(map[string]any) bool, error) {
	conditions, rest, err := rowModelConditions(strings.TrimSpace(predicate), args, nil)
	if err != nil {
		return nil, err
	}
	if rest != "" {
		return nil, fmt.Errorf("row model: unhandled predicate tail %q", rest)
	}
	return func(row map[string]any) bool {
		for _, condition := range conditions {
			switch {
			case condition.list != nil:
				found := false
				for _, value := range condition.list {
					if rowModelSame(row[condition.column], value) {
						found = true
						break
					}
				}
				if !found {
					return false
				}
			default:
				if !rowModelSame(row[condition.column], condition.value) {
					return false
				}
			}
		}
		return true
	}, nil
}

func rowModelConditions(predicate string, args []any, conditions []rowModelCondition) ([]rowModelCondition, string, error) {
	for {
		predicate = strings.TrimSpace(predicate)
		if !strings.HasPrefix(predicate, "AND ") {
			break
		}
		predicate = strings.TrimSpace(strings.TrimPrefix(predicate, "AND "))
	}
	if predicate == "" {
		if len(args) != 0 {
			return nil, "", fmt.Errorf("row model: %d unused predicate args", len(args))
		}
		return conditions, "", nil
	}
	switch {
	case strings.HasPrefix(predicate, "scope = ?"):
		if len(args) == 0 {
			return nil, "", fmt.Errorf("row model: scope predicate arg underflow")
		}
		conditions = append(conditions, rowModelCondition{column: "scope", value: args[0]})
		return rowModelConditions(strings.TrimPrefix(predicate, "scope = ?"), args[1:], conditions)
	case strings.HasPrefix(predicate, "character_id = ?"):
		if len(args) == 0 {
			return nil, "", fmt.Errorf("row model: predicate arg underflow for %q", predicate)
		}
		conditions = append(conditions, rowModelCondition{column: "character_id", value: args[0]})
		return rowModelConditions(strings.TrimPrefix(predicate, "character_id = ?"), args[1:], conditions)
	case strings.HasPrefix(predicate, "collection_name = ?"):
		if len(args) == 0 {
			return nil, "", fmt.Errorf("row model: predicate arg underflow for %q", predicate)
		}
		conditions = append(conditions, rowModelCondition{column: "collection_name", value: args[0]})
		return rowModelConditions(strings.TrimPrefix(predicate, "collection_name = ?"), args[1:], conditions)
	case strings.HasPrefix(predicate, "entry_key IN ("):
		end := strings.Index(predicate, ")")
		if end < 0 {
			return nil, "", fmt.Errorf("row model: unterminated IN list in %q", predicate)
		}
		count := strings.Count(predicate[:end], "?")
		if len(args) < count {
			return nil, "", fmt.Errorf("row model: IN list arg underflow for %q", predicate)
		}
		conditions = append(conditions, rowModelCondition{column: "entry_key", list: append([]any(nil), args[:count]...)})
		return rowModelConditions(predicate[end+1:], args[count:], conditions)
	default:
		return nil, "", fmt.Errorf("row model: unhandled predicate %q", predicate)
	}
}

func rowModelSame(left, right any) bool {
	return rowModelValueText(left) == rowModelValueText(right)
}

// rowModelDB 是 rowModel 的 SQLDB 实现。它把本仓这几条语句形状翻译成行操作，
// 未覆盖的形状直接报错。
type rowModelDB struct {
	model *rowModel
	calls []fakeSQLCall
}

func newRowModelDB() *rowModelDB {
	return &rowModelDB{model: newRowModel()}
}

func (d *rowModelDB) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	d.calls = append(d.calls, fakeSQLCall{Query: query, Args: append([]any(nil), args...)})
	switch {
	case strings.HasPrefix(query, "INSERT INTO "):
		return nil, d.applyInsert(query, args)
	case strings.HasPrefix(query, "DELETE FROM "):
		return nil, d.applyDelete(query, args)
	default:
		return nil, fmt.Errorf("row model: unhandled exec: %s", query)
	}
}

func (d *rowModelDB) QueryContext(_ context.Context, query string, args ...any) (SQLRows, error) {
	d.calls = append(d.calls, fakeSQLCall{Query: query, Args: append([]any(nil), args...)})
	rows, err := d.selectRows(query, args)
	if err != nil {
		return nil, err
	}
	return &fakeSQLRows{rows: rows}, nil
}

func (d *rowModelDB) QueryRowContext(_ context.Context, query string, args ...any) SQLRow {
	d.calls = append(d.calls, fakeSQLCall{Query: query, Args: append([]any(nil), args...)})
	rows, err := d.selectRows(query, args)
	if err != nil {
		return fakeSQLRow{err: err}
	}
	if len(rows) == 0 {
		return fakeSQLRow{err: sql.ErrNoRows}
	}
	return rows[0]
}

func (d *rowModelDB) PingContext(context.Context) error { return nil }

func (d *rowModelDB) applyInsert(query string, args []any) error {
	table, rest, err := rowModelStatementTable(query, "INSERT INTO ")
	if err != nil {
		return err
	}
	open := strings.Index(rest, "(")
	closeIdx := strings.Index(rest, ") VALUES ")
	if open < 0 || closeIdx < 0 || closeIdx < open {
		return fmt.Errorf("row model: unhandled insert shape: %s", query)
	}
	columns := rowModelColumns(rest[open+1 : closeIdx])
	width := len(columns)
	if width == 0 || len(args) == 0 || len(args)%width != 0 {
		return fmt.Errorf("row model: insert arg width mismatch (%d args, %d columns): %s", len(args), width, query)
	}
	for start := 0; start < len(args); start += width {
		row := make(map[string]any, width)
		for idx, column := range columns {
			row[column] = args[start+idx]
		}
		if err := d.model.upsert(table, row); err != nil {
			return err
		}
	}
	return nil
}

func (d *rowModelDB) applyDelete(query string, args []any) error {
	rest := strings.TrimPrefix(query, "DELETE FROM ")
	where := strings.Index(rest, " WHERE ")
	if where < 0 {
		return fmt.Errorf("row model: unhandled delete shape: %s", query)
	}
	table := rowModelTableName(rest[:where])
	if _, ok := rowModelPrimaryKeys[table]; !ok {
		return fmt.Errorf("row model: unknown table %q in %s", table, query)
	}
	predicate := strings.ReplaceAll(strings.TrimPrefix(rest[where:], " WHERE "), "`", "")
	return d.model.deleteWhere(table, predicate, args)
}

func (d *rowModelDB) selectRows(query string, args []any) ([]fakeSQLRow, error) {
	rest := strings.TrimPrefix(query, "SELECT ")
	from := strings.Index(rest, " FROM ")
	if from < 0 {
		return nil, fmt.Errorf("row model: unhandled select shape: %s", query)
	}
	columns := rowModelColumns(rest[:from])
	rest = rest[from+len(" FROM "):]
	if idx := strings.Index(rest, " FOR UPDATE"); idx >= 0 {
		rest = rest[:idx]
	}
	table, predicate, orderBy, err := rowModelSelectTail(rest)
	if err != nil {
		return nil, err
	}
	matcher, err := rowModelMatcher(predicate, args)
	if err != nil {
		return nil, err
	}
	matched := make([]map[string]any, 0, len(d.model.tables[table]))
	for _, row := range d.model.tables[table] {
		if matcher(row) {
			matched = append(matched, row)
		}
	}
	sortColumns := orderBy
	if len(sortColumns) == 0 {
		sortColumns = rowModelPrimaryKeys[table]
	}
	sort.SliceStable(matched, func(left, right int) bool {
		for _, column := range sortColumns {
			leftText := rowModelValueText(matched[left][column])
			rightText := rowModelValueText(matched[right][column])
			if leftText != rightText {
				return leftText < rightText
			}
		}
		return false
	})
	rows := make([]fakeSQLRow, 0, len(matched))
	for _, row := range matched {
		values := make([]any, 0, len(columns))
		for _, column := range columns {
			values = append(values, row[column])
		}
		rows = append(rows, fakeSQLRow{values: values})
	}
	return rows, nil
}

func rowModelSelectTail(rest string) (table, predicate string, orderBy []string, err error) {
	where := strings.Index(rest, " WHERE ")
	if where < 0 {
		return "", "", nil, fmt.Errorf("row model: select without WHERE: %s", rest)
	}
	table = rowModelTableName(rest[:where])
	rest = rest[where+len(" WHERE "):]
	if idx := strings.Index(rest, " ORDER BY "); idx >= 0 {
		orderBy = rowModelColumns(rest[idx+len(" ORDER BY "):])
		rest = rest[:idx]
	}
	return table, strings.ReplaceAll(strings.TrimSpace(rest), "`", ""), orderBy, nil
}

func rowModelStatementTable(query, prefix string) (string, string, error) {
	rest := strings.TrimPrefix(query, prefix)
	open := strings.Index(rest, " (")
	if open < 0 {
		return "", "", fmt.Errorf("row model: statement without column list: %s", query)
	}
	table := rowModelTableName(rest[:open])
	if _, ok := rowModelPrimaryKeys[table]; !ok {
		return "", "", fmt.Errorf("row model: unknown table %q in %s", table, query)
	}
	return table, rest[open:], nil
}

func rowModelTableName(raw string) string {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "`", "")
	if idx := strings.LastIndex(raw, "."); idx >= 0 {
		raw = raw[idx+1:]
	}
	return raw
}

func rowModelColumns(raw string) []string {
	parts := strings.Split(strings.ReplaceAll(raw, "`", ""), ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// rowModelItemUnitOfWork 是直通事务边界：行模型本身是单线程的，不需要真事务。
type rowModelItemUnitOfWork struct {
	inventory repository.InventoryRepository
	equipment repository.EquipmentRepository
}

func (u rowModelItemUnitOfWork) WithinCharacterItems(
	_ context.Context,
	characterID string,
	apply func(repository.InventoryRepository, repository.EquipmentRepository) error,
) error {
	if strings.TrimSpace(characterID) == "" || apply == nil {
		return repository.ErrCharacterItemTransactionUnavailable
	}
	return apply(u.inventory, u.equipment)
}

// newRowModelGroup 装配一个指向行模型的仓储组。lockReads 置真时所有读都带 FOR UPDATE，
// 与 op19 的域侧事务（mysql_transaction.go:259）一致。
func newRowModelGroup(t *testing.T, db *rowModelDB, lockReads bool) repository.Group {
	t.Helper()
	router, err := newMySQLRouter(db, MySQLGroupOptions{
		DatabasePlan: repository.DatabasePlan{
			WriteDatabases: []string{"dnf_s1_w1"},
			ReadDatabases:  []string{"dnf_s1_w1"},
		},
		TablePrefix: "dnf",
		Now: func() time.Time {
			return time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("newMySQLRouter() error = %v", err)
	}
	router.lockReads = lockReads
	group := newMySQLGroupFromRouter(router, false)
	group.CharacterItems = rowModelItemUnitOfWork{inventory: group.Inventory, equipment: group.Equipment}
	return group
}

// rowModelRawEntry 造一段足够长的 raw entry（>= 0x38 字节，覆盖所有被读的偏移）。
func rowModelRawEntry(seed byte) []byte {
	raw := make([]byte, 0x38)
	for idx := range raw {
		raw[idx] = seed + byte(idx)
	}
	return raw
}
