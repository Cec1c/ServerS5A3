package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

// Local database/sql protocol fixture only, no external MySQL or SQL config.
// The driver reuses the existing owner-column fixture but makes overlapping
// rows, missing locks, wrong write route and unexpected writes fail explicitly.
type reviewBuffSQLState struct {
	base                 semesterSQLState
	openRows, closedRows int
	isolation            driver.IsolationLevel
	scenario             string
}
type reviewBuffSQLConnector struct{ s *reviewBuffSQLState }

func (c reviewBuffSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return reviewBuffSQLConn{c.s}, nil
}
func (c reviewBuffSQLConnector) Driver() driver.Driver { return reviewBuffSQLDriver{c.s} }

type reviewBuffSQLDriver struct{ s *reviewBuffSQLState }

func (d reviewBuffSQLDriver) Open(string) (driver.Conn, error) { return reviewBuffSQLConn{d.s}, nil }

type reviewBuffSQLConn struct{ s *reviewBuffSQLState }

func (c reviewBuffSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c reviewBuffSQLConn) Close() error { return nil }
func (c reviewBuffSQLConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c reviewBuffSQLConn) BeginTx(_ context.Context, o driver.TxOptions) (driver.Tx, error) {
	c.s.isolation = o.Isolation
	c.s.base.begins++
	return semesterSQLTx{&c.s.base}, nil
}
func (c reviewBuffSQLConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.s.base.writes++
	return nil, errors.New("read-only fixture forbids writes")
}
func (c reviewBuffSQLConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if c.s.openRows != 0 {
		return nil, errors.New("next query before previous rows closed")
	}
	// Only exact qualified fixture tables may supply rows. A direct router's
	// prefix is normalized "dnf"; mysqlTable itself adds the separator.
	knownTable := false
	for _, suffix := range []string{mysqlCharTable, mysqlCharacterStatsTable, mysqlCharacterLocationTable, mysqlCharacterRosterTable, mysqlCharacterRosterEquipTable, mysqlCharacterRosterListTable, mysqlSettingsTable, mysqlSettingValuesTable, mysqlBagTable, mysqlInventoryItemsTable, mysqlInventoryExtraTable, mysqlEquipmentTable, mysqlEquipmentEntriesTable, mysqlEquipmentExtraTable, mysqlPetTable, mysqlPetEntriesTable, mysqlPetExtraTable, "legacy_character_items", mysqlLegacyItemExtraTable} {
		if reviewBuffSQLFromTable(q, suffix) {
			knownTable = true
		}
	}
	if !knownTable {
		return nil, fmt.Errorf("unmatched exact fixture table: %s", q)
	}
	rows, err := (semesterSQLConn{&c.s.base}).QueryContext(ctx, q, args)
	if err != nil {
		return nil, err
	}
	pair := rows.(*pairSQLRows)
	if (reviewBuffSQLFromTable(q, mysqlBagTable) || reviewBuffSQLFromTable(q, mysqlEquipmentTable)) && strings.HasPrefix(q, "SELECT character_id, updated_at FROM ") {
		pair.rows = [][]driver.Value{{"4", nil}}
	}
	if reviewBuffSQLFromTable(q, mysqlPetTable) && strings.HasPrefix(q, "SELECT character_id, equipped_key, town_display, updated_at") {
		pair.rows = [][]driver.Value{{"4", "", false, nil}}
	}
	if reviewBuffSQLFromTable(q, mysqlInventoryItemsTable) && strings.HasPrefix(q, "SELECT entry_key, item_id, item_count, bind_flag, expire_at, raw_entry") {
		if !strings.Contains(q, "ORDER BY entry_key FOR UPDATE") {
			return nil, errors.New("current item key lock order missing")
		}
		pair.rows = [][]driver.Value{{"0:54", int64(123), int64(1), false, nil, []byte{1, 2}}}
	}
	if reviewBuffSQLFromTable(q, mysqlInventoryExtraTable) && strings.HasPrefix(q, "SELECT entry_key, extra_key, extra_value") {
		if !strings.Contains(q, "ORDER BY entry_key, extra_key FOR UPDATE") {
			return nil, errors.New("current extra key lock order missing")
		}
		pair.rows = [][]driver.Value{{"0:54", "quality", "9"}}
	}
	if reviewBuffSQLFromTable(q, "legacy_character_items") && strings.HasPrefix(q, "SELECT item_uid, owner_scope,") {
		if len(args) != 2 || fmt.Sprint(args[0].Value) != "4" || !strings.Contains(q, "WHERE owner_scope = 'character' AND owner_id = ? AND list_type = ? ORDER BY slot_index, item_uid FOR UPDATE") {
			return nil, errors.New("legacy owner/list/key range missing")
		}
		pair.rows = [][]driver.Value{{int64(7000), "character", int64(4), int64(4), "equipment", int64(0), int64(54), int64(123), int64(1), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)}}
	}
	if reviewBuffSQLFromTable(q, mysqlLegacyItemExtraTable) && strings.HasPrefix(q, "SELECT item.item_uid, extra.extra_key, extra.extra_value") {
		if len(args) != 3 || fmt.Sprint(args[0].Value) != "4" || fmt.Sprint(args[1].Value) != "4" || !strings.Contains(q, " extra JOIN "+mysqlTable("write", "dnf", "legacy_character_items")+" item ON item.item_uid = extra.item_uid WHERE ") || !strings.Contains(q, "item.item_uid = extra.item_uid") || !strings.Contains(q, "item.character_id = ? AND item.list_type = ? ORDER BY item.item_uid, extra.extra_key FOR UPDATE") {
			return nil, errors.New("legacy extra UID/owner join missing")
		}
		uid := int64(7000)
		if c.s.scenario == "foreign_extra_uid" {
			uid = 7001
		}
		pair.rows = [][]driver.Value{{uid, "quality", "9"}}
	}
	c.s.openRows++
	return &reviewBuffSQLRows{Rows: pair, s: c.s, query: q}, nil
}

type reviewBuffSQLRows struct {
	driver.Rows
	s      *reviewBuffSQLState
	query  string
	closed bool
}

func (r *reviewBuffSQLRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.s.openRows--
	r.s.closedRows++
	if r.s.scenario == "item_close_error" && strings.HasPrefix(r.query, "SELECT entry_key, item_id,") {
		return errors.New("item rows close failure")
	}
	return r.Rows.Close()
}
func reviewBuffSQL(t *testing.T, scenario string) (*sql.DB, *reviewBuffSQLState) {
	t.Helper()
	state := &reviewBuffSQLState{scenario: scenario}
	db := sql.OpenDB(reviewBuffSQLConnector{state})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, state
}
func TestReviewBuffSQLInventoryLocksCloseBeforeExtra(t *testing.T) {
	for _, scenario := range []string{"success", "item_close_error"} {
		t.Run(scenario, func(t *testing.T) {
			db, state := reviewBuffSQL(t, scenario)
			tx, err := db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			got, err := loadBuffLockedItemStackCollection(context.Background(), sqlTransactionDB{tx: tx}, mysqlTable("write", "dnf", mysqlInventoryItemsTable), mysqlTable("write", "dnf", mysqlInventoryExtraTable), "character_id", "4", "slots", true)
			if scenario == "success" {
				if err != nil || got["0:54"].Extra["quality"] != "9" || len(state.base.queries) != 2 {
					t.Fatalf("metadata/lock/close failed: %+v %v queries%v", got, err, state.base.queries)
				}
			} else if err == nil || len(state.base.queries) != 1 {
				t.Fatalf("close error allowed next query: %v %v", err, state.base.queries)
			}
			if state.openRows != 0 || state.base.writes != 0 {
				t.Fatal("rows or write escaped")
			}
		})
	}
}
func TestReviewBuffSQLLegacyUIDExtraOwnerAndClose(t *testing.T) {
	for _, scenario := range []string{"success", "foreign_extra_uid"} {
		t.Run(scenario, func(t *testing.T) {
			db, state := reviewBuffSQL(t, scenario)
			tx, err := db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			router := mysqlRouter{db: sqlTransactionDB{tx: tx}, readDBs: []string{"write"}, writeDBs: []string{"write"}, tablePrefix: "dnf", lockReads: true, now: time.Now}
			store := &mysqlBuffOwnedLegacyStore{mysqlStoreBase: mysqlStoreBase{router: router}, characterID: "4"}
			got, err := store.SelectOwnedBuffItems(context.Background(), "4", []byte{0})
			if scenario == "success" {
				if err != nil || len(got) != 1 || got[0].ItemUID != 7000 || got[0].Item.Extra["quality"] != "9" {
					t.Fatalf("UID metadata not closed/joined: %+v %v", got, err)
				}
			} else if !errors.Is(err, repo.ErrBuffLegacyOwnerMismatch) {
				t.Fatalf("foreign extra UID consumed: %+v %v", got, err)
			}
			if state.openRows != 0 || state.closedRows != 2 || state.base.writes != 0 {
				t.Fatalf("rows/write escaped: %+v", state)
			}
		})
	}
}
func TestReviewBuffSQLOwnedScopeIsolationOrderAndRollback(t *testing.T) {
	for _, scenario := range []string{"success", "callback_failure"} {
		t.Run(scenario, func(t *testing.T) {
			db, state := reviewBuffSQL(t, scenario)
			u := &mysqlCeraShopAssetUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"readonly"}, writeDBs: []string{"write"}, tablePrefix: "dnf", now: time.Now}}
			err := u.WithinBuffOwnedSnapshot(context.Background(), "a4", "4", "character:4:buff_swap_native", func(_ repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository, _ repo.SettingsRepository, p repo.BuffPetMetadataReader, l repo.OwnedLegacyInventoryRepository) error {
				if _, _, err := i.Load(context.Background(), "4"); err != nil {
					return err
				}
				if _, _, err := e.Load(context.Background(), "4"); err != nil {
					return err
				}
				if _, _, err := p.LoadBuffPetEntries(context.Background(), "4"); err != nil {
					return err
				}
				if _, err := l.SelectOwnedBuffItems(context.Background(), "4", []byte{0}); err != nil {
					return err
				}
				if scenario == "callback_failure" {
					return errors.New("callback failure")
				}
				return nil
			})
			if state.isolation != driver.IsolationLevel(sql.LevelRepeatableRead) || state.base.begins != 1 || state.base.writes != 0 || state.openRows != 0 {
				t.Fatalf("physical isolation/close/write mismatch: %+v err%v", state, err)
			}
			last := 0
			seen := map[int]bool{}
			for _, q := range state.base.queries {
				stage := 0
				for n, markers := range map[int][]string{1: {mysqlSettingsTable, mysqlSettingValuesTable}, 2: {mysqlBagTable, mysqlInventoryItemsTable, mysqlInventoryExtraTable}, 3: {mysqlEquipmentTable, mysqlEquipmentEntriesTable, mysqlEquipmentExtraTable}, 4: {mysqlPetTable, mysqlPetEntriesTable, mysqlPetExtraTable}, 5: {"legacy_character_items", mysqlLegacyItemExtraTable}} {
					for _, marker := range markers {
						if reviewBuffSQLFromTable(q, marker) || strings.Contains(q, " JOIN "+mysqlTable("write", "dnf", marker)+" ") {
							stage = n
						}
					}
				}
				if stage < last {
					t.Fatalf("lock order reversed %d to%d: %s", last, stage, q)
				}
				last = stage
				seen[stage] = true
				if strings.Contains(q, "artifact") || strings.Contains(q, "clear_token") {
					t.Fatalf("unneeded pet asset dependency: %s", q)
				}
			}
			for stage := 1; stage <= 5; stage++ {
				if !seen[stage] {
					t.Fatalf("owned lock stage %d not exercised: err=%v queries=%q", stage, err, state.base.queries)
				}
			}
			if scenario == "success" {
				if err != nil || state.base.commits != 1 || state.base.rollbacks != 0 {
					t.Fatalf("commit mismatch: %+v %v", state, err)
				}
			} else if err == nil || state.base.commits != 0 || state.base.rollbacks != 1 {
				t.Fatalf("callback rollback mismatch: %+v %v", state, err)
			}
		})
	}
}

// Match the exact FROM identifier, including the boundary after a table name.
func reviewBuffSQLFromTable(q, suffix string) bool {
	return strings.Contains(q, " FROM "+mysqlTable("write", "dnf", suffix)+" ")
}
func TestReviewBuffSQLRejectsUnmatchedQueryFixtures(t *testing.T) {
	for _, scenario := range []string{"wrong_table", "no_lock", "read_route", "unclosed_rows", "wrong_join"} {
		t.Run(scenario, func(t *testing.T) {
			state := &reviewBuffSQLState{}
			state.base.begins = 1
			c := reviewBuffSQLConn{state}
			q := "SELECT character_id, updated_at FROM " + mysqlTable("write", "dnf", mysqlBagTable) + " WHERE character_id = ? FOR UPDATE"
			args := []driver.NamedValue{{Ordinal: 1, Value: "4"}}
			switch scenario {
			case "wrong_table":
				q = strings.Replace(q, "dnf_inventories", "dnf__inventories", 1)
			case "no_lock":
				q = strings.TrimSuffix(q, " FOR UPDATE")
			case "read_route":
				q = strings.Replace(q, "`write`", "`readonly`", 1)
			case "wrong_join":
				q = "SELECT item.item_uid, extra.extra_key, extra.extra_value FROM " + mysqlTable("write", "dnf", mysqlLegacyItemExtraTable) + " extra JOIN " + mysqlTable("write", "dnf", "wrong_items") + " item ON item.item_uid = extra.item_uid WHERE item.owner_scope = 'character' AND item.owner_id = ? AND item.character_id = ? AND item.list_type = ? ORDER BY item.item_uid, extra.extra_key FOR UPDATE"
				args = []driver.NamedValue{{Ordinal: 1, Value: "4"}, {Ordinal: 2, Value: "4"}, {Ordinal: 3, Value: int64(0)}}
			case "unclosed_rows":
				state.openRows = 1
			}
			rows, err := c.QueryContext(context.Background(), q, args)
			if err == nil || rows != nil {
				if rows != nil {
					rows.Close()
				}
				t.Fatalf("invalid query fixture accepted: %s rows=%v err=%v", q, rows, err)
			}
			if scenario == "wrong_join" && (err == nil || !strings.Contains(err.Error(), "legacy extra UID/owner join missing")) {
				t.Fatalf("wrong JOIN must fail the exact target guard: %v", err)
			}
			if state.base.writes != 0 || state.closedRows != 0 {
				t.Fatalf("rejected query had side effects: %+v", state)
			}
		})
	}
}
