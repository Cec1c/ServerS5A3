package mysql

import (
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

type battlePassCommitFixture struct {
	sqlTransaction
	err                error
	commits, rollbacks int
}

func (f *battlePassCommitFixture) Commit() error   { f.commits++; return f.err }
func (f *battlePassCommitFixture) Rollback() error { f.rollbacks++; return nil }
func TestBattlePassNotifyAfterCommitOnly(t *testing.T) {
	for _, mode := range []string{"成功", "失败", "回滚"} {
		t.Run(mode, func(t *testing.T) {
			feed := repo.NewBattlePassChangeFeed()
			calls := 0
			feed.Subscribe("a", func(e repo.BattlePassChange) {
				calls++
				if e.Counters["battlepass2:2026-10-31:17:-1"] != 1 {
					t.Fatal("计数丢失")
				}
			})
			raw := &battlePassCommitFixture{}
			tx := &committedSQLTransaction{sqlTransaction: raw}
			db := sqlTransactionDB{tx: tx}
			e := repo.BattlePassChange{AccountID: "a", CharacterID: "2", Counters: map[string]int64{"battlepass2:2026-10-31:17:-1": 1}}
			stageBattlePassChange(db, feed, e)
			if calls != 0 {
				t.Fatal("提交前通知")
			}
			if mode == "回滚" {
				_ = tx.Rollback()
			} else {
				if mode == "失败" {
					raw.err = errors.New("提交失败")
				}
				_ = tx.Commit()
			}
			want := 0
			if mode == "成功" {
				want = 1
			}
			if calls != want {
				t.Fatal("事务提醒不符", calls, want)
			}
			_ = tx.Rollback()
			if calls != want {
				t.Fatal("重复发布")
			}
		})
	}
}
