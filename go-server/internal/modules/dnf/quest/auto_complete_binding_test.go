package quest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestOwnerAutoCompleteMainRejectsChangedIdentityOrEligibility(t *testing.T) {
	for _, condition := range []string{"account", "job", "growth", "level", "binding_before_load", "binding_before_save"} {
		t.Run(condition, func(t *testing.T) {
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			c := repo.CharacterRecord{CharacterID: "17", AccountID: "account", Level: 90, Job: "2", Stats: map[string]int64{"grow_type": 0}}
			q := repo.QuestRecord{CharacterID: "17", States: map[int64]repo.QuestState{100: {Status: "active"}}}
			input := AutoCompleteMainInput{AccountID: "account", CharacterID: "17", Eligibility: CharacterEligibility{Level: 90, Job: 2}, CutoffLevel: 89, TargetQuestID: 100, CompletedAt: time.Now().UTC()}
			calls := 0
			switch condition {
			case "account":
				c.AccountID = "other"
			case "job":
				c.Job = "3"
			case "growth":
				c.Stats["grow_type"] = 1
			case "level":
				c.Level = 89
			case "binding_before_load":
				input.Revalidate = func() bool { calls++; return false }
			case "binding_before_save":
				input.Revalidate = func() bool { calls++; return calls == 1 }
			}
			if err := g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := g.Quest.Save(ctx, q); err != nil {
				t.Fatal(err)
			}
			owner, err := NewOwner(g)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = owner.ApplyAutoCompleteMain(ctx, autoCompleteMainTestCatalog(t), input); err == nil {
				t.Fatal("stale mutation was accepted")
			}
			stored, found, err := g.Quest.Load(ctx, "17")
			if err != nil || !found || !reflect.DeepEqual(q, stored) {
				t.Fatalf("quest mutated: found=%v err=%v got=%+v", found, err, stored)
			}
			if condition == "binding_before_save" && calls != 2 {
				t.Fatalf("validation calls=%d want=2", calls)
			}
		})
	}
}

type autoCompleteFailingLoad struct {
	repo.QuestRepository
	loads  int
	failAt int
}

func (r *autoCompleteFailingLoad) Load(ctx context.Context, id string) (repo.QuestRecord, bool, error) {
	r.loads++
	if r.loads >= r.failAt {
		return repo.QuestRecord{}, false, errors.New("injected quest read failure")
	}
	return r.QuestRepository.Load(ctx, id)
}

type autoCompleteVerifySettlement struct {
	repo.CharacterSettlementUnitOfWork
}

func (w autoCompleteVerifySettlement) WithinCharacterSettlement(ctx context.Context, id string, apply func(repo.Group) error) error {
	return w.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx repo.Group) error {
		tx.Quest = &autoCompleteFailingLoad{QuestRepository: tx.Quest, failAt: 2}
		return apply(tx)
	})
}

func TestOwnerAutoCompleteMainVerificationIsInsideCommit(t *testing.T) {
	for _, failInside := range []bool{true, false} {
		t.Run(map[bool]string{true: "verification_failure_rolls_back", false: "outside_reader_cannot_hide_commit"}[failInside], func(t *testing.T) {
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			stored := g.Quest
			c := repo.CharacterRecord{CharacterID: "17", AccountID: "account", Job: "2", Level: 90}
			q := repo.QuestRecord{CharacterID: "17", States: map[int64]repo.QuestState{100: {Status: "active"}}}
			if err := g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := g.Quest.Save(ctx, q); err != nil {
				t.Fatal(err)
			}
			outside := &autoCompleteFailingLoad{QuestRepository: g.Quest, failAt: 1}
			if failInside {
				g.CharacterSettlement = autoCompleteVerifySettlement{g.CharacterSettlement}
			} else {
				g.Quest = outside
			}
			o, err := NewOwner(g)
			if err != nil {
				t.Fatal(err)
			}
			result, err := o.ApplyAutoCompleteMain(ctx, autoCompleteMainTestCatalog(t), AutoCompleteMainInput{AccountID: "account", CharacterID: "17", Eligibility: CharacterEligibility{Level: 90, Job: 2}, CutoffLevel: 89, TargetQuestID: 100, CompletedAt: time.Now().UTC()})
			after, found, readErr := stored.Load(ctx, "17")
			if readErr != nil || !found {
				t.Fatal("load actual record", readErr)
			}
			if failInside {
				if err == nil || !reflect.DeepEqual(q, after) {
					t.Fatalf("verification did not roll back: err=%v quests=%+v", err, after)
				}
			} else if err != nil || outside.loads != 0 || after.States[100].Status != "completed" || !reflect.DeepEqual(result.PostCommitQuest, after) {
				t.Fatalf("commit snapshot lost: err=%v outsideReads=%d result=%+v actual=%+v", err, outside.loads, result.PostCommitQuest, after)
			}
		})
	}
}
