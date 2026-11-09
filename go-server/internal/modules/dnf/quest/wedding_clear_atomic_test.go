package quest

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/wedding"
)

type weddingClearQuestGate struct {
	r.CharacterTradeUnitOfWork
	unit                                 r.WeddingQuestUnitOfWork
	calls                                atomic.Uint32
	firstEntered, secondAttempt, release chan struct{}
	unlock                               sync.Once
}

func (g *weddingClearQuestGate) WithinWeddingQuests(ctx context.Context, ids []string, apply func(r.CharacterRepository, r.QuestRepository) error) error {
	call := g.calls.Add(1)
	if call == 2 {
		close(g.secondAttempt)
	}
	return g.unit.WithinWeddingQuests(ctx, ids, func(chars r.CharacterRepository, quests r.QuestRepository) error {
		if call == 1 {
			close(g.firstEntered)
			select {
			case <-g.release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return apply(chars, quests)
	})
}

func (g *weddingClearQuestGate) releaseFirst() { g.unlock.Do(func() { close(g.release) }) }

func TestWeddingClearAndCancellationSerializeWithoutRestoringRemovedQuests(t *testing.T) {
	for _, first := range []string{"clear", "cancel"} {
		t.Run(first+"-owns-transaction-first", func(t *testing.T) {
			catalog, original, accepted := weddingStoredClearFixture(t)
			group := original.repositories
			if _, err := original.ApplyAcceptAt(context.Background(), catalog, CharacterEligibility{Level: 30}, Command{
				AccountID: "a50002", SelectedCharacterID: 50002, QuestID: 20737,
			}, accepted); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"50001", "50002"} {
				if id == "50001" {
					character, _, err := group.Character.Load(context.Background(), id)
					if err != nil {
						t.Fatal(err)
					}
					if character.Stats == nil {
						character.Stats = map[string]int64{}
					}
					character.Stats["gold"] = 500000
					if err := r.SaveCharacterFields(context.Background(), group.Character, character, r.CharacterFieldStats); err != nil {
						t.Fatal(err)
					}
				}
				row, _, err := group.Quest.Load(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				row.States[90001] = r.QuestState{Status: "active", ProgressValue: 7}
				if err := group.Quest.Save(context.Background(), row); err != nil {
					t.Fatal(err)
				}
			}
			gate := &weddingClearQuestGate{CharacterTradeUnitOfWork: group.CharacterTrade,
				unit: group.CharacterTrade.(r.WeddingQuestUnitOfWork), firstEntered: make(chan struct{}),
				secondAttempt: make(chan struct{}), release: make(chan struct{})}
			defer gate.releaseFirst()
			group.CharacterTrade = gate
			owner, err := NewOwner(group)
			if err != nil {
				t.Fatal(err)
			}
			marriage := wedding.Owner{Repositories: group, Rules: &wedding.Rules{MinimumLevel: 30, QuestIDs: []int64{20737, 20738}, EngagementCancellationGold: 500000}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			type clearOutcome struct {
				result ClearMapPersistResult
				err    error
			}
			clearDone := make(chan clearOutcome, 1)
			cancelDone := make(chan error, 1)
			input := weddingNativeStoredClearInput(accepted.Add(time.Minute))
			clear := func() {
				result, err := owner.ApplyClearMapCompletion(ctx, catalog, "50001", input)
				clearDone <- clearOutcome{result, err}
			}
			cancelEngagement := func() {
				_, err := marriage.CancelEngagement(ctx, wedding.PairRequest{
					Actor:   wedding.Actor{AccountID: "a50001", CharacterID: "50001"},
					Partner: wedding.Actor{AccountID: "a50002", CharacterID: "50002"}, Now: accepted.Add(2 * time.Minute),
				})
				cancelDone <- err
			}
			if first == "clear" {
				go clear()
			} else {
				go cancelEngagement()
			}
			select {
			case <-gate.firstEntered:
			case <-ctx.Done():
				t.Fatal("first operation did not enter the shared transaction")
			}
			if first == "clear" {
				go cancelEngagement()
			} else {
				go clear()
			}
			select {
			case <-gate.secondAttempt:
			case <-ctx.Done():
				t.Fatal("second operation bypassed the shared transaction")
			}
			gate.releaseFirst()
			var cleared clearOutcome
			select {
			case cleared = <-clearDone:
			case <-ctx.Done():
				t.Fatal("clear did not finish")
			}
			if cleared.err != nil {
				t.Fatal(cleared.err)
			}
			select {
			case err := <-cancelDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("cancellation did not finish")
			}
			if first == "clear" {
				if len(cleared.result.Completions) != 1 || cleared.result.Completions[0].RemainingTrigger != 4 {
					t.Fatalf("first clear=%+v", cleared.result)
				}
			} else if len(cleared.result.Completions) != 0 || len(cleared.result.ChangedFields) != 0 {
				t.Fatalf("cancelled engagement still counted clear=%+v", cleared.result)
			}
			// A delayed replay must re-read the cancelled pair and removed tasks.
			late, err := owner.ApplyClearMapCompletion(ctx, catalog, "50001", input)
			if err != nil || len(late.Completions) != 0 || len(late.ChangedFields) != 0 {
				t.Fatalf("late clear=%+v err=%v", late, err)
			}
			for _, id := range []string{"50001", "50002"} {
				character, _, err := group.Character.Load(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				state, err := wedding.Read(character)
				if err != nil || state.Phase != wedding.Single || state.PartnerID != 0 {
					t.Fatalf("cancelled character=%+v err=%v", state, err)
				}
				if id == "50001" && character.Stats["gold"] != 0 {
					t.Fatal("cancellation fee did not commit exactly once")
				}
				row, _, err := group.Quest.Load(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				for _, qid := range []int64{20737, 20738} {
					if _, ok := row.States[qid]; ok {
						t.Fatalf("cancelled task %d restored in States", qid)
					}
					if _, ok := row.Progress[qid]; ok {
						t.Fatalf("cancelled task %d restored in Progress", qid)
					}
				}
				if row.States[90001].ProgressValue != 7 {
					t.Fatal("unrelated quest changed")
				}
			}
		})
	}
}

type weddingClearFailAfterApply struct {
	r.CharacterTradeUnitOfWork
	unit r.WeddingQuestUnitOfWork
	err  error
}

func (u weddingClearFailAfterApply) WithinWeddingQuests(ctx context.Context, ids []string, apply func(r.CharacterRepository, r.QuestRepository) error) error {
	return u.unit.WithinWeddingQuests(ctx, ids, func(chars r.CharacterRepository, quests r.QuestRepository) error {
		if err := apply(chars, quests); err != nil {
			return err
		}
		return u.err
	})
}

func TestWeddingClearTransactionFailureReturnsNoUncommittedProgress(t *testing.T) {
	catalog, owner, accepted := weddingStoredClearFixture(t)
	before, _, _ := owner.quests.Load(context.Background(), "50001")
	failure := errors.New("wedding pair commit failed")
	owner.repositories.CharacterTrade = weddingClearFailAfterApply{CharacterTradeUnitOfWork: owner.repositories.CharacterTrade,
		unit: owner.repositories.CharacterTrade.(r.WeddingQuestUnitOfWork), err: failure}
	result, err := owner.ApplyClearMapCompletion(context.Background(), catalog, "50001", weddingNativeStoredClearInput(accepted.Add(time.Minute)))
	if !errors.Is(err, failure) || len(result.Completions) != 0 || len(result.ChangedFields) != 0 {
		t.Fatalf("uncommitted result=%+v err=%v", result, err)
	}
	after, _, _ := owner.quests.Load(context.Background(), "50001")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed wedding transaction leaked progress or run receipt")
	}
}

func TestWeddingClearRequiresPairQuestUnitWithoutBaseRepositoryFallback(t *testing.T) {
	catalog, owner, accepted := weddingStoredClearFixture(t)
	before, _, _ := owner.quests.Load(context.Background(), "50001")
	owner.repositories.CharacterTrade = nil
	result, err := owner.ApplyClearMapCompletion(context.Background(), catalog, "50001", weddingNativeStoredClearInput(accepted.Add(time.Minute)))
	if !errors.Is(err, ErrOwnerUnavailable) || len(result.Completions) != 0 {
		t.Fatalf("missing unit result=%+v err=%v", result, err)
	}
	after, _, _ := owner.quests.Load(context.Background(), "50001")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("missing unit wrote through base quest repository")
	}
	// Ordinary clear-map completion still has its original quest-only path.
	group := memory.NewMemoryGroup()
	group.CharacterTrade = nil
	if err := group.Character.Save(context.Background(), r.CharacterRecord{CharacterID: "19", AccountID: "ordinary"}); err != nil {
		t.Fatal(err)
	}
	if err := group.Quest.Save(context.Background(), r.QuestRecord{CharacterID: "19", States: map[int64]r.QuestState{3145: {Status: "active", ProgressValue: 1}}}); err != nil {
		t.Fatal(err)
	}
	ordinary, err := NewOwner(group)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := ordinary.ApplyClearMapCompletion(context.Background(), clearMapTestCatalog(t), "19", ClearMapCompletionInput{MapID: 76126, CompletionKey: "ordinary-no-pair-unit", CompletedAt: accepted})
	if err != nil || len(completed.Completions) != 1 || completed.Completions[0].RemainingTrigger != 0 {
		t.Fatalf("ordinary clear=%+v err=%v", completed, err)
	}
}
