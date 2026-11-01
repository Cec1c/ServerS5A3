package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/repository"
)

func TestMySQLActivityMetricWidthUpgrade(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid disposable MySQL DSN")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	name := fmt.Sprintf("activity_width_test_%d", time.Now().UnixNano())
	plan := repository.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	opts := SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}
	if err := EnsureMySQLSchema(ctx, db, opts); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec("DROP DATABASE `" + name + "`"); err != nil {
			t.Error(err)
		}
	}()
	// Upgrade an existing installation, retaining its previously recorded activity.
	for _, table := range []string{activityEventsTable, activityCountersTable} {
		if _, err := db.Exec("ALTER TABLE " + mysqlTable(name, "dnf", table) + " MODIFY COLUMN metric VARCHAR(32) NOT NULL"); err != nil {
			t.Fatal(err)
		}
	}
	g, err := NewMySQLGroupFromDB(db, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		g.Account.Save(ctx, repository.AccountRecord{AccountID: "a", Metadata: map[string]string{}}),
		g.Character.Save(ctx, repository.CharacterRecord{CharacterID: "19", AccountID: "a", Name: "fixture", Level: 90, Job: "0", Stats: map[string]int64{}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	record := func(event repository.ActivityEvent) (bool, error) {
		var created bool
		err := g.CharacterSettlement.WithinCharacterSettlement(ctx, "19", func(tx repository.Group) error {
			var err error
			created, err = tx.Activity.Record(ctx, event)
			return err
		})
		return created, err
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	old := repository.ActivityEvent{ID: "old:fatigue", AccountID: "a", CharacterID: "19", Metric: repository.ActivityFatigue, Amount: 1, OccurredAt: at, DungeonID: 310, Source: "dungeon_room"}
	if added, err := record(old); err != nil || !added {
		t.Fatalf("seed added=%v err=%v", added, err)
	}
	for i := 0; i < 2; i++ {
		if err := EnsureMySQLSchema(ctx, db, opts); err != nil {
			t.Fatal(err)
		}
	}
	event := repository.ActivityEvent{ID: "recommended:clear-1", AccountID: "a", CharacterID: "19", Metric: repository.ActivityRecommendedDungeon, Amount: 1, OccurredAt: at, DungeonID: 310, Source: "recommended_clear"}
	for i := 0; i < 2; i++ {
		added, err := record(event)
		if err != nil || added != (i == 0) {
			t.Fatalf("recommended write %d added=%v err=%v", i, added, err)
		}
	}
	var count, total int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + mysqlTable(name, "dnf", activityEventsTable)).Scan(&count); err != nil || count != 2 {
		t.Fatalf("event count=%d err=%v", count, err)
	}
	if err := db.QueryRow("SELECT SUM(total) FROM "+mysqlTable(name, "dnf", activityCountersTable)+" WHERE metric=? AND bucket='all'", repository.ActivityRecommendedDungeon).Scan(&total); err != nil || total != 1 {
		t.Fatalf("recommended total=%d err=%v", total, err)
	}
	if added, err := record(old); err != nil || added {
		t.Fatalf("old event not retained added=%v err=%v", added, err)
	}
}
