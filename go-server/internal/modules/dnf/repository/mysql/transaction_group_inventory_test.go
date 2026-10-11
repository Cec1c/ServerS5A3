package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

// Exercise real database/sql transaction plumbing without a live player database.
type inventoryGroupSQLState struct {
	begins, commits, rollbacks int
	queries                    []string
	writes                     int
	commitErr                  error
}
type inventoryGroupConnector struct{ state *inventoryGroupSQLState }

func (c inventoryGroupConnector) Connect(context.Context) (driver.Conn, error) {
	return inventoryGroupConn{c.state}, nil
}
func (c inventoryGroupConnector) Driver() driver.Driver { return inventoryGroupDriver{c.state} }

type inventoryGroupDriver struct{ state *inventoryGroupSQLState }

func (d inventoryGroupDriver) Open(string) (driver.Conn, error) {
	return inventoryGroupConn{d.state}, nil
}

type inventoryGroupConn struct{ state *inventoryGroupSQLState }

func (c inventoryGroupConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}
func (c inventoryGroupConn) Close() error { return nil }
func (c inventoryGroupConn) Begin() (driver.Tx, error) {
	c.state.begins++
	return inventoryGroupTx{c.state}, nil
}
func (c inventoryGroupConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}
func (c inventoryGroupConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.queries = append(c.state.queries, q)
	return inventoryGroupRows{}, nil
}
func (c inventoryGroupConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.state.writes++
	return driver.RowsAffected(1), nil
}

type inventoryGroupTx struct{ state *inventoryGroupSQLState }

func (t inventoryGroupTx) Commit() error   { t.state.commits++; return t.state.commitErr }
func (t inventoryGroupTx) Rollback() error { t.state.rollbacks++; return nil }

type inventoryGroupRows struct{}

func (inventoryGroupRows) Columns() []string         { return []string{"unused"} }
func (inventoryGroupRows) Close() error              { return nil }
func (inventoryGroupRows) Next([]driver.Value) error { return io.EOF }

func TestMySQLCharacterInventoryGroupUsesOneScopedTransaction(t *testing.T) {
	for _, scenario := range []string{"success", "callback_failure", "commit_failure", "scope_failure", "panic"} {
		t.Run(scenario, func(t *testing.T) {
			state := &inventoryGroupSQLState{}
			if scenario == "commit_failure" {
				state.commitErr = errors.New("commit failed")
			}
			db := sql.OpenDB(inventoryGroupConnector{state})
			defer db.Close()
			u := &mysqlCharacterTradeUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"read_only"}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_"}}
			ctx := context.Background()
			invoke := func() error {
				return u.WithinCharacterInventoryGroup(ctx, []string{"21", "19", "20"}, func(c repository.CharacterRepository, i repository.InventoryRepository) error {
					for _, id := range []string{"19", "20", "21"} {
						if _, _, err := c.Load(ctx, id); err != nil {
							return err
						}
						if _, _, err := i.Load(ctx, id); err != nil {
							return err
						}
					}
					if _, _, err := i.Load(ctx, "22"); err == nil {
						t.Fatal("out of scope read exposed")
					}
					if scenario == "scope_failure" {
						return repository.SaveInventoryFields(ctx, i, repository.InventoryRecord{CharacterID: "22"}, repository.InventoryFieldSlots)
					}
					for _, id := range []string{"19", "20", "21"} {
						if err := repository.SaveInventoryFields(ctx, i, repository.InventoryRecord{CharacterID: id, Slots: map[string]repository.ItemStack{"0:50": {ItemID: 777, Count: 2}}}, repository.InventoryFieldSlots); err != nil {
							return err
						}
					}
					if scenario == "callback_failure" {
						return errors.New("last member failed")
					}
					if scenario == "panic" {
						panic("callback panicked")
					}
					return nil
				})
			}
			var err error
			if scenario == "panic" {
				func() {
					defer func() {
						if recover() == nil {
							t.Error("expected callback panic")
						}
					}()
					_ = invoke()
				}()
			} else {
				err = invoke()
			}
			if (err == nil) != (scenario == "success") && scenario != "panic" {
				t.Fatalf("scenario=%s err=%v", scenario, err)
			}
			wantCommit, wantRollback := 0, 1
			if scenario == "success" || scenario == "commit_failure" {
				wantCommit, wantRollback = 1, 0
			}
			if state.begins != 1 || state.commits != wantCommit || state.rollbacks != wantRollback {
				t.Fatalf("transaction counts %+v", state)
			}
			if scenario == "scope_failure" && state.writes != 0 {
				t.Fatal("out of scope write reached SQL")
			}
			for _, q := range state.queries {
				if strings.Contains(q, "read_only") || !strings.HasSuffix(q, "FOR UPDATE") {
					t.Fatalf("unlocked or replica transaction read: %s", q)
				}
			}
		})
	}
}
