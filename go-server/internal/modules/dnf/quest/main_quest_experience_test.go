package quest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestMainQuestExperienceMultiplierTriplesOriginalComponentsAndReplaysCommittedValues(t *testing.T) {
	ctx := context.Background()
	repositories := finishOwnerSeed(t, ctx)
	definition := finishOwnerCatalog().byID[3145]
	definition.Grade = "[epic]"
	catalog := mainQuestExperienceTestCatalog(definition)
	input := finishOwnerInput(finishOwnerProgression(t), finishOwnerAllocator(nil))
	input.MainQuestExperienceMultiplier = 3
	input.ExperienceBonusPercent = 20
	character, _, err := repositories.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	character.Stats["experience_potion_quest_basis_points"] = 1000
	character.Stats["experience_potion_quest_until_ms"] = input.CommittedAt.Add(time.Minute).UnixMilli()
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}

	first, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.BaseExperienceDelta != 330 ||
		first.ExperienceBonusDelta != 99 ||
		first.ExperienceDelta != 429 ||
		first.PreviousLevel != 1 ||
		first.NewLevel != 3 ||
		first.NewExperience != 429 ||
		first.SPDelta != 60 {
		t.Fatalf("main quest progression=%+v, want original 110+22+11=143 scaled to 330+66+33=429", first)
	}

	// The durable receipt owns the committed multiplier. A replay after both
	// the contract and potion expire must still return the three-times values.
	input.MainQuestExperienceMultiplier = 1
	input.ExperienceBonusPercent = 0
	input.CommittedAt = input.CommittedAt.Add(time.Hour)
	replayed, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed ||
		replayed.BaseExperienceDelta != 330 ||
		replayed.ExperienceBonusDelta != 99 ||
		replayed.ExperienceDelta != 429 ||
		replayed.NewLevel != 3 ||
		replayed.NewExperience != 429 ||
		replayed.SPDelta != 60 {
		t.Fatalf("main quest receipt replay=%+v", replayed)
	}
	character, found, err := repositories.Character.Load(ctx, "19")
	if err != nil || !found || character.Level != 3 || character.Stats["exp"] != 429 {
		t.Fatalf("committed or replayed character=%+v found=%t err=%v", character, found, err)
	}
}

func TestMainQuestExperienceMultiplierAppliesToEpicChildrenOnly(t *testing.T) {
	ctx := context.Background()
	parent := finishOwnerCatalog().byID[3145]
	parent.Grade = "[epic]"

	t.Run("side quest unchanged", func(t *testing.T) {
		repositories := finishOwnerSeed(t, ctx)
		input := finishOwnerInput(finishOwnerProgression(t), finishOwnerAllocator(nil))
		input.MainQuestExperienceMultiplier = 3
		owner, err := NewOwner(repositories)
		if err != nil {
			t.Fatal(err)
		}
		result, err := owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.BaseExperienceDelta != 110 || result.ExperienceBonusDelta != 0 || result.ExperienceDelta != 110 {
			t.Fatalf("side quest result=%+v", result)
		}
	})

	t.Run("epic child scaled", func(t *testing.T) {
		repositories := finishOwnerSeed(t, ctx)
		child := parent
		child.ID = 3146
		child.MainQuestID = parent.ID
		child.Grade = "[sub]"
		child.Path = "n_quest/finish_child.qst"
		mainQuestExperienceTestSeedState(t, repositories, child.ID, "run-19/op117/child")
		input := finishOwnerInput(finishOwnerProgression(t), finishOwnerAllocator(nil))
		input.QuestID = child.ID
		input.ExpectedCompletionKey = "run-19/op117/child"
		input.MainQuestExperienceMultiplier = 3
		owner, err := NewOwner(repositories)
		if err != nil {
			t.Fatal(err)
		}
		result, err := owner.ApplyFinishSettlement(ctx, mainQuestExperienceTestCatalog(parent, child), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.BaseExperienceDelta != 330 || result.ExperienceBonusDelta != 0 || result.ExperienceDelta != 330 {
			t.Fatalf("epic child result=%+v", result)
		}
	})

	t.Run("missing epic parent unchanged", func(t *testing.T) {
		repositories := finishOwnerSeed(t, ctx)
		child := parent
		child.ID = 3147
		child.MainQuestID = 9999
		child.Grade = "[sub]"
		child.Path = "n_quest/orphan_child.qst"
		mainQuestExperienceTestSeedState(t, repositories, child.ID, "run-19/op117/orphan")
		input := finishOwnerInput(finishOwnerProgression(t), finishOwnerAllocator(nil))
		input.QuestID = child.ID
		input.ExpectedCompletionKey = "run-19/op117/orphan"
		input.MainQuestExperienceMultiplier = 3
		owner, err := NewOwner(repositories)
		if err != nil {
			t.Fatal(err)
		}
		result, err := owner.ApplyFinishSettlement(ctx, mainQuestExperienceTestCatalog(child), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.BaseExperienceDelta != 110 || result.ExperienceDelta != 110 {
			t.Fatalf("orphan child result=%+v", result)
		}
	})

	t.Run("no experience remains zero", func(t *testing.T) {
		repositories := finishOwnerSeed(t, ctx)
		child := parent
		child.ID = 3148
		child.MainQuestID = parent.ID
		child.Grade = "[sub]"
		child.Path = "n_quest/no_exp_child.qst"
		child.NoExperience = true
		mainQuestExperienceTestSeedState(t, repositories, child.ID, "run-19/op117/no-exp")
		input := finishOwnerInput(finishOwnerProgression(t), finishOwnerAllocator(nil))
		input.QuestID = child.ID
		input.ExpectedCompletionKey = "run-19/op117/no-exp"
		input.MainQuestExperienceMultiplier = 3
		owner, err := NewOwner(repositories)
		if err != nil {
			t.Fatal(err)
		}
		result, err := owner.ApplyFinishSettlement(ctx, mainQuestExperienceTestCatalog(parent, child), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.BaseExperienceDelta != 0 || result.ExperienceBonusDelta != 0 || result.ExperienceDelta != 0 {
			t.Fatalf("no-experience child result=%+v", result)
		}
	})
}

func TestMainQuestExperienceMultiplierRejectsInvalidAndOverflowWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		multiplier int
		want       int
		wantError  bool
	}{
		{multiplier: 0, want: 1},
		{multiplier: 1, want: 1},
		{multiplier: 100, want: 100},
		{multiplier: -1, wantError: true},
		{multiplier: 101, wantError: true},
	} {
		got, err := normalizeMainQuestExperienceMultiplier(test.multiplier)
		if test.wantError {
			if !errors.Is(err, ErrFinishMainQuestExperienceMultiplierInvalid) {
				t.Fatalf("normalize(%d) error=%v", test.multiplier, err)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Fatalf("normalize(%d)=%d,%v want=%d", test.multiplier, got, err, test.want)
		}
	}

	ctx := context.Background()
	repositories := finishOwnerSeed(t, ctx)
	definition := finishOwnerCatalog().byID[3145]
	definition.Grade = "[epic]"
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	invalidInput := finishOwnerInput(finishOwnerProgression(t), finishOwnerAllocator(nil))
	invalidInput.MainQuestExperienceMultiplier = 101
	if _, err := owner.ApplyFinishSettlement(ctx, mainQuestExperienceTestCatalog(definition), invalidInput); !errors.Is(err, ErrFinishMainQuestExperienceMultiplierInvalid) {
		t.Fatalf("invalid input error=%v", err)
	}
	tables, err := progression.Load(ctx, finishProgressionSource{
		progression.ExperienceTablePath: "100 250 500\n",
		progression.SkillPointTablePath: "[sp table]\n1 0\n2 30\n3 30\n4 40\n[/sp table]\n[tp table]\n50 1\n[/tp table]\n",
		progression.QuestParameterPath:  "[difficulty]\n`N` 100\n[/difficulty]\n[exp reward table]\n3904515723 -1\n[gold reward table]\n10 -1\n[green level penalty]\n80\n[grey level penalty]\n30\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	allocatorCalls := 0
	allocator := func(record *dnfrepo.InventoryRecord, request FinishItemGrantRequest) ([]FinishCommittedItem, error) {
		allocatorCalls++
		return finishOwnerAllocator(nil)(record, request)
	}
	input := finishOwnerInput(tables, allocator)
	input.MainQuestExperienceMultiplier = 2
	_, err = owner.ApplyFinishSettlement(ctx, mainQuestExperienceTestCatalog(definition), input)
	if !errors.Is(err, ErrFinishMainQuestExperienceOverflow) {
		t.Fatalf("overflow error=%v", err)
	}
	if allocatorCalls != 0 {
		t.Fatalf("overflow allocated %d item grants", allocatorCalls)
	}
	character, found, err := repositories.Character.Load(ctx, "19")
	if err != nil || !found || character.Level != 1 || character.Stats["exp"] != 0 {
		t.Fatalf("overflow character=%+v found=%t err=%v", character, found, err)
	}
	quests, found, err := repositories.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("overflow quest found=%t err=%v", found, err)
	}
	if state := quests.States[3145]; state.Status != "active" || state.Extra["reward_state"] != "pending" || state.Extra[finishSettlementReceiptKey] != "" {
		t.Fatalf("overflow mutated quest=%+v", state)
	}
	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found || len(inventory.Slots) != 0 {
		t.Fatalf("overflow inventory=%+v found=%t err=%v", inventory, found, err)
	}
}

func TestMainQuestExperienceMultiplierLateFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	repositories := finishOwnerSeed(t, ctx)
	definition := finishOwnerCatalog().byID[3145]
	definition.Grade = "[epic]"
	input := finishOwnerInput(finishOwnerProgression(t), nil)
	input.MainQuestExperienceMultiplier = 3
	wantErr := errors.New("late main quest reward failure")
	input.AllocateItem = finishOwnerAllocator(wantErr)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}

	_, err = owner.ApplyFinishSettlement(ctx, mainQuestExperienceTestCatalog(definition), input)
	if !errors.Is(err, wantErr) {
		t.Fatalf("late failure error=%v", err)
	}
	character, found, err := repositories.Character.Load(ctx, "19")
	if err != nil || !found || character.Level != 1 || character.Stats["exp"] != 0 {
		t.Fatalf("rollback character=%+v found=%t err=%v", character, found, err)
	}
	skill, found, err := repositories.Skill.Load(ctx, "19")
	if err != nil || !found || !reflect.DeepEqual(skill.Points, dnfrepo.SkillPointState{SyncedLevel: 1}) {
		t.Fatalf("rollback skill=%+v found=%t err=%v", skill, found, err)
	}
	quests, found, err := repositories.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("rollback quest found=%t err=%v", found, err)
	}
	if state := quests.States[3145]; state.Status != "active" || state.Extra["reward_state"] != "pending" || state.Extra[finishSettlementReceiptKey] != "" {
		t.Fatalf("rollback mutated quest=%+v", state)
	}
	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found || len(inventory.Slots) != 0 {
		t.Fatalf("rollback inventory=%+v found=%t err=%v", inventory, found, err)
	}
}

func mainQuestExperienceTestCatalog(definitions ...Definition) *Catalog {
	catalog := &Catalog{
		ordered: append([]Definition(nil), definitions...),
		byID:    make(map[int64]Definition, len(definitions)),
	}
	for _, definition := range definitions {
		catalog.byID[definition.ID] = definition
	}
	return catalog
}

func mainQuestExperienceTestSeedState(t *testing.T, repositories dnfrepo.Group, questID int64, completionKey string) {
	t.Helper()
	if err := repositories.Quest.Save(context.Background(), dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			questID: {
				Status: "active",
				Extra: map[string]string{
					"completion_key":  completionKey,
					"completion_kind": "clear_map",
					"reward_state":    "pending",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}
