package mysql

import (
	"context"
	r "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
)

func TestAdventureSummaryDoesNotLoadCharacterOrAccountAggregates(t *testing.T) {
	db := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{{values: []any{1}}, {values: []any{90}}}}}, rows: []fakeSQLRow{{values: []any{uint64(123)}}}}
	g, err := NewMySQLGroup(db, MySQLGroupOptions{DatabasePlan: r.DatabasePlan{WriteDatabases: []string{"summary"}}})
	if err != nil {
		t.Fatal(err)
	}
	levels, err := r.ListAccountCharacterLevels(context.Background(), g.Character, "a", 4)
	if err != nil || len(levels) != 2 || levels[1].Level != 90 {
		t.Fatal(levels, err)
	}
	honor, found, err := r.LoadAccountHonorExperience(context.Background(), g.Account, "a")
	if err != nil || !found || honor != 123 || len(db.execs) != 2 {
		t.Fatal(honor, found, err, db.execs)
	}
	for _, q := range db.execs {
		if strings.Contains(q.Query, "metadata") || strings.Contains(q.Query, "character_stats") || strings.Contains(q.Query, "roster") {
			t.Fatal(q.Query)
		}
	}
	if !strings.Contains(db.execs[0].Query, "delete_flag = 0 ORDER BY slot ASC, character_id ASC LIMIT ?") {
		t.Fatal(db.execs[0])
	}
}
