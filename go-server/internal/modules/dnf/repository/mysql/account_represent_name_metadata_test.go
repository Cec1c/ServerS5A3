package mysql

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestMySQLRepresentAccountNameWritesOnlyNicknameAndOwnedMetadata(t *testing.T) {
	database := &fakeSQLDB{}
	repositories := newTestMySQLGroup(t, database)
	now := time.Unix(2_000_000_000, 0).UTC()
	writer, ok := repositories.Account.(interface {
		SaveRepresentAccountName(context.Context, string, string, string, string, string, time.Time) error
	})
	if !ok {
		t.Fatal("MySQL account repository does not implement the narrow represent-name writer")
	}
	if err := writer.SaveRepresentAccountName(
		context.Background(),
		"account-1",
		"group1",
		"adventure_group_created_date",
		"2026-10-27",
		"active",
		now,
	); err != nil {
		t.Fatal(err)
	}

	accountCall := requireExecContaining(t, database, "`dnf_s1_w1`.`dnf_accounts`")
	assertContains(t, accountCall.Query, "INSERT INTO")
	assertContains(t, accountCall.Query, "represent_account_name")
	assertContains(t, accountCall.Query, "ON DUPLICATE KEY UPDATE")
	assertNotContains(t, accountCall.Query, "honor_exp")
	assertNotContains(t, accountCall.Query, "state =")
	if len(accountCall.Args) != 5 || accountCall.Args[0] != "account-1" || accountCall.Args[2] != "group1" {
		t.Fatalf("account upsert args=%#v", accountCall.Args)
	}

	metadataCall := requireExecContaining(t, database, "`dnf_s1_w1`.`dnf_account_metadata`")
	assertContains(t, metadataCall.Query, "ON DUPLICATE KEY UPDATE")
	if len(metadataCall.Args) != 3 ||
		metadataCall.Args[0] != "account-1" ||
		metadataCall.Args[1] != "adventure_group_created_date" ||
		metadataCall.Args[2] != "2026-10-27" {
		t.Fatalf("metadata upsert args=%#v", metadataCall.Args)
	}
	for _, call := range database.execs {
		assertNotContains(t, call.Query, "DELETE FROM")
	}
}

func TestMySQLRepresentAccountNameNarrowWriteUsesOneTransaction(t *testing.T) {
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

	if err := store.SaveRepresentAccountName(
		context.Background(),
		"account-1",
		"group1",
		"adventure_group_created_date",
		"2026-10-27",
		"active",
		time.Unix(2_000_000_000, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	if state.begins != 1 || state.commits != 1 || state.rollbacks != 0 || state.outsideTxOperations != 0 {
		t.Fatalf("transaction state=%+v", state)
	}
	if state.writes != 2 {
		t.Fatalf("write count=%d want=2 SQL=%v", state.writes, state.writeSQL)
	}
	if !strings.Contains(state.writeSQL[0], "`dnf_accounts`") ||
		!strings.Contains(state.writeSQL[1], "`dnf_account_metadata`") {
		t.Fatalf("write order=%v", state.writeSQL)
	}
}
