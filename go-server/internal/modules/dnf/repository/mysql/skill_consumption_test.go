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

func TestMySQLRankSkillConsumptionScopedLedgerAndRollback(t *testing.T) {
	for _, mode := range []string{"success", "foreign_owner", "counter_failure", "late_abort", "commit_failure", "panic"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			state := &activitySQLState{accountMailSQLState: accountMailSQLState{scenario: mode}}
			db := sql.OpenDB(activitySQLConnector{state})
			defer db.Close()
			router := mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"readonly"}, writeDBs: []string{"shard_a", "shard_b"}, tablePrefix: "dnf_", now: time.Now}
			var err error
			var panicked bool
			func() {
				defer func() { panicked = recover() != nil }()
				err = (&mysqlAccountCharacterAssetUnitOfWork{router: router}).WithinAccountCharacterSkillAssets(ctx, "a", "19", func(a repo.AccountInventoryRepository, c repo.CharacterRepository, s repo.SkillRepository) error {
					if _, _, err := a.Load(ctx, "other"); err == nil {
						t.Fatal("越账号访问未被拒绝")
					}
					if _, _, err := s.Load(ctx, "20"); err == nil {
						t.Fatal("越角色技能访问未被拒绝")
					}
					if err := repo.RecordCharacterColorless(ctx, c, repo.CharacterRecord{AccountID: "a", CharacterID: "19"}, 2, "skill", time.Now()); err != nil {
						return err
					}
					if mode == "late_abort" {
						return errors.New("模拟技能扣除后段失败")
					}
					if mode == "panic" {
						panic("模拟技能事务异常")
					}
					return nil
				})
			}()
			if mode == "success" {
				if err != nil || panicked || state.commits != 1 || state.rollbacks != 0 {
					t.Fatal(err, panicked, state)
				}
			} else {
				if err == nil && !panicked {
					t.Fatal("事务失败未传播")
				}
				if mode != "commit_failure" && (state.commits != 0 || state.rollbacks != 1) {
					t.Fatal(err, state)
				}
			}
			for _, q := range state.queries {
				if strings.Contains(q, "readonly") || !strings.HasSuffix(q, "FOR UPDATE") {
					t.Fatal("技能账本未锁定主库", q)
				}
			}
		})
	}
}
