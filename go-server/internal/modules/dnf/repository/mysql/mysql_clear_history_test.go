package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/moderation"
	"longheng.io/server/internal/modules/dnf/repository"
	"os"
	"testing"
	"time"
)

func TestClearHistoryMySQLShardsRollbackAndFrequency(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable DB not configured")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	shards := []string{fmt.Sprintf("clear_a_%d", time.Now().UnixNano()), fmt.Sprintf("clear_b_%d", time.Now().UnixNano())}
	for _, database := range shards {
		defer db.Exec("DROP DATABASE IF EXISTS `" + database + "`")
	}
	plan := repository.DatabasePlan{WriteDatabases: shards}
	if err := EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	group, err := NewMySQLGroupFromDB(db, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Hour).Add(10 * time.Minute)
	for _, id := range []string{"1", "2", "3", "4"} {
		if err := group.Account.Save(ctx, repository.AccountRecord{AccountID: "a" + id}); err != nil {
			t.Fatal(err)
		}
		if err := group.Character.Save(ctx, repository.CharacterRecord{AccountID: "a" + id, CharacterID: id, Name: "role" + id}); err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 3; n++ {
			c := moderation.Clear{Key: fmt.Sprint(n), AccountID: "a" + id, CharacterID: id, CharacterName: "role" + id, DungeonID: 123, DungeonName: "fixture", FinishedAt: now.Add(time.Duration(n) * time.Minute), StartedAt: now.Add(-time.Minute), DurationMS: 60000 + int64(n)*1000}
			if n > 0 {
				spent := int64(5)
				c.FatigueSpent = &spent
			}
			for repeat := 0; repeat < 2; repeat++ {
				if err := group.CharacterSettlement.WithinCharacterSettlement(ctx, id, func(tx repository.Group) error {
					return tx.Character.(repository.ClearHistoryWriter).AppendClear(ctx, c)
				}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	reader := group.Character.(repository.ClearHistoryReader)
	q := moderation.Query{From: now.Add(-time.Hour), To: now.Add(time.Hour), Limit: 2}
	page, err := reader.QueryClears(ctx, q)
	if err != nil || !page.HasMore || len(page.Rows) != 2 || page.Rows[0].Count != 3 || page.Rows[0].PeakHourly != 3 || page.Rows[0].FastestMS != 60000 || page.Rows[0].AverageMS != 61000 || page.Rows[0].FatigueSpent == nil || *page.Rows[0].FatigueSpent != 10 || *page.Rows[0].AverageFatigue != 5 || page.Rows[0].FatigueSamples != 2 {
		t.Fatalf("page %+v %v", page, err)
	}
	q.Offset = 2
	page, err = reader.QueryClears(ctx, q)
	if err != nil || page.HasMore || len(page.Rows) != 2 {
		t.Fatal(page, err)
	}
	expected := errors.New("rollback")
	err = group.CharacterSettlement.WithinCharacterSettlement(ctx, "1", func(tx repository.Group) error {
		c := moderation.Clear{Key: "failed", AccountID: "a1", CharacterID: "1", DungeonID: 123, FinishedAt: now, StartedAt: now, DurationMS: 1}
		if err := tx.Character.(repository.ClearHistoryWriter).AppendClear(ctx, c); err != nil {
			return err
		}
		return expected
	})
	if !errors.Is(err, expected) {
		t.Fatal(err)
	}
	q.Offset = 0
	q.MaxDurationMS = 100
	q.Limit = 100
	page, err = reader.QueryClears(ctx, q)
	if err != nil || len(page.Rows) != 0 {
		t.Fatal("rollback/filter", page, err)
	}
	q.MaxDurationMS = 0
	q.MinHourlyPeak = 4
	page, err = reader.QueryClears(ctx, q)
	if err != nil || len(page.Rows) != 0 {
		t.Fatal("frequency", page, err)
	}
}
