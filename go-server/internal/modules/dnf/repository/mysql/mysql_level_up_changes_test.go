package mysql

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/progression"
	r "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestLevelUpNotificationCommitRollbackAndUnchangedSave(t *testing.T) {
	for _, mode := range []string{"commit", "rollback", "failed_commit", "unchanged", "receipt_only"} {
		t.Run(mode, func(t *testing.T) {
			feed := &r.LevelUpChangeFeed{}
			calls := 0
			defer feed.Subscribe(func(id string) {
				if id != "77" {
					t.Fatal(id)
				}
				calls++
			})()
			raw := &battlePassCommitFixture{}
			tx := &committedSQLTransaction{sqlTransaction: raw}
			record := r.CharacterRecord{CharacterID: "77", AccountID: "a", Level: 85, Stats: map[string]int64{r.LevelUpEventKey(85): 1}}
			var previous *r.CharacterRecord
			if mode == "unchanged" {
				c := r.CloneCharacter(record)
				previous = &c
			}
			if mode == "receipt_only" {
				record.Stats[r.LevelUpEventKey(85)] = -1
			}
			stageLevelUpChange(sqlTransactionDB{tx: tx}, feed, record, previous, map[r.CharacterField]struct{}{r.CharacterFieldStats: {}})
			if calls != 0 {
				t.Fatal("uncommitted notification")
			}
			if mode == "rollback" {
				tx.Rollback()
			} else {
				if mode == "failed_commit" {
					raw.err = errors.New("commit failed")
				}
				tx.Commit()
			}
			want := 0
			if mode == "commit" {
				want = 1
			}
			if calls != want {
				t.Fatal(calls, want)
			}
			tx.Rollback()
			if calls != want {
				t.Fatal("replayed")
			}
		})
	}
}

func TestProgressionTransactionPublishesOnlySuccessfulLevelCommit(t *testing.T) {
	for _, failure := range []int{0, 2, 3} {
		state := progressionCommitState(89, 100, progressionCommitPoints(89))
		state.failExec = failure
		u := newProgressionCommitDriverUOW(t, state).(*mysqlCharacterProgressionUnitOfWork)
		calls := 0
		unsubscribe := u.router.levelUpChanges.Subscribe(func(id string) {
			calls++
			if state.commits != 1 {
				t.Error("not committed")
			}
		})
		_, err := progression.Commit(context.Background(), u, progressionCommitPlan(state, 90, 140))
		unsubscribe()
		if (err == nil) != (failure == 0) || (calls == 1) != (failure == 0) {
			t.Fatal(failure, calls, err)
		}
	}
}
