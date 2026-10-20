package dnfbridge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	dnfbooster "longheng.io/server/internal/modules/dnf/booster"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

// Opt in only against a disposable MySQL instance; this creates and drops a
// uniquely named test database and never uses an existing player database.
func TestLotteryOpeningPriceMySQLAtomicAssets(t *testing.T) {
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
	name := fmt.Sprintf("lottery_cost_test_%d", time.Now().UnixNano())
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
	seed := lotteryPriceAssets(t, 1000007)
	character, _, _ := seed.Character.Load(ctx, "19")
	character.Stats["lottery_test_marker"] = 17
	inventory, _, _ := seed.Inventory.Load(ctx, "19")
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	catalog := lotteryPriceFixture(t, "1000000")
	definition, err := resolveCurrentBoosterDefinition(catalog, 900, currentBoosterRequestRandom)
	if err != nil {
		t.Fatal(err)
	}
	s := currentBoosterTestService(catalog, repos)
	open := func() error {
		_, err := s.commitCurrentBooster(ctx, &gameSession{selectedCharacterID: 19}, catalog, definition, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: 75})
		return err
	}
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := open(); err == nil {
				success.Add(1)
			} else if !errors.Is(err, dnfbooster.ErrGoldInsufficient) {
				t.Errorf("concurrent open: %v", err)
			}
		}()
	}
	wg.Wait()
	reloaded, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := reloaded.Character.Load(ctx, "19")
	if err != nil || c.Stats["gold"] != 7 || c.Stats["fatigue"] != 120 || c.Stats["lottery_test_marker"] != 17 || success.Load() != 1 {
		t.Fatalf("gold=%v success=%d err=%v", c.Stats, success.Load(), err)
	}
	inv, _, err := reloaded.Inventory.Load(ctx, "19")
	if err != nil || inv.Slots["0:75"].Count != 1 || inv.Slots["0:121"].Count != 4 {
		t.Fatalf("inventory=%+v err=%v", inv, err)
	}
	var rewards int64
	for _, row := range inv.Slots {
		if row.ItemID == 2600014 {
			rewards += row.Count
		}
	}
	if rewards != 1 {
		t.Fatalf("reward count=%d", rewards)
	}
	c.Stats["gold"] = 1000007
	if err := repos.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	beforeChar, _, _ := repos.Character.Load(ctx, "19")
	beforeInv, _, _ := repos.Inventory.Load(ctx, "19")
	// Inventory is saved first, then character gold, then supplementary stats.
	// A rejected stat insert must roll back all three physical writes.
	if _, err := db.ExecContext(ctx, "CREATE TRIGGER `"+name+"`.`reject_lottery_stats` BEFORE INSERT ON `"+name+"`.`dnf_character_stats` FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'injected lottery stat failure'"); err != nil {
		t.Fatal(err)
	}
	if err := open(); err == nil {
		t.Fatal("injected late database failure committed")
	}
	afterChar, _, _ := reloaded.Character.Load(ctx, "19")
	afterInv, _, _ := reloaded.Inventory.Load(ctx, "19")
	if !reflect.DeepEqual(beforeChar, afterChar) || !reflect.DeepEqual(beforeInv, afterInv) {
		t.Fatal("failed settlement changed durable gold, jars or rewards")
	}
}
