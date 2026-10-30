package quest

import (
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPlanHuntEnemyKillPartialPlanningParentChildJoint(t *testing.T) {
	const (
		parentID         int64 = 100
		completedChildID int64 = 101
		missingChildID   int64 = 102
	)
	completedAt := time.Date(2026, 10, 30, 17, 30, 0, 0, time.UTC)
	catalog := &Catalog{byID: map[int64]Definition{
		parentID: {
			ID: parentID, Path: "n_quest/partial_parent.qst", Grade: "[epic]", Type: "[quest clear]",
			IntData: []int64{completedChildID, missingChildID},
		},
		completedChildID: {
			ID: completedChildID, Path: "n_quest/partial_completed_child.qst", Grade: "[sub]", Type: "[hunt enemy]",
			MainQuestID: parentID, IntData: []int64{3, -1, 77110, 1, 2},
		},
		missingChildID: {
			ID: missingChildID, Path: "n_quest/partial_missing_child.qst", Grade: "[sub]", Type: "[hunt enemy]",
			MainQuestID: parentID, IntData: []int64{3, -1, 77110, 1, 1},
		},
	}}
	record := dnfrepo.QuestRecord{
		PlanningPartial: true,
		CharacterID:     "partial-planning",
		States: map[int64]dnfrepo.QuestState{
			parentID: {
				Status: "active", ProgressValue: 2,
				Extra: map[string]string{"keep": "states-parent"},
			},
			// Historical partial planning omits Extra, but this header still
			// tells the parent that the child prerequisite is completed.
			completedChildID: {Status: "completed", ProgressValue: 0},
		},
		Progress: map[int64]dnfrepo.QuestState{
			parentID: {
				Status: "active", ProgressValue: 2,
				Extra: map[string]string{"keep": "progress-parent"},
			},
			// States must shadow this active Progress alias even though the
			// States header is completed and has no historical Extra loaded.
			completedChildID: {
				Status: "active", ProgressValue: 2,
				Extra: map[string]string{"shadowed": "progress-child"},
			},
		},
	}
	before := dnfrepo.CloneQuest(record)
	input := HuntEnemyKillInput{
		DungeonID:     3,
		Difficulty:    0,
		EnemyCode:     77110,
		EnemyType:     1,
		CompletionKey: "partial-planning/enemy/77110",
		CompletedAt:   completedAt,
	}

	plan, err := catalog.PlanHuntEnemyKill(record, input)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Record.PlanningPartial {
		t.Fatal("planner lost partial planning provenance")
	}
	if want := []dnfrepo.QuestField{dnfrepo.QuestFieldStates, dnfrepo.QuestFieldProgress}; !reflect.DeepEqual(plan.ChangedFields, want) {
		t.Fatalf("changed fields=%v, want=%v", plan.ChangedFields, want)
	}
	if len(plan.Completions) != 1 || plan.Completions[0].QuestID != missingChildID ||
		plan.Completions[0].PreviousTrigger != 1 || plan.Completions[0].CurrentTrigger != 0 ||
		!plan.Completions[0].Completed || plan.Completions[0].Idempotent {
		t.Fatalf("completions=%+v", plan.Completions)
	}

	historical := plan.Record.States[completedChildID]
	if historical.Status != "completed" || historical.ProgressValue != 0 || historical.Extra != nil {
		t.Fatalf("historical completed child changed=%+v", historical)
	}
	shadowed := plan.Record.Progress[completedChildID]
	if shadowed.Status != "active" || shadowed.ProgressValue != 2 || shadowed.Extra["shadowed"] != "progress-child" {
		t.Fatalf("States did not shadow the Progress alias: %+v", shadowed)
	}

	missing := plan.Record.States[missingChildID]
	if missing.Status != "completed" || missing.ProgressValue != 0 ||
		missing.Extra["auto_activated_by_main_quest"] != "true" ||
		missing.Extra["auto_activation_reason"] != "active_quest_clear_parent_reconcile" ||
		missing.Extra["auto_complete_reason"] != "no_reward_sub_quest" {
		t.Fatalf("missing child activation/completion=%+v", missing)
	}
	if _, duplicated := plan.Record.Progress[missingChildID]; duplicated {
		t.Fatal("missing child was activated into both States and Progress")
	}

	for _, field := range []struct {
		name  string
		state dnfrepo.QuestState
	}{
		{name: "States", state: plan.Record.States[parentID]},
		{name: "Progress", state: plan.Record.Progress[parentID]},
	} {
		if field.state.Status != "active" || field.state.ProgressValue != 0 {
			t.Fatalf("%s parent did not use completed child headers: %+v", field.name, field.state)
		}
	}
	if plan.Record.States[parentID].Extra["keep"] != "states-parent" ||
		plan.Record.Progress[parentID].Extra["keep"] != "progress-parent" {
		t.Fatalf("active parent Extra was not preserved")
	}
	if !reflect.DeepEqual(record, before) {
		t.Fatal("planner mutated its partial-record input")
	}

	stateParent := plan.Record.States[parentID]
	stateParent.Extra["keep"] = "plan-only"
	plan.Record.States[parentID] = stateParent
	progressParent := plan.Record.Progress[parentID]
	progressParent.Extra["keep"] = "plan-only"
	plan.Record.Progress[parentID] = progressParent
	progressChild := plan.Record.Progress[completedChildID]
	progressChild.Extra["shadowed"] = "plan-only"
	plan.Record.Progress[completedChildID] = progressChild
	if record.States[parentID].Extra["keep"] != "states-parent" ||
		record.Progress[parentID].Extra["keep"] != "progress-parent" ||
		record.Progress[completedChildID].Extra["shadowed"] != "progress-child" {
		t.Fatal("plan aliases active Extra maps from the partial-record input")
	}

	replay, err := catalog.PlanHuntEnemyKill(plan.Record, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Completions) != 0 || len(replay.ChangedFields) != 0 {
		t.Fatalf("replay reactivated the child: completions=%+v fields=%v", replay.Completions, replay.ChangedFields)
	}
	if !reflect.DeepEqual(replay.Record, plan.Record) {
		t.Fatal("replay changed an already reconciled partial record")
	}
}

func TestPlanHuntMonsterKillPartialPlanningStateShadowAndExtraIsolation(t *testing.T) {
	const questID int64 = 200
	completedAt := time.Date(2026, 10, 30, 17, 45, 0, 0, time.UTC)
	catalog := &Catalog{byID: map[int64]Definition{
		questID: {
			ID: questID, Path: "n_quest/partial_monster.qst", Type: "[hunt monster]",
			IntData: []int64{311, 2, 9602, 2},
		},
	}}
	record := dnfrepo.QuestRecord{
		PlanningPartial: true,
		CharacterID:     "partial-planning",
		States: map[int64]dnfrepo.QuestState{
			questID: {
				Status: "active", ProgressValue: 2,
				Extra: map[string]string{"keep": "states"},
			},
		},
		Progress: map[int64]dnfrepo.QuestState{
			questID: {
				Status: "active", ProgressValue: 2,
				Extra: map[string]string{"keep": "progress"},
			},
		},
	}
	before := dnfrepo.CloneQuest(record)
	plan, err := catalog.PlanHuntMonsterKill(record, HuntMonsterKillInput{
		DungeonID:     311,
		Difficulty:    2,
		MonsterCode:   9602,
		CompletionKey: "partial-planning/monster/9602",
		CompletedAt:   completedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Completions) != 1 || plan.Completions[0].QuestID != questID ||
		plan.Completions[0].Field != dnfrepo.QuestFieldStates ||
		plan.Completions[0].PreviousTrigger != 2 || plan.Completions[0].CurrentTrigger != 1 {
		t.Fatalf("completions=%+v", plan.Completions)
	}
	if want := []dnfrepo.QuestField{dnfrepo.QuestFieldStates}; !reflect.DeepEqual(plan.ChangedFields, want) {
		t.Fatalf("changed fields=%v, want=%v", plan.ChangedFields, want)
	}
	if state := plan.Record.States[questID]; state.ProgressValue != 1 || state.Extra["keep"] != "states" {
		t.Fatalf("States hunt state=%+v", state)
	}
	if shadowed := plan.Record.Progress[questID]; shadowed.ProgressValue != 2 || shadowed.Extra["keep"] != "progress" {
		t.Fatalf("Progress alias was changed despite States precedence: %+v", shadowed)
	}
	if !reflect.DeepEqual(record, before) {
		t.Fatal("planner mutated its partial-record input")
	}

	state := plan.Record.States[questID]
	state.Extra["keep"] = "plan-only"
	plan.Record.States[questID] = state
	progress := plan.Record.Progress[questID]
	progress.Extra["keep"] = "plan-only"
	plan.Record.Progress[questID] = progress
	if record.States[questID].Extra["keep"] != "states" || record.Progress[questID].Extra["keep"] != "progress" {
		t.Fatal("plan aliases active Extra maps from the partial-record input")
	}
}
