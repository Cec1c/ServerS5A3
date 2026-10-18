package quest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestOwnerApplyRoomClearMapCompletionPersistsAndReplaysIdempotently(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Quest.Save(ctx, roomClearMapActiveRecord()); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	input := RoomClearMapInput{
		DungeonID:      82,
		MapID:          92247,
		LinkedQuestIDs: []int64{3560},
		CompletionKey:  "run-19/room/92247",
		CompletedAt:    time.Date(2026, 10, 18, 14, 5, 0, 0, time.UTC),
	}

	first, err := owner.ApplyRoomClearMapCompletion(ctx, roomClearMapTestCatalog(t), "19", input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Applied || first.Idempotent || len(first.Completions) != 1 ||
		len(first.ChangedFields) != 1 || first.PostCommitQuest.States[3560].Status != "completed" ||
		first.PostCommitQuest.States[3525].ProgressValue != 0 {
		t.Fatalf("first result = %+v", first)
	}
	stored, found, err := repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load stored found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(stored, first.PostCommitQuest) {
		t.Fatalf("stored=%+v\npost=%+v", stored, first.PostCommitQuest)
	}

	second, err := owner.ApplyRoomClearMapCompletion(ctx, roomClearMapTestCatalog(t), "19", input)
	if err != nil {
		t.Fatal(err)
	}
	if second.Applied || !second.Idempotent || len(second.ChangedFields) != 0 ||
		len(second.Completions) != 1 || !second.Completions[0].Idempotent {
		t.Fatalf("second result = %+v", second)
	}
	afterReplay, found, err := repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load replay found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(afterReplay, first.PostCommitQuest) {
		t.Fatalf("replay mutated stored record\nstored=%+v\nreplay=%+v", first.PostCommitQuest, afterReplay)
	}
}

func TestOwnerApplyRoomClearMapCompletionRollsBackSaveThenFailsAndRetries(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account"}); err != nil {
		t.Fatal(err)
	}
	original := roomClearMapActiveRecord()
	if err := repos.Quest.Save(ctx, original); err != nil {
		t.Fatal(err)
	}

	commitErr := errors.New("late room clear-map commit failure")
	saveCalls := 0
	failing := repos
	failing.CharacterSettlement = &roomClearMapFailAfterApply{
		CharacterSettlementUnitOfWork: repos.CharacterSettlement,
		err:                           commitErr,
		saveCalls:                     &saveCalls,
	}
	failingOwner, err := NewOwner(failing)
	if err != nil {
		t.Fatal(err)
	}
	input := RoomClearMapInput{
		DungeonID:      82,
		MapID:          92247,
		LinkedQuestIDs: []int64{3560},
		CompletionKey:  "run-19/retry/92247",
		CompletedAt:    time.Date(2026, 10, 18, 14, 6, 0, 0, time.UTC),
	}
	if _, err := failingOwner.ApplyRoomClearMapCompletion(ctx, roomClearMapTestCatalog(t), "19", input); !errors.Is(err, commitErr) {
		t.Fatalf("late commit error = %v, want %v", err, commitErr)
	}
	if saveCalls != 1 {
		t.Fatalf("transaction quest saves = %d, want 1 before injected failure", saveCalls)
	}
	stored, found, err := repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load rollback found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(stored, original) {
		t.Fatalf("late failure was not rolled back\nstored=%+v\noriginal=%+v", stored, original)
	}

	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := owner.ApplyRoomClearMapCompletion(ctx, roomClearMapTestCatalog(t), "19", input)
	if err != nil {
		t.Fatal(err)
	}
	if !retried.Applied || retried.PostCommitQuest.States[3560].Status != "completed" ||
		retried.PostCommitQuest.States[3525].ProgressValue != 0 {
		t.Fatalf("retry result = %+v", retried)
	}
	stored, found, err = repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load retry found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(stored, retried.PostCommitQuest) {
		t.Fatalf("retry stored=%+v\npost=%+v", stored, retried.PostCommitQuest)
	}
}

type roomClearMapFailAfterApply struct {
	dnfrepo.CharacterSettlementUnitOfWork
	err       error
	saveCalls *int
}

func (u *roomClearMapFailAfterApply) WithinCharacterSettlement(
	ctx context.Context,
	characterID string,
	apply func(dnfrepo.Group) error,
) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, characterID, func(tx dnfrepo.Group) error {
		tx.Quest = roomClearMapCountingQuest{QuestRepository: tx.Quest, saveCalls: u.saveCalls}
		if err := apply(tx); err != nil {
			return err
		}
		return u.err
	})
}

type roomClearMapCountingQuest struct {
	dnfrepo.QuestRepository
	saveCalls *int
}

func (q roomClearMapCountingQuest) Save(ctx context.Context, record dnfrepo.QuestRecord) error {
	*q.saveCalls++
	return q.QuestRepository.Save(ctx, record)
}
