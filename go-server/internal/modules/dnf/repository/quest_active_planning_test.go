package repository

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type questActivePlanningTestRepo struct {
	active      QuestRecord
	activeLoads int
	idStates    map[int64]QuestState
	idsFound    bool
	idLoads     int
	idRequests  [][]int64
	saveCalls   int
	saveErr     error
	beforeSave  func()
}

func newQuestActivePlanningTestRepo(record QuestRecord) *questActivePlanningTestRepo {
	return &questActivePlanningTestRepo{active: CloneQuest(record),
		idStates: make(map[int64]QuestState), idsFound: true}
}
func (r *questActivePlanningTestRepo) Load(context.Context, string) (QuestRecord, bool, error) {
	return QuestRecord{}, false, errors.New("unexpected full load")
}
func (r *questActivePlanningTestRepo) Save(context.Context, QuestRecord) error { return nil }
func (r *questActivePlanningTestRepo) LoadQuestActivePlanning(context.Context, string) (QuestRecord, bool, error) {
	r.activeLoads++
	return CloneQuest(r.active), true, nil
}
func (r *questActivePlanningTestRepo) LoadQuestIDs(_ context.Context, id string, ids []int64) (QuestRecord, bool, error) {
	r.idLoads++
	r.idRequests = append(r.idRequests, append([]int64(nil), ids...))
	if !r.idsFound {
		return QuestRecord{}, false, nil
	}
	record := QuestRecord{CharacterID: id, PlanningPartial: true}
	for _, questID := range ids {
		if state, ok := r.idStates[questID]; ok {
			if record.States == nil {
				record.States = make(map[int64]QuestState)
			}
			record.States[questID] = state
		}
	}
	return record, true, nil
}
func (r *questActivePlanningTestRepo) SaveQuestChanges(context.Context, QuestRecord, QuestRecord, ...QuestField) error {
	r.saveCalls++
	if r.beforeSave != nil {
		r.beforeSave()
	}
	return r.saveErr
}
func questActivePlanningTestRecord() QuestRecord {
	return QuestRecord{CharacterID: "character-a", PlanningPartial: true, States: map[int64]QuestState{
		1: {Status: "active", Extra: map[string]string{"source": "active"}}}}
}
func requireQuestActivePlanningCounts(t *testing.T, repo *questActivePlanningTestRepo, active, ids int) {
	t.Helper()
	if repo.activeLoads != active || repo.idLoads != ids {
		t.Fatalf("loads active=%d ids=%d, want %d/%d", repo.activeLoads, repo.idLoads, active, ids)
	}
}
func TestQuestActivePlanningReusesOneRequestSnapshot(t *testing.T) {
	repo := newQuestActivePlanningTestRepo(questActivePlanningTestRecord())
	repo.idStates[10] = QuestState{Status: "completed", Extra: map[string]string{"source": "dependency"}}
	ctx := WithQuestPlanningScope(context.Background(), repo, "character-a")
	deps := func(QuestRecord) []int64 { return []int64{10} }
	if _, found, err := LoadQuestActivePlanning(ctx, repo, "character-a"); err != nil || !found {
		t.Fatalf("active load: found=%v err=%v", found, err)
	}
	first, found, err := LoadQuestDependentPlanning(ctx, repo, "character-a", "catalog-a", deps)
	if err != nil || !found || first.States[10].Extra["source"] != "dependency" {
		t.Fatalf("dependent load: record=%+v found=%v err=%v", first, found, err)
	}
	if _, found, err := LoadQuestDependentPlanning(ctx, repo, "character-a", "catalog-a", deps); err != nil || !found {
		t.Fatalf("dependent cache hit: found=%v err=%v", found, err)
	}
	requireQuestActivePlanningCounts(t, repo, 1, 1)
}

func TestQuestActivePlanningDependencyIDsAreNormalized(t *testing.T) {
	tests := []struct {
		name      string
		ids, want []int64
	}{
		{"empty", nil, nil},
		{"duplicate and non-positive", []int64{0, -1, 10, 10, 0, 11, 11}, []int64{10, 11}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newQuestActivePlanningTestRepo(questActivePlanningTestRecord())
			ctx := WithQuestPlanningScope(context.Background(), repo, "character-a")
			deps := func(QuestRecord) []int64 { return tt.ids }
			if _, found, err := LoadQuestDependentPlanning(ctx, repo, "character-a", "catalog", deps); err != nil || !found {
				t.Fatalf("load: found=%v err=%v", found, err)
			}
			wantLoads := 0
			if len(tt.want) != 0 {
				wantLoads = 1
				if len(repo.idRequests) != 1 || !reflect.DeepEqual(repo.idRequests[0], tt.want) {
					t.Fatalf("dependency requests = %v, want [%v]", repo.idRequests, tt.want)
				}
			}
			requireQuestActivePlanningCounts(t, repo, 1, wantLoads)
		})
	}
}

func TestQuestActivePlanningCatalogKeysDoNotShareTaskSnapshots(t *testing.T) {
	repo := newQuestActivePlanningTestRepo(questActivePlanningTestRecord())
	repo.idStates[10] = QuestState{Status: "completed", Extra: map[string]string{"task": "ten"}}
	repo.idStates[20] = QuestState{Status: "completed", Extra: map[string]string{"task": "twenty"}}
	ctx := WithQuestPlanningScope(context.Background(), repo, "character-a")
	load := func(key string, dependency int64) QuestRecord {
		deps := func(QuestRecord) []int64 { return []int64{dependency} }
		record, found, err := LoadQuestDependentPlanning(ctx, repo, "character-a", key, deps)
		if err != nil || !found {
			t.Fatalf("load %s: found=%v err=%v", key, found, err)
		}
		return record
	}
	a, b, again := load("catalog-a", 10), load("catalog-b", 20), load("catalog-a", 10)
	if _, ok := a.States[20]; ok || a.States[10].Extra["task"] != "ten" {
		t.Fatalf("catalog-a snapshot contains catalog-b data: %+v", a.States)
	}
	if _, ok := b.States[10]; ok || b.States[20].Extra["task"] != "twenty" {
		t.Fatalf("catalog-b snapshot contains catalog-a data: %+v", b.States)
	}
	if _, hasB := again.States[20]; hasB || again.States[10].Extra["task"] != "ten" {
		t.Fatalf("catalog-a snapshot was polluted: %+v", again.States)
	}
	requireQuestActivePlanningCounts(t, repo, 1, 3)
}

func TestQuestActivePlanningReturnsIsolatedCopies(t *testing.T) {
	repo := newQuestActivePlanningTestRepo(questActivePlanningTestRecord())
	repo.idStates[10] = QuestState{Status: "completed", Extra: map[string]string{"source": "dependency"}}
	ctx := WithQuestPlanningScope(context.Background(), repo, "character-a")
	deps := func(QuestRecord) []int64 { return []int64{10} }
	first, found, err := LoadQuestDependentPlanning(ctx, repo, "character-a", "catalog", deps)
	if err != nil || !found {
		t.Fatalf("dependent prime: found=%v err=%v", found, err)
	}
	first.States[1].Extra["source"] = "caller"
	first.States[10].Extra["source"] = "caller"
	first.States[99] = QuestState{}
	second, found, err := LoadQuestDependentPlanning(ctx, repo, "character-a", "catalog", deps)
	if err != nil || !found || second.States[1].Extra["source"] != "active" ||
		second.States[10].Extra["source"] != "dependency" {
		t.Fatalf("caller mutation reached dependent cache: %+v found=%v err=%v", second.States, found, err)
	}
	if _, ok := second.States[99]; ok {
		t.Fatal("caller added quest to cached snapshot")
	}
	requireQuestActivePlanningCounts(t, repo, 1, 1)
}

func TestQuestActivePlanningSaveInvalidatesBothSnapshots(t *testing.T) {
	wantErr := errors.New("injected write failure")
	tests := []struct {
		name string
		err  error
	}{{"success", nil}, {"failure", wantErr}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newQuestActivePlanningTestRepo(questActivePlanningTestRecord())
			repo.idStates[10] = QuestState{Status: "completed", Extra: map[string]string{"source": "old"}}
			repo.saveErr = tt.err
			ctx := WithQuestPlanningScope(context.Background(), repo, "character-a")
			deps := func(QuestRecord) []int64 { return []int64{10} }
			before, found, err := LoadQuestDependentPlanning(ctx, repo, "character-a", "catalog", deps)
			if err != nil || !found {
				t.Fatalf("prime: found=%v err=%v", found, err)
			}
			repo.beforeSave = func() {
				repo.active.States[1] = QuestState{Status: "active", Extra: map[string]string{"source": "reloaded"}}
				repo.idStates[10] = QuestState{Status: "completed", Extra: map[string]string{"source": "reloaded"}}
			}
			saveErr := SaveQuestChanges(ctx, repo, before, CloneQuest(before), QuestFieldStates)
			if !errors.Is(saveErr, tt.err) || repo.saveCalls != 1 {
				t.Fatalf("save error=%v calls=%d, want %v/1", saveErr, repo.saveCalls, tt.err)
			}
			reloaded, found, err := LoadQuestDependentPlanning(ctx, repo, "character-a", "catalog", deps)
			if err != nil || !found || reloaded.States[1].Extra["source"] != "reloaded" ||
				reloaded.States[10].Extra["source"] != "reloaded" {
				t.Fatalf("post-save reload: record=%+v found=%v err=%v", reloaded, found, err)
			}
			requireQuestActivePlanningCounts(t, repo, 2, 2)
		})
	}
}

func TestQuestActivePlanningCanceledContextBypassesCache(t *testing.T) {
	repo := newQuestActivePlanningTestRepo(questActivePlanningTestRecord())
	repo.idStates[10] = QuestState{Status: "completed"}
	parent, cancel := context.WithCancel(context.Background())
	ctx := WithQuestPlanningScope(parent, repo, "character-a")
	deps := func(QuestRecord) []int64 { return []int64{10} }
	if _, found, err := LoadQuestDependentPlanning(ctx, repo, "character-a", "catalog", deps); err != nil || !found {
		t.Fatalf("prime: found=%v err=%v", found, err)
	}
	cancel()
	_, activeFound, activeErr := LoadQuestActivePlanning(ctx, repo, "character-a")
	_, dependentFound, dependentErr := LoadQuestDependentPlanning(ctx, repo, "character-a", "catalog", deps)
	if activeFound || !errors.Is(activeErr, context.Canceled) || dependentFound ||
		!errors.Is(dependentErr, context.Canceled) {
		t.Fatalf("canceled cache hits: active=%v/%v dependent=%v/%v",
			activeFound, activeErr, dependentFound, dependentErr)
	}
	requireQuestActivePlanningCounts(t, repo, 1, 1)
}

func TestQuestActivePlanningMissingDependencyReturnsVersionConflict(t *testing.T) {
	repo := newQuestActivePlanningTestRepo(questActivePlanningTestRecord())
	repo.idsFound = false
	ctx := WithQuestPlanningScope(context.Background(), repo, "character-a")
	deps := func(QuestRecord) []int64 { return []int64{10} }
	if record, found, err := LoadQuestDependentPlanning(ctx, repo, "character-a", "catalog", deps); found ||
		!errors.Is(err, ErrQuestVersionConflict) || record.CharacterID != "" {
		t.Fatalf("missing dependency: record=%+v found=%v err=%v", record, found, err)
	}
	repo.idsFound, repo.idStates[10] = true, QuestState{Status: "completed"}
	if _, found, err := LoadQuestDependentPlanning(ctx, repo, "character-a", "catalog", deps); err != nil || !found {
		t.Fatalf("retry: found=%v err=%v", found, err)
	}
	requireQuestActivePlanningCounts(t, repo, 2, 2)
}
