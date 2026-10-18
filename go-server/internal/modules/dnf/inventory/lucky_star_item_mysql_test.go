package inventory

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/rental"
	r "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

func itemTransactionMySQLFixture(t *testing.T) (*sql.DB, string, func() r.Group) {
	t.Helper()
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL not configured")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid disposable DSN")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open disposable DB")
	}
	t.Cleanup(func() { db.Close() })
	name := fmt.Sprintf("item_tx_test_%d", time.Now().UnixNano())
	plan := r.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err = repomysql.EnsureMySQLSchema(context.Background(), db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec("DROP DATABASE `" + name + "`"); err != nil {
			t.Error(err)
		}
	})
	return db, name, func() r.Group {
		g, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
}

func TestLuckyStarItemMySQLRollbackAndReplay(t *testing.T) {
	db, name, newGroup := itemTransactionMySQLFixture(t)
	ctx := context.Background()
	_, seed, q, now := luckyItemFixture(t)
	repos := newGroup()
	account, _, _ := seed.Account.Load(ctx, q.AccountID)
	actor, _, _ := seed.Character.Load(ctx, q.CharacterID)
	bag, _, _ := seed.Inventory.Load(ctx, q.CharacterID)
	for _, err := range []error{repos.Account.Save(ctx, account), repos.Character.Save(ctx, actor), repos.Inventory.Save(ctx, bag)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	owner, _ := NewOwner(repos)
	before, _, _ := repos.Inventory.Load(ctx, q.CharacterID)
	wallet, _, _ := repos.Account.Load(ctx, q.AccountID)
	// The final guard fails after both saves, proving the SQL transaction rolls
	// back inventory, wallet and receipt together.
	checks := 0
	q.Revalidate = func() bool { checks++; return checks < 2 }
	if _, err := owner.UseLuckyStarItem(ctx, q, now); err == nil {
		t.Fatal("late rejection accepted")
	}
	after, _, _ := newGroup().Inventory.Load(ctx, q.CharacterID)
	aa, _, _ := newGroup().Account.Load(ctx, q.AccountID)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(wallet, aa) {
		t.Fatal("late SQL rejection left changes")
	}
	q.Revalidate = func() bool { return true }
	start := make(chan struct{})
	done := make(chan error, 8)
	for range 8 {
		worker, _ := NewOwner(newGroup())
		go func() { <-start; _, err := worker.UseLuckyStarItem(ctx, q, now); done <- err }()
	}
	close(start)
	for range 8 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	after, _, _ = newGroup().Inventory.Load(ctx, q.CharacterID)
	aa, _, _ = newGroup().Account.Load(ctx, q.AccountID)
	if aa.Metadata[rental.PointMetadataKey] != "60" || after.Slots["0:87"].Count != 2 {
		t.Fatal("SQL duplicate consumption")
	}
	// Server-side failure after the inventory save must also roll back.
	if _, err := db.Exec("CREATE TRIGGER `" + name + "`.reject_wallet BEFORE UPDATE ON `" + name + "`.dnf_accounts FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected wallet failure'"); err != nil {
		t.Fatal(err)
	}
	q.RequestID = fmt.Sprintf("%064x", 2)
	if _, err := owner.UseLuckyStarItem(ctx, q, now); err == nil {
		t.Fatal("SQL save failure accepted")
	}
	b, _, _ := newGroup().Inventory.Load(ctx, q.CharacterID)
	a, _, _ := newGroup().Account.Load(ctx, q.AccountID)
	if !reflect.DeepEqual(after, b) || !reflect.DeepEqual(aa, a) {
		t.Fatal("failed save left debit or receipt")
	}
}
