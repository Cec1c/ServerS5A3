package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestMySQLAccountMetadataEntryReadUsesOwnedSingleQuery(t *testing.T) {
	database := &fakeSQLDB{rows: []fakeSQLRow{{values: []any{"premium_expire_84", "2000000000"}}}}
	store := newTestMySQLGroup(t, database).Account.(repository.AccountMetadataEntryReader)
	value, found, err := store.LoadMetadataEntry(context.Background(), " account-1 ", " premium_expire_84 ")
	if err != nil || !found || value != "2000000000" {
		t.Fatalf("窄读取结果不符：%q %t %v", value, found, err)
	}
	call := requireOneExec(t, database)
	for _, part := range []string{"SELECT m.entry_key, m.entry_value", "INNER JOIN", "m.account_id = a.account_id", "a.account_id = ?", "m.entry_key = ?", "`dnf_s1_r1`.`dnf_accounts`", "`dnf_s1_r1`.`dnf_account_metadata`"} {
		if !strings.Contains(call.Query, part) {
			t.Fatalf("未保留账号存在性、读路由或指定键边界：%s", call.Query)
		}
	}
	if len(call.Args) != 2 || call.Args[0] != "account-1" || call.Args[1] != "premium_expire_84" {
		t.Fatal("账号与键未参数化", call.Args)
	}
	for _, text := range []string{"ORDER BY", "honor_exp", "UPDATE", "DELETE"} {
		if strings.Contains(call.Query, text) {
			t.Fatal("只读窄查询加载了完整账号或包含写入", call.Query)
		}
	}
}

func TestMySQLAccountMetadataEntryReadMissingAndError(t *testing.T) {
	broken := errors.New("读取失败")
	for _, e := range []error{sql.ErrNoRows, broken} {
		database := &fakeSQLDB{rows: []fakeSQLRow{{err: e}}}
		reader := newTestMySQLGroup(t, database).Account.(repository.AccountMetadataEntryReader)
		value, found, err := reader.LoadMetadataEntry(context.Background(), "account-1", "premium_expire_84")
		if found || value != "" || (e == broken && !errors.Is(err, broken)) || (e == sql.ErrNoRows && err != nil) {
			t.Fatalf("缺失与数据库错误混淆：%q %t %v", value, found, err)
		}
		requireOneExec(t, database)
	}
}

func TestMySQLAccountMetadataEntryReadValidatesBeforeSQLAndKeepsLockRoute(t *testing.T) {
	database := &fakeSQLDB{}
	store := newTestMySQLGroup(t, database).Account.(*mysqlAccountStore)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		ctx          context.Context
		account, key string
	}{{ctx, "a", "k"}, {context.Background(), " ", "k"}, {context.Background(), "a", " "}} {
		if _, _, err := store.LoadMetadataEntry(tc.ctx, tc.account, tc.key); err == nil {
			t.Fatal("非法参数或取消未被拒绝")
		}
	}
	if len(database.execs) != 0 {
		t.Fatal("参数验证前执行了SQL")
	}
	locked := *store
	// 与实际事务构造器一致：读库先改为写库，再启用锁定读。
	locked.router.readDBs = append([]string(nil), locked.router.writeDBs...)
	locked.router.lockReads = true
	_, _, err := locked.LoadMetadataEntry(context.Background(), "account-1", "premium_expire_84")
	if err != nil {
		t.Fatal(err)
	}
	call := requireOneExec(t, database)
	if !strings.HasSuffix(call.Query, "FOR UPDATE") || !strings.Contains(call.Query, "`dnf_s1_w1`.`dnf_accounts`") {
		t.Fatal("未保留事务内的写库及锁定读路由", call.Query)
	}
}

func TestMySQLAccountMetadataEntryReadPreservesExactDictionaryKey(t *testing.T) {
	for _, key := range []string{"PREMIUM_EXPIRE_84", "premium_expire_84 "} {
		database := &fakeSQLDB{rows: []fakeSQLRow{{values: []any{key, "2000000000"}}}}
		reader := newTestMySQLGroup(t, database).Account.(repository.AccountMetadataEntryReader)
		value, found, err := reader.LoadMetadataEntry(context.Background(), "account-1", "premium_expire_84")
		if err != nil || found || value != "" {
			t.Fatal("数据库宽松校对改变了原字典精确键语义", key, value, found, err)
		}
		requireOneExec(t, database)
	}
}
