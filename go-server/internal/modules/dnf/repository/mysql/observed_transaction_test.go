package mysql

import (
	"context"
	"database/sql"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/metrics"
	"testing"
	"time"
)

func TestObservedSQLTransactionCompletesOnceAndKeepsConnection(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{true: "commit", false: "rollback"}[commit], func(t *testing.T) {
			db, state := openPreparedSQLFixture(t)
			prepared := NewPreparedSQLDB(db, 4)
			defer prepared.Close()
			o := NewSQLObservation(metrics.New(), db)
			adapter := &ObservedSQLDB{SQLDB: prepared, Observation: o}
			tx, err := beginObservedSQLTransaction(context.Background(), adapter, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			tx.started = time.Now().Add(-time.Second)
			scoped := newSQLTransactionDB(tx.Tx, adapter)
			if scoped.tx != tx.Tx || scoped.prepared != prepared || scoped.observation != o {
				t.Fatal("transaction ownership changed")
			}
			if _, err := scoped.ExecContext(context.Background(), "UPDATE observation SET n=?", 1); err != nil {
				t.Fatal(err)
			}
			feed := repo.NewBattlePassChangeFeed()
			notifications := 0
			feed.Subscribe("account", func(repo.BattlePassChange) { notifications++ })
			stageBattlePassChange(scoped, feed, repo.BattlePassChange{AccountID: "account", Fatigue: true})
			if notifications != 0 {
				t.Fatal("published before observed/prepared transaction committed")
			}
			if observationCount(&o.series[1][sqlObserveTransaction]) != 0 {
				t.Fatal("observed before completion")
			}
			if commit {
				err = tx.Commit()
			} else {
				err = tx.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
			wantNotifications := 0
			if commit {
				wantNotifications = 1
			}
			if notifications != wantNotifications {
				t.Fatal("feed detached from observed transaction", notifications)
			}
			if err = tx.Rollback(); !errors.Is(err, sql.ErrTxDone) {
				t.Fatal(err)
			}
			if notifications != wantNotifications {
				t.Fatal("cleanup published twice")
			}
			s := &o.series[1][sqlObserveTransaction]
			if observationCount(s) != 1 || s.nanos.Load() < int64(time.Second) || s.errors.Load() != 0 {
				t.Fatal("wrong lifetime or duplicate cleanup")
			}
			c := state.snapshot()
			if c.begins != 1 || c.commits+c.rollbacks != 1 {
				t.Fatalf("unexpected SQL transaction operations: %+v", c)
			}
		})
	}
}

func TestObservedSQLTransactionBeginFailureHasNoLifetime(t *testing.T) {
	db, _ := openPreparedSQLFixture(t)
	o := NewSQLObservation(metrics.New(), db)
	adapter := &ObservedSQLDB{SQLDB: SQLDBAdapter{DB: db}, Observation: o}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tx, err := beginObservedSQLTransaction(ctx, adapter, nil)
	if tx != nil || !errors.Is(err, context.Canceled) || observationCount(&o.series[1][sqlObserveTransaction]) != 0 {
		t.Fatal(tx, err)
	}
}
