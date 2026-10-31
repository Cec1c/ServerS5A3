package repository

import (
	"context"
	"errors"
	"testing"
	"time"
)

type questPlanningScopeTestRepo struct {
	records        map[string]QuestRecord
	planningErrors map[string][]error
	planningLoads  map[string]int
	fullLoads      int
	saveCalls      int
	saveErr        error
	afterSave      func()
}

func newQuestPlanningScopeTestRepo(records ...QuestRecord) *questPlanningScopeTestRepo {
	r := &questPlanningScopeTestRepo{
		records:        make(map[string]QuestRecord, len(records)),
		planningErrors: make(map[string][]error),
		planningLoads:  make(map[string]int),
	}
	for _, record := range records {
		r.records[record.CharacterID] = CloneQuest(record)
	}
	return r
}

func (r *questPlanningScopeTestRepo) Load(context.Context, string) (QuestRecord, bool, error) {
	r.fullLoads++
	return QuestRecord{}, false, errors.New("unexpected full load")
}

func (r *questPlanningScopeTestRepo) Save(_ context.Context, record QuestRecord) error {
	r.records[record.CharacterID] = CloneQuest(record)
	return nil
}

func (r *questPlanningScopeTestRepo) LoadQuestPlanning(_ context.Context, id string) (QuestRecord, bool, error) {
	r.planningLoads[id]++
	if errs := r.planningErrors[id]; len(errs) != 0 {
		err := errs[0]
		r.planningErrors[id] = errs[1:]
		return QuestRecord{}, false, err
	}
	record, found := r.records[id]
	if !found {
		return QuestRecord{}, false, nil
	}
	return CloneQuest(record), true, nil
}

func (r *questPlanningScopeTestRepo) SaveQuestChanges(_ context.Context, _ QuestRecord, after QuestRecord, _ ...QuestField) error {
	r.saveCalls++
	if r.saveErr != nil {
		return r.saveErr
	}
	r.records[after.CharacterID] = CloneQuest(after)
	if r.afterSave != nil {
		r.afterSave()
	}
	return nil
}

func (r *questPlanningScopeTestRepo) updateState(characterID string, questID int64, update func(*QuestState)) {
	record := CloneQuest(r.records[characterID])
	state := record.States[questID]
	update(&state)
	record.States[questID] = state
	r.records[characterID] = record
}

func questPlanningScopeTestRecord(characterID string, progress int64, receipt string) QuestRecord {
	return QuestRecord{
		CharacterID: characterID,
		States: map[int64]QuestState{
			1: {Status: "active", TriggerType: 2, ProgressValue: progress, Extra: map[string]string{"receipt": receipt}},
			2: {Status: "completed"},
		},
		Progress: map[int64]QuestState{
			3: {Status: "active", ProgressValue: 7, Extra: map[string]string{"progress_receipt": receipt}},
		},
		UpdatedAt: time.Unix(1800000000, 0),
	}
}

func assertQuestPlanningLoadCount(t *testing.T, repo *questPlanningScopeTestRepo, id string, want int) {
	t.Helper()
	if got := repo.planningLoads[id]; got != want {
		t.Fatalf("LoadQuestPlanning(%q) calls = %d, want %d", id, got, want)
	}
	if repo.fullLoads != 0 {
		t.Fatalf("unexpected fallback full loads = %d", repo.fullLoads)
	}
}

func TestQuestPlanningScopeReusesClonedSnapshot(t *testing.T) {
	repo := newQuestPlanningScopeTestRepo(questPlanningScopeTestRecord("character-a", 4, "kept"))
	ctx := WithQuestPlanningScope(context.Background(), repo, "character-a")

	first, found, err := LoadQuestPlanning(ctx, repo, "character-a")
	if err != nil || !found {
		t.Fatalf("first planning load: found=%v err=%v", found, err)
	}
	assertQuestPlanningLoadCount(t, repo, "character-a", 1)

	state := first.States[1]
	state.Extra["receipt"] = "caller mutation"
	first.States[1] = state
	progress := first.Progress[3]
	progress.Extra["progress_receipt"] = "caller mutation"
	first.Progress[3] = progress

	second, found, err := LoadQuestPlanning(ctx, repo, "character-a")
	if err != nil || !found {
		t.Fatalf("cached planning load: found=%v err=%v", found, err)
	}
	assertQuestPlanningLoadCount(t, repo, "character-a", 1)
	if got := second.States[1].Extra["receipt"]; got != "kept" {
		t.Fatalf("cached state receipt = %q, want kept", got)
	}
	if got := second.Progress[3].Extra["progress_receipt"]; got != "kept" {
		t.Fatalf("cached progress receipt = %q, want kept", got)
	}

	state = second.States[1]
	state.Extra["receipt"] = "cached caller mutation"
	second.States[1] = state
	third, found, err := LoadQuestPlanning(ctx, repo, "character-a")
	if err != nil || !found {
		t.Fatalf("second cached planning load: found=%v err=%v", found, err)
	}
	assertQuestPlanningLoadCount(t, repo, "character-a", 1)
	if got := third.States[1].Extra["receipt"]; got != "kept" {
		t.Fatalf("mutated cached receipt = %q, want kept", got)
	}
}

func TestQuestPlanningScopeIsolatesRequests(t *testing.T) {
	baseContext := context.Background()

	t.Run("independent contexts", func(t *testing.T) {
		repo := newQuestPlanningScopeTestRepo(questPlanningScopeTestRecord("character-a", 4, "a"))
		ctxA := WithQuestPlanningScope(baseContext, repo, "character-a")
		ctxB := WithQuestPlanningScope(baseContext, repo, "character-a")

		for _, ctx := range []context.Context{ctxA, ctxB} {
			if _, found, err := LoadQuestPlanning(ctx, repo, "character-a"); err != nil || !found {
				t.Fatalf("scoped load: found=%v err=%v", found, err)
			}
		}
		assertQuestPlanningLoadCount(t, repo, "character-a", 2)

		if _, _, err := LoadQuestPlanning(ctxA, repo, "character-a"); err != nil {
			t.Fatalf("first scope reload: %v", err)
		}
		assertQuestPlanningLoadCount(t, repo, "character-a", 2)
	})

	t.Run("different character ids", func(t *testing.T) {
		repo := newQuestPlanningScopeTestRepo(
			questPlanningScopeTestRecord("character-a", 4, "a"),
			questPlanningScopeTestRecord("character-b", 5, "b"),
		)
		ctxA := WithQuestPlanningScope(baseContext, repo, "character-a")
		ctxB := WithQuestPlanningScope(baseContext, repo, "character-b")

		for id, ctx := range map[string]context.Context{"character-a": ctxA, "character-b": ctxB} {
			if _, found, err := LoadQuestPlanning(ctx, repo, id); err != nil || !found {
				t.Fatalf("load %q: found=%v err=%v", id, found, err)
			}
		}
		assertQuestPlanningLoadCount(t, repo, "character-a", 1)
		assertQuestPlanningLoadCount(t, repo, "character-b", 1)
	})

	t.Run("different repositories", func(t *testing.T) {
		repoA := newQuestPlanningScopeTestRepo(questPlanningScopeTestRecord("character-a", 4, "a"))
		repoB := newQuestPlanningScopeTestRepo(questPlanningScopeTestRecord("character-a", 5, "b"))
		ctxA := WithQuestPlanningScope(baseContext, repoA, "character-a")
		ctxB := WithQuestPlanningScope(baseContext, repoB, "character-a")

		if _, found, err := LoadQuestPlanning(ctxA, repoA, "character-a"); err != nil || !found {
			t.Fatalf("repository A load: found=%v err=%v", found, err)
		}
		if _, found, err := LoadQuestPlanning(ctxB, repoB, "character-a"); err != nil || !found {
			t.Fatalf("repository B load: found=%v err=%v", found, err)
		}
		assertQuestPlanningLoadCount(t, repoA, "character-a", 1)
		assertQuestPlanningLoadCount(t, repoB, "character-a", 1)
	})
}

func TestQuestPlanningScopeDoesNotCacheLoadErrors(t *testing.T) {
	repo := newQuestPlanningScopeTestRepo(questPlanningScopeTestRecord("character-a", 4, "kept"))
	wantErr := errors.New("injected planning read failure")
	repo.planningErrors["character-a"] = []error{wantErr}
	ctx := WithQuestPlanningScope(context.Background(), repo, "character-a")

	if _, found, err := LoadQuestPlanning(ctx, repo, "character-a"); found || !errors.Is(err, wantErr) {
		t.Fatalf("failed planning load: found=%v err=%v", found, err)
	}
	assertQuestPlanningLoadCount(t, repo, "character-a", 1)

	record, found, err := LoadQuestPlanning(ctx, repo, "character-a")
	if err != nil || !found {
		t.Fatalf("retry planning load: found=%v err=%v", found, err)
	}
	if got := record.States[1].Extra["receipt"]; got != "kept" {
		t.Fatalf("retried receipt = %q, want kept", got)
	}
	assertQuestPlanningLoadCount(t, repo, "character-a", 2)
}

func TestQuestPlanningScopeChecksCanceledContextOnCacheHit(t *testing.T) {
	repo := newQuestPlanningScopeTestRepo(questPlanningScopeTestRecord("character-a", 4, "kept"))
	parent, cancel := context.WithCancel(context.Background())
	ctx := WithQuestPlanningScope(parent, repo, "character-a")
	if _, found, err := LoadQuestPlanning(ctx, repo, "character-a"); err != nil || !found {
		t.Fatalf("prime planning load: found=%v err=%v", found, err)
	}
	assertQuestPlanningLoadCount(t, repo, "character-a", 1)

	cancel()
	record, found, err := LoadQuestPlanning(ctx, repo, "character-a")
	if record.CharacterID != "" || found || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cache hit: record=%+v found=%v err=%v", record, found, err)
	}
	assertQuestPlanningLoadCount(t, repo, "character-a", 1)
}

func TestQuestPlanningScopeInvalidatesAfterSuccessfulSave(t *testing.T) {
	repo := newQuestPlanningScopeTestRepo(questPlanningScopeTestRecord("character-a", 4, "kept"))
	ctx := WithQuestPlanningScope(context.Background(), repo, "character-a")
	before, found, err := LoadQuestPlanning(ctx, repo, "character-a")
	if err != nil || !found {
		t.Fatalf("prime planning load: found=%v err=%v", found, err)
	}
	assertQuestPlanningLoadCount(t, repo, "character-a", 1)

	after := CloneQuest(before)
	state := after.States[1]
	state.ProgressValue = 5
	after.States[1] = state
	repo.afterSave = func() {
		repo.updateState("character-a", 2, func(state *QuestState) {
			state.Extra = map[string]string{"gm_change": "committed"}
		})
	}
	if err := SaveQuestChanges(ctx, repo, before, after, QuestFieldStates); err != nil {
		t.Fatalf("SaveQuestChanges: %v", err)
	}
	if repo.saveCalls != 1 {
		t.Fatalf("SaveQuestChanges calls = %d, want 1", repo.saveCalls)
	}

	reloaded, found, err := LoadQuestPlanning(ctx, repo, "character-a")
	if err != nil || !found {
		t.Fatalf("post-save planning load: found=%v err=%v", found, err)
	}
	assertQuestPlanningLoadCount(t, repo, "character-a", 2)
	if got := reloaded.States[1].ProgressValue; got != 5 {
		t.Fatalf("saved progress = %d, want 5", got)
	}
	if got := reloaded.States[2].Extra["gm_change"]; got != "committed" {
		t.Fatalf("unaffected quest was not reread after save: %q", got)
	}
}

func TestQuestPlanningScopeInvalidatesAfterFailedSave(t *testing.T) {
	repo := newQuestPlanningScopeTestRepo(questPlanningScopeTestRecord("character-a", 4, "kept"))
	wantErr := errors.New("injected planning write failure")
	repo.saveErr = wantErr
	ctx := WithQuestPlanningScope(context.Background(), repo, "character-a")
	before, found, err := LoadQuestPlanning(ctx, repo, "character-a")
	if err != nil || !found {
		t.Fatalf("prime planning load: found=%v err=%v", found, err)
	}
	assertQuestPlanningLoadCount(t, repo, "character-a", 1)

	after := CloneQuest(before)
	after.States[1] = QuestState{Status: "active", TriggerType: 2, ProgressValue: 5}
	repo.updateState("character-a", 2, func(state *QuestState) {
		state.Extra = map[string]string{"gm_change": "visible to retry"}
	})
	if err := SaveQuestChanges(ctx, repo, before, after, QuestFieldStates); !errors.Is(err, wantErr) {
		t.Fatalf("SaveQuestChanges error = %v, want %v", err, wantErr)
	}
	if repo.saveCalls != 1 {
		t.Fatalf("SaveQuestChanges calls = %d, want 1", repo.saveCalls)
	}

	reloaded, found, err := LoadQuestPlanning(ctx, repo, "character-a")
	if err != nil || !found {
		t.Fatalf("post-failure planning load: found=%v err=%v", found, err)
	}
	assertQuestPlanningLoadCount(t, repo, "character-a", 2)
	if got := reloaded.States[1].ProgressValue; got != 4 {
		t.Fatalf("failed save changed progress to %d, want 4", got)
	}
	if got := reloaded.States[2].Extra["gm_change"]; got != "visible to retry" {
		t.Fatalf("unaffected quest was not reread after failure: %q", got)
	}
}
