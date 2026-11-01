//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Hold the account lock until the competing settlement is observed waiting,
// then change its character row. This reproduces the read-view/locking-read
// conflict without scheduler sleeps or any player database.
func TestMySQLSettlementOwnerLookupSurvivesConcurrentCharacterUpdate(t *testing.T) {
	if os.Getenv("DNF_FATE_DISPOSABLE_DB") != "1" {
		t.Fatal("requires explicitly disposable database")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("requires isolated TCP DSN without selected database")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("requires loopback database")
	}
	cfg.ParseTime = true
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	open := func() *sql.DB {
		t.Helper()
		db, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		t.Cleanup(func() { db.Close() })
		return db
	}
	observer, blocker, worker := open(), open(), open()
	name := fmt.Sprintf("settlement_owner_%d", time.Now().UnixNano())
	plan := repo.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := EnsureMySQLSchema(ctx, observer, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := observer.ExecContext(cleanup, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	})
	group, err := NewMySQLGroupFromDB(worker, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	var blockerID, workerID int64
	if err := blocker.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&blockerID); err != nil {
		t.Fatal(err)
	}
	if err := worker.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&workerID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if err := group.Account.Save(ctx, repo.AccountRecord{AccountID: id}); err != nil {
			t.Fatal(err)
		}
	}
	for _, changedOwner := range []bool{false, true} {
		t.Run(fmt.Sprintf("owner-changed-%t", changedOwner), func(t *testing.T) {
			if err := group.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "a", Name: "fixture", Level: 20}); err != nil {
				t.Fatal(err)
			}
			first, err := blocker.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Rollback()
			var owner string
			if err := first.QueryRowContext(ctx, "SELECT account_id FROM `"+name+"`.`dnf_accounts` WHERE account_id=? FOR UPDATE", "a").Scan(&owner); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			called := false
			go func() {
				done <- group.CharacterSettlement.WithinCharacterSettlement(ctx, "19", func(tx repo.Group) error {
					called = true
					ch, found, err := tx.Character.Load(ctx, "19")
					if err != nil {
						return err
					}
					if !found || ch.AccountID != "a" || ch.Level != 21 {
						return fmt.Errorf("stale character after lock: found=%t level=%d", found, ch.Level)
					}
					ch.Stats = map[string]int64{"settlement_fixture": 1}
					return repo.SaveCharacterFields(ctx, tx.Character, ch, repo.CharacterFieldStats)
				})
			}()
			waiting := false
			for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(150 * time.Millisecond):
				}
				var count int
				err := observer.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.INNODB_LOCK_WAITS w JOIN information_schema.INNODB_TRX r ON r.trx_id=w.requesting_trx_id JOIN information_schema.INNODB_TRX b ON b.trx_id=w.blocking_trx_id WHERE r.trx_mysql_thread_id=? AND b.trx_mysql_thread_id=?`, workerID, blockerID).Scan(&count)
				if err != nil {
					t.Fatal(err)
				}
				if count > 0 {
					waiting = true
					break
				}
			}
			if !waiting {
				first.Rollback()
				t.Fatalf("no actual account lock wait: %v", <-done)
			}
			newOwner := "a"
			if changedOwner {
				newOwner = "b"
			}
			if _, err := first.ExecContext(ctx, "UPDATE `"+name+"`.`dnf_characters` SET level=21,account_id=? WHERE character_id='19'", newOwner); err != nil {
				t.Fatal(err)
			}
			if err := first.Commit(); err != nil {
				t.Fatal(err)
			}
			err = <-done
			if changedOwner {
				if !errors.Is(err, repo.ErrCharacterSettlementTransactionUnavailable) || called {
					t.Fatalf("ownership change err=%v callback=%t", err, called)
				}
			} else if err != nil || !called {
				t.Fatalf("concurrent update err=%v callback=%t", err, called)
			}
			ch, found, err := group.Character.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("readback found=%t err=%v", found, err)
			}
			want := int64(1)
			if changedOwner {
				want = 0
			}
			if ch.Level != 21 || ch.AccountID != newOwner || ch.Stats["settlement_fixture"] != want {
				t.Fatalf("readback level=%d owner=%s metric=%d", ch.Level, ch.AccountID, ch.Stats["settlement_fixture"])
			}
		})
	}
}
