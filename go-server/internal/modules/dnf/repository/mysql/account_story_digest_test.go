// 本文件覆盖账号级"剧情回顾已看过"记录的 MySQL 写入形状。
package mysql

import (
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestMySQLAccountStoryDigestViewedLevelUsesOneMonotonicUpsert(t *testing.T) {
	sqlDB := &fakeSQLDB{}
	repositories := newTestMySQLGroup(t, sqlDB)
	if err := repository.AdvanceAccountStoryDigestViewedLevel(
		context.Background(),
		repositories.Account,
		"dnf:10001",
		90,
	); err != nil {
		t.Fatal(err)
	}
	if len(sqlDB.execs) != 2 {
		t.Fatalf("exec count = %d, want account touch + metadata upsert: %v", len(sqlDB.execs), execQueries(sqlDB))
	}
	accountCall := requireExecContaining(t, sqlDB, "`dnf_s1_w1`.`dnf_accounts` SET updated_at")
	if len(accountCall.Args) != 2 || accountCall.Args[1] != "dnf:10001" {
		t.Fatalf("account update args=%#v", accountCall.Args)
	}
	wantTime := time.Date(2026, 6, 29, 3, 0, 0, 0, time.UTC)
	if got, ok := accountCall.Args[0].(time.Time); !ok || !got.Equal(wantTime) {
		t.Fatalf("updated_at = %#v, want %s", accountCall.Args[0], wantTime)
	}

	metadataCall := requireExecContaining(t, sqlDB, "`dnf_s1_w1`.`dnf_account_metadata`")
	assertContains(t, metadataCall.Query, "ON DUPLICATE KEY UPDATE")
	// GREATEST(旧值, 新值)：重放、并发与乱序上报都不会让账号记录回退。
	assertContains(
		t,
		metadataCall.Query,
		"`entry_value` = GREATEST(CAST(`entry_value` AS UNSIGNED), CAST(VALUES(`entry_value`) AS UNSIGNED))",
	)
	if len(metadataCall.Args) != 3 ||
		metadataCall.Args[0] != "dnf:10001" ||
		metadataCall.Args[1] != repository.AccountStoryDigestViewedLevelMetadataKey ||
		metadataCall.Args[2] != "90" {
		t.Fatalf("metadata upsert args=%#v", metadataCall.Args)
	}
	for _, call := range sqlDB.execs {
		assertNotContains(t, call.Query, "DELETE FROM")
	}
}

func TestMySQLAccountStoryDigestViewedLevelReplayIsIdempotent(t *testing.T) {
	sqlDB := &fakeSQLDB{}
	repositories := newTestMySQLGroup(t, sqlDB)
	for attempt := 0; attempt < 3; attempt++ {
		if err := repository.AdvanceAccountStoryDigestViewedLevel(
			context.Background(),
			repositories.Account,
			"dnf:10001",
			90,
		); err != nil {
			t.Fatal(err)
		}
	}
	// 三次重放写出逐字节相同的语句与参数：单调性由 SQL 的 GREATEST 保证，
	// 不需要调用方先读后写，因此不存在并发回退窗口。
	first := requireExecContaining(t, sqlDB, "`dnf_s1_w1`.`dnf_account_metadata`")
	if len(sqlDB.execs) != 6 {
		t.Fatalf("exec count = %d, want 6: %v", len(sqlDB.execs), execQueries(sqlDB))
	}
	for index := 0; index < 6; index += 2 {
		if sqlDB.execs[index].Query != sqlDB.execs[0].Query {
			t.Fatalf("replay %d wrote a different query: %v", index, sqlDB.execs[index].Query)
		}
		if sqlDB.execs[index+1].Query != first.Query {
			t.Fatalf("replay %d metadata query = %q, want %q", index, sqlDB.execs[index+1].Query, first.Query)
		}
	}
}

func TestMySQLAccountStoryDigestViewedLevelRejectsBlankAccount(t *testing.T) {
	sqlDB := &fakeSQLDB{}
	repositories := newTestMySQLGroup(t, sqlDB)
	if err := repository.AdvanceAccountStoryDigestViewedLevel(context.Background(), repositories.Account, "   ", 90); err == nil {
		t.Fatal("blank account id advanced the record")
	}
	if len(sqlDB.execs) != 0 {
		t.Fatalf("blank account id wrote SQL: %v", execQueries(sqlDB))
	}
}
