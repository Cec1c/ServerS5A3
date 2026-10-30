package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	repository "longheng.io/server/internal/modules/dnf/repository"
)

func TestMySQLAccountCreateIfAbsentWritesMetadataOnlyForNewAccount(t *testing.T) {
	database := &fakeSQLDB{execResult: driver.RowsAffected(1)}
	repositories := newTestMySQLGroup(t, database)
	writer, ok := repositories.Account.(repository.AccountCreateIfAbsentWriter)
	if !ok {
		t.Fatal("MySQL account repository does not implement CreateIfAbsent")
	}
	created, err := writer.CreateIfAbsent(context.Background(), repository.AccountRecord{
		AccountID: "dnf:new",
		State:     "active",
		Metadata: map[string]string{
			"source":                          "dnfbridge",
			"account_record_bootstrap":        "true",
			"account_record_bootstrap_source": "login_token_account_bound",
		},
		CreatedAt: time.Unix(2_000_000_000, 0).UTC(),
		UpdatedAt: time.Unix(2_000_000_000, 0).UTC(),
	})
	if err != nil || !created {
		t.Fatalf("created=%t err=%v", created, err)
	}
	accountCall := requireExecContaining(t, database, "`dnf_s1_w1`.`dnf_accounts`")
	assertContains(t, accountCall.Query, "INSERT IGNORE")
	if len(database.execs) != 4 {
		t.Fatalf("SQL count=%d want account plus three metadata inserts: %v", len(database.execs), execQueries(database))
	}
	for _, call := range database.execs {
		assertNotContains(t, call.Query, "DELETE FROM")
		assertNotContains(t, call.Query, "ON DUPLICATE KEY UPDATE")
	}
}

func TestMySQLAccountCreateIfAbsentLeavesConcurrentWinnerUntouched(t *testing.T) {
	database := &fakeSQLDB{execResult: driver.RowsAffected(0)}
	repositories := newTestMySQLGroup(t, database)
	writer := repositories.Account.(repository.AccountCreateIfAbsentWriter)
	created, err := writer.CreateIfAbsent(context.Background(), repository.AccountRecord{
		AccountID: "dnf:existing",
		State:     "active",
		Metadata:  map[string]string{"account_record_bootstrap": "true"},
	})
	if err != nil || created {
		t.Fatalf("created=%t err=%v", created, err)
	}
	if len(database.execs) != 1 || !strings.Contains(database.execs[0].Query, "INSERT IGNORE") {
		t.Fatalf("duplicate creator touched existing state: %v", execQueries(database))
	}
}

func TestMySQLAccountCreateIfAbsentUsesOnePhysicalTransaction(t *testing.T) {
	state := &raidLedgerSQLState{}
	database := sql.OpenDB(raidLedgerSQLConnector{state: state})
	defer database.Close()
	store := &mysqlAccountStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
		db:          SQLDBAdapter{DB: database},
		readDBs:     []string{"s1_w1"},
		writeDBs:    []string{"s1_w1"},
		tablePrefix: "dnf",
		now:         time.Now,
	}}}
	created, err := store.CreateIfAbsent(context.Background(), repository.AccountRecord{
		AccountID: "dnf:new",
		State:     "active",
		Metadata:  map[string]string{"source": "dnfbridge", "account_record_bootstrap": "true"},
	})
	if err != nil || !created {
		t.Fatalf("created=%t err=%v", created, err)
	}
	if state.begins != 1 || state.commits != 1 || state.rollbacks != 0 || state.outsideTxOperations != 0 {
		t.Fatalf("transaction state=%+v", state)
	}
	if state.writes != 3 || !strings.Contains(state.writeSQL[0], "`dnf_accounts`") {
		t.Fatalf("writes=%d SQL=%v", state.writes, state.writeSQL)
	}
}
