package mysql

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	repository "longheng.io/server/internal/modules/dnf/repository"
)

// 原子创建已要求真实事务；保留原“首次元数据／竞争不覆盖”断言，
// 将不支持BeginTx的旧fake改为可注入主键竞争的SQL事务驱动。
func accountCreateCompatibilityStore(t *testing.T, state *accountCreateSQLState) *mysqlAccountStore {
	t.Helper()
	database := sql.OpenDB(accountCreateConnector{state})
	t.Cleanup(func() { _ = database.Close() })
	return &mysqlAccountStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
		db: SQLDBAdapter{DB: database}, readDBs: []string{"readonly"}, writeDBs: []string{"dnf_s1_w1"}, tablePrefix: "dnf", now: time.Now,
	}}}
}

func TestMySQLAccountCreateIfAbsentWritesMetadataOnlyForNewAccount(t *testing.T) {
	state := &accountCreateSQLState{}
	store := accountCreateCompatibilityStore(t, state)
	var writer repository.AccountCreateIfAbsentWriter = store
	created, err := writer.CreateIfAbsent(context.Background(), repository.AccountRecord{
		AccountID: "a", State: "active", Metadata: map[string]string{
			"source": "dnfbridge", "account_record_bootstrap": "true", "account_record_bootstrap_source": "login_token_account_bound",
		}, CreatedAt: time.Unix(2_000_000_000, 0).UTC(), UpdatedAt: time.Unix(2_000_000_000, 0).UTC(),
	})
	if err != nil || !created {
		t.Fatalf("created=%t err=%v", created, err)
	}
	if len(state.calls) != 4 || state.metadataWrites != 3 || state.commits != 1 || state.rollbacks != 0 {
		t.Fatalf("账号与三个元数据未共同提交：%+v", state)
	}
	if !strings.Contains(state.calls[0], "`dnf_accounts`") || !strings.Contains(state.calls[0], "INSERT INTO") {
		t.Fatal(state.calls)
	}
	for _, query := range state.calls {
		if strings.Contains(query, "IGNORE") || strings.Contains(query, "ON DUPLICATE") || strings.Contains(query, "DELETE FROM") {
			t.Fatal("不得吞错或覆盖旧记录", query)
		}
	}
}

func TestMySQLAccountCreateIfAbsentLeavesConcurrentWinnerUntouched(t *testing.T) {
	state := &accountCreateSQLState{existing: true}
	store := accountCreateCompatibilityStore(t, state)
	created, err := store.CreateIfAbsent(context.Background(), repository.AccountRecord{AccountID: "a", State: "active", Metadata: map[string]string{"account_record_bootstrap": "true"}})
	if err != nil || created {
		t.Fatalf("created=%t err=%v", created, err)
	}
	if len(state.calls) != 2 || !strings.Contains(state.calls[1], "FOR UPDATE") || state.metadataWrites != 0 || state.commits != 0 || state.rollbacks != 1 {
		t.Fatalf("竞争者更新了赢家数据，或未从写库核对：%+v", state)
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
