package mysql

import (
	"context"
	"strings"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestAdventureRankMySQLReadsAuthoritativeStatsOnPrimary(t *testing.T) {
	state := &rankSQLState{}
	activity := rankSQLStore(t, state)
	characters := &mysqlCharStore{mysqlStoreBase: activity.mysqlStoreBase}
	rows, err := characters.AdventureRankContributions(context.Background())
	if err != nil || len(rows) != 3 || len(state.queries) != 1 || len(state.writes) != 0 {
		t.Fatal(rows, err, state)
	}
	query := state.queries[0]
	for _, needed := range []string{"shard_a", "shard_b", "UNION ALL", "character_stats", "INNER JOIN", "c.delete_flag = 0", "s.stat_key = ?", "LIMIT 100001"} {
		if !strings.Contains(query, needed) {
			t.Fatal("未按冒险团权威角色统计读取", needed, query)
		}
	}
	if strings.Contains(query, "replica") || strings.Contains(query, "activity_events") || strings.Contains(query, "activity_counters") {
		t.Fatal(query)
	}
	if len(state.parameters[0]) != 2 {
		t.Fatal(state.parameters)
	}
	for _, p := range state.parameters[0] {
		if p.Value != repo.ActivityRecommendedDungeon {
			t.Fatal(p)
		}
	}
}
