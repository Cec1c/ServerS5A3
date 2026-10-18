package signupbenefits_test

import (
	"context"
	"database/sql"
	"fmt"
	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/characterdata"
	"longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
	"longheng.io/server/internal/modules/dnf/signupbenefits"
	"os"
	"testing"
	"time"
)

func TestSignupPolicyMySQL(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	ctx := context.Background()
	admin, err := sql.Open("mysql", dsn)
	require(t, err)
	defer admin.Close()
	name := fmt.Sprintf("benefits_test_%d", time.Now().UnixNano())
	_, err = admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4")
	require(t, err)
	defer admin.Exec("DROP DATABASE `" + name + "`")
	cfg, err := driver.ParseDSN(dsn)
	require(t, err)
	cfg.DBName = name
	db, err := sql.Open("mysql", cfg.FormatDSN())
	require(t, err)
	defer db.Close()
	plan := repository.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	require(t, repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{AutoCreate: true, DatabasePlan: plan}))
	g, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
	require(t, err)
	_, err = db.Exec("CREATE TABLE web_accounts (id INT PRIMARY KEY, account_id VARCHAR(64) NOT NULL)")
	require(t, err)
	s, err := signupbenefits.NewStore(db, "web_accounts")
	require(t, err)
	require(t, s.EnsureSchema(ctx))
	p, err := s.Policy(ctx)
	require(t, err)
	if p.AccountCera != 0 || p.CharacterGold != 0 {
		t.Fatal(p)
	}
	p.AccountCera = 200
	p.CharacterGold = 300
	p, err = s.SavePolicy(ctx, p)
	require(t, err)
	if _, err = s.SavePolicy(ctx, signupbenefits.Policy{}); err == nil {
		t.Fatal("stale revision accepted")
	}
	// Entitlement and registration must disappear together on rollback.
	tx, err := db.BeginTx(ctx, nil)
	require(t, err)
	require(t, s.CaptureSignup(ctx, tx, "rolled-back"))
	require(t, tx.Rollback())
	amount, err := s.SignupCera(ctx, "rolled-back")
	require(t, err)
	if amount != 0 {
		t.Fatal(amount)
	}
	tx, err = db.BeginTx(ctx, nil)
	require(t, err)
	require(t, s.CaptureSignup(ctx, tx, "new"))
	require(t, tx.Commit())
	p.AccountCera = 900
	p, err = s.SavePolicy(ctx, p)
	require(t, err)
	amount, err = s.SignupCera(ctx, "new")
	require(t, err)
	if amount != 200 {
		t.Fatal("signup snapshot changed")
	}
	old, err := s.SignupCera(ctx, "old")
	require(t, err)
	if old != 0 {
		t.Fatal("old account granted")
	}
	_, err = db.Exec("INSERT INTO web_accounts VALUES (1,'new')")
	require(t, err)
	require(t, g.Account.Save(ctx, repository.AccountRecord{AccountID: "new"}))
	creator := characterdata.NewCreator(g)
	for i := 1; i <= 12; i++ {
		id := fmt.Sprint(i)
		require(t, creator.Create(ctx, characterdata.Creation{
			Account: &repository.AccountRecord{AccountID: "new"}, WelcomeCera: amount,
			Character: repository.CharacterRecord{CharacterID: id, AccountID: "new", Slot: i - 1, Name: "fixture" + id, CreatedAt: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC), Stats: map[string]int64{"gold": p.CharacterGold}},
		}))
	}
	a, _, err := g.Account.Load(ctx, "new")
	require(t, err)
	if a.Metadata["account_cera"] != "200" {
		t.Fatal("repeated welcome grant")
	}
	// The game schema initializer must not own GM jobs or campaign tables.
	var count int
	require(t, db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=? AND table_name IN ('web_accounts_benefit_jobs','web_accounts_benefit_campaigns')", name).Scan(&count))
	if count != 0 {
		t.Fatal("game created GM tables")
	}
	// Existing GM records must survive server initialization unchanged.
	_, err = db.Exec("CREATE TABLE web_accounts_benefit_campaigns (id INT PRIMARY KEY, payload TEXT)")
	require(t, err)
	_, err = db.Exec("INSERT INTO web_accounts_benefit_campaigns VALUES (1,'preserve-receipts')")
	require(t, err)
	require(t, s.EnsureSchema(ctx))
	var payload string
	require(t, db.QueryRow("SELECT payload FROM web_accounts_benefit_campaigns WHERE id=1").Scan(&payload))
	if payload != "preserve-receipts" {
		t.Fatal("GM state changed")
	}
}
