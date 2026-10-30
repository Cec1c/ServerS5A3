//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/raidcurrency"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// TestMySQLFateLedgerInterleavesWithExistingAccountWriters exercises actual
// InnoDB lock waits through two independent connection pools. The third pool
// observes INNODB_LOCK_WAITS before releasing the first transaction; elapsed
// sleep alone is never accepted as evidence that the competing writer ran.
func TestMySQLFateLedgerInterleavesWithExistingAccountWriters(t *testing.T) {
	if os.Getenv("DNF_FATE_DISPOSABLE_DB") != "1" {
		t.Fatal("requires an explicitly isolated Fate test database instance")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" {
		t.Fatal("invalid isolated loopback DSN")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" || cfg.DBName != "" {
		t.Fatal("test requires loopback and no existing database name")
	}
	cfg.ParseTime = true
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	open := func() *sql.DB {
		t.Helper()
		db, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			t.Fatal("open isolated database")
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if err := db.PingContext(ctx); err != nil {
			db.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	observer, left, right := open(), open(), open()
	var leftID, rightID int64
	if err := left.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&leftID); err != nil {
		t.Fatal(err)
	}
	if err := right.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&rightID); err != nil {
		t.Fatal(err)
	}
	if leftID == rightID {
		t.Fatal("not independent database connections")
	}
	name := fmt.Sprintf("fate_lock_%d", time.Now().UnixNano())
	if !regexp.MustCompile(`^fate_lock_[0-9]+$`).MatchString(name) {
		t.Fatal("unsafe fixture name")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := observer.ExecContext(cleanup, "DROP DATABASE IF EXISTS `"+name+"`"); err != nil {
			t.Error(err)
		}
	})
	plan := repo.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := EnsureMySQLSchema(ctx, observer, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	group := func(db *sql.DB) repo.Group {
		t.Helper()
		g, err := NewMySQLGroupFromDB(db, MySQLGroupOptions{DatabasePlan: plan})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	l, r := group(left), group(right)
	now := time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC)
	const accountID, characterID = "fate-lock-account", "19"
	seed := func() {
		t.Helper()
		if err := l.Account.Save(ctx, repo.AccountRecord{AccountID: accountID, State: "active", Metadata: map[string]string{"keep": "original"}}); err != nil {
			t.Fatal(err)
		}
		if err := l.Character.Save(ctx, repo.CharacterRecord{CharacterID: characterID, AccountID: accountID, Name: "FateLock", Job: "0", Level: 90, Stats: map[string]int64{"gold": 99}}); err != nil {
			t.Fatal(err)
		}
	}
	credit := func(a repo.AccountRepository) error {
		v, found, err := a.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("account missing")
		}
		granted, _, err := raidcurrency.Credit(&v, 37, now)
		if err != nil {
			return err
		}
		if granted != 37 {
			return fmt.Errorf("grant=%d", granted)
		}
		return a.Save(ctx, v)
	}
	mark := func(a repo.AccountRepository) error {
		v, found, err := a.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("account missing")
		}
		if v.Metadata == nil {
			v.Metadata = map[string]string{}
		}
		v.Metadata["other_writer"] = "committed"
		return a.Save(ctx, v)
	}
	type transaction func(repo.Group, func() error) error
	ledger := func(g repo.Group, barrier func() error) error {
		return g.AccountAssets.(repo.AccountCharacterLedgerAssetUnitOfWork).WithinAccountCharacterLedgerAssets(ctx, accountID, characterID, func(a repo.AccountRepository, _ repo.AccountInventoryRepository, c repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository) error {
			if err := barrier(); err != nil {
				return err
			}
			v, found, err := c.Load(ctx, characterID)
			if err != nil {
				return err
			}
			if !found || v.AccountID != accountID {
				return fmt.Errorf("character ownership changed")
			}
			return credit(a)
		})
	}
	writers := []struct {
		name string
		run  transaction
	}{
		{"character-settlement", func(g repo.Group, b func() error) error {
			return g.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx repo.Group) error {
				if err := b(); err != nil {
					return err
				}
				return mark(tx.Account)
			})
		}},
		{"guild-settlement", func(g repo.Group, b func() error) error {
			return g.CharacterSettlement.(repo.GuildSettlementUnitOfWork).WithinGuildSettlement(ctx, repo.GuildOwner{AccountID: accountID, CharacterID: characterID}, "lock-test", func(tx repo.Group) error {
				if err := b(); err != nil {
					return err
				}
				return mark(tx.Account)
			})
		}},
		{"account-mailbox", func(g repo.Group, b func() error) error {
			return g.MailboxAssets.(repo.AccountMailboxUnitOfWork).WithinAccountMailbox(ctx, accountID, characterID, func(a repo.AccountRepository, _ repo.MailboxRepository) error {
				if err := b(); err != nil {
					return err
				}
				return mark(a)
			})
		}},
	}
	awaitWait := func(requester, blocker int64) error {
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			// MariaDB 11.4 keeps a shared transaction/lock snapshot until it
			// has been unread for >100ms (trx0i_s.cc can_cache_be_updated).
			// Leave that idle interval before EVERY read, including the first
			// one in a new case. Faster polling can pin an empty snapshot or
			// falsely reuse the preceding case's wait for these same pools.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
			var count int
			err := observer.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.INNODB_LOCK_WAITS w JOIN information_schema.INNODB_TRX r ON r.trx_id=w.requesting_trx_id JOIN information_schema.INNODB_TRX b ON b.trx_id=w.blocking_trx_id WHERE r.trx_mysql_thread_id=? AND b.trx_mysql_thread_id=?`, requester, blocker).Scan(&count)
			if err != nil {
				return err
			}
			if count > 0 {
				return nil
			}
		}
		return fmt.Errorf("no actual InnoDB lock wait from %d to %d", requester, blocker)
	}
	for _, writer := range writers {
		for _, ledgerFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/ledger-first-%t", writer.name, ledgerFirst), func(t *testing.T) {
				seed()
				first, second := transaction(ledger), writer.run
				if !ledgerFirst {
					first, second = writer.run, transaction(ledger)
				}
				locked, release := make(chan struct{}), make(chan struct{})
				firstDone, secondDone := make(chan error, 1), make(chan error, 1)
				go func() {
					firstDone <- first(l, func() error {
						close(locked)
						select {
						case <-release:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					})
				}()
				select {
				case <-locked:
				case err := <-firstDone:
					t.Fatalf("first transaction before barrier: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				go func() { secondDone <- second(r, func() error { return nil }) }()
				waitErr := awaitWait(rightID, leftID)
				close(release)
				e1, e2 := <-firstDone, <-secondDone
				if waitErr != nil || e1 != nil || e2 != nil {
					t.Fatalf("wait=%v first=%v second=%v", waitErr, e1, e2)
				}
				v, found, err := l.Account.Load(ctx, accountID)
				if err != nil || !found {
					t.Fatalf("account found=%t err=%v", found, err)
				}
				s, err := raidcurrency.Read(v, now)
				if err != nil || s.Balance != 37 || s.Earned != 37 || v.Metadata["keep"] != "original" || v.Metadata["other_writer"] != "committed" {
					t.Fatalf("lost metadata: state=%+v metadata=%v err=%v", s, v.Metadata, err)
				}
			})
		}
	}
	for _, kind := range []string{"narrow-name", "create-if-absent"} {
		t.Run(kind+"/after-ledger-lock", func(t *testing.T) {
			seed()
			locked, release := make(chan struct{}), make(chan struct{})
			firstDone, secondDone := make(chan error, 1), make(chan error, 1)
			go func() {
				firstDone <- ledger(l, func() error {
					close(locked)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
			select {
			case <-locked:
			case err := <-firstDone:
				t.Fatalf("ledger before barrier: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			go func() {
				if kind == "narrow-name" {
					secondDone <- r.Account.(*mysqlAccountStore).SaveRepresentAccountName(ctx, accountID, "FateGroup", "adventure_group_created_date", "2026-10-27", "active", now)
					return
				}
				created, err := r.Account.(repo.AccountCreateIfAbsentWriter).CreateIfAbsent(ctx, repo.AccountRecord{AccountID: accountID, State: "bootstrap", Metadata: map[string]string{"stale": "must-not-appear"}})
				if err == nil && created {
					err = fmt.Errorf("bootstrap replaced existing account")
				}
				secondDone <- err
			}()
			waitErr := awaitWait(rightID, leftID)
			close(release)
			e1, e2 := <-firstDone, <-secondDone
			if waitErr != nil || e1 != nil || e2 != nil {
				t.Fatalf("wait=%v ledger=%v writer=%v", waitErr, e1, e2)
			}
			v, found, err := l.Account.Load(ctx, accountID)
			if err != nil || !found {
				t.Fatalf("account found=%t err=%v", found, err)
			}
			s, err := raidcurrency.Read(v, now)
			if err != nil || s.Balance != 37 || s.Earned != 37 || v.Metadata["keep"] != "original" || v.Metadata["stale"] != "" || v.State != "active" {
				t.Fatalf("lost metadata: state=%+v metadata=%v err=%v", s, v.Metadata, err)
			}
			if kind == "narrow-name" && (v.RepresentAccountName != "FateGroup" || !strings.HasPrefix(v.Metadata["adventure_group_created_date"], "2026-10-27")) {
				t.Fatalf("nickname missing: %+v", v)
			}
		})
	}
}
