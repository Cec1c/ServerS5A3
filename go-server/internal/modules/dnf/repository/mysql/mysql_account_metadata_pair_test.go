package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

type pairSQLState struct {
	locks, queries, writes     []string
	commits, rollbacks, failAt int
	missing                    string
}
type pairSQLConnector struct{ state *pairSQLState }

func (c pairSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return pairSQLConn{c.state}, nil
}
func (c pairSQLConnector) Driver() driver.Driver { return pairSQLDriver{c.state} }

type pairSQLDriver struct{ state *pairSQLState }

func (d pairSQLDriver) Open(string) (driver.Conn, error) { return pairSQLConn{d.state}, nil }

type pairSQLConn struct{ state *pairSQLState }

func (c pairSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c pairSQLConn) Close() error                                                 { return nil }
func (c pairSQLConn) Begin() (driver.Tx, error)                                    { return pairSQLTx{c.state}, nil }
func (c pairSQLConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) { return c.Begin() }
func (c pairSQLConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.queries = append(c.state.queries, q)
	id := args[0].Value.(string)
	if strings.HasPrefix(q, "SELECT account_id,") {
		c.state.locks = append(c.state.locks, id)
		rows := &pairSQLRows{columns: []string{"id", "state", "honor", "name", "created", "updated"}}
		if id != c.state.missing {
			rows.rows = [][]driver.Value{{id, "active", int64(0), "name-" + id, nil, nil}}
		}
		return rows, nil
	}
	if strings.HasPrefix(q, "SELECT entry_key,") {
		return &pairSQLRows{columns: []string{"key", "value"}, rows: [][]driver.Value{{"unrelated", "keep-" + id}}}, nil
	}
	return nil, errors.New("unexpected query")
}
func (c pairSQLConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	id := ""
	if strings.HasPrefix(q, "UPDATE ") {
		id = args[1].Value.(string)
	} else if strings.HasPrefix(q, "INSERT ") {
		id = args[0].Value.(string)
	} else {
		return nil, errors.New("unexpected write")
	}
	c.state.writes = append(c.state.writes, id+":"+q)
	if len(c.state.writes) == c.state.failAt {
		return nil, errors.New("second owner write failed")
	}
	return driver.RowsAffected(1), nil
}

type pairSQLTx struct{ state *pairSQLState }

func (t pairSQLTx) Commit() error   { t.state.commits++; return nil }
func (t pairSQLTx) Rollback() error { t.state.rollbacks++; return nil }

type pairSQLRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (r *pairSQLRows) Columns() []string { return r.columns }
func (r *pairSQLRows) Close() error      { return nil }
func (r *pairSQLRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.index])
	r.index++
	return nil
}

func TestMySQLPairUsesOrderedWriterLocksAndRollsBackSecondOwner(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		failAt                   int
		missing                  string
		callbackFail, invalidKey bool
		wantWrites, wantCommits  int
	}{
		{name: "commit", wantWrites: 4, wantCommits: 1},
		{name: "second_insert_failure", failAt: 4, wantWrites: 4},
		{name: "missing", missing: "b"},
		{name: "domain_reject", callbackFail: true},
		{name: "invalid_second_key", invalidKey: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &pairSQLState{failAt: tc.failAt, missing: tc.missing}
			database := sql.OpenDB(pairSQLConnector{state})
			defer database.Close()
			store := &mysqlAccountStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: SQLDBAdapter{DB: database}, readDBs: []string{"replica"}, writeDBs: []string{"writer"}, tablePrefix: "dnf_", now: time.Now}}}
			err := store.UpdateMetadataPair(context.Background(), "b", "a", func(b, a repo.AccountRecord) (map[string]string, map[string]string, error) {
				if a.AccountID != "a" || b.AccountID != "b" || a.Metadata["unrelated"] != "keep-a" || b.Metadata["unrelated"] != "keep-b" {
					t.Fatal("callback identity or metadata")
				}
				if tc.callbackFail {
					return nil, nil, errors.New("domain reject")
				}
				key := "pair:b"
				if tc.invalidKey {
					key = " "
				}
				return map[string]string{"pair:a": "left"}, map[string]string{key: "right"}, nil
			}, time.Now())
			if (err == nil) != (tc.wantCommits == 1) || len(state.writes) != tc.wantWrites || state.commits != tc.wantCommits {
				t.Fatalf("err=%v state=%+v", err, state)
			}
			if len(state.locks) != 2 || state.locks[0] != "a" || state.locks[1] != "b" {
				t.Fatal("non-deterministic locking", state.locks)
			}
			for _, q := range state.queries {
				if !strings.Contains(q, "`writer`.") || !strings.HasSuffix(q, "FOR UPDATE") {
					t.Fatal("read escaped writer transaction", q)
				}
			}
			if tc.wantCommits == 0 && state.rollbacks != 1 {
				t.Fatal("failure not rolled back", state.rollbacks)
			}
			for _, q := range state.writes {
				if strings.Contains(q, "DELETE ") {
					t.Fatal("replaced unrelated metadata")
				}
			}
		})
	}
}

func TestMySQLPairRefusesNonTransactionalDatabase(t *testing.T) {
	s := &mysqlAccountStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: &fakeSQLDB{}, writeDBs: []string{"writer"}}}}
	called := false
	err := s.UpdateMetadataPair(nil, "a", "b", func(repo.AccountRecord, repo.AccountRecord) (map[string]string, map[string]string, error) {
		called = true
		return nil, nil, nil
	}, time.Now())
	if !errors.Is(err, repo.ErrAccountMetadataUnavailable) || called {
		t.Fatal("unsafe transaction fallback", err)
	}
}
