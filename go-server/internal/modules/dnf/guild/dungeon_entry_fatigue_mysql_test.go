package guild

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

func TestDungeonEntryFatigueMySQLFourOwnersRollbackReplayAndReconnect(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid disposable DSN")
	}
	cfg.DBName, cfg.ParseTime = "", true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open disposable DB")
	}
	defer db.Close()
	ctx := context.Background()
	name := fmt.Sprintf("guild_entry_fp_test_%d", time.Now().UnixNano())
	plan := r.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	}()
	group := func() r.Group {
		g, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	repos := group()
	fixture, q, now := dungeonEntryFatigueFixture(t, 4)
	for _, e := range q.Entries {
		if err := repos.Account.Save(ctx, q.Accounts[e.Owner.AccountID]); err != nil {
			t.Fatal(err)
		}
		c, _, _ := fixture.Character.Load(ctx, e.Owner.CharacterID)
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	settings, _, _ := fixture.Settings.Load(ctx, RegistryScope)
	if err := repos.Settings.Save(ctx, settings); err != nil {
		t.Fatal(err)
	}
	// Fails on the receipt, after all four character updates have executed.
	if _, err := db.ExecContext(ctx, "CREATE TRIGGER `"+name+"`.reject_entry_receipt BEFORE INSERT ON `"+name+"`.dnf_setting_values FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test entry receipt failure'"); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitDungeonEntryFatigue(ctx, repos.Guild, q, now); err == nil {
		t.Fatal("SQL receipt failure committed")
	}
	if _, err := db.ExecContext(ctx, "DROP TRIGGER `"+name+"`.reject_entry_receipt"); err != nil {
		t.Fatal(err)
	}
	assertDungeonEntryFatigue(t, group(), q, 8, false)
	start, results := make(chan struct{}), make(chan error, 8)
	for range 8 {
		g := group()
		go func() { <-start; _, err := CommitDungeonEntryFatigue(ctx, g.Guild, q, now); results <- err }()
	}
	close(start)
	for range 8 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	assertDungeonEntryFatigue(t, group(), q, 0, true)
	// A new connection/group represents runtime reconstruction at zero FP.
	restarted, err := CommitDungeonEntryFatigue(ctx, group().Guild, q, now)
	if err != nil || !restarted.Replayed {
		t.Fatal("reconnect replay", restarted.Replayed, err)
	}
	assertDungeonEntryFatigue(t, group(), q, 0, true)
}
