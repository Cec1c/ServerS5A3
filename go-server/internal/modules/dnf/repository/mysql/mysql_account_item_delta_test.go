package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

// Use the existing SQL row model through database/sql so the production UOW,
// scoped repositories, FOR UPDATE reads and rollback are all exercised.
type cargoDeltaSQLState struct {
	db                                 *rowModelDB
	before                             *rowModel
	commits, rollbacks, writes, failAt int
}
type cargoDeltaConnector struct{ s *cargoDeltaSQLState }

func (c cargoDeltaConnector) Connect(context.Context) (driver.Conn, error) {
	return cargoDeltaConn{c.s}, nil
}
func (c cargoDeltaConnector) Driver() driver.Driver { return cargoDeltaDriver{c.s} }

type cargoDeltaDriver struct{ s *cargoDeltaSQLState }

func (d cargoDeltaDriver) Open(string) (driver.Conn, error) { return cargoDeltaConn{d.s}, nil }

type cargoDeltaConn struct{ s *cargoDeltaSQLState }

func (c cargoDeltaConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c cargoDeltaConn) Close() error { return nil }
func (c cargoDeltaConn) Begin() (driver.Tx, error) {
	c.s.before = cloneCargoDeltaModel(c.s.db.model)
	return cargoDeltaTx{c.s}, nil
}
func (c cargoDeltaConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}
func (c cargoDeltaConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.s.writes++
	if c.s.failAt > 0 && c.s.writes == c.s.failAt {
		return nil, errors.New("injected cargo write failure")
	}
	_, err := c.s.db.ExecContext(ctx, q, cargoDeltaArgs(args)...)
	return driver.RowsAffected(1), err
}
func cargoDeltaArgs(args []driver.NamedValue) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = a.Value
	}
	return out
}
func (c cargoDeltaConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	parent := strings.HasPrefix(q, "SELECT character_id, updated_at") || strings.HasPrefix(q, "SELECT account_id, updated_at")
	if strings.Contains(q, "replica") || parent && !strings.HasSuffix(q, " FOR UPDATE") {
		return nil, errors.New("cargo read did not lock writer")
	}
	c.s.db.calls = append(c.s.db.calls, fakeSQLCall{Query: q, Args: cargoDeltaArgs(args)})
	rows, err := c.s.db.selectRows(q, cargoDeltaArgs(args))
	if err != nil {
		return nil, err
	}
	columns := rowModelColumns(strings.TrimPrefix(strings.SplitN(q, " FROM ", 2)[0], "SELECT "))
	out := &pairSQLRows{columns: columns}
	for _, row := range rows {
		values := make([]driver.Value, len(row.values))
		for i, v := range row.values {
			values[i], err = driver.DefaultParameterConverter.ConvertValue(v)
			if err != nil {
				return nil, err
			}
		}
		out.rows = append(out.rows, values)
	}
	return out, nil
}

type cargoDeltaTx struct{ s *cargoDeltaSQLState }

func (t cargoDeltaTx) Commit() error   { t.s.commits++; return nil }
func (t cargoDeltaTx) Rollback() error { t.s.rollbacks++; t.s.db.model = t.s.before; return nil }
func cloneCargoDeltaModel(m *rowModel) *rowModel {
	out := newRowModel()
	for table, rows := range m.tables {
		for key, row := range rows {
			copy := make(map[string]any, len(row))
			for col, v := range row {
				if b, ok := v.([]byte); ok {
					v = append([]byte(nil), b...)
				}
				copy[col] = v
			}
			out.tables[table][key] = copy
		}
	}
	return out
}

func cargoDeltaFixture(t *testing.T, rows int) (*rowModelDB, repository.InventoryRecord, repository.AccountInventoryRecord) {
	t.Helper()
	db := newRowModelDB()
	g := newRowModelGroup(t, db, false)
	i := repository.InventoryRecord{CharacterID: "77", Slots: map[string]repository.ItemStack{}, Warehouse: map[string]repository.ItemStack{}}
	a := repository.AccountInventoryRecord{AccountID: "acct-77", Slots: map[string]repository.ItemStack{}}
	for n := 0; n < rows; n++ {
		i.Slots[bagMoveSlotKey(0, int16(n))] = bagMoveStackable(int64(500000+n), 5)
		a.Slots[bagMoveSlotKey(12, int16(n))] = bagMoveStackable(int64(600000+n), 7)
	}
	i.Warehouse["2:0"] = bagMoveStackable(900000, 3)
	if err := g.Inventory.Save(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	if err := g.AccountInventory.Save(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	db.calls = nil
	return db, i, a
}

func cargoDeltaUOW(s *cargoDeltaSQLState) (*mysqlAccountCharacterItemUnitOfWork, *sql.DB) {
	db := sql.OpenDB(cargoDeltaConnector{s})
	u := &mysqlAccountCharacterItemUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"replica"}, writeDBs: []string{"dnf_s1_w1"}, tablePrefix: "dnf", now: time.Now}}
	return u, db
}

func TestMySQLAccountItemDeltaMatchesFullSaveAndPreservesUnrelatedRows(t *testing.T) {
	for _, mode := range []string{"move", "merge", "split", "raw_and_extra", "warehouse", "same_transaction_twice", "same_transaction_revert", "partial_then_other_collection"} {
		t.Run(mode, func(t *testing.T) {
			var snapshots []map[string][]string
			var counts []int
			for _, optimized := range []bool{false, true} {
				model, i, a := cargoDeltaFixture(t, 80)
				state := &cargoDeltaSQLState{db: model}
				u, db := cargoDeltaUOW(state)
				mutate := func(i *repository.InventoryRecord, a *repository.AccountInventoryRecord) {
					switch mode {
					case "move", "same_transaction_twice":
						a.Slots["12:90"] = i.Slots["0:1"]
						delete(i.Slots, "0:1")
					case "merge":
						x := a.Slots["12:2"]
						x.Count += i.Slots["0:1"].Count
						a.Slots["12:2"] = x
						delete(i.Slots, "0:1")
					case "split":
						x := i.Slots["0:1"]
						x.Count -= 2
						i.Slots["0:1"] = x
						x.Count = 2
						a.Slots["12:90"] = x
					case "raw_and_extra":
						x := i.Slots["0:1"]
						x.RawEntry[10] ^= 0x20
						delete(x.Extra, "pvf_path")
						x.Extra["new_field"] = "value"
						i.Slots["0:1"] = x
					case "warehouse", "partial_then_other_collection":
						x := i.Warehouse["2:0"]
						x.Count = 1
						i.Warehouse["2:0"] = x
						a.Slots["12:90"] = x
						if mode == "partial_then_other_collection" {
							x = i.Slots["0:1"]
							x.Count = 3
							i.Slots["0:1"] = x
						}
					case "same_transaction_revert":
						x := a.Slots["12:2"]
						x.Count = 5
						a.Slots["12:2"] = x
					}
				}
				var err error
				if !optimized {
					mutate(&i, &a)
					g := newRowModelGroup(t, model, false)
					err = g.Inventory.Save(context.Background(), i)
					if err == nil {
						err = g.AccountInventory.Save(context.Background(), a)
					}
					if mode == "same_transaction_twice" {
						x := a.Slots["12:90"]
						x.Count++
						a.Slots["12:90"] = x
						err = g.AccountInventory.Save(context.Background(), a)
					}
					if mode == "same_transaction_revert" {
						x := a.Slots["12:2"]
						x.Count = 7
						a.Slots["12:2"] = x
						err = g.AccountInventory.Save(context.Background(), a)
					}
				} else {
					err = u.WithinAccountCharacterItems(context.Background(), a.AccountID, i.CharacterID, func(ar repository.AccountInventoryRepository, ir repository.InventoryRepository) error {
						ctx := context.Background()
						var e error
						var found bool
						i, found, e = ir.Load(ctx, i.CharacterID)
						if e != nil || !found {
							return fmt.Errorf("inventory load found=%v err=%w", found, e)
						}
						a, found, e = ar.Load(ctx, a.AccountID)
						if e != nil || !found {
							return fmt.Errorf("cargo load found=%v err=%w", found, e)
						}
						mutate(&i, &a)
						if mode == "partial_then_other_collection" {
							if e = repository.SaveInventoryFields(ctx, ir, i, repository.InventoryFieldWarehouse); e != nil {
								return e
							}
							// The unsaved slots edit must still be dirty after this partial save.
						}
						if e = ir.Save(ctx, i); e != nil {
							return e
						}
						if e = ar.Save(ctx, a); e != nil {
							return e
						}
						if mode == "same_transaction_twice" {
							x := a.Slots["12:90"]
							x.Count++
							a.Slots["12:90"] = x
							return ar.Save(ctx, a)
						}
						if mode == "same_transaction_revert" {
							x := a.Slots["12:2"]
							x.Count = 7
							a.Slots["12:2"] = x
							return ar.Save(ctx, a)
						}
						return nil
					})
				}
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
				snapshots = append(snapshots, model.model.snapshot())
				writes := 0
				for _, call := range model.calls {
					if !strings.HasPrefix(call.Query, "SELECT ") {
						writes++
					}
				}
				counts = append(counts, writes)
				if optimized {
					for _, call := range model.calls {
						if strings.HasPrefix(call.Query, "SELECT ") {
							continue
						}
						for _, arg := range call.Args {
							if arg == "0:79" || arg == "12:79" {
								t.Fatal("rewrote unrelated slot", call.Query)
							}
						}
					}
				}
			}
			if !reflect.DeepEqual(snapshots[0], snapshots[1]) {
				t.Fatal("delta differs from full save")
			}
			if counts[1] >= counts[0]/4 {
				t.Fatalf("write amplification remains: full=%d delta=%d", counts[0], counts[1])
			}
			t.Logf("full SQL writes=%d; transaction delta writes=%d", counts[0], counts[1])
		})
	}
}

func TestMySQLAccountItemDeltaRollsBackAfterCharacterAndCargoDeletes(t *testing.T) {
	model, _, _ := cargoDeltaFixture(t, 10)
	before := model.model.snapshot()
	state := &cargoDeltaSQLState{db: model, failAt: 9}
	u, db := cargoDeltaUOW(state)
	defer db.Close()
	err := u.WithinAccountCharacterItems(context.Background(), "acct-77", "77", func(ar repository.AccountInventoryRepository, ir repository.InventoryRepository) error {
		ctx := context.Background()
		i, _, e := ir.Load(ctx, "77")
		if e != nil {
			return e
		}
		a, _, e := ar.Load(ctx, "acct-77")
		if e != nil {
			return e
		}
		a.Slots["12:90"] = i.Slots["0:1"]
		delete(i.Slots, "0:1")
		if e = ir.Save(ctx, i); e != nil {
			return e
		}
		return ar.Save(ctx, a)
	})
	if err == nil || state.writes != state.failAt || state.commits != 0 || state.rollbacks != 1 || !reflect.DeepEqual(before, model.model.snapshot()) {
		t.Fatalf("rollback err=%v writes=%d commits=%d rollbacks=%d", err, state.writes, state.commits, state.rollbacks)
	}
}

func TestMySQLAccountItemDeltaRequiresLoadedSnapshotAndSkipsUnchangedRows(t *testing.T) {
	for _, mode := range []string{"no_load", "unchanged", "foreign_account", "foreign_character"} {
		t.Run(mode, func(t *testing.T) {
			model, i, a := cargoDeltaFixture(t, 10)
			before := model.model.snapshot()
			state := &cargoDeltaSQLState{db: model}
			u, db := cargoDeltaUOW(state)
			defer db.Close()
			err := u.WithinAccountCharacterItems(context.Background(), a.AccountID, i.CharacterID, func(ar repository.AccountInventoryRepository, ir repository.InventoryRepository) error {
				ctx := context.Background()
				if mode != "no_load" {
					var e error
					i, _, e = ir.Load(ctx, "77")
					if e != nil {
						return e
					}
					a, _, e = ar.Load(ctx, "acct-77")
					if e != nil {
						return e
					}
				}
				if mode == "foreign_account" {
					a.AccountID = "other"
					return ar.Save(ctx, a)
				}
				if mode == "foreign_character" {
					i.CharacterID = "78"
					return ir.Save(ctx, i)
				}
				if e := ir.Save(ctx, i); e != nil {
					return e
				}
				return ar.Save(ctx, a)
			})
			if strings.HasPrefix(mode, "foreign_") {
				if err == nil || state.writes != 0 || state.rollbacks != 1 {
					t.Fatalf("scope failure: err=%v writes=%d", err, state.writes)
				}
			} else if err != nil || state.commits != 1 {
				t.Fatalf("commit: %v", err)
			}
			if !reflect.DeepEqual(before, model.model.snapshot()) {
				t.Fatal("unchanged save changed rows")
			}
			if mode == "unchanged" && state.writes != 2 {
				t.Fatalf("unchanged save wrote %d statements, want two parent timestamps", state.writes)
			}
			if mode == "no_load" {
				if state.writes < 100 {
					t.Fatal("unloaded save did not retain full replacement")
				}
				for _, c := range model.calls {
					if strings.Contains(c.Query, "entry_key IN") {
						t.Fatal("unloaded delta write", c.Query)
					}
				}
			}
		})
	}
}
