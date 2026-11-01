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

func TestMySQLGoldOriginalTransactionsOwnershipAndRollback(t *testing.T) {
	for _, kind := range []string{"account", "mail", "trade", "warehouse"} {
		for _, mode := range []string{"success", "foreign_owner", "counter_failure", "late_abort", "commit_failure", "panic"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				ctx := context.Background()
				state := &activitySQLState{accountMailSQLState: accountMailSQLState{scenario: mode}}
				db := sql.OpenDB(activitySQLConnector{state})
				defer db.Close()
				router := mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"readonly"}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_", now: time.Now}
				apply := func(c repo.CharacterRepository) error {
					err := repo.RecordCharacterGold(ctx, c, repo.CharacterRecord{AccountID: "a", CharacterID: "19"}, repo.ActivityGoldRepair, 10, time.Now())
					if err != nil {
						return err
					}
					if mode == "late_abort" {
						return errors.New("后续资产失败")
					}
					if mode == "panic" {
						panic("事务异常")
					}
					return nil
				}
				var err error
				var panicked bool
				func() {
					defer func() { panicked = recover() != nil }()
					switch kind {
					case "account":
						err = (&mysqlAccountCharacterAssetUnitOfWork{router: router}).WithinAccountCharacterAssets(ctx, "a", "19", func(_ repo.AccountInventoryRepository, c repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository) error {
							return apply(c)
						})
					case "mail":
						err = (&mysqlMailboxAssetUnitOfWork{router: router}).WithinMailboxAssets(ctx, "19", "20", func(c repo.CharacterRepository, _ repo.InventoryRepository, _ repo.MailboxRepository) error {
							return apply(c)
						})
					case "trade":
						err = (&mysqlCharacterTradeUnitOfWork{router: router}).WithinCharacterTrade(ctx, "19", "20", func(c repo.CharacterRepository, _ repo.InventoryRepository) error { return apply(c) })
					case "warehouse":
						err = (&mysqlCharacterTradeUnitOfWork{router: router}).WithinCharacterWarehouseTrade(ctx, []string{"19", "20"}, []string{"a", "b"}, func(c repo.CharacterRepository, _ repo.InventoryRepository, _ repo.AccountInventoryRepository) error {
							return apply(c)
						})
					}
				}()
				if mode == "success" {
					if err != nil || panicked || state.commits != 1 || state.rollbacks != 0 {
						t.Fatal(err, panicked, state)
					}
				} else {
					if err == nil && !panicked {
						t.Fatal("失败未传播")
					}
					if mode != "commit_failure" && (state.commits != 0 || state.rollbacks != 1) {
						t.Fatal(err, state)
					}
				}
				for _, q := range state.queries {
					if strings.Contains(q, "readonly") || !strings.HasSuffix(q, "FOR UPDATE") {
						t.Fatal("统计脱离资产事务", q)
					}
				}
				writes := 0
				eventTable, _ := router.writeTable(activityEventsTable, "a")
				for _, q := range state.statements {
					if strings.Contains(q, "activity_events") {
						writes++
						if !strings.Contains(q, eventTable) {
							t.Fatal("账号分片错误", q)
						}
					}
				}
				if mode == "success" && writes != 1 {
					t.Fatal("未写唯一金币事件", state.statements)
				}
				if mode == "foreign_owner" && writes != 0 {
					t.Fatal("允许伪造账号")
				}
			})
		}
	}
}
