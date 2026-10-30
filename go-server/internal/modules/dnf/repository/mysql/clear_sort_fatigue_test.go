package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"longheng.io/server/internal/modules/dnf/moderation"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClearHistorySortingFatigueAndOldSchema(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable DB required")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	shards := []string{fmt.Sprintf("sort_a_%d", time.Now().UnixNano()), fmt.Sprintf("sort_b_%d", time.Now().UnixNano())}
	for _, name := range shards {
		defer db.Exec("DROP DATABASE IF EXISTS `" + name + "`")
	}
	opts := SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: repository.DatabasePlan{WriteDatabases: shards}}
	if err = EnsureMySQLSchema(ctx, db, opts); err != nil {
		t.Fatal(err)
	}
	group, err := NewMySQLGroupFromDB(db, MySQLGroupOptions{DatabasePlan: opts.DatabasePlan})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 29, 3, 0, 0, 0, time.UTC)
	old := moderation.Clear{Key: "old", AccountID: "old", CharacterID: "9", CharacterName: "old", DungeonID: 9, DungeonName: "old", StartedAt: now, FinishedAt: now, DurationMS: 9000}
	if err = group.Character.(repository.ClearHistoryWriter).AppendClear(ctx, old); err != nil {
		t.Fatal(err)
	}
	for _, name := range shards {
		if _, err = db.Exec("ALTER TABLE " + mysqlTable(name, "dnf", "clear_history") + " DROP COLUMN fatigue_spent"); err != nil {
			t.Fatal(err)
		}
	}
	// Repeated startup upgrades populated legacy tables without inventing zeroes.
	for i := 0; i < 2; i++ {
		if err = EnsureMySQLSchema(ctx, db, opts); err != nil {
			t.Fatal(err)
		}
	}
	mem := memory.NewMemoryGroup()
	mem.Character.(repository.ClearHistoryWriter).AppendClear(ctx, old)
	for i := 1; i <= 3; i++ {
		id := fmt.Sprint(i)
		for n := 0; n < i; n++ {
			spent := int64((3 - i) * 2)
			c := moderation.Clear{Key: fmt.Sprint(n), AccountID: "a" + id, CharacterID: id, CharacterName: "role" + id, DungeonID: int64(i), DungeonName: "dungeon" + id, Difficulty: i, StartedAt: now, FinishedAt: now.Add(time.Duration(i*10+n) * time.Second), DurationMS: int64(i) * 1000, FatigueSpent: &spent}
			if i == 1 {
				c.FatigueSpent = nil
			}
			for _, repo := range []repository.CharacterRepository{group.Character, mem.Character} {
				if err := repo.(repository.ClearHistoryWriter).AppendClear(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	q := moderation.Query{From: now.Add(-time.Hour), To: now.Add(time.Hour), Limit: 1}
	expected := map[string]string{"accountId": "1239", "characterId": "1239", "characterName": "9123", "dungeonId": "1239", "dungeonName": "1239", "difficulty": "9123", "count": "1923", "fastestMs": "1239", "averageMs": "1239", "peakHourly": "1923", "lastClearAt": "9123", "fatigueSpent": "3219", "averageFatigue": "3219"}
	for key, want := range expected {
		for _, direction := range []string{"asc", "desc"} {
			t.Run(key+"_"+direction, func(t *testing.T) {
				if direction == "desc" {
					switch key {
					case "count", "peakHourly":
						want = "3219"
					case "fatigueSpent", "averageFatigue":
						want = "2319"
					default:
						r := []rune(want)
						for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
							r[i], r[j] = r[j], r[i]
						}
						want = string(r)
					}
				}
				for _, chars := range []repository.CharacterRepository{group.Character, mem.Character} {
					q.SortBy, q.SortOrder = key, direction
					var got strings.Builder
					for q.Offset = 0; q.Offset < 4; q.Offset++ {
						page, e := chars.(repository.ClearHistoryReader).QueryClears(ctx, q)
						if e != nil || len(page.Rows) != 1 {
							t.Fatalf("page=%+v err=%v", page, e)
						}
						row := page.Rows[0]
						got.WriteString(row.CharacterID)
						if page.HasMore != (q.Offset < 3) {
							t.Fatal("invalid continuation")
						}
						if row.CharacterID == "9" || row.CharacterID == "1" {
							if row.FatigueSpent != nil || row.AverageFatigue != nil || row.FatigueSamples != 0 {
								t.Fatal("legacy reported zero fatigue")
							}
						}
						if row.CharacterID == "3" {
							if row.FatigueSpent == nil || *row.FatigueSpent != 0 || *row.AverageFatigue != 0 || row.FatigueSamples != 3 {
								t.Fatal("free clears are not unknown")
							}
						}
						if row.CharacterID == "2" {
							if *row.FatigueSpent != 4 || *row.AverageFatigue != 2 || row.FatigueSamples != 2 {
								t.Fatal("wrong aggregate")
							}
						}
					}
					if got.String() != want {
						t.Fatalf("got=%s want=%s", got.String(), want)
					}
				}
			})
		}
	}
	q.Offset = 0
	q.Limit = 100
	q.SortBy = "fatigueSpent"
	q.SortOrder = "asc"
	a, _ := group.Character.(repository.ClearHistoryReader).QueryClears(ctx, q)
	b, _ := mem.Character.(repository.ClearHistoryReader).QueryClears(ctx, q)
	if !reflect.DeepEqual(a.Rows, b.Rows) {
		t.Fatalf("memory/mysql mismatch: %+v %+v", a.Rows, b.Rows)
	}
	q.SortBy = "count DESC;DROP TABLE x"
	if _, err := group.Character.(repository.ClearHistoryReader).QueryClears(ctx, q); err == nil {
		t.Fatal("invalid sort accepted")
	}
}
