package gmtool

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// ---------------------------------------------------------------------------
// 账号备份/恢复的测试台
//
// 本包的测试机里没有 MySQL（api_accounts_search_test.go:30-34 已说明同一件事）。
// 这里沿用那份先例，再加一层"会真执行"的替身：一个 database/sql 驱动，
// 认识本实现生成的**全部** SQL 形状（SELECT/DELETE/INSERT + WHERE 过滤 + 事务回滚），
// 因此可以真的做"导出 -> 改库 -> 恢复 -> 再导出"的往返断言，而不是只比对字符串。
//
// 替身覆盖的 SQL 形状（就是实现里唯一会生成的那些，遇到别的形状直接报错）：
//
//	SELECT * FROM `t` WHERE <pred>
//	  <pred> ∈ {`c` = ?, `c` IN (?,...), (`account_id` = ? AND `delete_flag` = 0), 1 = 0}
//	SELECT `c` FROM `t` WHERE <pred>
//	SELECT 1 FROM `t` WHERE `account_id` = ? LIMIT 1
//	DELETE FROM `t` WHERE <pred>
//	INSERT INTO `t` (`c1`, `c2`) VALUES (?, ?)
//	SELECT table_name, column_name FROM information_schema.columns ...（返回 legacy 夹具）
//	SELECT id, account, account_id, ... FROM `dnf90_web_accounts` ORDER BY id
// ---------------------------------------------------------------------------

// backupTestTable 是替身库里的一张表：列、列类型（给 ColumnTypes 用）与行数据。
type backupTestTable struct {
	columns []string
	types   []string
	rows    [][]any
}

// backupTestDB 是替身库本体。所有连接共享同一个实例，事务用快照实现回滚。
type backupTestDB struct {
	mu     sync.Mutex
	tables map[string]*backupTestTable
	order  []string

	launcherRows []backupTestLauncherRow

	// failInsertTable 非空时，对这张表的任何 INSERT 都返回错误（测"中途失败必须回滚"）。
	failInsertTable string
	// log 记录执行过的写语句（用于"零写入"断言）。
	log []string
}

type backupTestLauncherRow struct {
	panelID   int64
	account   string
	accountID string
}

func newBackupTestDB() *backupTestDB {
	return &backupTestDB{tables: map[string]*backupTestTable{}}
}

func (db *backupTestDB) addTable(name string, columns []string, types []string, rows ...[]any) *backupTestTable {
	db.mu.Lock()
	defer db.mu.Unlock()
	table := &backupTestTable{columns: columns, types: types, rows: rows}
	if _, exists := db.tables[name]; !exists {
		db.order = append(db.order, name)
	}
	db.tables[name] = table
	return table
}

func (db *backupTestDB) snapshot() map[string][][]any {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.snapshotLocked()
}

func (db *backupTestDB) snapshotLocked() map[string][][]any {
	out := make(map[string][][]any, len(db.tables))
	for name, table := range db.tables {
		rows := make([][]any, 0, len(table.rows))
		for _, row := range table.rows {
			rows = append(rows, append([]any{}, row...))
		}
		out[name] = rows
	}
	return out
}

func (db *backupTestDB) restore(snapshot map[string][][]any) {
	db.mu.Lock()
	defer db.mu.Unlock()
	for name, rows := range snapshot {
		if table, ok := db.tables[name]; ok {
			table.rows = rows
		}
	}
}

func (db *backupTestDB) table(name string) *backupTestTable {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.tables[name]
}

func (db *backupTestDB) writes() []string {
	db.mu.Lock()
	defer db.mu.Unlock()
	return append([]string(nil), db.log...)
}

// backupTestConnector / Driver / Conn：database/sql 的最小驱动实现。
type backupTestConnector struct{ db *backupTestDB }

func (c backupTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &backupTestConn{db: c.db}, nil
}

func (c backupTestConnector) Driver() driver.Driver { return backupTestDriver{} }

type backupTestDriver struct{}

func (backupTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("backup test driver: use the connector")
}

type backupTestConn struct{ db *backupTestDB }

func (c *backupTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("backup test driver: prepare is not supported")
}

func (c *backupTestConn) Close() error { return nil }

func (c *backupTestConn) Begin() (driver.Tx, error) {
	return &backupTestTx{db: c.db, snapshot: c.db.snapshot()}, nil
}

func (c *backupTestConn) Ping(context.Context) error { return nil }

type backupTestTx struct {
	db       *backupTestDB
	snapshot map[string][][]any
	done     bool
}

func (tx *backupTestTx) Commit() error {
	tx.done = true
	return nil
}

func (tx *backupTestTx) Rollback() error {
	if tx.done {
		return nil
	}
	tx.done = true
	tx.db.restore(tx.snapshot)
	return nil
}

func (c *backupTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	values := backupTestArgs(args)
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	if strings.Contains(query, "information_schema.columns") {
		rows := make([][]any, 0, 16)
		for _, name := range c.db.order {
			table, ok := c.db.tables[name]
			if !ok || !strings.HasPrefix(name, backupLegacyTablePrefix) {
				continue
			}
			for _, column := range table.columns {
				rows = append(rows, []any{name, column})
			}
		}
		return &backupTestRows{columns: []string{"table_name", "column_name"},
			types: []string{"VARCHAR", "VARCHAR"}, rows: rows}, nil
	}
	if strings.Contains(query, "last_login_ip") {
		rows := make([][]any, 0, len(c.db.launcherRows))
		for _, row := range c.db.launcherRows {
			rows = append(rows, []any{row.panelID, row.account, row.accountID, nil, nil, nil})
		}
		return &backupTestRows{
			columns: []string{"id", "account", "account_id", "created_at", "last_login_at", "last_login_ip"},
			types:   []string{"BIGINT", "VARCHAR", "VARCHAR", "DATETIME", "DATETIME", "VARCHAR"},
			rows:    rows,
		}, nil
	}
	name, projection, where, err := backupTestParseSelect(query)
	if err != nil {
		return nil, err
	}
	table, ok := c.db.tables[name]
	if !ok {
		return nil, backupTestMissingTable(name)
	}
	indexes, err := backupTestSelectIndexes(table, projection)
	if err != nil {
		return nil, err
	}
	matched, err := backupTestFilterLocked(table, where, values)
	if err != nil {
		return nil, err
	}
	columns := make([]string, 0, len(indexes))
	types := make([]string, 0, len(indexes))
	rows := make([][]any, 0, len(matched))
	for _, index := range indexes {
		if index < 0 {
			columns = append(columns, "1")
			types = append(types, "BIGINT")
			continue
		}
		columns = append(columns, table.columns[index])
		types = append(types, table.types[index])
	}
	// 按表内下标升序返回（map 迭代顺序是随机的，会让导出的行序不可复现）。
	order := make([]int, 0, len(matched))
	for rowIndex := range matched {
		order = append(order, rowIndex)
	}
	sort.Ints(order)
	for _, rowIndex := range order {
		row := make([]any, 0, len(indexes))
		for _, index := range indexes {
			if index < 0 {
				row = append(row, int64(1))
				continue
			}
			row = append(row, table.rows[rowIndex][index])
		}
		rows = append(rows, row)
	}
	return &backupTestRows{columns: columns, types: types, rows: rows}, nil
}

func (c *backupTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	values := backupTestArgs(args)
	trimmed := strings.TrimSpace(query)
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	switch {
	case strings.HasPrefix(trimmed, "DELETE FROM "):
		name, where, err := backupTestParseDelete(query)
		if err != nil {
			return nil, err
		}
		table, ok := c.db.tables[name]
		if !ok {
			return nil, backupTestMissingTable(name)
		}
		matched, err := backupTestFilterLocked(table, where, values)
		if err != nil {
			return nil, err
		}
		kept := make([][]any, 0, len(table.rows))
		removed := int64(0)
		for index, row := range table.rows {
			if _, drop := matched[index]; drop {
				removed++
				continue
			}
			kept = append(kept, row)
		}
		table.rows = kept
		c.db.log = append(c.db.log, "DELETE "+name)
		return driver.RowsAffected(removed), nil
	case strings.HasPrefix(trimmed, "INSERT INTO "):
		name, columns, err := backupTestParseInsert(query)
		if err != nil {
			return nil, err
		}
		if c.db.failInsertTable == name {
			return nil, fmt.Errorf("backup test driver: injected insert failure on %s", name)
		}
		table, ok := c.db.tables[name]
		if !ok {
			return nil, backupTestMissingTable(name)
		}
		row := make([]any, len(table.columns))
		for index, column := range table.columns {
			position := -1
			for slot, candidate := range columns {
				if candidate == column {
					position = slot
					break
				}
			}
			if position < 0 || position >= len(values) {
				return nil, fmt.Errorf("backup test driver: field %s of %s has no default value", column, name)
			}
			row[index] = values[position]
		}
		for _, column := range columns {
			if _, ok := backupTestColumnIndex(table, column); !ok {
				return nil, fmt.Errorf("backup test driver: unknown column %s in %s", column, name)
			}
		}
		table.rows = append(table.rows, row)
		c.db.log = append(c.db.log, "INSERT "+name)
		return driver.RowsAffected(1), nil
	default:
		return nil, fmt.Errorf("backup test driver: unexpected exec %q", query)
	}
}

func backupTestMissingTable(name string) error {
	return fmt.Errorf("Error 1146: Table '%s' doesn't exist", name)
}

func backupTestArgs(args []driver.NamedValue) []any {
	out := make([]any, 0, len(args))
	for _, arg := range args {
		out = append(out, arg.Value)
	}
	return out
}

// backupTestRows 实现 driver.Rows（外加列类型，供 ColumnTypes 使用）。
type backupTestRows struct {
	columns []string
	types   []string
	rows    [][]any
	index   int
}

func (r *backupTestRows) Columns() []string { return r.columns }
func (r *backupTestRows) Close() error      { return nil }

func (r *backupTestRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	row := r.rows[r.index]
	r.index++
	for index := range dest {
		if index < len(row) {
			dest[index] = row[index]
			continue
		}
		dest[index] = nil
	}
	return nil
}

func (r *backupTestRows) ColumnTypeDatabaseTypeName(index int) string {
	if index < 0 || index >= len(r.types) {
		return ""
	}
	return r.types[index]
}

// ---------------------------------------------------------------------------
// 极简 SQL 解析（只认实现生成的形状，遇到别的形状直接报错 = 测试台不会静默放行）
// ---------------------------------------------------------------------------

func backupTestParseSelect(query string) (table string, projection []string, where string, err error) {
	upper := strings.ToUpper(query)
	fromIndex := strings.Index(upper, " FROM ")
	if !strings.HasPrefix(upper, "SELECT ") || fromIndex < 0 {
		return "", nil, "", fmt.Errorf("backup test driver: unexpected select %q", query)
	}
	projection = backupTestSplitColumns(strings.TrimSpace(query[len("SELECT "):fromIndex]))
	table = backupTestFirstIdentifier(query[fromIndex:])
	if table == "" {
		return "", nil, "", fmt.Errorf("backup test driver: no table in %q", query)
	}
	whereIndex := strings.Index(upper, " WHERE ")
	if whereIndex < 0 {
		return table, projection, "1 = 1", nil
	}
	where = strings.TrimSpace(query[whereIndex+len(" WHERE "):])
	for _, suffix := range []string{" ORDER BY ", " LIMIT "} {
		if cut := strings.Index(strings.ToUpper(where), suffix); cut >= 0 {
			where = strings.TrimSpace(where[:cut])
		}
	}
	return table, projection, where, nil
}

func backupTestParseDelete(query string) (table string, where string, err error) {
	upper := strings.ToUpper(query)
	if !strings.HasPrefix(upper, "DELETE FROM ") {
		return "", "", fmt.Errorf("backup test driver: unexpected delete %q", query)
	}
	table = backupTestFirstIdentifier(query)
	if table == "" {
		return "", "", fmt.Errorf("backup test driver: no table in %q", query)
	}
	whereIndex := strings.Index(upper, " WHERE ")
	if whereIndex < 0 {
		return table, "1 = 1", nil
	}
	return table, strings.TrimSpace(query[whereIndex+len(" WHERE "):]), nil
}

func backupTestParseInsert(query string) (table string, columns []string, err error) {
	upper := strings.ToUpper(query)
	if !strings.HasPrefix(upper, "INSERT INTO ") {
		return "", nil, fmt.Errorf("backup test driver: unexpected insert %q", query)
	}
	table = backupTestFirstIdentifier(query)
	if table == "" {
		return "", nil, fmt.Errorf("backup test driver: no table in %q", query)
	}
	open := strings.Index(query, "(")
	close := strings.Index(query, ")")
	if open < 0 || close <= open {
		return "", nil, fmt.Errorf("backup test driver: malformed insert %q", query)
	}
	return table, backupTestSplitColumns(query[open+1 : close]), nil
}

func backupTestFirstIdentifier(fragment string) string {
	for index := 0; index < len(fragment); index++ {
		if fragment[index] != '`' {
			continue
		}
		end := strings.Index(fragment[index+1:], "`")
		if end < 0 {
			return ""
		}
		return fragment[index+1 : index+1+end]
	}
	return ""
}

func backupTestSplitColumns(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.Trim(strings.TrimSpace(part), "`")
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func backupTestColumnIndex(table *backupTestTable, column string) (int, bool) {
	for index, name := range table.columns {
		if strings.EqualFold(name, column) {
			return index, true
		}
	}
	return -1, false
}

func backupTestSelectIndexes(table *backupTestTable, projection []string) ([]int, error) {
	if len(projection) == 1 && projection[0] == "*" {
		indexes := make([]int, len(table.columns))
		for index := range table.columns {
			indexes[index] = index
		}
		return indexes, nil
	}
	indexes := make([]int, 0, len(projection))
	for _, column := range projection {
		if column == "1" {
			// `SELECT 1 FROM ...` 的存在性探测：给一列假值即可。
			indexes = append(indexes, -1)
			continue
		}
		index, ok := backupTestColumnIndex(table, column)
		if !ok {
			return nil, fmt.Errorf("Error 1054: Unknown column '%s'", column)
		}
		indexes = append(indexes, index)
	}
	return indexes, nil
}

// backupTestFilterLocked 求值 WHERE（调用方必须已持锁）。返回命中的行下标集合。
func backupTestFilterLocked(table *backupTestTable, where string, args []any) (map[int]struct{}, error) {
	trimmed := strings.TrimSpace(where)
	matched := map[int]struct{}{}
	if trimmed == "1 = 0" {
		return matched, nil
	}
	if trimmed == "1 = 1" {
		for index := range table.rows {
			matched[index] = struct{}{}
		}
		return matched, nil
	}
	// `(account_id = ? AND delete_flag = 0)`：导出 dnf_characters 专用。
	if strings.Contains(trimmed, " AND `delete_flag` = 0") {
		index, ok := backupTestColumnIndex(table, "account_id")
		if !ok {
			return nil, errors.New("backup test driver: account_id is missing")
		}
		deleteIndex, ok := backupTestColumnIndex(table, "delete_flag")
		if !ok {
			return nil, errors.New("backup test driver: delete_flag is missing")
		}
		for rowIndex, row := range table.rows {
			if !backupTestSameValue(row[index], args[0]) {
				continue
			}
			if backupTestText(row[deleteIndex]) != "0" {
				continue
			}
			matched[rowIndex] = struct{}{}
		}
		return matched, nil
	}
	if position := strings.Index(trimmed, " IN ("); position >= 0 {
		head := trimmed[:position]
		inArgs := args
		excludeIndex := -1
		var excludeValue any
		// 复合形状：`account_id` <> ? AND `character_id` IN (?, ...)
		// （backupConflictingCharacterIDs 的"角色 ID 是否被别的账号占用"查询）。
		if cut := strings.Index(head, " <> ? AND "); cut >= 0 {
			excludeColumn := strings.Trim(strings.TrimSpace(head[:cut]), "`")
			head = head[cut+len(" <> ? AND "):]
			if len(args) == 0 {
				return nil, errors.New("backup test driver: <> ? expects an argument")
			}
			index, ok := backupTestColumnIndex(table, excludeColumn)
			if !ok {
				return nil, fmt.Errorf("Error 1054: Unknown column '%s'", excludeColumn)
			}
			excludeIndex = index
			excludeValue = args[0]
			inArgs = args[1:]
		}
		column := strings.Trim(strings.TrimSpace(head), "`")
		index, ok := backupTestColumnIndex(table, column)
		if !ok {
			return nil, fmt.Errorf("Error 1054: Unknown column '%s'", column)
		}
		for rowIndex, row := range table.rows {
			if excludeIndex >= 0 && backupTestSameValue(row[excludeIndex], excludeValue) {
				continue
			}
			for _, arg := range inArgs {
				if backupTestSameValue(row[index], arg) {
					matched[rowIndex] = struct{}{}
					break
				}
			}
		}
		return matched, nil
	}
	if strings.HasSuffix(trimmed, " = ?") {
		column := strings.Trim(strings.TrimSpace(strings.TrimSuffix(trimmed, " = ?")), "`")
		index, ok := backupTestColumnIndex(table, column)
		if !ok {
			return nil, fmt.Errorf("Error 1054: Unknown column '%s'", column)
		}
		if len(args) != 1 {
			return nil, fmt.Errorf("backup test driver: %s = ? expects 1 argument, got %d", column, len(args))
		}
		for rowIndex, row := range table.rows {
			if backupTestSameValue(row[index], args[0]) {
				matched[rowIndex] = struct{}{}
			}
		}
		return matched, nil
	}
	return nil, fmt.Errorf("backup test driver: unsupported where clause %q", where)
}

// backupTestText 把驱动值规范化成可比较的文本（时间按写回库时的同一格式）。
func backupTestText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		if typed {
			return "1"
		}
		return "0"
	case time.Time:
		return typed.Format(backupSQLTimeLayout)
	default:
		return fmt.Sprint(typed)
	}
}

func backupTestSameValue(left any, right any) bool {
	leftText := backupTestText(left)
	rightText := backupTestText(right)
	if leftText == rightText {
		return true
	}
	leftNumber, leftErr := strconv.ParseInt(leftText, 10, 64)
	rightNumber, rightErr := strconv.ParseInt(rightText, 10, 64)
	return leftErr == nil && rightErr == nil && leftNumber == rightNumber
}

// ---------------------------------------------------------------------------
// 夹具
// ---------------------------------------------------------------------------

const (
	backupTestAccountKey    = "dnf:10001"
	backupTestOtherAccount  = "dnf:10006"
	backupTestPanelID       = int64(10001)
	backupTestCharacterA    = "11"
	backupTestCharacterB    = "12"
	backupTestOtherChar     = "21"
	backupTestSoftDeletedID = "13"
	backupTestBinaryText    = "\x00\x01binary\xff"
)

var backupTestBaseTime = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

// backupTestFixtureDB 建一个"两张账号、三个角色"的替身库：
// 每一张核心表都在（导出对缺表是硬失败），其中一部分带数据。
func backupTestFixtureDB() *backupTestDB {
	db := newBackupTestDB()
	db.launcherRows = []backupTestLauncherRow{
		{panelID: backupTestPanelID, account: "alice", accountID: backupTestAccountKey},
		{panelID: 10006, account: "bob", accountID: backupTestOtherAccount},
	}
	for _, spec := range backupCoreTableSpecs() {
		link := "character_id"
		if spec.link == backupLinkAccount || spec.link == backupLinkCharacterRoster {
			link = "account_id"
		}
		columns := []string{link, "entry_key", "payload", "raw_entry", "expire_at"}
		types := []string{"VARCHAR", "VARCHAR", "LONGTEXT", "LONGBLOB", "DATETIME"}
		if spec.link == backupLinkCharacterRoster {
			columns = []string{"account_id", "character_id", "slot", "name", "job", "level", "delete_flag"}
			types = []string{"VARCHAR", "VARCHAR", "INT", "VARCHAR", "VARCHAR", "INT", "TINYINT"}
		}
		db.addTable(spec.name, columns, types)
	}
	// legacy 镜像表：两张按 character_id 归属、一张 related 子表、
	// 一张只有 account_id（必须完全不进备份）。
	db.addTable("dnf_legacy_character_items",
		[]string{"item_uid", "character_id", "list_type", "slot_index", "item_template_id", "stack_count"},
		[]string{"BIGINT", "BIGINT", "BIGINT", "BIGINT", "BIGINT", "BIGINT"},
		[]any{int64(9001), int64(11), int64(0), int64(0), int64(5001), int64(3)},
		[]any{int64(9500), int64(21), int64(0), int64(0), int64(5002), int64(1)})
	db.addTable("dnf_legacy_character_item_extra",
		[]string{"item_uid", "extra_key", "extra_value"},
		[]string{"BIGINT", "VARCHAR", "LONGTEXT"},
		[]any{int64(9001), "reinforce", "7"},
		[]any{int64(9500), "reinforce", "9"})
	db.addTable("dnf_legacy_character_userinfo90_control",
		[]string{"character_id", "value"},
		[]string{"BIGINT", "BIGINT"},
		[]any{int64(11), int64(3)},
		[]any{int64(21), int64(4)})
	db.addTable("dnf_legacy_accounts",
		[]string{"account_id", "name"},
		[]string{"BIGINT", "VARCHAR"},
		[]any{int64(0), "legacy-row"})

	db.addTable("dnf_accounts",
		[]string{"account_id", "state", "honor_exp", "represent_account_name"},
		[]string{"VARCHAR", "VARCHAR", "BIGINT", "VARCHAR"},
		[]any{backupTestAccountKey, "open", int64(4321), "Alice"},
		[]any{backupTestOtherAccount, "open", int64(1), "Bob"})
	db.addTable("dnf_account_metadata",
		[]string{"account_id", "entry_key", "entry_value"},
		[]string{"VARCHAR", "VARCHAR", "LONGTEXT"},
		[]any{backupTestAccountKey, "account_cera", "1234"},
		[]any{backupTestAccountKey, "account_cargo_gold", "777"},
		[]any{backupTestOtherAccount, "account_cera", "9"})
	db.addTable("dnf_characters",
		[]string{"account_id", "character_id", "slot", "name", "job", "level", "delete_flag"},
		[]string{"VARCHAR", "VARCHAR", "INT", "VARCHAR", "VARCHAR", "INT", "TINYINT"},
		[]any{backupTestAccountKey, backupTestCharacterA, int64(0), "AliceMain", "11", int64(90), int64(0)},
		[]any{backupTestAccountKey, backupTestCharacterB, int64(1), "AliceAlt", "12", int64(60), int64(0)},
		// 软删除的角色：**不导出**，但恢复时一定会被清掉。
		[]any{backupTestAccountKey, backupTestSoftDeletedID, int64(2), "AliceOld", "13", int64(10), int64(1)},
		[]any{backupTestOtherAccount, backupTestOtherChar, int64(0), "BobMain", "11", int64(70), int64(0)})
	db.addTable("dnf_inventory_items",
		[]string{"character_id", "collection_name", "entry_key", "item_id", "item_count", "raw_entry", "expire_at"},
		[]string{"VARCHAR", "VARCHAR", "VARCHAR", "BIGINT", "BIGINT", "LONGBLOB", "DATETIME"},
		[]any{backupTestCharacterA, "inventory", "slot-0", int64(1001), int64(5), []byte(backupTestBinaryText), backupTestBaseTime},
		[]any{backupTestCharacterA, "inventory", "slot-1", int64(1002), int64(1), nil, nil},
		[]any{backupTestCharacterB, "warehouse", "slot-0", int64(2001), int64(2), []byte("text-ok"), nil},
		[]any{backupTestOtherChar, "inventory", "slot-0", int64(3001), int64(9), nil, nil})
	db.addTable("dnf_mails",
		[]string{"character_id", "mail_id", "title", "body", "gold"},
		[]string{"VARCHAR", "VARCHAR", "VARCHAR", "LONGTEXT", "BIGINT"},
		[]any{backupTestCharacterA, "mail-1", "欢迎", "正文", int64(0)},
		[]any{backupTestOtherChar, "mail-1", "别人的邮件", "正文", int64(0)})
	db.addTable("dnf_mail_attachments",
		[]string{"character_id", "mail_id", "attachment_index", "item_id", "raw_entry"},
		[]string{"VARCHAR", "VARCHAR", "INT", "BIGINT", "LONGBLOB"},
		[]any{backupTestCharacterA, "mail-1", int64(0), int64(4242), []byte(backupTestBinaryText)})
	return db
}

func backupTestWebAccount() webAccount {
	return webAccount{PanelID: backupTestPanelID, Account: "alice", AccountID: backupTestAccountKey}
}

func backupTestCharacterIDs() []string {
	return []string{backupTestCharacterA, backupTestCharacterB}
}

func backupTestOpenDB(t *testing.T, db *backupTestDB) *sql.DB {
	t.Helper()
	sqlDB := sql.OpenDB(backupTestConnector{db: db})
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return sqlDB
}

// backupTestHarness 造一个带替身数据库的写测试台（内存仓储 + 替身 SQL）。
func backupTestHarness(t *testing.T, db *backupTestDB) *gmtoolWriteHarness {
	t.Helper()
	harness := newGmtoolWriteHarness(t, nil)
	harness.server.db = backupTestOpenDB(t, db)
	harness.server.cfg.Table = "dnf90_web_accounts"
	return harness
}

func backupTestExport(t *testing.T, db *backupTestDB) backupFile {
	t.Helper()
	file, err := backupExportAccount(context.Background(), backupTestOpenDB(t, db),
		backupTestWebAccount(), backupTestCharacterIDs())
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	return file
}

func backupTestDump(t *testing.T, file backupFile, name string) (backupTableDump, bool) {
	t.Helper()
	for _, dump := range file.Tables {
		if dump.Name == name {
			return dump, true
		}
	}
	return backupTableDump{}, false
}

// backupTestFindRow 按多个列值定位唯一一行。
func backupTestFindRow(t *testing.T, dump backupTableDump, want map[string]string) []backupValue {
	t.Helper()
	indexes := make(map[string]int, len(want))
	for column := range want {
		index, ok := dump.backupColumnIndex(column)
		if !ok {
			t.Fatalf("表 %s 没有列 %s", dump.Name, column)
		}
		indexes[column] = index
	}
	for _, row := range dump.Rows {
		matched := true
		for column, value := range want {
			actual, err := row[indexes[column]].toDBValue()
			if err != nil {
				t.Fatalf("表 %s 列 %s: %v", dump.Name, column, err)
			}
			if backupTestText(actual) != value {
				matched = false
				break
			}
		}
		if matched {
			return row
		}
	}
	t.Fatalf("表 %s 里找不到 %v（现有 %d 行）", dump.Name, want, len(dump.Rows))
	return nil
}

// backupTestForeignRows 取出"别的账号/别的角色"的全部行（按表分组），
// 用来断言恢复不碰别人的数据。
func backupTestForeignRows(db *backupTestDB) map[string][][]any {
	db.mu.Lock()
	defer db.mu.Unlock()
	out := map[string][][]any{}
	for name, table := range db.tables {
		var column string
		switch {
		case backupTestHasColumn(table, "character_id"):
			column = "character_id"
		case backupTestHasColumn(table, "account_id"):
			column = "account_id"
		default:
			continue
		}
		index, _ := backupTestColumnIndex(table, column)
		rows := make([][]any, 0, 4)
		for _, row := range table.rows {
			if backupTestSameValue(row[index], backupTestOtherAccount) ||
				backupTestSameValue(row[index], backupTestOtherChar) {
				rows = append(rows, append([]any{}, row...))
			}
		}
		if len(rows) > 0 {
			out[name] = rows
		}
	}
	return out
}

func backupTestHasColumn(table *backupTestTable, column string) bool {
	_, ok := backupTestColumnIndex(table, column)
	return ok
}

// ---------------------------------------------------------------------------
// 导出
// ---------------------------------------------------------------------------

// TestBackupExportOnlyCoversTheAccount：导出的每一行都必须属于这个账号/这些角色，
// 别的账号的数据一个字节都不能进备份；软删除的角色不导出；accounts 表恒在。
func TestBackupExportOnlyCoversTheAccount(t *testing.T) {
	db := backupTestFixtureDB()
	file := backupTestExport(t, db)

	if file.Version != backupFormatVersion {
		t.Fatalf("version = %d, want %d", file.Version, backupFormatVersion)
	}
	if file.AccountID != backupTestPanelID || file.AccountKey != backupTestAccountKey {
		t.Fatalf("accountID/accountKey = %d/%q", file.AccountID, file.AccountKey)
	}
	if !reflect.DeepEqual(file.CharacterIDs, backupTestCharacterIDs()) {
		t.Fatalf("characterIDs = %v, want %v", file.CharacterIDs, backupTestCharacterIDs())
	}
	if _, ok := backupTestDump(t, file, "dnf_accounts"); !ok {
		t.Fatal("备份里必须有 dnf_accounts（A21:133）")
	}
	if _, ok := backupTestDump(t, file, "dnf_legacy_accounts"); ok {
		t.Fatal("没有 character_id 的镜像表不能进备份")
	}
	if _, ok := backupTestDump(t, file, "dnf_legacy_character_item_extra"); !ok {
		t.Fatal("父表在范围内的 related 子表必须在备份里")
	}
	if _, ok := backupTestDump(t, file, "dnf_legacy_character_userinfo90_control"); !ok {
		t.Fatal("有 character_id 的镜像表必须在备份里")
	}

	characters, ok := backupTestDump(t, file, "dnf_characters")
	if !ok {
		t.Fatal("备份缺少 dnf_characters")
	}
	if len(characters.Rows) != 2 {
		t.Fatalf("dnf_characters 行数 = %d, want 2（软删除的角色不导出）", len(characters.Rows))
	}
	if ids, err := characters.backupKeyValues("character_id"); err != nil ||
		!reflect.DeepEqual(ids, []string{backupTestCharacterA, backupTestCharacterB}) {
		t.Fatalf("导出的角色 = %v (%v)", ids, err)
	}

	// 全表扫描：任何一张表里都不许出现别的账号/别的角色的行。
	for _, dump := range file.Tables {
		for _, column := range []string{"account_id", "character_id"} {
			index, ok := dump.backupColumnIndex(column)
			if !ok {
				continue
			}
			for rowIndex, row := range dump.Rows {
				value, err := row[index].toDBValue()
				if err != nil {
					t.Fatalf("表 %s: %v", dump.Name, err)
				}
				text := backupTestText(value)
				allowed := text == backupTestAccountKey || text == backupTestCharacterA || text == backupTestCharacterB
				if !allowed {
					t.Fatalf("表 %s 第 %d 行的 %s = %q 不属于本账号", dump.Name, rowIndex+1, column, text)
				}
			}
		}
	}

	// 二进制必须按 base64 存（不能当文本塞进 JSON）。
	items, ok := backupTestDump(t, file, "dnf_inventory_items")
	if !ok {
		t.Fatal("备份缺少 dnf_inventory_items")
	}
	binaryRow := backupTestFindRow(t, items, map[string]string{
		"character_id": backupTestCharacterA, "entry_key": "slot-0",
	})
	rawIndex, _ := items.backupColumnIndex("raw_entry")
	if binaryRow[rawIndex].Type != backupValueBlob {
		t.Fatalf("二进制列的类型 = %q, want blob", binaryRow[rawIndex].Type)
	}
	decoded, err := binaryRow[rawIndex].toDBValue()
	if err != nil {
		t.Fatalf("blob 解码失败: %v", err)
	}
	if string(decoded.([]byte)) != backupTestBinaryText {
		t.Fatalf("blob 往返后 = %q, want %q", decoded, backupTestBinaryText)
	}
	// NULL 与 DATETIME 都要保住。
	nullRow := backupTestFindRow(t, items, map[string]string{
		"character_id": backupTestCharacterA, "entry_key": "slot-1",
	})
	if nullRow[rawIndex].Type != backupValueNull {
		t.Fatalf("NULL 列的类型 = %q", nullRow[rawIndex].Type)
	}
	expireIndex, _ := items.backupColumnIndex("expire_at")
	if binaryRow[expireIndex].Type != backupValueText ||
		*binaryRow[expireIndex].Text != backupTestBaseTime.Format(backupSQLTimeLayout) {
		t.Fatalf("DATETIME 列 = %+v", binaryRow[expireIndex])
	}
}

// TestBackupExportKeepsEmptyAccountsDump：账号还没有 dnf_accounts 行时，
// 备份仍要带上（空）accounts 表 —— 那是"属于哪个账号"的证据（A21:133）。
func TestBackupExportKeepsEmptyAccountsDump(t *testing.T) {
	db := backupTestFixtureDB()
	db.table("dnf_accounts").rows = [][]any{{backupTestOtherAccount, "open", int64(1), "Bob"}}

	file := backupTestExport(t, db)
	dump, ok := backupTestDump(t, file, "dnf_accounts")
	if !ok {
		t.Fatal("空 accounts 表也必须留在备份里")
	}
	if len(dump.Rows) != 0 {
		t.Fatalf("dnf_accounts 行数 = %d, want 0", len(dump.Rows))
	}
}

// TestBackupExportFailsLoudlyOnMissingCoreTable：核心表缺失时导出必须失败，
// 绝不产出"少一张表"的备份。
func TestBackupExportFailsLoudlyOnMissingCoreTable(t *testing.T) {
	db := backupTestFixtureDB()
	delete(db.tables, "dnf_mail_attachments")

	if _, err := backupExportAccount(context.Background(), backupTestOpenDB(t, db),
		backupTestWebAccount(), backupTestCharacterIDs()); err == nil {
		t.Fatal("缺核心表时导出必须失败")
	}
}

// TestBackupExportRejectsCharacterListMismatch：角色行与角色清单不一致时导出必须
// 失败 —— 否则会产出一份"自己的恢复一定会拒绝"的备份（归属校验要求每一行的
// character_id 都在 characterIDs 里）。
func TestBackupExportRejectsCharacterListMismatch(t *testing.T) {
	db := backupTestFixtureDB()

	_, err := backupExportAccount(context.Background(), backupTestOpenDB(t, db),
		backupTestWebAccount(), []string{backupTestCharacterA}) // 少了 12
	if err == nil || !strings.Contains(err.Error(), "角色行") {
		t.Fatalf("err = %v", err)
	}
}

// TestBackupExportCharacterIDsComeFromTheDatabase：导出路由的角色清单必须直接读库
// （dnf_characters 的 delete_flag = 0 行），而不是选人界面槽位视图。
func TestBackupExportCharacterIDsComeFromTheDatabase(t *testing.T) {
	db := backupTestFixtureDB()
	harness := backupTestHarness(t, db) // 内存仓储里一个角色都没有

	status, exported := backupTestPost(t, harness, "/api/accounts/10001/backup", map[string]string{"id": "10001"}, nil)
	if status != http.StatusOK || exported["success"] != true {
		t.Fatalf("导出响应 = %d %v", status, exported)
	}
	ids, ok := exported["characterIDs"].([]any)
	if !ok || len(ids) != 2 || ids[0] != backupTestCharacterA || ids[1] != backupTestCharacterB {
		t.Fatalf("characterIDs = %v, want 数据库里的 [11 12]", exported["characterIDs"])
	}
}

// ---------------------------------------------------------------------------
// 往返：导出 -> 改库 -> 恢复 -> 再导出
// ---------------------------------------------------------------------------

// TestBackupRoundTripRestoresExactlyWhatWasExported 是本功能的核心断言：
// 恢复之后"再导出一次"必须与恢复前那份备份**完全一致**（含 BLOB/NULL/DATETIME），
// 而且只影响这个账号。
func TestBackupRoundTripRestoresExactlyWhatWasExported(t *testing.T) {
	db := backupTestFixtureDB()
	before := backupTestExport(t, db)

	// 备份之后再动这个账号：加一个角色、改数值、发一封新邮件、删掉一条元数据。
	db.table("dnf_characters").rows = append(db.table("dnf_characters").rows,
		[]any{backupTestAccountKey, "99", int64(3), "AliceNew", "11", int64(5), int64(0)})
	db.table("dnf_inventory_items").rows = append(db.table("dnf_inventory_items").rows,
		[]any{"99", "inventory", "slot-0", int64(7777), int64(1), nil, nil})
	db.table("dnf_account_metadata").rows = [][]any{{backupTestAccountKey, "account_cera", "999999"}}
	db.table("dnf_mails").rows = append(db.table("dnf_mails").rows,
		[]any{backupTestCharacterA, "mail-2", "新邮件", "正文", int64(0)})

	foreignBefore := backupTestForeignRows(db)
	result, err := backupRestoreAccount(context.Background(), backupTestOpenDB(t, db), backupTestWebAccount(), before)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.DeletedExistingCharacterCount != 4 {
		t.Fatalf("deletedExistingCharacterCount = %d, want 4（3 个原角色含软删除 + 备份后新建的 99）",
			result.DeletedExistingCharacterCount)
	}
	if result.RestoredCharacterCount != 2 || !reflect.DeepEqual(result.CharacterIDs, backupTestCharacterIDs()) {
		t.Fatalf("restored = %d %v", result.RestoredCharacterCount, result.CharacterIDs)
	}
	if result.SourceBackupVersion != backupFormatVersion || result.UpgradedFromVersion != 0 {
		t.Fatalf("source/upgraded = %d/%d", result.SourceBackupVersion, result.UpgradedFromVersion)
	}
	if result.RestoredRowCount == 0 || result.DeletedRowCount == 0 {
		t.Fatalf("行数统计 = %d / %d", result.DeletedRowCount, result.RestoredRowCount)
	}
	// 本仓没有全局代理键，因此不存在"重映射"这回事，如实回报 0。
	if result.RemappedMailboxMessageIDCount != 0 || result.RemappedMailboxAuditIDCount != 0 ||
		result.RemappedAvatarUIDCount != 0 || result.RemappedCreatureUIDCount != 0 {
		t.Fatalf("本仓不应有任何重映射: %+v", result)
	}

	after := backupTestExport(t, db)
	backupTestRequireSameBackup(t, before, after)
	// 恢复必须彻底：备份之后新建的角色 99 及其物品都不能留下。
	if rows := db.table("dnf_characters").rows; len(rows) != 3 {
		t.Fatalf("dnf_characters 行数 = %d, want 3（两个本账号角色 + 别人的一个）", len(rows))
	}
	if rows := db.table("dnf_inventory_items").rows; len(rows) != 4 {
		t.Fatalf("dnf_inventory_items 行数 = %d, want 4", len(rows))
	}
	// 别人的数据一个字节都不能动。
	if foreignAfter := backupTestForeignRows(db); !reflect.DeepEqual(foreignBefore, foreignAfter) {
		t.Fatalf("恢复碰了别的账号的数据:\nbefore=%v\nafter=%v", foreignBefore, foreignAfter)
	}
	// legacy 子表：本账号父表的键（9001）原样回来，别人那条（9500）不许动。
	var kept, restored bool
	for _, row := range db.table("dnf_legacy_character_item_extra").rows {
		switch backupTestText(row[0]) {
		case "9001":
			restored = true
		case "9500":
			kept = true
		}
	}
	if !restored || !kept {
		t.Fatalf("legacy 子表 = %v", db.table("dnf_legacy_character_item_extra").rows)
	}
}

// backupTestComparable 去掉导出时间（每次都不同），用于"两次导出应当相同"的比较。
func backupTestComparable(file backupFile) backupFile {
	file.ExportedAt = ""
	return file
}

// backupTestRequireSameBackup 逐表比较两份备份，失败时只报**第一张不一样的表**
// （整份 JSON 太长，逐字节 diff 读不出结论）。
//
// 行序**不参与**比较：备份的 SQL 是 `SELECT * ... WHERE <归属谓词>`，没有 ORDER BY，
// 数据库给出的行序是自然序而不是契约（innoDB 通常按主键序返回，但 SQL 不保证）。
// 备份的语义是"这一组行"，所以这里按**行集合**比较（列名与列顺序仍然逐字比较）。
func backupTestRequireSameBackup(t *testing.T, before backupFile, after backupFile) {
	t.Helper()
	beforeComparable := backupTestComparable(before)
	afterComparable := backupTestComparable(after)
	if beforeComparable.Version != afterComparable.Version ||
		beforeComparable.AccountID != afterComparable.AccountID ||
		beforeComparable.AccountKey != afterComparable.AccountKey ||
		!reflect.DeepEqual(beforeComparable.CharacterIDs, afterComparable.CharacterIDs) {
		t.Fatalf("往返后备份头不一致: %+v vs %+v", beforeComparable, afterComparable)
	}
	beforeTables := backupTestCanonicalTables(t, before)
	afterTables := backupTestCanonicalTables(t, after)
	for name, table := range beforeTables {
		other, ok := afterTables[name]
		if !ok {
			t.Fatalf("往返后少了表 %s", name)
		}
		if !reflect.DeepEqual(table, other) {
			t.Fatalf("表 %s 往返不一致（列 %v/%v，行数 %d/%d）:\nbefore=%v\nafter=%v",
				name, table.columns, other.columns, len(table.rows), len(other.rows), table.rows, other.rows)
		}
		delete(afterTables, name)
	}
	if len(afterTables) != 0 {
		names := make([]string, 0, len(afterTables))
		for name := range afterTables {
			names = append(names, name)
		}
		sort.Strings(names)
		t.Fatalf("往返后多了表: %v", names)
	}
}

// backupTestCanonicalTable 是一张表的"行集合"形态：列逐字保留，行排序后比较。
type backupTestCanonicalTable struct {
	columns []string
	rows    []string
}

func backupTestCanonicalTables(t *testing.T, file backupFile) map[string]backupTestCanonicalTable {
	t.Helper()
	out := make(map[string]backupTestCanonicalTable, len(file.Tables))
	for _, dump := range file.Tables {
		rows := make([]string, 0, len(dump.Rows))
		for _, row := range dump.Rows {
			raw, err := json.Marshal(row)
			if err != nil {
				t.Fatalf("marshal 表 %s 的行: %v", dump.Name, err)
			}
			rows = append(rows, string(raw))
		}
		sort.Strings(rows)
		out[dump.Name] = backupTestCanonicalTable{columns: append([]string{}, dump.Columns...), rows: rows}
	}
	return out
}

func backupTestJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// TestBackupRestoreIsIdempotent：连续恢复两次结果一致（第二次不该产生额外行）。
func TestBackupRestoreIsIdempotent(t *testing.T) {
	db := backupTestFixtureDB()
	file := backupTestExport(t, db)
	sqlDB := backupTestOpenDB(t, db)

	first, err := backupRestoreAccount(context.Background(), sqlDB, backupTestWebAccount(), file)
	if err != nil {
		t.Fatalf("first restore: %v", err)
	}
	second, err := backupRestoreAccount(context.Background(), sqlDB, backupTestWebAccount(), file)
	if err != nil {
		t.Fatalf("second restore: %v", err)
	}
	// 第二次删除的行数会比第一次少（软删除的那个角色第一次就已经清掉了），
	// 但写入的行数必须一样 —— 恢复是幂等的。
	if first.RestoredRowCount != second.RestoredRowCount {
		t.Fatalf("两次恢复写入的行数不同: %d vs %d", first.RestoredRowCount, second.RestoredRowCount)
	}
	if first.DeletedRowCount < second.DeletedRowCount {
		t.Fatalf("第二次删除的行数不该更多: %d vs %d", first.DeletedRowCount, second.DeletedRowCount)
	}
	after := backupTestExport(t, db)
	backupTestRequireSameBackup(t, file, after)
}

// TestBackupRestoreRollsBackOnMidwayFailure：写入中途失败必须整事务回滚，
// 库里的旧数据一格都不能变（硬性要求 6）。
func TestBackupRestoreRollsBackOnMidwayFailure(t *testing.T) {
	db := backupTestFixtureDB()
	file := backupTestExport(t, db)
	snapshotBefore := db.snapshot()
	db.failInsertTable = "dnf_mail_attachments"

	result, err := backupRestoreAccount(context.Background(), backupTestOpenDB(t, db), backupTestWebAccount(), file)
	if err == nil {
		t.Fatal("注入的写入失败必须让恢复失败")
	}
	if !strings.Contains(err.Error(), "回滚") {
		t.Fatalf("错误里必须说明已回滚: %v", err)
	}
	if result.RestoredRowCount != 0 {
		t.Fatalf("失败时不应报告写入行数: %d", result.RestoredRowCount)
	}
	if after := db.snapshot(); !reflect.DeepEqual(snapshotBefore, after) {
		t.Fatal("恢复失败后数据被改动了（回滚没生效）")
	}
}

// ---------------------------------------------------------------------------
// 恢复的拒绝面（每一条都必须"具名失败 + 零写入"）
// ---------------------------------------------------------------------------

func backupTestAssertNoWrites(t *testing.T, db *backupTestDB, before map[string][][]any) {
	t.Helper()
	if after := db.snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatal("被拒绝的恢复改动了数据")
	}
	for _, write := range db.writes() {
		t.Fatalf("被拒绝的恢复执行了写语句: %v", write)
	}
}

func TestBackupRestoreRejectsUnsupportedVersion(t *testing.T) {
	db := backupTestFixtureDB()
	file := backupTestExport(t, db)
	file.Version = 3 // A21 的版本号
	before := db.snapshot()

	_, err := backupRestoreAccount(context.Background(), backupTestOpenDB(t, db), backupTestWebAccount(), file)
	if err == nil || !strings.Contains(err.Error(), "不支持的备份文件版本") {
		t.Fatalf("err = %v", err)
	}
	backupTestAssertNoWrites(t, db, before)
}

func TestBackupRestoreRejectsUnknownTable(t *testing.T) {
	db := backupTestFixtureDB()
	file := backupTestExport(t, db)
	// 伪造一张"本服务端认识之外"的表：启动器登录凭据表就是最危险的那一张。
	file.Tables = append(file.Tables, backupTableDump{
		Name:    "dnf90_web_accounts",
		Columns: []string{"id", "account_id"},
		Rows: [][]backupValue{{
			{Type: backupValueInteger, Integer: backupTestInt64(backupTestPanelID)},
			{Type: backupValueText, Text: backupTestString(backupTestAccountKey)},
		}},
	})
	before := db.snapshot()

	_, err := backupRestoreAccount(context.Background(), backupTestOpenDB(t, db), backupTestWebAccount(), file)
	if err == nil || !strings.Contains(err.Error(), "不认识的表") {
		t.Fatalf("err = %v", err)
	}
	backupTestAssertNoWrites(t, db, before)
}

func TestBackupRestoreRejectsOutOfScopeRow(t *testing.T) {
	db := backupTestFixtureDB()
	file := backupTestExport(t, db)
	for index, dump := range file.Tables {
		if dump.Name != "dnf_inventory_items" {
			continue
		}
		columnIndex, _ := dump.backupColumnIndex("character_id")
		dump.Rows[0][columnIndex] = backupValue{Type: backupValueText, Text: backupTestString(backupTestOtherChar)}
		file.Tables[index] = dump
	}
	before := db.snapshot()

	_, err := backupRestoreAccount(context.Background(), backupTestOpenDB(t, db), backupTestWebAccount(), file)
	if err == nil || !strings.Contains(err.Error(), "不在备份的角色列表内") {
		t.Fatalf("err = %v", err)
	}
	backupTestAssertNoWrites(t, db, before)
}

func TestBackupRestoreRejectsCharacterSetMismatch(t *testing.T) {
	db := backupTestFixtureDB()
	file := backupTestExport(t, db)
	file.CharacterIDs = append(file.CharacterIDs, "9999")
	before := db.snapshot()

	_, err := backupRestoreAccount(context.Background(), backupTestOpenDB(t, db), backupTestWebAccount(), file)
	if err == nil || !strings.Contains(err.Error(), "characterIDs 与 dnf_characters 表不一致") {
		t.Fatalf("err = %v", err)
	}
	backupTestAssertNoWrites(t, db, before)
}

func TestBackupRestoreRejectsMissingAccountsDump(t *testing.T) {
	db := backupTestFixtureDB()
	file := backupTestExport(t, db)
	kept := make([]backupTableDump, 0, len(file.Tables))
	for _, dump := range file.Tables {
		if dump.Name == "dnf_accounts" {
			continue
		}
		kept = append(kept, dump)
	}
	file.Tables = kept
	before := db.snapshot()

	_, err := backupRestoreAccount(context.Background(), backupTestOpenDB(t, db), backupTestWebAccount(), file)
	if err == nil || !strings.Contains(err.Error(), "缺少 dnf_accounts") {
		t.Fatalf("err = %v", err)
	}
	backupTestAssertNoWrites(t, db, before)
}

func TestBackupRestoreRejectsTargetSchemaMismatch(t *testing.T) {
	db := backupTestFixtureDB()
	file := backupTestExport(t, db)
	// 目标库少了一列：必须在任何写入之前具名失败。
	table := db.table("dnf_mails")
	table.columns = []string{"character_id", "mail_id", "title"}
	table.types = []string{"VARCHAR", "VARCHAR", "VARCHAR"}
	before := db.snapshot()

	_, err := backupRestoreAccount(context.Background(), backupTestOpenDB(t, db), backupTestWebAccount(), file)
	if err == nil || !strings.Contains(err.Error(), "无法恢复表 dnf_mails") {
		t.Fatalf("err = %v", err)
	}
	backupTestAssertNoWrites(t, db, before)
}

func TestBackupRestoreSkipsMissingLegacyMirrorTable(t *testing.T) {
	db := backupTestFixtureDB()
	file := backupTestExport(t, db)
	// 目标库已经不再有这张镜像表：跳过并如实回报，不让整次恢复失败。
	delete(db.tables, "dnf_legacy_character_userinfo90_control")

	result, err := backupRestoreAccount(context.Background(), backupTestOpenDB(t, db), backupTestWebAccount(), file)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !reflect.DeepEqual(result.SkippedTables, []string{"dnf_legacy_character_userinfo90_control"}) {
		t.Fatalf("skippedTables = %v", result.SkippedTables)
	}
}

// ---------------------------------------------------------------------------
// HTTP 层
// ---------------------------------------------------------------------------

func backupTestPost(t *testing.T, harness *gmtoolWriteHarness, target string, pathValues map[string]string, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	for key, value := range pathValues {
		request.SetPathValue(key, value)
	}
	recorder := httptest.NewRecorder()
	if strings.HasSuffix(target, "/backup") {
		harness.server.backupHandleAccountExport(recorder, request)
	} else {
		harness.server.backupHandleAccountRestore(recorder, request)
	}
	response := map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return recorder.Code, response
}

func backupTestInt64(value int64) *int64 { return &value }

func backupTestString(value string) *string { return &value }

// backupTestHTTPHarness 造一个"启动器账号表 + 两个角色"的 HTTP 测试台。
func backupTestHTTPHarness(t *testing.T, db *backupTestDB) *gmtoolWriteHarness {
	t.Helper()
	harness := backupTestHarness(t, db)
	ctx := context.Background()
	for _, characterID := range backupTestCharacterIDs() {
		record := dnfrepo.CharacterRecord{
			CharacterID: characterID,
			AccountID:   backupTestAccountKey,
			Name:        "char-" + characterID,
			Job:         "11",
			Level:       90,
			Stats:       map[string]int64{"delete_flag": 0},
		}
		if err := harness.group.Character.Save(ctx, record); err != nil {
			t.Fatalf("save character %s: %v", characterID, err)
		}
	}
	return harness
}

// TestBackupHTTPExportThenRestoreThroughThePanelContract 走一遍面板真实走的路：
// POST /api/accounts/{id}/backup 的响应体（含 success）原样 POST 回
// /api/accounts/restore（sidebar.js:250-311）。
func TestBackupHTTPExportThenRestoreThroughThePanelContract(t *testing.T) {
	db := backupTestFixtureDB()
	harness := backupTestHTTPHarness(t, db)

	status, exported := backupTestPost(t, harness, "/api/accounts/10001/backup", map[string]string{"id": "10001"}, nil)
	if status != http.StatusOK || exported["success"] != true {
		t.Fatalf("导出响应 = %d %v", status, exported)
	}
	if exported["version"] != float64(backupFormatVersion) {
		t.Fatalf("导出响应缺少 version: %v", exported["version"])
	}
	if exported["accountID"] != float64(backupTestPanelID) {
		t.Fatalf("导出响应 accountID = %v", exported["accountID"])
	}
	if tables, ok := exported["tables"].([]any); !ok || len(tables) == 0 {
		t.Fatalf("导出响应没有 tables: %v", exported["tables"])
	}
	if ids, ok := exported["characterIDs"].([]any); !ok || len(ids) != 2 {
		t.Fatalf("导出响应 characterIDs = %v", exported["characterIDs"])
	}

	// 面板会把响应（连同 success:true）原样存成文件、再原样回传。
	status, restored := backupTestPost(t, harness, "/api/accounts/restore", nil, exported)
	if status != http.StatusOK || restored["success"] != true {
		t.Fatalf("恢复响应 = %d %v", status, restored)
	}
	if restored["accountID"] != float64(backupTestPanelID) {
		t.Fatalf("恢复响应 accountID = %v", restored["accountID"])
	}
	if restored["restoredCharacterCount"] != float64(2) {
		t.Fatalf("restoredCharacterCount = %v", restored["restoredCharacterCount"])
	}
	// 被覆盖的旧角色数来自**数据库**口径（含 delete_flag != 0 的软删除角色），
	// 因此是 3（11/12/13），而不是内存仓储里那 2 个可见角色。
	if restored["deletedExistingCharacterCount"] != float64(3) {
		t.Fatalf("deletedExistingCharacterCount = %v", restored["deletedExistingCharacterCount"])
	}
	if _, ok := restored["remappedMailboxMessageIdCount"]; !ok {
		t.Fatalf("恢复响应缺少 remappedMailboxMessageIdCount: %v", restored)
	}
	if _, ok := restored["remappedMailboxAuditIdCount"]; !ok {
		t.Fatalf("恢复响应缺少 remappedMailboxAuditIdCount: %v", restored)
	}
}

// TestBackupRoutePatternsCoexistWithExistingRoutes 证明交给 gmtool.go 的那三行注册
// 与既有路由**不冲突**（Go 1.22 ServeMux 对冲突模式直接 panic），并且请求真的落到
// 本批的 handler 上（不是被别的模式吃掉）。
func TestBackupRoutePatternsCoexistWithExistingRoutes(t *testing.T) {
	mux := http.NewServeMux()
	noop := func(http.ResponseWriter, *http.Request) {}
	// 与 gmtool.go 里同前缀的既有模式（逐字取自 gmtool.go:262-268，只取 /api/accounts* 与
	// /api/characters* 直接相邻的那些，用于证明模式集合不冲突）。
	for _, pattern := range []string{
		"GET /api/accounts",
		"GET /api/accounts/{id}/detail",
		"POST /api/accounts/{id}/currency",
		"POST /api/accounts/{id}/honor-level",
		"POST /api/accounts/{id}/honor-level/max",
		"POST /api/accounts/{id}/growth-capsule",
		"POST /api/accounts/{id}/growth-capsule/max",
		"POST /api/accounts/create-for-clone",
		"GET /api/characters",
		"GET /api/characters/{id}",
		"POST /api/characters/{id}/items/delete",
	} {
		mux.HandleFunc(pattern, noop)
	}
	// 本批要粘进 gmtool.go 的三行。
	mux.HandleFunc("POST /api/accounts/{id}/backup", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("backup:" + r.PathValue("id")))
	})
	mux.HandleFunc("POST /api/accounts/restore", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("restore"))
	})
	mux.HandleFunc("GET /api/environment", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("environment"))
	})

	for _, probe := range []struct {
		method string
		target string
		want   string
	}{
		{http.MethodPost, "/api/accounts/10001/backup", "backup:10001"},
		{http.MethodPost, "/api/accounts/restore", "restore"},
		{http.MethodGet, "/api/environment", "environment"},
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(probe.method, probe.target, nil))
		if recorder.Code != http.StatusOK || recorder.Body.String() != probe.want {
			t.Fatalf("%s %s -> %d %q, want %q",
				probe.method, probe.target, recorder.Code, recorder.Body.String(), probe.want)
		}
	}
	// 三条新路由都必须带方法前缀：别的动词不能命中（保持"未移植"兜底可见）。
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/accounts/10001/backup", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/accounts/{id}/backup -> %d, want 405", recorder.Code)
	}
}

// TestBackupHandlersFailWithoutDatabase：s.db 为 nil（测试台默认）时必须明确失败，
// 绝不能 panic。
func TestBackupHandlersFailWithoutDatabase(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	for _, probe := range []struct {
		name   string
		target string
		values map[string]string
	}{
		{"导出", "/api/accounts/10001/backup", map[string]string{"id": "10001"}},
		{"恢复", "/api/accounts/restore", nil},
	} {
		status, response := backupTestPost(t, harness, probe.target, probe.values, map[string]any{})
		if status != http.StatusOK || response["success"] != false {
			t.Fatalf("%s: %d %v", probe.name, status, response)
		}
		if message, _ := response["error"].(string); !strings.Contains(message, "数据库连接未就绪") {
			t.Fatalf("%s: error = %v", probe.name, response["error"])
		}
	}
}

// TestBackupRestoreRejectsUnknownJSONField：拼错的键必须具名拒绝（否则会把
// 一整张表恢复成空表）。
func TestBackupRestoreRejectsUnknownJSONField(t *testing.T) {
	db := backupTestFixtureDB()
	harness := backupTestHTTPHarness(t, db)
	file := backupTestExport(t, db)
	raw, err := json.Marshal(file)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	payload := map[string]any{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	payload["characterIDsX"] = payload["characterIDs"]
	before := db.snapshot()

	status, response := backupTestPost(t, harness, "/api/accounts/restore", nil, payload)
	if status != http.StatusOK || response["success"] != false {
		t.Fatalf("响应 = %d %v", status, response)
	}
	if message, _ := response["error"].(string); !strings.Contains(message, "不认识的字段") {
		t.Fatalf("error = %v", response["error"])
	}
	backupTestAssertNoWrites(t, db, before)
}

// TestBackupRestoreRejectsCrossAccountBackup：A 账号的备份不能往 B 账号恢复。
func TestBackupRestoreRejectsCrossAccountBackup(t *testing.T) {
	db := backupTestFixtureDB()
	harness := backupTestHTTPHarness(t, db)
	file := backupTestExport(t, db)
	file.AccountKey = backupTestOtherAccount // 面板 #10001 现在的 account_id 是 dnf:10001
	before := db.snapshot()

	status, response := backupTestPost(t, harness, "/api/accounts/restore", nil, file)
	if status != http.StatusOK || response["success"] != false {
		t.Fatalf("响应 = %d %v", status, response)
	}
	if message, _ := response["error"].(string); !strings.Contains(message, "拒绝跨账号恢复") {
		t.Fatalf("error = %v", response["error"])
	}
	backupTestAssertNoWrites(t, db, before)
}

// TestBackupRestoreRejectsMissingTargetAccount：目标账号在启动器账号表里不存在时
// 具名拒绝（而不是凭空建一个账号）。
func TestBackupRestoreRejectsMissingTargetAccount(t *testing.T) {
	db := backupTestFixtureDB()
	harness := backupTestHTTPHarness(t, db)
	file := backupTestExport(t, db)
	file.AccountID = 424242
	before := db.snapshot()

	status, response := backupTestPost(t, harness, "/api/accounts/restore", nil, file)
	if status != http.StatusOK || response["success"] != false {
		t.Fatalf("响应 = %d %v", status, response)
	}
	if message, _ := response["error"].(string); !strings.Contains(message, "账号不存在") {
		t.Fatalf("error = %v", response["error"])
	}
	backupTestAssertNoWrites(t, db, before)
}

// TestBackupExportRejectsMissingAccount：导出也要先确认账号存在。
func TestBackupExportRejectsMissingAccount(t *testing.T) {
	db := backupTestFixtureDB()
	harness := backupTestHTTPHarness(t, db)

	status, response := backupTestPost(t, harness, "/api/accounts/999/backup", map[string]string{"id": "999"}, nil)
	if status != http.StatusOK || response["success"] != false {
		t.Fatalf("响应 = %d %v", status, response)
	}
	if message, _ := response["error"].(string); !strings.Contains(message, "账号不存在") {
		t.Fatalf("error = %v", response["error"])
	}
}

// TestBackupExportRejectsInvalidPathID：非法的 id 参数也要明确失败。
func TestBackupExportRejectsInvalidPathID(t *testing.T) {
	db := backupTestFixtureDB()
	harness := backupTestHTTPHarness(t, db)

	status, response := backupTestPost(t, harness, "/api/accounts/abc/backup", map[string]string{"id": "abc"}, nil)
	if status != http.StatusOK || response["success"] != false {
		t.Fatalf("响应 = %d %v", status, response)
	}
	if message, _ := response["error"].(string); !strings.Contains(message, "账号编号无效") {
		t.Fatalf("error = %v", response["error"])
	}
}

// TestBackupCoreTableSpecsArePinned：清单是本功能的核心数据，钉住关键表的
// 存在性、顺序（父表在子表之前）与"全局表/登录凭据表绝不入内"。
func TestBackupCoreTableSpecsArePinned(t *testing.T) {
	specs := backupCoreTableSpecs()
	order := make([]int, 0, len(specs))
	position := map[string]int{}
	for index, spec := range specs {
		if _, exists := position[spec.name]; exists {
			t.Fatalf("清单里有重复表: %s", spec.name)
		}
		if !strings.HasPrefix(spec.name, "dnf_") {
			t.Fatalf("表名必须带 dnf_ 前缀: %s", spec.name)
		}
		if spec.optional {
			t.Fatalf("核心表不能被标成可选: %s", spec.name)
		}
		position[spec.name] = index
		order = append(order, index)
	}
	for _, name := range []string{
		"dnf_accounts", "dnf_account_metadata", "dnf_characters", "dnf_character_stats",
		"dnf_inventory_items", "dnf_equipment_entries", "dnf_pet_entries", "dnf_quest_states",
		"dnf_skill_states", "dnf_skill_layouts", "dnf_dungeon_permissions", "dnf_mails",
		"dnf_mail_attachments", "dnf_account_inventory_items", "dnf_character_rosters",
		"dnf_character_locations", "dnf_pet_clear_tokens", "dnf_skill_cooldowns",
	} {
		if _, ok := position[name]; !ok {
			t.Fatalf("清单缺少 %s", name)
		}
	}
	for _, name := range []string{"dnf_packet_templates", "dnf_settings", "dnf_setting_values", "dnf90_web_accounts"} {
		if _, ok := position[name]; ok {
			t.Fatalf("全局表/登录凭据表不该进备份清单: %s", name)
		}
	}
	if position["dnf_characters"] > position["dnf_character_stats"] {
		t.Fatal("dnf_characters 必须排在角色子表之前")
	}
	if position["dnf_account_inventory_items"] > position["dnf_account_inventory_item_extra"] {
		t.Fatal("账号背包主表必须排在子表之前")
	}
	if position["dnf_mail_attachments"] > position["dnf_mail_attachment_extra"] {
		t.Fatal("邮件附件主表必须排在扩展子表之前")
	}
	for index, value := range order {
		if value != index {
			t.Fatalf("清单顺序不是按声明顺序: %v", order)
		}
	}
}

// TestBackupIdentifierQuoteRejectsInjection：表名/列名入 SQL 前必须过白名单。
func TestBackupIdentifierQuoteRejectsInjection(t *testing.T) {
	for _, value := range []string{"dnf_characters", "dnf_mail_attachment_extra", "_x1"} {
		if _, err := backupQuoteIdent(value); err != nil {
			t.Fatalf("合法标识符被拒: %q: %v", value, err)
		}
	}
	for _, value := range []string{"", " drop table x", "dnf_characters;--", "1abc", "a-b", "`x`"} {
		if _, err := backupQuoteIdent(value); err == nil {
			t.Fatalf("非法标识符被放行: %q", value)
		}
	}
}

// TestBackupValueConversionRoundTrip：每一个值类型都能无损往返。
func TestBackupValueConversionRoundTrip(t *testing.T) {
	cases := []any{int64(42), 1.5, "文本", []byte{0x00, 0xff, 0x10}, true, nil, backupTestBaseTime}
	for _, value := range cases {
		converted, err := backupValueFromDB(value, "LONGTEXT")
		if err != nil {
			t.Fatalf("FromDB(%T): %v", value, err)
		}
		restored, err := converted.toDBValue()
		if err != nil {
			t.Fatalf("toDBValue(%T): %v", value, err)
		}
		if backupTestText(restored) != backupTestText(value) {
			t.Fatalf("%T 往返 = %v, want %v", value, backupTestText(restored), backupTestText(value))
		}
	}
	// 二进制列必须走 base64，非法 UTF-8 一律按二进制处理。
	binaryValue, err := backupValueFromDB([]byte(backupTestBinaryText), "LONGBLOB")
	if err != nil {
		t.Fatalf("FromDB(blob): %v", err)
	}
	if binaryValue.Type != backupValueBlob {
		t.Fatalf("LONGBLOB 的类型 = %q", binaryValue.Type)
	}
	unknownType, err := backupValueFromDB([]byte(backupTestBinaryText), "")
	if err != nil {
		t.Fatalf("FromDB(unknown type): %v", err)
	}
	if unknownType.Type != backupValueBlob {
		t.Fatalf("非法 UTF-8 在未知列类型下也必须按二进制: %q", unknownType.Type)
	}
	// 不认识的 Go 类型必须明确失败，而不是被 fmt.Sprint 编进备份。
	if _, err := backupValueFromDB(struct{}{}, "VARCHAR"); err == nil {
		t.Fatal("未知值类型必须失败")
	}
	// 值类型损坏时必须失败。
	if _, err := (backupValue{Type: "integer"}).toDBValue(); err == nil {
		t.Fatal("缺 integer 字段必须失败")
	}
	if _, err := (backupValue{Type: "blob", Blob: backupTestString("!!!")}).toDBValue(); err == nil {
		t.Fatal("非法 base64 必须失败")
	}
	if _, err := (backupValue{Type: "wat"}).toDBValue(); err == nil {
		t.Fatal("未知类型必须失败")
	}
}

// TestBackupSameKeyIsNumericAware：权威表是 VARCHAR、legacy 镜像表是 BIGINT，
// 同一个键在两边读出来要能画上等号。
func TestBackupSameKeyIsNumericAware(t *testing.T) {
	if !backupSameKey("11", "11") || !backupSameKey(" 11 ", "11") || !backupSameKey("011", "11") {
		t.Fatal("数字键必须按数值比较")
	}
	if backupSameKey("11", "12") || backupSameKey("dnf:1", "dnf:2") || backupSameKey("", "1") {
		t.Fatal("不同键不能相等")
	}
}

// ---------------------------------------------------------------------------
// /api/environment
// ---------------------------------------------------------------------------

// envTestPVFSource 是一个带启动期快照的假 PVF 来源（生产档是 platformpvf.Archive）。
type envTestPVFSource struct {
	snapshot platformpvf.ArchiveSnapshot
}

func (s envTestPVFSource) ReadText(relativePath string) (string, error) {
	return "", &readonlyTestMissingPath{path: relativePath}
}

func (s envTestPVFSource) Snapshot() platformpvf.ArchiveSnapshot { return s.snapshot }

func envTestGet(t *testing.T, harness *gmtoolWriteHarness) map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/environment", nil)
	recorder := httptest.NewRecorder()
	harness.server.envHandleInfo(recorder, request)
	response := map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return response
}

// TestEnvironmentReportIsHonestAndSecretFree：报告里只许出现能观测到的值，
// 且**绝不能**回显口令/用户名/DSN。
func TestEnvironmentReportIsHonestAndSecretFree(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	const password = "aa123456"
	const user = "root"
	harness.server.cfg.DSN = user + ":" + password + "@tcp(127.0.0.1:13306)/dnf_local?parseTime=true&charset=utf8mb4,utf8"
	harness.server.cfg.DatabaseLabel = "MariaDB dnf_local"
	harness.server.cfg.Table = "dnf90_web_accounts"
	harness.server.cfg.PVFPath = "data/dnf/Script.pvf"
	harness.server.cfg.Listen = "127.0.0.1:18111"
	source := envTestPVFSource{snapshot: platformpvf.ArchiveSnapshot{
		Format:    platformpvf.FormatProtectedNKPI,
		Path:      "data/dnf/Script.pvf",
		Size:      939675,
		Checksum:  "deadbeef",
		FileCount: 54907,
		LoadedAt:  "2026-09-27T12:00:00Z",
	}}
	harness.server.deps.PVF = func() (dnfpvf.Source, error) { return source, nil }

	report := envTestGet(t, harness)
	raw := backupTestJSON(t, report)
	for _, secret := range []string{password, user + ":", "tcp(127.0.0.1:13306)", "charset=utf8mb4"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("报告里回显了机密 %q：%s", secret, raw)
		}
	}
	if report["success"] != true {
		t.Fatalf("success = %v", report["success"])
	}
	if report["reportVersion"] != float64(envReportVersion) {
		t.Fatalf("reportVersion = %v", report["reportVersion"])
	}
	database, ok := report["database"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 database 块: %v", report)
	}
	if database["name"] != "dnf_local" || database["host"] != "127.0.0.1:13306" || database["configured"] != true {
		t.Fatalf("database = %v", database)
	}
	if database["reachable"] != nil {
		t.Fatalf("没有连接池时 reachable 必须是 null: %v", database["reachable"])
	}
	pvf, ok := report["pvf"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 pvf 块: %v", report)
	}
	if pvf["configuredPath"] != "data/dnf/Script.pvf" || pvf["loaded"] != true {
		t.Fatalf("pvf = %v", pvf)
	}
	if pvf["checksumSha256"] != "deadbeef" || pvf["fileCount"] != float64(54907) ||
		pvf["format"] != string(platformpvf.FormatProtectedNKPI) {
		t.Fatalf("PVF 快照字段 = %v", pvf)
	}
	panel, ok := report["panel"].(map[string]any)
	if !ok || panel["toolVersion"] != ToolVersion || panel["accountsTable"] != "dnf90_web_accounts" {
		t.Fatalf("panel = %v", report["panel"])
	}
	if panel["listen"] != "127.0.0.1:18111" {
		t.Fatalf("panel.listen = %v", panel["listen"])
	}
	build, ok := report["build"].(map[string]any)
	if !ok || build["available"] != true {
		t.Fatalf("build = %v", report["build"])
	}
	if _, ok := report["switches"].([]any); !ok {
		t.Fatalf("缺少 switches: %v", report["switches"])
	}
	backupReport, ok := report["backup"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 backup 块: %v", report["backup"])
	}
	if backupReport["formatVersion"] != float64(backupFormatVersion) {
		t.Fatalf("backup.formatVersion = %v", backupReport["formatVersion"])
	}
	if _, ok := report["limitations"].([]any); !ok {
		t.Fatalf("缺少 limitations: %v", report["limitations"])
	}
}

// TestEnvironmentReportWithoutDatabaseOrPVF：取不到的值必须如实为空/报错，不 panic。
func TestEnvironmentReportWithoutDatabaseOrPVF(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	harness.server.cfg.DSN = "root:pw@tcp(127.0.0.1:13306)/dnf_local"
	harness.server.deps.PVF = nil

	report := envTestGet(t, harness)
	database, ok := report["database"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 database 块: %v", report)
	}
	if database["reachable"] != nil {
		t.Fatalf("没有连接池时 reachable 必须是 null: %v", database["reachable"])
	}
	pvf, ok := report["pvf"].(map[string]any)
	if !ok || pvf["loaded"] != false {
		t.Fatalf("pvf = %v", report["pvf"])
	}
	if message, _ := pvf["error"].(string); !strings.Contains(message, "未注入") {
		t.Fatalf("pvf.error = %v", pvf["error"])
	}
	if pvf["checksumSha256"] != nil {
		t.Fatalf("没有快照时不允许编造哈希: %v", pvf["checksumSha256"])
	}
	// DSN 解析不了时也不许把原文带出去。
	harness.server.cfg.DSN = "this-is-not-a-dsn"
	report = envTestGet(t, harness)
	database, _ = report["database"].(map[string]any)
	if message, _ := database["error"].(string); !strings.Contains(message, "无法解析") {
		t.Fatalf("database.error = %v", database["error"])
	}
	if strings.Contains(backupTestJSON(t, report), "this-is-not-a-dsn") {
		t.Fatal("DSN 原文被回显了")
	}
}

// TestEnvironmentSwitchesUseTheirOwnParsers：报告里的 enabled 必须与各开关自己的
// 解析函数完全一致（空 = 各开关的默认值）。
func TestEnvironmentSwitchesUseTheirOwnParsers(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	switches := envTestSwitchMap(t, envTestGet(t, harness))
	for _, name := range []string{
		requireOfflineEnv, itemRemoveEnv, gmMailOptionsEnv, gmAccountSearchEnv,
		questMutateEnv, questProfessionWriteEnv, questChainResetEnv, equipSlotQuestSyncEnv,
	} {
		if _, ok := switches[name]; !ok {
			t.Fatalf("开关清单缺少 %s（现有：%v）", name, switches)
		}
	}
	if switches[requireOfflineEnv]["enabled"] != false || switches[requireOfflineEnv]["defaultWhenUnset"] != "off" {
		t.Fatalf("默认档 require_offline = %v", switches[requireOfflineEnv])
	}
	if switches[questMutateEnv]["enabled"] != true || switches[questMutateEnv]["defaultWhenUnset"] != "on" {
		t.Fatalf("默认档 quest_mutate = %v", switches[questMutateEnv])
	}

	t.Setenv(requireOfflineEnv, "on")
	t.Setenv(questMutateEnv, "off")
	t.Setenv(questChainResetEnv, "ON")
	t.Setenv(itemRemoveEnv, "true") // 只认字面 on：这里必须仍然是 off
	switches = envTestSwitchMap(t, envTestGet(t, harness))
	if switches[requireOfflineEnv]["enabled"] != true || switches[requireOfflineEnv]["value"] != "on" {
		t.Fatalf("require_offline 未随环境变化: %v", switches[requireOfflineEnv])
	}
	// 宿主启动时注入的选项还是旧值 → 报告必须把这种不一致显示出来。
	if switches[requireOfflineEnv]["wiredOption"] != false {
		t.Fatalf("wiredOption 应显示启动时注入的值: %v", switches[requireOfflineEnv])
	}
	if switches[questMutateEnv]["enabled"] != false {
		t.Fatalf("quest_mutate off 未生效: %v", switches[questMutateEnv])
	}
	if switches[questChainResetEnv]["enabled"] != true {
		t.Fatalf("quest_chain_reset on 未生效: %v", switches[questChainResetEnv])
	}
	if switches[itemRemoveEnv]["enabled"] != false {
		t.Fatalf("item_remove 只认字面 on: %v", switches[itemRemoveEnv])
	}
}

func envTestSwitchMap(t *testing.T, report map[string]any) map[string]map[string]any {
	t.Helper()
	list, ok := report["switches"].([]any)
	if !ok {
		t.Fatalf("switches = %v", report["switches"])
	}
	out := map[string]map[string]any{}
	for _, entry := range list {
		item, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("switch = %v", entry)
		}
		name, _ := item["name"].(string)
		if name == "" {
			t.Fatalf("switch 缺少 name: %v", item)
		}
		out[name] = item
	}
	return out
}
