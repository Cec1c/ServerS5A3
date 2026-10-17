package dungeon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/rental"
	repo "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

// This opt-in check creates and removes its own database on a disposable server.
func TestLuckyStarMySQLConcurrentWallet(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open test database failed")
	}
	defer db.Close()
	ctx := context.Background()
	name := fmt.Sprintf("lucky_star_test_%d", time.Now().UnixNano())
	plan := repo.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	}()
	g, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "dnf:1", Metadata: map[string]string{LuckyStarMetadataKey: "10", "unchanged": "keep"}}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 8; i++ {
		if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: strconv.Itoa(i), AccountID: "dnf:1", Slot: i - 1, Name: "stars" + strconv.Itoa(i), Level: 50, Stats: map[string]int64{"gold": 30}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 8; i++ {
		c, found, err := g.Character.Load(ctx, strconv.Itoa(i))
		if err != nil || !found || c.CharacterID != strconv.Itoa(i) || c.AccountID != "dnf:1" {
			t.Fatalf("fixture character %d found=%v id=%q owner=%q err=%v", i, found, c.CharacterID, c.AccountID, err)
		}
	}
	owner, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	rentalOwner, err := rental.NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 9)
	for i := 1; i <= 8; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := owner.AwardLuckyStar(ctx, LuckyStarCommand{AccountID: "dnf:1", CharacterID: id, CharacterLevel: 50, RecommendedMin: 45, RecommendedMax: 55})
			errs <- err
		}(strconv.Itoa(i))
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := rentalOwner.Charge(ctx, rental.ChargeCommand{AccountID: "dnf:1", CharacterID: "1", Count: 2, Limit: 999, GoldPerPoint: 3})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	reloaded, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := reloaded.Account.Load(ctx, "dnf:1")
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := reloaded.Character.Load(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Metadata[LuckyStarMetadataKey] != "20" || a.Metadata["unchanged"] != "keep" || c.Stats["gold"] != 24 {
		t.Fatalf("account=%v gold=%d", a.Metadata, c.Stats["gold"])
	}
	failing := g
	failing.RentalAssets = luckyStarLateFailure{g.RentalAssets}
	failedOwner, _ := NewOwner(failing)
	if _, err := failedOwner.AwardLuckyStar(ctx, LuckyStarCommand{AccountID: "dnf:1", CharacterID: "1", CharacterLevel: 50, RecommendedMin: 45, RecommendedMax: 55}); !errors.Is(err, errLuckyStarLateFailure) {
		t.Fatalf("late failure err=%v", err)
	}
	a, _, err = reloaded.Account.Load(ctx, "dnf:1")
	if err != nil || a.Metadata[LuckyStarMetadataKey] != "20" || a.Metadata["unchanged"] != "keep" {
		t.Fatalf("rollback metadata=%v err=%v", a.Metadata, err)
	}
}
