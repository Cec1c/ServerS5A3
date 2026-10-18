package mysql

import (
	"context"
	"database/sql"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

func TestBoosterLedgerMySQLUsesOneLockedTransaction(t *testing.T) {
	for _, mode := range []string{"success", "scope", "late_abort", "write_failure", "commit_failure", "panic", "cancel", "late_write"} {
		t.Run(mode, func(t *testing.T) {
			state := &epicBookSQLState{accountMailSQLState: accountMailSQLState{scenario: mode}}
			db := sql.OpenDB(epicBookSQLConnector{state})
			defer db.Close()
			u := &mysqlAccountCharacterAssetUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"readonly"}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_", now: time.Now}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var err error
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				err = u.WithinAccountCharacterLedgerAssets(ctx, "a", "19", func(a repo.AccountRepository, _ repo.AccountInventoryRepository, _ repo.CharacterRepository, i repo.InventoryRepository, _ repo.EquipmentRepository) error {
					if mode == "scope" {
						_, _, e := a.Load(ctx, "foreign")
						return e
					}
					account, _, e := a.Load(ctx, "a")
					if e != nil {
						return e
					}
					account.Metadata[repo.AccountEpicPieceMetadataKey(10300000)] = "1030"
					if e = a.Save(ctx, account); e != nil {
						return e
					}
					if e = i.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:9": {ItemID: 600, Count: 1}}}); e != nil {
						return e
					}
					if mode == "late_abort" {
						return errors.New("开盒后续奖励失败")
					}
					if mode == "panic" {
						panic("开盒异常")
					}
					if mode == "cancel" {
						cancel()
					}
					return nil
				})
			}()
			if mode == "success" {
				if err != nil || state.begins != 1 || state.commits != 1 || state.rollbacks != 0 {
					t.Fatal("礼包未在单个事务提交", err, state)
				}
			} else if (!panicked && err == nil) || state.begins != 1 || (state.commits > 0 && mode != "commit_failure") {
				t.Fatal("失败礼包仍提交", err, state)
			}
			if mode != "success" && mode != "commit_failure" && state.rollbacks != 1 {
				t.Fatal("失败礼包没有回滚", state)
			}
			if len(state.queries) == 0 || !strings.Contains(state.queries[0], "FOR UPDATE") || !strings.Contains(state.queries[0], "account") {
				t.Fatal("未先锁账号", state.queries)
			}
			for _, q := range state.queries {
				if strings.Contains(q, "readonly") {
					t.Fatal("查询逃出写事务", q)
				}
			}
		})
	}
}
