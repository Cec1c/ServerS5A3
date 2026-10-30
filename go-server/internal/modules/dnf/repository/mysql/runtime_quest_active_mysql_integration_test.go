//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/metrics"
	"testing"
)

func runActiveQuestRealDBCase(t *testing.T, ctx context.Context, db *sql.DB, name string) {
	t.Helper()
	observer := NewSQLObservation(metrics.New(), db)
	g, err := NewMySQLGroup(&ObservedSQLDB{SQLDB: SQLDBAdapter{DB: db}, Observation: observer}, MySQLGroupOptions{DatabasePlan: repository.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}})
	if err != nil {
		t.Fatal(err)
	}
	original := repository.QuestRecord{CharacterID: "active-view", States: map[int64]repository.QuestState{}, Progress: map[int64]repository.QuestState{}}
	for id := int64(1); id <= 674; id++ {
		extra := make(map[string]string)
		for k := 0; k < 6; k++ {
			extra[fmt.Sprint(k)] = "preserved historical receipt"
		}
		original.States[id] = repository.QuestState{Status: "completed", Extra: extra}
	}
	original.States[1] = repository.QuestState{Status: "active", ProgressValue: 2, Extra: map[string]string{"count": "2"}}
	original.States[2] = repository.QuestState{Status: "\u3000In_Pro-g ress\u00a0", ProgressValue: 2, Extra: map[string]string{"count": "2"}}
	original.Progress[674] = repository.QuestState{Status: "accepted", ProgressValue: 7}
	// Removing middle whitespace in SQL creates a superset; Go must discard it.
	original.States[673] = repository.QuestState{Status: "ac\ttive", ProgressValue: 8}
	if err := g.Quest.Save(ctx, original); err != nil {
		t.Fatal(err)
	}
	if err := validateActiveQuestStatusSQL(); err != nil {
		t.Fatal(err)
	}
	rowsBefore := observer.series[0][sqlObserveQuery].rows.Load()
	scope := repository.WithQuestPlanningScope(ctx, g.Quest, "active-view")
	active, found, err := repository.LoadQuestActivePlanning(scope, g.Quest, "active-view")
	if err != nil || !found || len(active.States) != 3 || len(active.Progress) != 1 || active.States[674].Status != "completed" || active.Progress[674].ProgressValue != 7 {
		t.Fatalf("active view: %v %v states=%d progress=%d", found, err, len(active.States), len(active.Progress))
	}
	if _, found := active.States[673]; found {
		t.Fatal("false-positive SQL status survived Go validation")
	}
	key := new(int)
	deps := func(repository.QuestRecord) []int64 { return []int64{100, 101, 100, 0, -1} }
	view, found, err := repository.LoadQuestDependentPlanning(scope, g.Quest, "active-view", key, deps)
	if err != nil || !found || len(view.States) != 5 || view.States[100].Status != "completed" {
		t.Fatal("dependency view", found, err, len(view.States))
	}
	queries := observationCount(&observer.series[0][sqlObserveQuery])
	if _, _, err := repository.LoadQuestDependentPlanning(scope, g.Quest, "active-view", key, deps); err != nil {
		t.Fatal(err)
	}
	if observationCount(&observer.series[0][sqlObserveQuery]) != queries {
		t.Fatal("request view queried twice")
	}
	if rows := observer.series[0][sqlObserveQuery].rows.Load() - rowsBefore; rows > 40 {
		t.Fatalf("loaded unrelated history rows: %d", rows)
	}
	after := repository.CloneQuest(view)
	state := after.States[1]
	state.ProgressValue = 1
	after.States[1] = state
	if err := repository.SaveQuestChanges(scope, g.Quest, view, after, repository.QuestFieldStates); err != nil {
		t.Fatal(err)
	}
	refreshed, _, err := repository.LoadQuestDependentPlanning(scope, g.Quest, "active-view", key, deps)
	if err != nil || refreshed.States[1].ProgressValue != 1 {
		t.Fatal("write did not invalidate request view", err)
	}
	full, _, err := g.Quest.Load(ctx, "active-view")
	if err != nil || len(full.States) != 674 || full.States[600].Extra["5"] != "preserved historical receipt" || full.Progress[674].ProgressValue != 7 {
		t.Fatal("delta lost untouched history", err)
	}
}
