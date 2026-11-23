package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

type benchmarkRowEntry struct {
	needle string
	row    fakeSQLRow
}

type benchmarkRowsEntry struct {
	needle string
	rows   []fakeSQLRow
}

type benchmarkSQLDB struct {
	rowEntries  []benchmarkRowEntry
	rowsEntries []benchmarkRowsEntry
}

func (d *benchmarkSQLDB) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errors.New("benchmark repository is read-only")
}

func (d *benchmarkSQLDB) QueryContext(_ context.Context, query string, _ ...any) (SQLRows, error) {
	for _, entry := range d.rowsEntries {
		if strings.Contains(query, entry.needle) {
			return &fakeSQLRows{rows: entry.rows}, nil
		}
	}
	return &fakeSQLRows{}, nil
}

func (d *benchmarkSQLDB) QueryRowContext(_ context.Context, query string, _ ...any) SQLRow {
	for _, entry := range d.rowEntries {
		if strings.Contains(query, entry.needle) {
			return entry.row
		}
	}
	return fakeSQLRow{err: sql.ErrNoRows}
}

func (d *benchmarkSQLDB) PingContext(context.Context) error { return nil }

var (
	benchmarkCharacterSink repository.CharacterRecord
	benchmarkInventorySink repository.InventoryRecord
	benchmarkEquipmentSink repository.EquipmentRecord
	benchmarkAccountSink   repository.AccountRecord
	benchmarkSettingsSink  repository.SettingsRecord
	benchmarkRevisionSink  string
)

func benchmarkMySQLGroup(b *testing.B, database SQLDB) repository.Group {
	b.Helper()
	group, err := NewMySQLGroup(database, MySQLGroupOptions{
		DatabasePlan: repository.DatabasePlan{
			WriteDatabases: []string{"dnf_bench"},
			ReadDatabases:  []string{"dnf_bench"},
		},
		TablePrefix: "dnf",
	})
	if err != nil {
		b.Fatal(err)
	}
	return group
}

func BenchmarkMySQLRepositoryCharacterLoad(b *testing.B) {
	group := benchmarkMySQLGroup(b, benchmarkCharacterDatabase())
	store := group.Character.(*mysqlCharStore)
	// The locked legacy path exercises the static base-query template and the
	// same record/snapshot ownership rules without requiring a benchmark SQL
	// driver that implements BeginTx. The joined-query template is covered by
	// the package unit and integration tests.
	store.router.lockReads = true
	ctx := context.Background()
	for _, tracked := range []bool{false, true} {
		b.Run("tracked="+boolBenchmarkName(tracked), func(b *testing.B) {
			store.trackStatChanges = tracked
			store.loadedSnapshot = nil
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				record, found, err := store.Load(ctx, "char-bench")
				if err != nil || !found {
					b.Fatalf("Load() found=%t err=%v", found, err)
				}
				benchmarkCharacterSink = record
			}
		})
	}
}

func BenchmarkMySQLRepositoryInventoryLoad(b *testing.B) {
	group := benchmarkMySQLGroup(b, benchmarkInventoryDatabase())
	store := group.Inventory.(*mysqlBagStore)
	ctx := context.Background()
	for _, tracked := range []bool{false, true} {
		b.Run("tracked="+boolBenchmarkName(tracked), func(b *testing.B) {
			store.trackSlotChanges = tracked
			store.loadedSnapshot = nil
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				record, found, err := store.Load(ctx, "char-bench")
				if err != nil || !found {
					b.Fatalf("Load() found=%t err=%v", found, err)
				}
				benchmarkInventorySink = record
			}
		})
	}
}

func BenchmarkMySQLRepositoryEquipmentLoad(b *testing.B) {
	group := benchmarkMySQLGroup(b, benchmarkEquipmentDatabase())
	store := group.Equipment.(*mysqlEquipmentStore)
	ctx := context.Background()
	for _, tracked := range []bool{false, true} {
		b.Run("tracked="+boolBenchmarkName(tracked), func(b *testing.B) {
			store.trackEntryChanges = tracked
			store.loadedSnapshot = nil
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				record, found, err := store.Load(ctx, "char-bench")
				if err != nil || !found {
					b.Fatalf("Load() found=%t err=%v", found, err)
				}
				benchmarkEquipmentSink = record
			}
		})
	}
}

func BenchmarkMySQLRepositoryAccountLoad(b *testing.B) {
	group := benchmarkMySQLGroup(b, benchmarkAccountDatabase())
	store := group.Account.(*mysqlAccountStore)
	ctx := context.Background()
	for _, tracked := range []bool{false, true} {
		b.Run("tracked="+boolBenchmarkName(tracked), func(b *testing.B) {
			store.trackMetadataChanges = tracked
			store.loadedSnapshot = nil
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				record, found, err := store.Load(ctx, "acc-bench")
				if err != nil || !found {
					b.Fatalf("Load() found=%t err=%v", found, err)
				}
				benchmarkAccountSink = record
			}
		})
	}
}

func BenchmarkMySQLRepositorySettingsLoad(b *testing.B) {
	group := benchmarkMySQLGroup(b, benchmarkSettingsDatabase())
	store := group.Settings.(*mysqlSettingsStore)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		record, found, err := store.Load(ctx, "scope-bench")
		if err != nil || !found {
			b.Fatalf("Load() found=%t err=%v", found, err)
		}
		benchmarkSettingsSink = record
	}
}

func BenchmarkMySQLGuildSnapshotRevisionFastPath(b *testing.B) {
	state := &benchGuildSQLState{}
	database := sql.OpenDB(benchGuildConnector{state: state})
	b.Cleanup(func() { _ = database.Close() })
	store := &mysqlSettingsStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
		db:                     SQLDBAdapter{DB: database},
		readDBs:                []string{"dnf_bench"},
		writeDBs:               []string{"dnf_bench"},
		tablePrefix:            "dnf",
		storedGuildFingerprint: true,
	}}}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		revision, found, err := store.LoadGuildSnapshotRevision(ctx)
		if err != nil || !found || revision != "digest" {
			b.Fatalf("revision=%q found=%t err=%v", revision, found, err)
		}
		benchmarkRevisionSink = revision
	}
}

func boolBenchmarkName(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func benchmarkInventoryDatabase() *benchmarkSQLDB {
	items := make([]fakeSQLRow, 0, 12)
	extras := make([]fakeSQLRow, 0, 24)
	for i := 0; i < 12; i++ {
		key := "slot-" + string(rune('a'+i))
		items = append(items, fakeSQLRow{values: []any{key, int64(1000 + i), int64(1 + i), i%2 == 0, nil, []byte{byte(i), 1}}})
		extras = append(extras,
			fakeSQLRow{values: []any{key, "kind", "stackable"}},
			fakeSQLRow{values: []any{key, "pvf", "item/path"}},
		)
	}
	return &benchmarkSQLDB{
		rowEntries: []benchmarkRowEntry{{
			needle: "`dnf_inventories`",
			row:    fakeSQLRow{values: []any{"char-bench", nil}},
		}},
		rowsEntries: []benchmarkRowsEntry{
			{needle: "`dnf_inventory_items`", rows: items},
			{needle: "`dnf_inventory_item_extra`", rows: extras},
		},
	}
}

func benchmarkEquipmentDatabase() *benchmarkSQLDB {
	entries := make([]fakeSQLRow, 0, 12)
	extras := make([]fakeSQLRow, 0, 24)
	for i := 0; i < 12; i++ {
		key := "entry-" + string(rune('a'+i))
		entries = append(entries, fakeSQLRow{values: []any{key, int16(i), int64(2000 + i), i%2 == 0, nil, []byte{byte(i), 2}}})
		extras = append(extras,
			fakeSQLRow{values: []any{key, "marker", "value"}},
			fakeSQLRow{values: []any{key, "source", "bench"}},
		)
	}
	return &benchmarkSQLDB{
		rowEntries: []benchmarkRowEntry{{
			needle: "`dnf_equipments`",
			row:    fakeSQLRow{values: []any{"char-bench", nil}},
		}},
		rowsEntries: []benchmarkRowsEntry{
			{needle: "`dnf_equipment_entries`", rows: entries},
			{needle: "`dnf_equipment_entry_extra`", rows: extras},
		},
	}
}

func benchmarkAccountDatabase() *benchmarkSQLDB {
	metadata := make([]fakeSQLRow, 0, 32)
	for i := 0; i < 32; i++ {
		metadata = append(metadata, fakeSQLRow{values: []any{"key-" + string(rune('a'+i%26)) + string(rune('0'+i/26)), "value"}})
	}
	return &benchmarkSQLDB{
		rowEntries: []benchmarkRowEntry{{
			needle: "`dnf_accounts`",
			row:    fakeSQLRow{values: []any{"acc-bench", "active", int64(0), nil, nil, nil}},
		}},
		rowsEntries: []benchmarkRowsEntry{{
			needle: "`dnf_account_metadata`",
			rows:   metadata,
		}},
	}
}

func benchmarkSettingsDatabase() *benchmarkSQLDB {
	values := make([]fakeSQLRow, 0, 16)
	for i := 0; i < 16; i++ {
		values = append(values, fakeSQLRow{values: []any{"key-" + string(rune('a'+i)), "value"}})
	}
	return &benchmarkSQLDB{
		rowEntries: []benchmarkRowEntry{{
			needle: "`dnf_settings`",
			row:    fakeSQLRow{values: []any{"scope-bench", nil}},
		}},
		rowsEntries: []benchmarkRowsEntry{{
			needle: "`dnf_setting_values`",
			rows:   values,
		}},
	}
}

func benchmarkCharacterDatabase() *benchmarkSQLDB {
	values := []any{"char-bench", "acc-bench", 1, "name", "1", 90}
	for _, column := range mysqlCharacterStatColumns {
		values = append(values, column.fallback)
	}
	values = append(values, nil, nil)

	stats := make([]fakeSQLRow, 0, 16)
	for i := 0; i < 16; i++ {
		stats = append(stats, fakeSQLRow{values: []any{"extra_stat_" + string(rune('a'+i)), int64(i + 1)}})
	}
	equipment := make([]fakeSQLRow, 0, 8)
	for i := 0; i < 8; i++ {
		equipment = append(equipment, fakeSQLRow{values: []any{i, 3000 + i, []byte{byte(i)}, 1, 0, 0, 0}})
	}
	lists := []fakeSQLRow{
		{values: []any{"flags", int64(1)}},
		{values: []any{"flags", int64(2)}},
		{values: []any{"linked_ids", int64(3)}},
		{values: []any{"linked_ids", int64(4)}},
	}
	return &benchmarkSQLDB{
		rowEntries: []benchmarkRowEntry{
			{needle: "`dnf_characters`", row: fakeSQLRow{values: values}},
			{needle: "`dnf_character_locations`", row: fakeSQLRow{values: []any{1, 2, 3, "room"}}},
			{needle: "`dnf_character_rosters`", row: fakeSQLRow{values: characterRosterArgs("char-bench", repository.CharacterRoster{})[1:]}},
		},
		rowsEntries: []benchmarkRowsEntry{
			{needle: "`dnf_character_stats`", rows: stats},
			{needle: "`dnf_character_roster_equipment`", rows: equipment},
			{needle: "`dnf_character_roster_lists`", rows: lists},
		},
	}
}

type benchGuildSQLState struct {
	mu      sync.Mutex
	begins  int
	commits int
	openTx  bool
}

type benchGuildConnector struct{ state *benchGuildSQLState }

func (c benchGuildConnector) Connect(context.Context) (driver.Conn, error) {
	return benchGuildConn{state: c.state}, nil
}

func (c benchGuildConnector) Driver() driver.Driver {
	return benchGuildDriver{state: c.state}
}

type benchGuildDriver struct{ state *benchGuildSQLState }

func (d benchGuildDriver) Open(string) (driver.Conn, error) {
	return benchGuildConn{state: d.state}, nil
}

type benchGuildConn struct{ state *benchGuildSQLState }

func (c benchGuildConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected benchmark prepare")
}

func (c benchGuildConn) Close() error { return nil }

func (c benchGuildConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c benchGuildConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.begins++
	c.state.openTx = true
	return benchGuildTx{state: c.state}, nil
}

func (c benchGuildConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &pairSQLRows{columns: []string{"exists", "fingerprint"}, rows: [][]driver.Value{{true, "digest"}}}, nil
}

func (c benchGuildConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("benchmark guild read attempted a write")
}

type benchGuildTx struct{ state *benchGuildSQLState }

func (tx benchGuildTx) Commit() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.openTx = false
	tx.state.commits++
	return nil
}

func (tx benchGuildTx) Rollback() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.openTx = false
	return nil
}
