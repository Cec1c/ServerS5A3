package quest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

type pr55RecordingWeddingUnit struct {
	r.CharacterTradeUnitOfWork
	unit     r.WeddingQuestUnitOfWork
	recorder *clearMapDeltaInputRecorder
	failure  error
}

func (u *pr55RecordingWeddingUnit) WithinWeddingQuests(
	ctx context.Context,
	ids []string,
	apply func(r.CharacterRepository, r.QuestRepository) error,
) error {
	return u.unit.WithinWeddingQuests(ctx, ids, func(characters r.CharacterRepository, quests r.QuestRepository) error {
		u.recorder = &clearMapDeltaInputRecorder{QuestRepository: quests, failure: u.failure}
		return apply(characters, u.recorder)
	})
}

func TestPR55PairLockedWeddingAcceptUsesCompleteDeltaAndLoadedBefore(t *testing.T) {
	ctx := context.Background()
	owner, _, now := weddingAcceptCancellationFixture(t)
	catalog := weddingPreparationPVFCatalog(t, 20738, nil)
	loaded := r.QuestRecord{
		CharacterID: "1",
		States: map[int64]r.QuestState{
			9001: {Status: "active", ProgressValue: 7, Extra: map[string]string{"source": "loaded"}},
		},
		Progress: map[int64]r.QuestState{
			9002: {Status: "active", ProgressValue: 3, Extra: map[string]string{"source": "loaded"}},
		},
	}
	if err := owner.repositories.Quest.Save(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	unit, ok := owner.repositories.CharacterTrade.(r.WeddingQuestUnitOfWork)
	if !ok {
		t.Fatal("memory wedding quest unit unavailable")
	}
	failure := errors.New("record pair-locked delta")
	wrapped := &pr55RecordingWeddingUnit{
		CharacterTradeUnitOfWork: owner.repositories.CharacterTrade,
		unit:                     unit,
		failure:                  failure,
	}
	owner.repositories.CharacterTrade = wrapped

	result, err := owner.ApplyAcceptAt(ctx, catalog, CharacterEligibility{Level: 30, Job: 0}, Command{
		AccountID: "a1", SelectedCharacterID: 1, QuestID: 20737,
	}, now.Add(time.Minute))
	if !errors.Is(err, failure) || result.QuestID != 0 {
		t.Fatalf("pair-locked accept result=%+v err=%v", result, err)
	}
	if wrapped.recorder == nil || wrapped.recorder.calls != 1 {
		t.Fatalf("pair-locked delta recorder=%+v", wrapped.recorder)
	}
	if !reflect.DeepEqual(wrapped.recorder.before, loaded) {
		t.Fatalf("delta before changed: got=%+v want=%+v", wrapped.recorder.before, loaded)
	}
	if !reflect.DeepEqual(wrapped.recorder.fields, []r.QuestField{r.QuestFieldStates}) {
		t.Fatalf("delta fields=%v want states", wrapped.recorder.fields)
	}
	state, found := wrapped.recorder.after.States[20737]
	if !found || state.Status != "active" {
		t.Fatalf("pair-locked accept after states=%+v", wrapped.recorder.after.States)
	}
	if !r.QuestStatesEqual(wrapped.recorder.after.States[9001], loaded.States[9001]) ||
		!r.QuestStatesEqual(wrapped.recorder.after.Progress[9002], loaded.Progress[9002]) {
		t.Fatal("pair-locked accept delta changed unrelated quest rows")
	}
}
