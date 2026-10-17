package mysql

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"strings"
	"testing"
)

func TestMySQLPendingLevelUpRecoveryReadsCommittedWriterAndBindsCursor(t *testing.T) {
	database := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{{values: []any{"77"}}, {values: []any{"88"}}}}}}
	repos := newTestMySQLGroup(t, database)
	reader, ok := repos.Character.(repository.PendingLevelUpReader)
	if !ok {
		t.Fatal("production character repository has no event recovery reader")
	}
	ids, err := reader.ListPendingLevelUpCharacters(context.Background(), "55", 2)
	if err != nil || !reflect.DeepEqual(ids, []string{"77", "88"}) {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	if len(database.execs) != 1 {
		t.Fatalf("queries=%v", database.execs)
	}
	query := database.execs[0]
	for _, part := range []string{"`dnf_s1_w1`.`dnf_character_stats`", "s.stat_value > 0", "c.delete_flag = 0", "s.character_id > ?", "ORDER BY s.character_id LIMIT ?"} {
		if !strings.Contains(query.Query, part) {
			t.Fatalf("query lacks %q: %s", part, query.Query)
		}
	}
	if !reflect.DeepEqual(query.Args, []any{len(repository.LevelUpEventStatPrefix), repository.LevelUpEventStatPrefix, "55", 2}) {
		t.Fatalf("bound arguments=%#v", query.Args)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.ListPendingLevelUpCharacters(ctx, "", 128); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

func TestMySQLLevelUpHistoryIsNotDeletedOrRegressedByOrdinaryStatsReplacement(t *testing.T) {
	database := &fakeSQLDB{}
	err := replaceCharacterExtraStats(context.Background(), database, "`test`.`dnf_character_stats`", "77", map[string]int64{repository.LevelUpEventKey(85): 123, "level_up_reward_mail_85": 9, "ordinary_stat": 7})
	if err != nil {
		t.Fatal(err)
	}
	deleteQuery := database.execs[0]
	if !strings.Contains(deleteQuery.Query, "AND LEFT(stat_key, ?) <> ? AND LEFT(stat_key, ?) <> ?") || len(deleteQuery.Args) != 5 {
		t.Fatalf("ordinary replacement erases history=%+v", deleteQuery)
	}
	for _, q := range database.execs[1:] {
		key := q.Args[1].(string)
		if key == repository.LevelUpEventKey(85) && !strings.Contains(q.Query, "stat_value < 0 OR VALUES(stat_value) >= 0") {
			t.Fatalf("pending snapshot can revive consumed event=%s", q.Query)
		}
		if key == "level_up_reward_mail_85" && !strings.Contains(q.Query, "IF(stat_value = 0, VALUES(stat_value), stat_value)") {
			t.Fatalf("receipt can be overwritten=%s", q.Query)
		}
		if key == "ordinary_stat" && strings.Contains(q.Query, "DUPLICATE") {
			t.Fatalf("ordinary stats behavior changed=%s", q.Query)
		}
	}
}
