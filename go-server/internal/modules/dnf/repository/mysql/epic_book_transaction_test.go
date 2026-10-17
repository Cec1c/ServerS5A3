package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

type epicBookSQLState struct {
	accountMailSQLState
	writesSQL []string
	values    [][]driver.NamedValue
}
type epicBookSQLConnector struct{ s *epicBookSQLState }

func (c epicBookSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return epicBookSQLConn{accountMailSQLConn{&c.s.accountMailSQLState}, c.s}, nil
}
func (c epicBookSQLConnector) Driver() driver.Driver {
	return accountMailSQLDriver{&c.s.accountMailSQLState}
}

type epicBookSQLConn struct {
	accountMailSQLConn
	state *epicBookSQLState
}

func (c epicBookSQLConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, "SELECT entry_key, entry_value FROM") {
		c.state.queries = append(c.state.queries, q)
		balance := "1000"
		if c.state.scenario == "insufficient" {
			balance = "999"
		}
		return &pairSQLRows{columns: []string{"entry_key", "entry_value"}, rows: [][]driver.Value{{"epic_piece:10300000", balance}, {"other", "keep"}}}, nil
	}
	if strings.HasPrefix(q, "SELECT account_id, updated_at FROM") {
		c.state.queries = append(c.state.queries, q)
		return &pairSQLRows{columns: []string{"account_id", "updated_at"}, rows: [][]driver.Value{{"a", nil}}}, nil
	}
	return c.accountMailSQLConn.QueryContext(ctx, q, args)
}
func (c epicBookSQLConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.state.writesSQL = append(c.state.writesSQL, q)
	c.state.values = append(c.state.values, append([]driver.NamedValue(nil), args...))
	if c.state.scenario == "late_write" && strings.Contains(q, "inventory_items`") && !strings.Contains(q, "account_inventory_items") && strings.HasPrefix(q, "INSERT") {
		return nil, errors.New("last item write fails")
	}
	return c.accountMailSQLConn.ExecContext(ctx, q, args)
}

func TestMySQLEpicBookDebitAndAssetsOneTransactionRound9(t *testing.T) {
	for _, mode := range []string{"success", "insufficient", "foreign_owner", "account_missing", "scope", "late_abort", "write_failure", "commit_failure", "panic", "cancel", "late_write"} {
		t.Run(mode, func(t *testing.T) {
			state := &epicBookSQLState{accountMailSQLState: accountMailSQLState{scenario: mode}}
			db := sql.OpenDB(epicBookSQLConnector{state})
			defer db.Close()
			u := &mysqlAccountCharacterAssetUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"readonly"}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_", now: time.Now}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var got error
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				got = u.WithinEpicBookAssets(ctx, "a", "19", func(d repo.EpicBookPieceDebitor, ch repo.CharacterRecord, s repo.AccountInventoryRepository, i repo.InventoryRepository) error {
					if mode == "scope" {
						_, _, e := s.Load(ctx, "other")
						return e
					}
					balance, e := d.DebitPiece(ctx, 10300000, 1000, time.Now())
					if e != nil {
						return e
					}
					if balance != 0 {
						t.Fatal(balance)
					}
					if e = s.Save(ctx, repo.AccountInventoryRecord{AccountID: "a", Slots: map[string]repo.ItemStack{}}); e != nil {
						return e
					}
					if e = i.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:9": {ItemID: 450004, Count: 1}}}); e != nil {
						return e
					}
					if mode == "late_abort" {
						return errors.New("abort")
					}
					if mode == "panic" {
						panic("abort")
					}
					if mode == "cancel" {
						cancel()
					}
					return nil
				})
			}()
			if mode == "success" {
				if got != nil || state.begins != 1 || state.commits != 1 || state.rollbacks != 0 {
					t.Fatalf("transaction %v %+v", got, state)
				}
			} else if (!panicked && got == nil) || state.begins != 1 || state.commits > 0 && mode != "commit_failure" {
				t.Fatalf("failed action committed: %s err=%v %+v", mode, got, state)
			}
			if mode != "success" && mode != "commit_failure" && state.rollbacks != 1 {
				t.Fatalf("missing SQL rollback %s: %+v", mode, state)
			}
			for _, q := range state.queries {
				if strings.Contains(q, "readonly") {
					t.Fatal("read outside write transaction", q)
				}
			}
			if len(state.queries) > 0 && !strings.Contains(state.queries[0], "FOR UPDATE") {
				t.Fatal("account row was not locked first", state.queries[0])
			}
			for _, q := range state.writesSQL {
				if strings.HasPrefix(q, "DELETE") && strings.Contains(q, "account_metadata") {
					t.Fatal("whole metadata overwritten", q)
				}
			}
			if mode == "success" {
				metadataWrites := 0
				for n, q := range state.writesSQL {
					if strings.HasPrefix(q, "INSERT") && strings.Contains(q, "account_metadata`") {
						metadataWrites++
						args := state.values[n]
						if len(args) != 3 || args[0].Value != "a" || args[1].Value != "epic_piece:10300000" || args[2].Value != "0" {
							t.Fatalf("wrong targeted debit args %+v", args)
						}
					}
				}
				if metadataWrites != 1 {
					t.Fatal("debit must update exactly one metadata row", metadataWrites)
				}
			}
		})
	}
}
