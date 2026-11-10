package quest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type clearMapDeltaInputRecorder struct {
	r.QuestRepository
	before, after r.QuestRecord
	fields        []r.QuestField
	calls         int
	failure       error
}

func (s *clearMapDeltaInputRecorder) SaveQuestChanges(_ context.Context, before, after r.QuestRecord, fields ...r.QuestField) error {
	s.calls++
	s.before, s.after = r.CloneQuest(before), r.CloneQuest(after)
	s.fields = append([]r.QuestField(nil), fields...)
	return s.failure
}

type clearMapOpaqueQuestStore struct {
	r.QuestRepository
	fullSaves int
}

func (s *clearMapOpaqueQuestStore) Save(ctx context.Context, record r.QuestRecord) error {
	s.fullSaves++
	return s.QuestRepository.Save(ctx, record)
}

func TestOrdinaryClearWithoutDeltaRefusesToRewriteWeddingPreparation(t *testing.T) {
	ctx := context.Background()
	for _, field := range []string{"states", "progress"} {
		for _, weddingID := range []int64{20737, 20738} {
			t.Run(field+"/"+fmt.Sprint(weddingID), func(t *testing.T) {
				group := memory.NewMemoryGroup()
				if err := group.Character.Save(ctx, r.CharacterRecord{CharacterID: "19", AccountID: "ordinary"}); err != nil {
					t.Fatal(err)
				}
				before := r.QuestRecord{CharacterID: "19", States: map[int64]r.QuestState{3145: {Status: "active", ProgressValue: 1}}}
				weddingState := r.QuestState{Status: "active", ProgressValue: 5}
				if field == "states" {
					before.States[weddingID] = weddingState
				} else {
					before.Progress = map[int64]r.QuestState{weddingID: weddingState}
				}
				if err := group.Quest.Save(ctx, before); err != nil {
					t.Fatal(err)
				}
				// A decorator exposing only the base repository must not be
				// bypassed even when its hidden base advertises delta support.
				delta := &clearMapDeltaInputRecorder{QuestRepository: group.Quest}
				opaque := &clearMapOpaqueQuestStore{QuestRepository: delta}
				group.Quest = opaque
				owner, err := NewOwner(group)
				if err != nil {
					t.Fatal(err)
				}
				result, err := owner.ApplyClearMapCompletion(ctx, clearMapTestCatalog(t), "19", ClearMapCompletionInput{
					MapID: 76126, CompletionKey: "ordinary-with-preparation-no-delta", CompletedAt: time.Now(),
				})
				if !errors.Is(err, ErrOwnerUnavailable) || len(result.Completions) != 0 || opaque.fullSaves != 0 || delta.calls != 0 {
					t.Fatalf("unsafe fallback result=%+v err=%v full=%d delta=%d", result, err, opaque.fullSaves, delta.calls)
				}
				after, _, err := group.Quest.Load(ctx, "19")
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("rejected ordinary clear changed snapshot: %+v err=%v", after, err)
				}
			})
		}
	}
}

func TestOrdinaryClearDeltaIncludesLinkedActivationAndParentChanges(t *testing.T) {
	ctx := context.Background()
	source := catalogTestSource{
		DefaultList:             "3054 `clear_sub.qst`\n3146 `parent.qst`\n",
		"n_quest/clear_sub.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]", "[type]\n`[clear map]`\n[main quest]\n3146\n[int data]\n76136\n"),
		"n_quest/parent.qst":    questCatalogTestDefinition("[epic]", 1, 99, "[all]", "[type]\n`[quest clear]`\n[int data]\n3157 3054\n"),
	}
	index, err := pvf.Build(ctx, source, pvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	group := memory.NewMemoryGroup()
	if err := group.Character.Save(ctx, r.CharacterRecord{CharacterID: "19", AccountID: "ordinary"}); err != nil {
		t.Fatal(err)
	}
	before := r.QuestRecord{CharacterID: "19", States: map[int64]r.QuestState{
		3157: {Status: "completed", ProgressValue: 0}, 3146: {Status: "active", ProgressValue: 2},
		20737: {Status: "active", ProgressValue: 5, Extra: map[string]string{"wedding_partner": "20"}},
	}, Progress: map[int64]r.QuestState{20738: {Status: "active", ProgressValue: 1}}}
	if err := group.Quest.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("delta input recorded before real persistence")
	delta := &clearMapDeltaInputRecorder{QuestRepository: group.Quest, failure: failure}
	group.Quest = delta
	owner, err := NewOwner(group)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.ApplyClearMapCompletion(ctx, catalog, "19", ClearMapCompletionInput{
		MapID: 76136, CompletionKey: "ordinary-linked-child-parent-delta", CompletedAt: time.Now(),
	})
	if !errors.Is(err, failure) || delta.calls != 1 {
		t.Fatalf("delta not used: calls=%d err=%v", delta.calls, err)
	}
	if !reflect.DeepEqual(delta.before, before) || !reflect.DeepEqual(delta.fields, []r.QuestField{r.QuestFieldStates}) {
		t.Fatalf("delta did not receive complete before/after: before=%+v fields=%v", delta.before, delta.fields)
	}
	if ids := r.ChangedQuestIDs(delta.before.States, delta.after.States); !reflect.DeepEqual(ids, []int64{3054, 3146}) {
		t.Fatalf("linked activation/parent delta IDs=%v", ids)
	}
	if delta.after.States[3054].Status != "completed" || delta.after.States[3146].ProgressValue != 0 {
		t.Fatal("delta omitted linked activation or parent progress")
	}
	if !r.QuestStatesEqual(delta.before.States[20737], delta.after.States[20737]) || !r.QuestStatesEqual(delta.before.Progress[20738], delta.after.Progress[20738]) {
		t.Fatal("ordinary clear changed unrelated wedding preparation")
	}
}
