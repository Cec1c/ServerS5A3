package quest

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestOwnerApplyFinishSettlementCapAdvancesHonorExpertOnce(t *testing.T) {
	ctx := context.Background()
	repos := finishHonorSeed(t, ctx, 2, 250, 0, 90)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	honorCalls := 0
	input := finishHonorInput(t, finishOwnerProgression(t), func(context.Context) (*dnfhonor.Tables, error) {
		honorCalls++
		return finishHonorTables(t), nil
	})
	input.MaximumCharacterLevel = 2

	result, err := owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.NewLevel != 2 || result.PreviousLevel != 2 || result.NewExperience != 360 ||
		result.SPDelta != 0 || result.TPDelta != 0 || result.HonorExpertGain != 110 {
		t.Fatalf("capped settlement=%+v", result)
	}
	if honorCalls != 1 {
		t.Fatalf("honor loader calls=%d want=1", honorCalls)
	}
	character, _, err := repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if character.Level != 2 || character.Stats["exp"] != 360 ||
		character.Stats[dnfhonor.ExpertLevelStatKey] != 1 ||
		character.Stats[dnfhonor.ExpertProgressExperienceStatKey] != 100 {
		t.Fatalf("persisted character=%+v", character)
	}
	skill, _, err := repos.Skill.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if skill.Points.SyncedLevel != 2 || skill.Points.TotalSP != 300 || skill.Points.RemainingSP != 100 {
		t.Fatalf("capped skill points=%+v", skill.Points)
	}
}

func TestOwnerApplyFinishSettlementHonorExpertOnlyCountsCapOverflow(t *testing.T) {
	ctx := context.Background()
	repos := finishHonorSeed(t, ctx, 1, 90, 0, 50)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	honorCalls := 0
	input := finishHonorInput(t, finishOwnerProgression(t), func(context.Context) (*dnfhonor.Tables, error) {
		honorCalls++
		return finishHonorTables(t), nil
	})
	input.MaximumCharacterLevel = 2

	result, err := owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.PreviousLevel != 1 || result.NewLevel != 2 || result.NewExperience != 200 ||
		result.SPDelta != 30 || result.TPDelta != 0 || result.HonorExpertGain != 100 {
		t.Fatalf("threshold-crossing settlement=%+v", result)
	}
	if honorCalls != 1 {
		t.Fatalf("honor loader calls=%d want=1", honorCalls)
	}
	character, _, err := repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if character.Level != 2 || character.Stats[dnfhonor.ExpertLevelStatKey] != 1 ||
		character.Stats[dnfhonor.ExpertProgressExperienceStatKey] != 50 {
		t.Fatalf("overflow honor state=%+v", character.Stats)
	}
}

func TestOwnerApplyFinishSettlementNoExperienceSkipsHonorTables(t *testing.T) {
	ctx := context.Background()
	repos := finishHonorSeed(t, ctx, 2, 250, 0, 90)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	input := finishHonorInput(t, finishOwnerProgression(t), func(context.Context) (*dnfhonor.Tables, error) {
		t.Fatal("honor tables loaded for a zero-experience finish")
		return nil, nil
	})
	input.MaximumCharacterLevel = 2
	input.AllocateItem = nil
	definition := Definition{
		ID: 3145, Path: "n_quest/no_exp.qst", Type: "[clear map]",
		LevelMin: 1, LevelMax: 99, Difficulty: "N", NoExperience: true,
	}
	catalog := &Catalog{ordered: []Definition{definition}, byID: map[int64]Definition{definition.ID: definition}}

	result, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExperienceDelta != 0 || result.HonorExpertGain != 0 || result.NewLevel != 2 {
		t.Fatalf("zero-experience settlement=%+v", result)
	}
	character, _, err := repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if character.Stats["exp"] != 250 ||
		character.Stats[dnfhonor.ExpertLevelStatKey] != 0 ||
		character.Stats[dnfhonor.ExpertProgressExperienceStatKey] != 90 {
		t.Fatalf("zero-experience state=%+v", character.Stats)
	}
}

func TestOwnerApplyFinishSettlementHonorExpertReachesFinalLevel(t *testing.T) {
	ctx := context.Background()
	repos := finishHonorSeed(t, ctx, 2, 250, 0, 0)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	input := finishHonorInput(t, finishOwnerProgression(t), func(context.Context) (*dnfhonor.Tables, error) {
		return finishHonorTables(t), nil
	})
	input.MaximumCharacterLevel = 2
	input.ExperienceBonusPercent = 200

	result, err := owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExperienceDelta != 330 || result.HonorExpertGain != 330 {
		t.Fatalf("final-level settlement=%+v", result)
	}
	character, _, err := repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if character.Stats[dnfhonor.ExpertLevelStatKey] != 2 ||
		character.Stats[dnfhonor.ExpertProgressExperienceStatKey] != 0 {
		t.Fatalf("final honor state=%+v", character.Stats)
	}
}

func TestOwnerApplyFinishSettlementHonorReplayAndLegacyReceipt(t *testing.T) {
	ctx := context.Background()
	repos := finishHonorSeed(t, ctx, 2, 250, 0, 90)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	input := finishHonorInput(t, finishOwnerProgression(t), func(context.Context) (*dnfhonor.Tables, error) {
		return finishHonorTables(t), nil
	})
	input.MaximumCharacterLevel = 2
	first, err := owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.HonorExpertGain != 110 {
		t.Fatalf("first honor gain=%d", first.HonorExpertGain)
	}

	input.LoadHonorTables = func(context.Context) (*dnfhonor.Tables, error) {
		t.Fatal("honor tables loaded during replay")
		return nil, nil
	}
	replay, err := owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.HonorExpertGain != 110 || replay.NewExperience != first.NewExperience {
		t.Fatalf("honor replay=%+v", replay)
	}

	record, found, err := repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	record = dnfrepo.CloneQuest(record)
	state := record.States[input.QuestID]
	var oldReceipt map[string]any
	if err := json.Unmarshal([]byte(state.Extra[finishSettlementReceiptKey]), &oldReceipt); err != nil {
		t.Fatal(err)
	}
	delete(oldReceipt, "honor_expert_gain")
	delete(oldReceipt, "post_honor_expert_level")
	delete(oldReceipt, "post_honor_expert_progress_experience")
	raw, err := json.Marshal(oldReceipt)
	if err != nil {
		t.Fatal(err)
	}
	state.Extra[finishSettlementReceiptKey] = string(raw)
	record.States[input.QuestID] = state
	if err := repos.Quest.Save(ctx, record); err != nil {
		t.Fatal(err)
	}

	legacy, err := owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.Replayed || legacy.HonorExpertGain != 0 || legacy.ExperienceDelta != first.ExperienceDelta {
		t.Fatalf("legacy replay=%+v", legacy)
	}
	character, _, err := repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if character.Stats["exp"] != 360 ||
		character.Stats[dnfhonor.ExpertLevelStatKey] != 1 ||
		character.Stats[dnfhonor.ExpertProgressExperienceStatKey] != 100 {
		t.Fatalf("replay duplicated progression=%+v", character.Stats)
	}
}

func TestOwnerApplyFinishSettlementRollsBackHonorOnLateItemFailure(t *testing.T) {
	ctx := context.Background()
	repos := finishHonorSeed(t, ctx, 2, 250, 0, 90)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	honorCalls := 0
	input := finishHonorInput(t, finishOwnerProgression(t), func(context.Context) (*dnfhonor.Tables, error) {
		honorCalls++
		return finishHonorTables(t), nil
	})
	input.MaximumCharacterLevel = 2
	wantErr := errors.New("item allocation failed after honor planning")
	input.AllocateItem = finishOwnerAllocator(wantErr)

	_, err = owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input)
	if !errors.Is(err, wantErr) {
		t.Fatalf("settlement error=%v want=%v", err, wantErr)
	}
	if honorCalls != 1 {
		t.Fatalf("honor loader calls=%d want=1 before late failure", honorCalls)
	}
	character, _, err := repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if character.Level != 2 || character.Stats["exp"] != 250 ||
		character.Stats[dnfhonor.ExpertLevelStatKey] != 0 ||
		character.Stats[dnfhonor.ExpertProgressExperienceStatKey] != 90 {
		t.Fatalf("late failure persisted character=%+v", character)
	}
	skill, _, err := repos.Skill.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if skill.Points.TotalSP != 300 || skill.Points.RemainingSP != 100 || skill.Points.SyncedLevel != 2 {
		t.Fatalf("late failure persisted skill=%+v", skill.Points)
	}
	quests, _, err := repos.Quest.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	state := quests.States[input.QuestID]
	if state.Status != "active" || state.Extra["reward_state"] != "pending" || state.Extra[finishSettlementReceiptKey] != "" {
		t.Fatalf("late failure persisted quest=%+v", state)
	}
}

func TestOwnerApplyFinishSettlementBeforeMaximumLevelSkipsHonorTables(t *testing.T) {
	ctx := context.Background()
	repos := finishHonorSeed(t, ctx, 1, 0, 0, 0)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	input := finishHonorInput(t, finishOwnerProgression(t), func(context.Context) (*dnfhonor.Tables, error) {
		t.Fatal("honor tables loaded before maximum level")
		return nil, nil
	})

	result, err := owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.PreviousLevel != 1 || result.NewLevel != 2 || result.HonorExpertGain != 0 {
		t.Fatalf("pre-cap settlement=%+v", result)
	}
}

func TestOwnerApplyFinishSettlementCapRejectsExistingIllegalLevel(t *testing.T) {
	ctx := context.Background()
	repos := finishHonorSeed(t, ctx, 91, 0, 0, 0)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	input := finishHonorInput(t, finishOwnerProgression(t), func(context.Context) (*dnfhonor.Tables, error) {
		t.Fatal("honor tables loaded for an illegal existing level")
		return nil, nil
	})

	_, err = owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input)
	if !errors.Is(err, ErrFinishCharacterAboveMaximumLevel) {
		t.Fatalf("illegal level error=%v", err)
	}
	character, _, err := repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if character.Level != 91 {
		t.Fatalf("illegal level changed to %d", character.Level)
	}
}

func TestOwnerApplyFinishSettlementBelowCapKeepsUint32OverflowRejection(t *testing.T) {
	ctx := context.Background()
	repos := finishHonorSeed(t, ctx, 2, math.MaxUint32-1, 0, 90)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	input := finishHonorInput(t, finishOwnerProgression(t), func(context.Context) (*dnfhonor.Tables, error) {
		t.Fatal("honor tables loaded after experience overflow")
		return nil, nil
	})
	input.MaximumCharacterLevel = 3

	_, err = owner.ApplyFinishSettlement(ctx, finishOwnerCatalog(), input)
	if !errors.Is(err, progression.ErrExperienceOutOfRange) {
		t.Fatalf("overflow error=%v", err)
	}
	character, _, err := repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if character.Stats["exp"] != math.MaxUint32-1 ||
		character.Stats[dnfhonor.ExpertLevelStatKey] != 0 ||
		character.Stats[dnfhonor.ExpertProgressExperienceStatKey] != 90 {
		t.Fatalf("overflow mutated state=%+v", character.Stats)
	}
}

func finishHonorInput(
	t *testing.T,
	tables *progression.Tables,
	loadHonor func(context.Context) (*dnfhonor.Tables, error),
) FinishCommitInput {
	t.Helper()
	input := finishOwnerInput(tables, finishOwnerAllocator(nil))
	input.LoadHonorTables = loadHonor
	return input
}

func finishHonorSeed(
	t *testing.T,
	ctx context.Context,
	level int,
	experience int64,
	honorLevel int64,
	honorProgress int64,
) dnfrepo.Group {
	t.Helper()
	repos := finishOwnerSeed(t, ctx)
	character, found, err := repos.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Level = level
	character.Stats["exp"] = experience
	character.Stats[dnfhonor.ExpertLevelStatKey] = honorLevel
	character.Stats[dnfhonor.ExpertProgressExperienceStatKey] = honorProgress
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	skill, found, err := repos.Skill.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load skill found=%t err=%v", found, err)
	}
	skill.Points = dnfrepo.SkillPointState{
		TotalSP: 300, RemainingSP: 100, SyncedLevel: level,
	}
	if err := repos.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	return repos
}

func finishHonorTables(t *testing.T) *dnfhonor.Tables {
	t.Helper()
	tables, err := dnfhonor.LoadTables(finishProgressionSource{
		dnfhonor.TablePath: `[grade]
1 ` + "`effect/one.ani` `medal.img` 0 `icon.img` 0" + ` 1 0
[/grade]
[maxexp on maxlevel]
29
[expert info]
[grade info]
0 ` + "`challenger`" + `
[min lv]
0
[max lv]
0
[/grade info]
[grade info]
1 ` + "`veteran`" + `
[medal img]
` + "`expert-medal.img`" + ` 2
[icon img]
` + "`expert-icon.img`" + ` 3
[min lv]
1
[max lv]
-1
[/grade info]
[/expert info]
[honor expert exp table]
1 ` + "`100`" + ` 2 ` + "`204`" + `
[/honor expert exp table]
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tables
}
