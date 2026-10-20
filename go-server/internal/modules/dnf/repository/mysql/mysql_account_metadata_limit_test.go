package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

type metadataLimitState struct {
	count                      int64
	current                    string
	missing                    bool
	queries                    []string
	writes, commits, rollbacks int
	failWrite                  bool
}
type metadataLimitConnector struct{ state *metadataLimitState }

func (c metadataLimitConnector) Connect(context.Context) (driver.Conn, error) {
	return metadataLimitConn{c.state}, nil
}
func (c metadataLimitConnector) Driver() driver.Driver { return metadataLimitDriver{c.state} }

type metadataLimitDriver struct{ state *metadataLimitState }

func (d metadataLimitDriver) Open(string) (driver.Conn, error) {
	return metadataLimitConn{d.state}, nil
}

type metadataLimitConn struct{ state *metadataLimitState }

func (c metadataLimitConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (c metadataLimitConn) Close() error              { return nil }
func (c metadataLimitConn) Begin() (driver.Tx, error) { return metadataLimitTx{c.state}, nil }
func (c metadataLimitConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}
func (c metadataLimitConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.queries = append(c.state.queries, q)
	var values []driver.Value
	if strings.HasPrefix(q, "SELECT account_id") && !c.state.missing {
		values = []driver.Value{"dnf:1"}
	}
	if strings.HasPrefix(q, "SELECT entry_value") && c.state.current != "" {
		values = []driver.Value{c.state.current}
	}
	if strings.HasPrefix(q, "SELECT COUNT(*)") {
		values = []driver.Value{c.state.count}
	}
	return &metadataLimitRows{values: values}, nil
}
func (c metadataLimitConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.state.writes++
	if c.state.failWrite && strings.HasPrefix(q, "INSERT") {
		return nil, errors.New("insert failed")
	}
	return driver.RowsAffected(1), nil
}

type metadataLimitTx struct{ state *metadataLimitState }

func (t metadataLimitTx) Commit() error   { t.state.commits++; return nil }
func (t metadataLimitTx) Rollback() error { t.state.rollbacks++; return nil }

type metadataLimitRows struct {
	values []driver.Value
	read   bool
}

func (*metadataLimitRows) Columns() []string { return []string{"value"} }
func (*metadataLimitRows) Close() error      { return nil }
func (r *metadataLimitRows) Next(dest []driver.Value) error {
	if r.read || len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values)
	r.read = true
	return nil
}

func TestMySQLMetadataCapacityLocksOwnerAndRollsBackFailures(t *testing.T) {
	for _, tc := range []struct {
		name            string
		state           metadataLimitState
		want            error
		commits, writes int
	}{
		{"available", metadataLimitState{count: 63}, nil, 1, 2},
		{"full", metadataLimitState{count: 64}, repository.ErrAccountMetadataLimitReached, 0, 0},
		{"idempotent_full", metadataLimitState{count: 64, current: "1"}, nil, 1, 2},
		{"missing_owner", metadataLimitState{missing: true}, repository.ErrAccountMetadataUnavailable, 0, 0},
		{"insert_failure", metadataLimitState{count: 63, failWrite: true}, errors.New("insert failed"), 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := tc.state
			database := sql.OpenDB(metadataLimitConnector{&state})
			defer database.Close()
			store := &mysqlAccountStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: SQLDBAdapter{DB: database}, writeDBs: []string{"shard_a"}, tablePrefix: "dnf_", now: time.Now}}}
			err := store.SaveMetadataEntryWithPrefixLimit(context.Background(), "dnf:1", "character_friend_v1:1:", "character_friend_v1:1:5", "1", 64, time.Now())
			if tc.want == nil {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want.Error()) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			if state.commits != tc.commits || state.writes != tc.writes || len(state.queries) == 0 || !strings.HasSuffix(state.queries[0], "FOR UPDATE") {
				t.Fatalf("transaction %+v", state)
			}
			if tc.commits == 0 && state.rollbacks != 1 {
				t.Fatal("failure did not roll back")
			}
			if tc.name == "idempotent_full" && len(state.queries) != 2 {
				t.Fatal("retry should bypass capacity count")
			}
		})
	}
}
