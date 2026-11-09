//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
	"net"
	"os"
	"reflect"
	"regexp"
	"sync"
	"testing"
	"time"
)

// This runs against a new, narrowly named database. It never selects or mutates
// the installed game schema and reopens independent pools for durable readback.
func TestWeddingLifecycleDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires explicit disposable database authorization")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("requires TCP DSN without selected database")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("requires loopback database")
	}
	cfg.ParseTime, cfg.Loc = true, time.UTC
	open := func() *sql.DB {
		t.Helper()
		db, e := sql.Open("mysql", cfg.FormatDSN())
		if e != nil {
			t.Fatal("open disposable wedding database")
		}
		return db
	}
	db := open()
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	name := fmt.Sprintf("wedding_lifecycle_test_%d", time.Now().UnixNano())
	safe := regexp.MustCompile(`^wedding_lifecycle_test_[0-9]+$`)
	if !safe.MatchString(name) {
		t.Fatal("invalid isolated schema")
	}
	if _, err = db.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		t.Fatal("create isolated wedding database")
	}
	t.Cleanup(func() {
		if !safe.MatchString(name) {
			t.Error("refused unsafe schema cleanup")
			return
		}
		cleanup := open()
		defer cleanup.Close()
		drop, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, e := cleanup.ExecContext(drop, "DROP DATABASE `"+name+"`"); e != nil {
			t.Error("isolated wedding schema cleanup failed")
		}
	})
	plan := r.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err = EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	group := func(pool *sql.DB) r.Group {
		t.Helper()
		g, e := NewMySQLGroupFromDB(pool, MySQLGroupOptions{DatabasePlan: plan})
		if e != nil {
			t.Fatal(e)
		}
		return g
	}
	g := group(db)
	ids := []string{"2", "5", "9"}
	scope, _ := r.WeddingPairScope(ids[:2])
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for _, id := range ids {
		if err = g.Account.Save(ctx, r.AccountRecord{AccountID: "wedding-life-" + id, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err = r.SaveCharacterFields(ctx, g.Character, r.CharacterRecord{CharacterID: id, AccountID: "wedding-life-" + id, Name: "Life" + id, Job: "0", Level: 30, Stats: map[string]int64{"gold": 1000}, UpdatedAt: now}, r.AllCharacterFields()...); err != nil {
			t.Fatal(err)
		}
		if err = g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{"0:65": {ItemID: 10002765, Count: 2}}, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err = g.Quest.Save(ctx, r.QuestRecord{CharacterID: id, States: map[int64]r.QuestState{20737: {Status: "completed", UpdatedAt: now}}, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	type snapshot struct {
		c     map[string]r.CharacterRecord
		i     map[string]r.InventoryRecord
		q     map[string]r.QuestRecord
		m     map[string]r.MailboxRecord
		found map[string]bool
		s     r.SettingsRecord
		sf    bool
	}
	read := func() snapshot {
		t.Helper()
		pool := open()
		defer pool.Close()
		fresh := group(pool)
		v := snapshot{c: map[string]r.CharacterRecord{}, i: map[string]r.InventoryRecord{}, q: map[string]r.QuestRecord{}, m: map[string]r.MailboxRecord{}, found: map[string]bool{}}
		for _, id := range ids {
			var e error
			v.c[id], _, e = fresh.Character.Load(ctx, id)
			if e != nil {
				t.Fatal(e)
			}
			v.i[id], _, e = fresh.Inventory.Load(ctx, id)
			if e != nil {
				t.Fatal(e)
			}
			v.q[id], _, e = fresh.Quest.Load(ctx, id)
			if e != nil {
				t.Fatal(e)
			}
			v.m[id], v.found[id], e = fresh.Mailbox.Load(ctx, id)
			if e != nil {
				t.Fatal(e)
			}
		}
		v.s, v.sf, err = fresh.Settings.Load(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := read()
	u := g.CharacterTrade.(r.WeddingGuestLifecycleUnitOfWork)
	injected := errors.New("fault after actual SQL writes")
	mutate := func(tx r.Group) error {
		if err := tx.Settings.Save(ctx, r.SettingsRecord{Scope: r.WeddingGuestIndexScope, Values: map[string]string{"next": "1"}, UpdatedAt: now}); err != nil {
			return err
		}
		if _, _, e := tx.Character.Load(ctx, "10"); e == nil {
			return errors.New("character scope escaped")
		}
		if e := tx.Settings.Save(ctx, r.SettingsRecord{Scope: r.CharacterContainerStateScope("2")}); e == nil {
			return errors.New("read-only capacity scope became writable")
		}
		for _, id := range ids {
			c, _, e := tx.Character.Load(ctx, id)
			if e != nil {
				return e
			}
			c.Stats["gold"] -= 100
			if e = r.SaveCharacterFields(ctx, tx.Character, c, r.CharacterFieldStats); e != nil {
				return e
			}
			if e = tx.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id, UpdatedAt: now}); e != nil {
				return e
			}
			if e = tx.Quest.Save(ctx, r.QuestRecord{CharacterID: id, UpdatedAt: now}); e != nil {
				return e
			}
			if e = tx.Mailbox.Save(ctx, r.MailboxRecord{CharacterID: id, Mails: map[string]r.MailRecord{"lifecycle-mail-" + id: {MailID: "lifecycle-mail-" + id, RecipientCharacterID: id, Gold: 100, CreatedAt: now}}, UpdatedAt: now}); e != nil {
				return e
			}
		}
		return tx.Settings.Save(ctx, r.SettingsRecord{Scope: scope, Values: map[string]string{"receipt": "committed"}, UpdatedAt: now})
	}
	if err = u.WithinWeddingGuestLifecycle(ctx, ids[:2], ids[2:], scope, func(tx r.Group) error {
		if e := mutate(tx); e != nil {
			return e
		}
		return injected
	}); !errors.Is(err, injected) {
		t.Fatal("rollback path did not reach injected SQL failure", err)
	}
	if after := read(); !reflect.DeepEqual(before, after) {
		t.Fatal("failed multi-owner lifecycle left durable partial writes")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("callback panic swallowed")
			}
		}()
		_ = u.WithinWeddingGuestLifecycle(ctx, ids[:2], ids[2:], scope, func(tx r.Group) error {
			if e := mutate(tx); e != nil {
				t.Fatal(e)
			}
			panic("rollback actual SQL")
		})
	}()
	if after := read(); !reflect.DeepEqual(before, after) {
		t.Fatal("panic left durable partial writes")
	}
	// Independent pools race the same receipt. The scope row serializes even
	// the first insert; exactly one guest debit and each one-time mail survive.
	var wg sync.WaitGroup
	failures := make(chan error, 6)
	for n := 0; n < 6; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool := open()
			defer pool.Close()
			unit := group(pool).CharacterTrade.(r.WeddingGuestLifecycleUnitOfWork)
			failures <- unit.WithinWeddingGuestLifecycle(ctx, ids[:2], ids[2:], scope, func(tx r.Group) error {
				s, _, e := tx.Settings.Load(ctx, scope)
				if e != nil {
					return e
				}
				if s.Values["receipt"] != "" {
					return nil
				}
				return mutate(tx)
			})
		}()
	}
	wg.Wait()
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	after := read()
	if !after.sf || after.s.Values["receipt"] != "committed" {
		t.Fatal("missing durable receipt")
	}
	for _, id := range ids {
		if after.c[id].Stats["gold"] != 900 || len(after.i[id].Slots) != 0 || len(after.q[id].States) != 0 || len(after.m[id].Mails) != 1 {
			t.Fatal("duplicate debit or partial settlement", id)
		}
	}
}
