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
	"longheng.io/server/internal/modules/dnf/wedding"
)

func weddingAcceptCancellationFixture(t *testing.T) (*Owner, wedding.Owner, time.Time) {
	t.Helper()
	_, owner, marriage, now := weddingQuestFixture(t)
	marriage.Rules = &wedding.Rules{MinimumLevel: 30, QuestIDs: []int64{20737, 20738}, EngagementCancellationGold: 500000}
	if _, err := marriage.Engage(context.Background(), wedding.PairRequest{
		Actor: wedding.Actor{AccountID: "a1", CharacterID: "1"}, Partner: wedding.Actor{AccountID: "a2", CharacterID: "2"}, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	c, _, _ := owner.repositories.Character.Load(context.Background(), "1")
	if c.Stats == nil {
		c.Stats = map[string]int64{}
	}
	c.Stats["gold"] = 500000
	if err := r.SaveCharacterFields(context.Background(), owner.repositories.Character, c, r.CharacterFieldStats); err != nil {
		t.Fatal(err)
	}
	return owner, marriage, now
}

func TestWeddingAcceptAndCancellationSerializeBothPreparationTasks(t *testing.T) {
	catalog := weddingPreparationPVFCatalog(t, 20738, nil)
	for _, questID := range []uint16{20737, 20738} {
		for _, first := range []string{"accept", "cancel"} {
			t.Run(fmt.Sprint(questID)+"/"+first+"-first", func(t *testing.T) {
				original, marriage, now := weddingAcceptCancellationFixture(t)
				group := original.repositories
				if questID == 20738 {
					if err := group.Quest.Save(context.Background(), r.QuestRecord{CharacterID: "1", States: map[int64]r.QuestState{20737: {Status: "completed"}}}); err != nil {
						t.Fatal(err)
					}
				}
				gate := &weddingClearQuestGate{CharacterTradeUnitOfWork: group.CharacterTrade, unit: group.CharacterTrade.(r.WeddingQuestUnitOfWork),
					firstEntered: make(chan struct{}), secondAttempt: make(chan struct{}), release: make(chan struct{})}
				defer gate.releaseFirst()
				group.CharacterTrade = gate
				owner, err := NewOwner(group)
				if err != nil {
					t.Fatal(err)
				}
				marriage.Repositories = group
				ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				type outcome struct {
					result AcceptResult
					err    error
				}
				accepted := make(chan outcome, 1)
				cancelled := make(chan error, 1)
				accept := func() {
					result, err := owner.ApplyAcceptAt(ctx, catalog, CharacterEligibility{Level: 30, Job: 0}, Command{AccountID: "a1", SelectedCharacterID: 1, QuestID: questID}, now.Add(time.Minute))
					accepted <- outcome{result, err}
				}
				cancel := func() {
					_, err := marriage.CancelEngagement(ctx, wedding.PairRequest{Actor: wedding.Actor{AccountID: "a1", CharacterID: "1"}, Partner: wedding.Actor{AccountID: "a2", CharacterID: "2"}, Now: now.Add(2 * time.Minute)})
					cancelled <- err
				}
				if first == "accept" {
					go accept()
				} else {
					go cancel()
				}
				select {
				case <-gate.firstEntered:
				case <-ctx.Done():
					t.Fatal("first operation bypassed pair transaction")
				}
				if first == "accept" {
					go cancel()
				} else {
					go accept()
				}
				select {
				case <-gate.secondAttempt:
				case <-ctx.Done():
					t.Fatal("second operation bypassed pair transaction")
				}
				gate.releaseFirst()
				var result outcome
				select {
				case result = <-accepted:
				case <-ctx.Done():
					t.Fatal("accept did not finish")
				}
				select {
				case err := <-cancelled:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("cancel did not finish")
				}
				if first == "accept" {
					if result.err != nil || result.result.QuestID != questID {
						t.Fatalf("first accept=%+v err=%v", result.result, result.err)
					}
				} else if !errors.Is(result.err, ErrQuestNotAcceptable) || result.result.QuestID != 0 {
					t.Fatalf("cancelled pair accepted task=%+v err=%v", result.result, result.err)
				}
				for _, id := range []string{"1", "2"} {
					c, _, _ := group.Character.Load(ctx, id)
					state, err := wedding.Read(c)
					if err != nil || state.Phase != wedding.Single {
						t.Fatalf("cancelled state=%+v err=%v", state, err)
					}
					q, _, _ := group.Quest.Load(ctx, id)
					for _, qid := range []int64{20737, 20738} {
						if _, ok := q.States[qid]; ok {
							t.Fatal("late accept restored cancelled task")
						}
						if _, ok := q.Progress[qid]; ok {
							t.Fatal("late accept restored cancelled progress")
						}
					}
					if id == "1" && c.Stats["gold"] != 0 {
						t.Fatal("cancellation did not charge exactly once")
					}
				}
			})
		}
	}
}

func TestWeddingAcceptRequiresPairUnitAndFailedCommitReturnsNoSuccess(t *testing.T) {
	owner, _, now := weddingAcceptCancellationFixture(t)
	catalog := weddingPreparationPVFCatalog(t, 20738, nil)
	cmd := Command{AccountID: "a1", SelectedCharacterID: 1, QuestID: 20737}
	unit := owner.repositories.CharacterTrade
	owner.repositories.CharacterTrade = nil
	if result, err := owner.ApplyAcceptAt(context.Background(), catalog, CharacterEligibility{Level: 30}, cmd, now); !errors.Is(err, ErrOwnerUnavailable) || result.QuestID != 0 {
		t.Fatalf("missing unit accepted=%+v err=%v", result, err)
	}
	failure := errors.New("accept pair commit failed")
	owner.repositories.CharacterTrade = weddingClearFailAfterApply{CharacterTradeUnitOfWork: unit, unit: unit.(r.WeddingQuestUnitOfWork), err: failure}
	if result, err := owner.ApplyAcceptAt(context.Background(), catalog, CharacterEligibility{Level: 30}, cmd, now); !errors.Is(err, failure) || result.QuestID != 0 {
		t.Fatalf("failed commit accepted=%+v err=%v", result, err)
	}
	if q, found, err := owner.quests.Load(context.Background(), "1"); err != nil || found {
		t.Fatalf("failed accept created quest row=%+v found=%t err=%v", q, found, err)
	}
}

func TestOrdinaryAcceptPreservesFirstRowCreationAndRejectsUnsafeOpaqueSnapshot(t *testing.T) {
	ctx := context.Background()
	group := memory.NewMemoryGroup()
	if err := group.Character.Save(ctx, r.CharacterRecord{CharacterID: "19", AccountID: "ordinary", Job: "2", Level: 1}); err != nil {
		t.Fatal(err)
	}
	delta := &clearMapDeltaInputRecorder{QuestRepository: group.Quest, failure: errors.New("delta requires existing parent")}
	group.Quest = delta
	owner, _ := NewOwner(group)
	catalog := testAcceptCatalog(t, "[type]\n`[clear map]`\n[int data]\n76126\n")
	cmd := Command{AccountID: "ordinary", SelectedCharacterID: 19, QuestID: 3145}
	if result, err := owner.ApplyAcceptAt(ctx, catalog, CharacterEligibility{Level: 1, Job: 2}, cmd, time.Now()); err != nil || result.QuestID != 3145 || result.InitTrigger != 1 || delta.calls != 0 {
		t.Fatalf("first ordinary accept=%+v err=%v deltaCalls=%d", result, err, delta.calls)
	}
	before := r.QuestRecord{CharacterID: "19", States: map[int64]r.QuestState{20737: {Status: "active", ProgressValue: 5}}}
	if err := delta.QuestRepository.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	opaque := &clearMapOpaqueQuestStore{QuestRepository: delta}
	group.Quest = opaque
	owner, _ = NewOwner(group)
	if result, err := owner.ApplyAcceptAt(ctx, catalog, CharacterEligibility{Level: 1, Job: 2}, cmd, time.Now()); !errors.Is(err, ErrOwnerUnavailable) || result.QuestID != 0 || opaque.fullSaves != 0 {
		t.Fatalf("unsafe ordinary accept=%+v err=%v fullSaves=%d", result, err, opaque.fullSaves)
	}
	after, _, _ := group.Quest.Load(ctx, "19")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected opaque accept changed old wedding snapshot")
	}
}

func TestOrdinaryAcceptDeltaContainsEveryLinkedActivationAndLeavesWeddingRowsUnchanged(t *testing.T) {
	ctx := context.Background()
	source := catalogTestSource{
		DefaultList:             "3146 `parent.qst`\n3054 `clear_sub.qst`\n3157 `hunt_sub.qst`\n",
		"n_quest/parent.qst":    questCatalogTestDefinition("[epic]", 1, 99, "[all]", "[type]\n`[quest clear]`\n[int data]\n3157 3054\n"),
		"n_quest/clear_sub.qst": questCatalogTestDefinition("[sub]", 1, 99, "[all]", "[type]\n`[clear map]`\n[main quest]\n3146\n[int data]\n76126\n"),
		"n_quest/hunt_sub.qst":  questCatalogTestDefinition("[sub]", 1, 99, "[all]", "[type]\n`[hunt enemy]`\n[main quest]\n3146\n[int data]\n3 -1 13099 3 1\n"),
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
	if err := group.Character.Save(ctx, r.CharacterRecord{CharacterID: "19", AccountID: "ordinary", Job: "2", Level: 1}); err != nil {
		t.Fatal(err)
	}
	before := r.QuestRecord{CharacterID: "19", States: map[int64]r.QuestState{20737: {Status: "active", ProgressValue: 5}}, Progress: map[int64]r.QuestState{20738: {Status: "active", ProgressValue: 1}}}
	if err := group.Quest.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("accept delta input recorded")
	delta := &clearMapDeltaInputRecorder{QuestRepository: group.Quest, failure: failure}
	group.Quest = delta
	owner, _ := NewOwner(group)
	_, err = owner.ApplyAcceptAt(ctx, catalog, CharacterEligibility{Level: 1, Job: 2}, Command{AccountID: "ordinary", SelectedCharacterID: 19, QuestID: 3146}, time.Now())
	if !errors.Is(err, failure) || delta.calls != 1 {
		t.Fatalf("accept did not use delta: calls=%d err=%v", delta.calls, err)
	}
	if ids := r.ChangedQuestIDs(delta.before.States, delta.after.States); !reflect.DeepEqual(ids, []int64{3054, 3146, 3157}) {
		t.Fatalf("accept delta activation IDs=%v", ids)
	}
	if !r.QuestStatesEqual(delta.before.States[20737], delta.after.States[20737]) || !r.QuestStatesEqual(delta.before.Progress[20738], delta.after.Progress[20738]) {
		t.Fatal("ordinary accept changed wedding preparation")
	}
}
