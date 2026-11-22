package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

type guildSettingsTestTx struct{}

func (*guildSettingsTestTx) Commit() error   { return nil }
func (*guildSettingsTestTx) Rollback() error { return nil }

type p0ArchiveSplitDBState struct {
	mu      sync.Mutex
	queries []string
	execs   []string
	primary map[string]map[string]string // writeDB 中的 scope -> (entry_key -> entry_value)
	replica map[string]map[string]string // readDB 中的 scope -> (entry_key -> entry_value)
}

type p0ArchiveSplitDBDriver struct{ state *p0ArchiveSplitDBState }

func (d *p0ArchiveSplitDBDriver) Open(string) (driver.Conn, error) {
	return &p0ArchiveSplitDBConn{state: d.state}, nil
}

type p0ArchiveSplitDBConn struct{ state *p0ArchiveSplitDBState }

func (c *p0ArchiveSplitDBConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c *p0ArchiveSplitDBConn) Close() error                        { return nil }
func (c *p0ArchiveSplitDBConn) Begin() (driver.Tx, error)           { return &guildSettingsTestTx{}, nil }
func (c *p0ArchiveSplitDBConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &guildSettingsTestTx{}, nil
}

func (c *p0ArchiveSplitDBConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.execs = append(c.state.execs, query)
	if strings.Contains(query, "`dnf_write_primary`.`dnf_settings`") && len(args) >= 1 {
		scope, _ := args[0].Value.(string)
		if _, ok := c.state.primary[scope]; !ok {
			c.state.primary[scope] = map[string]string{}
		}
	} else if strings.HasPrefix(query, "DELETE FROM `dnf_write_primary`.`dnf_setting_values`") && len(args) >= 1 {
		scope, _ := args[0].Value.(string)
		c.state.primary[scope] = map[string]string{}
	} else if strings.HasPrefix(query, "INSERT INTO `dnf_write_primary`.`dnf_setting_values`") && len(args) == 3 {
		scope, _ := args[0].Value.(string)
		k, _ := args[1].Value.(string)
		v, _ := args[2].Value.(string)
		if c.state.primary[scope] == nil {
			c.state.primary[scope] = map[string]string{}
		}
		c.state.primary[scope][k] = v
	}
	return driver.RowsAffected(1), nil
}

func (c *p0ArchiveSplitDBConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.queries = append(c.state.queries, query)

	store := c.state.replica
	if strings.Contains(query, "`dnf_write_primary`") {
		store = c.state.primary
	}
	scope := ""
	if len(args) > 0 {
		scope, _ = args[0].Value.(string)
	}
	vals, exists := store[scope]
	switch {
	case strings.Contains(query, ".`dnf_settings`"):
		if !exists {
			return &pairSQLRows{columns: []string{"scope", "updated_at"}}, nil
		}
		return &pairSQLRows{
			columns: []string{"scope", "updated_at"},
			rows:    [][]driver.Value{{scope, time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)}},
		}, nil
	case strings.Contains(query, ".`dnf_setting_values`"):
		rows := make([][]driver.Value, 0, len(vals))
		for k, v := range vals {
			rows = append(rows, []driver.Value{k, v})
		}
		return &pairSQLRows{columns: []string{"entry_key", "entry_value"}, rows: rows}, nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
}

// TestP0MySQLWithinGuildArchiveRoutesReadsToPrimaryForUpdate 验证：
// 即使读库（dnf_read_replica）因复制延迟看不到已有归档，WithinGuildArchive 仍把全部读取
// 定向到写库（dnf_write_primary）并带 FOR UPDATE，且先锁主注册表，能准确发现写库中已存在的
// receipt / hongbao 归档记录而不触发覆盖，同时拒绝越权 scope。
func TestP0MySQLWithinGuildArchiveRoutesReadsToPrimaryForUpdate(t *testing.T) {
	reqID := fmt.Sprintf("%064x", 0x77)
	receiptScope := repository.GuildReceiptArchiveScope(reqID)
	hongbaoScope := repository.GuildHongbaoArchiveScope(42)

	receiptConflictPayload, _ := json.Marshal(map[string]any{"version": 1, "kind": "teleport", "request_id": reqID})
	hongbaoConflictPayload, _ := json.Marshal(map[string]any{"version": 1, "record": map[string]any{"ID": 42}})

	state := &p0ArchiveSplitDBState{
		primary: map[string]map[string]string{
			repository.GuildRegistryScope: {"state": `{"version":1,"next_id":1}`},
			receiptScope:                  {"receipt": string(receiptConflictPayload)},
			hongbaoScope:                  {"hongbao": string(hongbaoConflictPayload)},
		},
		// 读库刻意留空，模拟主从复制延迟导致读路由返回不存在。
		replica: map[string]map[string]string{},
	}
	driverName := fmt.Sprintf("p0_archive_split_db_%d", time.Now().UnixNano())
	sql.Register(driverName, &p0ArchiveSplitDBDriver{state: state})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	router := mysqlRouter{
		db:          SQLDBAdapter{DB: db},
		readDBs:     []string{"dnf_read_replica"},
		writeDBs:    []string{"dnf_write_primary"},
		tablePrefix: "dnf",
		now:         func() time.Time { return time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC) },
	}
	uow := &mysqlGuildUnitOfWork{router: router}

	conflictErr := errors.New("detected primary archive conflict")
	err = uow.WithinGuildArchive(context.Background(), func(txSettings repository.SettingsRepository) error {
		if _, _, unauthErr := txSettings.Load(context.Background(), "unauthorized:scope"); !errors.Is(unauthErr, repository.ErrGuildTransaction) {
			t.Fatalf("expected ErrGuildTransaction for unauthorized scope, got %v", unauthErr)
		}
		gotReceipt, foundReceipt, loadErr := txSettings.Load(context.Background(), receiptScope)
		if loadErr != nil || !foundReceipt || gotReceipt.Values["receipt"] != string(receiptConflictPayload) {
			t.Fatalf("expected primary receipt archive visible under lag, found=%v got=%+v err=%v", foundReceipt, gotReceipt, loadErr)
		}
		gotHongbao, foundHongbao, loadErr := txSettings.Load(context.Background(), hongbaoScope)
		if loadErr != nil || !foundHongbao || gotHongbao.Values["hongbao"] != string(hongbaoConflictPayload) {
			t.Fatalf("expected primary hongbao archive visible under lag, found=%v got=%+v err=%v", foundHongbao, gotHongbao, loadErr)
		}
		return conflictErr
	})
	if !errors.Is(err, conflictErr) {
		t.Fatalf("expected conflictErr rollback, got %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.execs) != 1 || !strings.Contains(state.execs[0], "INSERT INTO `dnf_write_primary`.`dnf_settings`") {
		t.Fatalf("expected only registry lock INSERT and zero archive overwrites, execs=%v", state.execs)
	}
	if len(state.queries) == 0 {
		t.Fatal("expected primary SELECT FOR UPDATE queries")
	}
	for _, q := range state.queries {
		if strings.Contains(q, "`dnf_read_replica`") {
			t.Fatalf("WithinGuildArchive unexpectedly hit read replica: %s", q)
		}
		if !strings.Contains(q, "`dnf_write_primary`") || !strings.Contains(q, "FOR UPDATE") {
			t.Fatalf("WithinGuildArchive query must target primary write DB with FOR UPDATE: %s", q)
		}
	}
}
