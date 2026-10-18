package quest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestOwnerApplyFinishSettlementHonorRollsBackOnQuestSaveFailure(t *testing.T) {
	ctx := context.Background()
	repos := finishHonorSeed(t, ctx, 2, 250, 0, 90)
	beforeChar, _, _ := repos.Character.Load(ctx, "19")
	beforeSkill, _, _ := repos.Skill.Load(ctx, "19")
	beforeInventory, _, _ := repos.Inventory.Load(ctx, "19")
	beforeQuest, _, _ := repos.Quest.Load(ctx, "19")
	wantErr := errors.New("quest storage rejected receipt")
	repos.CharacterSettlement = finishHonorFailingSaveUOW{base: repos.CharacterSettlement, err: wantErr}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	input := finishHonorInput(t, finishOwnerProgression(t), func(context.Context) (*dnfhonor.Tables, error) { return finishHonorTables(t), nil })
	input.MaximumCharacterLevel = 2
	_, err = owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v", err)
	}
	afterChar, _, _ := repos.Character.Load(ctx, "19")
	afterSkill, _, _ := repos.Skill.Load(ctx, "19")
	afterInventory, _, _ := repos.Inventory.Load(ctx, "19")
	afterQuest, _, _ := repos.Quest.Load(ctx, "19")
	if !reflect.DeepEqual(beforeChar, afterChar) || !reflect.DeepEqual(beforeSkill, afterSkill) || !reflect.DeepEqual(beforeInventory, afterInventory) || !reflect.DeepEqual(beforeQuest, afterQuest) {
		t.Fatal("storage failure changed a settlement aggregate")
	}
}

func TestOwnerApplyFinishSettlementHonorLargeAwardCrossesSeveralLevelsToCap(t *testing.T) {
	ctx := context.Background()
	repos := finishHonorSeed(t, ctx, 1, 90, 0, 0)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	input := finishHonorInput(t, finishOwnerProgression(t), func(context.Context) (*dnfhonor.Tables, error) { return finishHonorTables(t), nil })
	input.MaximumCharacterLevel = 3
	input.ExperienceBonusPercent = 200
	got, err := owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExperienceDelta != 330 || got.NewLevel != 3 || got.SPDelta != 60 || got.HonorExpertGain != 170 {
		t.Fatalf("multi-level capped settlement=%+v", got)
	}
}

type finishHonorFailingSaveUOW struct {
	base dnfrepo.CharacterSettlementUnitOfWork
	err  error
}

func (u finishHonorFailingSaveUOW) WithinCharacterSettlement(ctx context.Context, id string, apply func(dnfrepo.Group) error) error {
	return u.base.WithinCharacterSettlement(ctx, id, func(tx dnfrepo.Group) error {
		tx.Quest = finishHonorFailingQuestSave{QuestRepository: tx.Quest, err: u.err}
		return apply(tx)
	})
}

type finishHonorFailingQuestSave struct {
	dnfrepo.QuestRepository
	err error
}

func (s finishHonorFailingQuestSave) Save(context.Context, dnfrepo.QuestRecord) error { return s.err }
