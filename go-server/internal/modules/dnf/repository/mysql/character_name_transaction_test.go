package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

type nameTxState struct {
	queries            []string
	commits, rollbacks int
	failLock           bool
}
type nameTxConnector struct{ state *nameTxState }

func (c nameTxConnector) Connect(context.Context) (driver.Conn, error) {
	return nameTxConn{c.state}, nil
}
func (c nameTxConnector) Driver() driver.Driver { return nameTxDriver{c.state} }

type nameTxDriver struct{ state *nameTxState }

func (d nameTxDriver) Open(string) (driver.Conn, error) { return nameTxConn{d.state}, nil }

type nameTxConn struct{ state *nameTxState }

func (c nameTxConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c nameTxConn) Close() error              { return nil }
func (c nameTxConn) Begin() (driver.Tx, error) { return nameTx{c.state}, nil }
func (c nameTxConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.state.queries = append(c.state.queries, q)
	return driver.RowsAffected(1), nil
}
func (c nameTxConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.queries = append(c.state.queries, q)
	if strings.HasPrefix(q, "SELECT scope FROM") {
		if c.state.failLock {
			return nil, errors.New("lock unavailable")
		}
		return &nameTxRows{value: repo.CharacterNameMutationScope}, nil
	}
	return &nameTxRows{done: true}, nil
}

type nameTx struct{ state *nameTxState }

func (t nameTx) Commit() error   { t.state.commits++; return nil }
func (t nameTx) Rollback() error { t.state.rollbacks++; return nil }

type nameTxRows struct {
	value string
	done  bool
}

func (r *nameTxRows) Columns() []string { return []string{"value"} }
func (r *nameTxRows) Close() error      { return nil }
func (r *nameTxRows) Next(v []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	v[0] = r.value
	return nil
}

func TestCharacterCreationAndRenameUseSameSQLNamespaceLock(t *testing.T) {
	for _, kind := range []string{"create", "rename", "lock_failure", "callback_failure", "panic"} {
		t.Run(kind, func(t *testing.T) {
			state := &nameTxState{failLock: kind == "lock_failure"}
			db := sql.OpenDB(nameTxConnector{state})
			defer db.Close()
			u := &mysqlCharacterCreationUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{db}, readDBs: []string{"stale_replica"}, writeDBs: []string{"writer_a", "writer_b"}, tablePrefix: "dnf_"}}
			called := false
			callback := func(g repo.Group) error {
				called = true
				if kind == "panic" {
					panic("owner failed")
				}
				if kind == "callback_failure" {
					return errors.New("owner failed")
				}
				_, _, err := g.Character.FindIDByName(context.Background(), "新角色")
				return err
			}
			var err error
			func() {
				defer func() {
					if recover() != nil && kind != "panic" {
						t.Fatal("unexpected panic")
					}
				}()
				if kind == "rename" {
					err = u.WithinCharacterNameMutation(context.Background(), "77", repo.Group{}, callback)
				} else {
					err = u.WithinCharacterCreation(context.Background(), "77", repo.Group{}, callback)
				}
			}()
			if kind == "lock_failure" && called {
				t.Fatal("callback ran without lock")
			}
			if kind == "lock_failure" || kind == "callback_failure" {
				if err == nil {
					t.Fatal("missing failure")
				}
			}
			if kind == "create" || kind == "rename" {
				if err != nil || state.commits != 1 || state.rollbacks != 0 {
					t.Fatal(err, state)
				}
				if len(state.queries) != 4 || !strings.HasPrefix(state.queries[0], "INSERT IGNORE") || !strings.HasSuffix(state.queries[1], "FOR UPDATE") {
					t.Fatal(state.queries)
				}
				for _, q := range state.queries[2:] {
					if strings.Contains(q, "stale_replica") || !strings.HasSuffix(q, "FOR UPDATE") {
						t.Fatal(q)
					}
				}
			} else if state.commits != 0 || state.rollbacks != 1 {
				t.Fatal("failed mutation escaped rollback", state)
			}
		})
	}
}
