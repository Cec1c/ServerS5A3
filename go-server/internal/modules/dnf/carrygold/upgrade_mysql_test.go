package carrygold

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

// This opt-in test creates and drops its own database on an explicitly
// disposable MySQL instance. Never point the DSN at a player database.
func TestCarryGoldMySQLAtomicStageAndWallet(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	cfg.DBName, cfg.ParseTime = "", true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open test database failed")
	}
	defer db.Close()
	ctx := context.Background()
	name := fmt.Sprintf("carry_gold_test_%d", time.Now().UnixNano())
	plan := dnfrepo.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	}()
	repos, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "17", AccountID: "a", Level: 90, Stats: map[string]int64{"gold": StageCost * 4, "fatigue": 92, "unrelated_extra_stat": 42}}); err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Upgrade(ctx, repos, "a", "17", 1)
			if err == nil {
				count.Add(1)
			} else if !errors.Is(err, ErrStage) {
				t.Errorf("parallel upgrade: %v", err)
			}
		}()
	}
	wg.Wait()
	// Rebuild repositories: no session cache can account for a passing reload.
	reloaded, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	assertStored := func() {
		t.Helper()
		c, found, err := reloaded.Character.Load(ctx, "17")
		if err != nil || !found || c.Stats["gold"] != StageCost*3 || c.Stats[StageStat] != 1 || c.Stats["fatigue"] != 92 || c.Stats["unrelated_extra_stat"] != 42 || count.Load() != 1 {
			t.Fatalf("reload count=%d found=%v stats=%v err=%v", count.Load(), found, c.Stats, err)
		}
	}
	assertStored()
	// Fail the supplementary stat write after the wallet row was updated.
	// Both physical tables must roll back, including the removed old stat rows.
	_, err = db.ExecContext(ctx, "CREATE TRIGGER `"+name+"`.`reject_stage` BEFORE INSERT ON `"+name+"`.`dnf_character_stats` FOR EACH ROW BEGIN IF NEW.stat_key = 'carry_gold_stage' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'injected stage failure'; END IF; END")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Upgrade(ctx, reloaded, "a", "17", 2); err == nil {
		t.Fatal("injected failure committed")
	}
	assertStored()
}
