package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestLevelUpRewardProbeOneQueryAndCanonicalEvents(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		value     any
		pending   bool
	}{
		{"absent", "", nil, false}, {"valid", r.LevelUpEventKey(85), int64(1), true},
		{"consumed", r.LevelUpEventKey(85), int64(-1), false}, {"zero", r.LevelUpEventKey(85), int64(0), false},
		{"leading zero", "level_up_event_085", int64(1), false}, {"outside range", "level_up_event_256", int64(1), false},
		{"audit only", "level_up_reward_85", int64(1), false}, {"bad level", "level_up_event_x", int64(1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{{values: []any{"77", "a", 90, tc.key, tc.value}}}}}}
			group, err := NewMySQLGroup(db, MySQLGroupOptions{DatabasePlan: r.DatabasePlan{ReadDatabases: []string{"probe"}, WriteDatabases: []string{"probe"}}})
			if err != nil {
				t.Fatal(err)
			}
			p, found, err := group.Character.(r.LevelUpRewardProbeReader).LoadLevelUpRewardProbe(context.Background(), "77")
			if err != nil || !found || p.Pending != tc.pending || p.Level != 90 || p.CharacterID != "77" || p.AccountID != "a" || len(db.execs) != 1 {
				t.Fatal(p, found, err, db.execs)
			}
			q := db.execs[0].Query
			if strings.Contains(q, "roster") || strings.Contains(q, "location") || !strings.Contains(q, "LEFT JOIN") {
				t.Fatal(q)
			}
		})
	}
}

func TestLevelUpRewardProbeMissingRowsAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rows    fakeSQLRows
		wantErr bool
	}{
		{"missing", fakeSQLRows{}, false},
		{"scan error", fakeSQLRows{rows: []fakeSQLRow{{err: errors.New("bad row")}}}, true},
		{"iteration error", fakeSQLRows{err: errors.New("read interrupted")}, true},
		{"foreign identity", fakeSQLRows{rows: []fakeSQLRow{{values: []any{"88", "a", 90, nil, nil}}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &fakeSQLDB{rowsets: []fakeSQLRows{tc.rows}}
			group, err := NewMySQLGroup(db, MySQLGroupOptions{DatabasePlan: r.DatabasePlan{WriteDatabases: []string{"probe"}}})
			if err != nil {
				t.Fatal(err)
			}
			_, found, err := group.Character.(r.LevelUpRewardProbeReader).LoadLevelUpRewardProbe(context.Background(), "77")
			if found || (err != nil) != tc.wantErr {
				t.Fatal(found, err)
			}
		})
	}
}
