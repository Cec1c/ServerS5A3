package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

type localLoginSQLFixture struct {
	fakeSQLDB
	affected    int64
	affectedErr error
}
type localLoginSQLResult struct {
	n   int64
	err error
}

func (r localLoginSQLResult) LastInsertId() (int64, error) { return 0, nil }
func (r localLoginSQLResult) RowsAffected() (int64, error) { return r.n, r.err }
func (f *localLoginSQLFixture) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	_, err := f.fakeSQLDB.ExecContext(ctx, q, args...)
	return localLoginSQLResult{f.affected, f.affectedErr}, err
}

func TestNativeAuth42LocalCredentialsUseExistingPrimaryTableAndConditionalUpdate(t *testing.T) {
	for _, kind := range []string{"valid", "missing", "query_error", "write_error", "stale", "affected_error", "multishard"} {
		t.Run(kind, func(t *testing.T) {
			f := &localLoginSQLFixture{affected: 1}
			f.rows = []fakeSQLRow{{values: []any{"dnf:10029", "q7a", []byte("offline-bcrypt-fixture"), "active"}}}
			plan := repository.DatabasePlan{WriteDatabases: []string{"owned_fixture"}, ReadDatabases: []string{"replica_fixture"}}
			switch kind {
			case "missing":
				f.rows[0].err = sql.ErrNoRows
			case "query_error":
				f.rows[0].err = errors.New("offline error")
			case "write_error":
				f.execErr = errors.New("offline error")
			case "stale":
				f.affected = 0
			case "affected_error":
				f.affectedErr = errors.New("offline error")
			case "multishard":
				plan.WriteDatabases = append(plan.WriteDatabases, "other_fixture")
			}
			group, err := NewMySQLGroup(f, MySQLGroupOptions{DatabasePlan: plan, TablePrefix: "custom_prefix"})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "multishard" {
				if group.LocalLogin != nil {
					t.Fatal("ambiguous shards enabled local login")
				}
				return
			}
			if group.LocalLogin == nil {
				t.Fatal("local credential repository not assembled")
			}
			c, found, err := group.LocalLogin.LoadCredential(context.Background(), "q7a")
			if kind == "missing" {
				if err != nil || found {
					t.Fatal("missing credential contract")
				}
				return
			}
			if kind == "query_error" {
				if err == nil || found {
					t.Fatal("query error accepted")
				}
				return
			}
			if err != nil || !found {
				t.Fatal("credential read failed")
			}
			if !strings.Contains(f.execs[0].Query, "`owned_fixture`.`dnf_login_accounts`") || strings.Contains(f.execs[0].Query, "replica_fixture") || strings.Contains(f.execs[0].Query, "custom_prefix") {
				t.Fatal("credential query used unproved schema or replica")
			}
			err = group.LocalLogin.RecordLogin(context.Background(), c)
			if (kind == "valid") != (err == nil) {
				t.Fatal("conditional login update result incorrect")
			}
			q := f.execs[len(f.execs)-1]
			if !strings.Contains(q.Query, "password_hash = ? AND state = ?") || len(q.Args) != 4 {
				t.Fatal("verified credential mutation guard missing")
			}
			for _, call := range f.execs {
				if strings.Contains(call.Query, "CREATE") || strings.Contains(call.Query, "INSERT") {
					t.Fatal("authentication invented schema or account")
				}
			}
		})
	}
}
