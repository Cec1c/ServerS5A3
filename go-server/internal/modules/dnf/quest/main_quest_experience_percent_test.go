package quest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCatalogRecordsExplicitQuestLevelPresence(t *testing.T) {
	source := finishProgressionSource{
		DefaultList:                 "3145 `with-level.qst`\n3146 `without-level.qst`\n3147 `empty-level.qst`\n",
		"n_quest/with-level.qst":    "[grade]\n`[epic]`\n[level]\n35 99\n",
		"n_quest/without-level.qst": "[grade]\n`[epic]`\n",
		"n_quest/empty-level.qst":   "[grade]\n`[epic]`\n[level]\n[/level]\n",
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	withLevel, ok := catalog.Find(3145)
	if !ok || !withLevel.HasLevel || withLevel.LevelMin != 35 || withLevel.LevelMax != 99 {
		t.Fatalf("explicit level definition=%+v found=%t", withLevel, ok)
	}
	withoutLevel, ok := catalog.Find(3146)
	if !ok || withoutLevel.HasLevel || withoutLevel.LevelMin != 1 {
		t.Fatalf("implicit level definition=%+v found=%t", withoutLevel, ok)
	}
	emptyLevel, ok := catalog.Find(3147)
	if !ok || emptyLevel.HasLevel || emptyLevel.LevelMin != 1 || emptyLevel.LevelMax != 99 {
		t.Fatalf("empty explicit level definition=%+v found=%t", emptyLevel, ok)
	}
}

func TestMainQuestExperiencePercentIsFinalAndOverridesExistingBonuses(t *testing.T) {
	ctx := context.Background()
	repositories := finishOwnerSeed(t, ctx)
	definition := finishOwnerCatalog().byID[3145]
	definition.Grade = "[epic]"
	definition.HasLevel = true
	input := finishOwnerInput(finishOwnerProgression(t), finishOwnerAllocator(nil))
	input.MainQuestExperienceMultiplier = 3
	input.MainQuestExperiencePercent = 15
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

	first, err := owner.ApplyFinishSettlement(ctx, mainQuestExperienceTestCatalog(definition), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.BaseExperienceDelta != 15 || first.ExperienceBonusDelta != 0 ||
		first.ExperienceDelta != 15 || first.NewExperience != 15 {
		t.Fatalf("percentage settlement=%+v want base=bonus=total=15", first)
	}

	input.MainQuestExperiencePercent = 20
	input.MainQuestExperienceMultiplier = 1
	input.ExperienceBonusPercent = 0
	input.CommittedAt = input.CommittedAt.Add(time.Hour)
	replayed, err := owner.ApplyFinishSettlement(ctx, mainQuestExperienceTestCatalog(definition), input)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.BaseExperienceDelta != 15 ||
		replayed.ExperienceBonusDelta != 0 || replayed.ExperienceDelta != 15 {
		t.Fatalf("receipt replay=%+v want original 15", replayed)
	}
}

func TestMainQuestExperiencePercentUsesQuestLevelWhenFinishedLater(t *testing.T) {
	ctx := context.Background()
	tables := mainQuestPercentProgression(t, 90)
	for _, characterLevel := range []int{35, 60, 89} {
		t.Run(fmt.Sprintf("character-level-%d", characterLevel), func(t *testing.T) {
			repositories := finishOwnerSeed(t, ctx)
			seedMainQuestPercentCharacter(t, ctx, repositories, characterLevel, 0)
			definition := mainQuestPercentDefinition(3145, 35)
			input := finishOwnerInput(tables, finishOwnerAllocator(nil))
			input.MainQuestExperiencePercent = 15
			owner, err := NewOwner(repositories)
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.ApplyFinishSettlement(ctx, mainQuestExperienceTestCatalog(definition), input)
			if err != nil {
				t.Fatal(err)
			}
			if result.BaseExperienceDelta != 235974 || result.ExperienceBonusDelta != 0 ||
				result.ExperienceDelta != 235974 {
				t.Fatalf("level %d stored quest settlement=%+v", characterLevel, result)
			}
		})
	}
}

func TestMainQuestExperiencePercentRequiresExplicitValidQuestLevel(t *testing.T) {
	ctx := context.Background()
	for _, definition := range []Definition{
		{ID: 3145, Path: "n_quest/missing_level.qst", Type: "[clear map]", Grade: "[epic]", Difficulty: "N"},
		{ID: 3145, Path: "n_quest/zero_level.qst", Type: "[clear map]", Grade: "[epic]", HasLevel: true, LevelMin: 0, LevelMax: 99, Difficulty: "N"},
		{ID: 3145, Path: "n_quest/inverted_level.qst", Type: "[clear map]", Grade: "[epic]", HasLevel: true, LevelMin: 35, LevelMax: 20, Difficulty: "N"},
	} {
		t.Run(definition.Path, func(t *testing.T) {
			repositories := finishOwnerSeed(t, ctx)
			input := finishOwnerInput(mainQuestPercentProgression(t, 90), finishOwnerAllocator(nil))
			input.MainQuestExperiencePercent = 15
			owner, err := NewOwner(repositories)
			if err != nil {
				t.Fatal(err)
			}
			_, err = owner.ApplyFinishSettlement(ctx, mainQuestExperienceTestCatalog(definition), input)
			if !errors.Is(err, ErrFinishMainQuestExperiencePercentLevelInvalid) {
				t.Fatalf("invalid quest level error=%v", err)
			}
			character, _, _ := repositories.Character.Load(ctx, "19")
			if character.Level != 1 || character.Stats["exp"] != 0 {
				t.Fatalf("invalid level mutated character=%+v", character)
			}
		})
	}

	for _, invalidPercent := range []int{-1, 101} {
		repositories := finishOwnerSeed(t, ctx)
		input := finishOwnerInput(finishOwnerProgression(t), finishOwnerAllocator(nil))
		input.MainQuestExperiencePercent = invalidPercent
		owner, err := NewOwner(repositories)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.ApplyFinishSettlement(ctx, mainQuestExperienceTestCatalog(mainQuestPercentDefinition(3145, 1)), input); !errors.Is(err, ErrFinishMainQuestExperiencePercentInvalid) {
			t.Fatalf("invalid percentage %d error=%v", invalidPercent, err)
		}
	}
}

func TestMainQuestExperiencePercentPreservesExcludedAndNoExperiencePaths(t *testing.T) {
	ctx := context.Background()
	parent := mainQuestPercentDefinition(3145, 1)
	child := mainQuestPercentDefinition(3146, 1)
	child.MainQuestID = parent.ID
	child.Grade = "[sub]"
	child.Path = "n_quest/percent_child.qst"
	side := mainQuestPercentDefinition(3145, 1)
	side.Grade = "[side]"
	jobChange := mainQuestPercentDefinition(3145, 1)
	jobChange.JobChangeQuest = 1
	ignoredLevel := mainQuestPercentDefinition(3145, 1)
	ignoredLevel.IgnoreQuestLevel4Exp = true

	for _, test := range []struct {
		name       string
		definition Definition
		wantBase   uint32
		wantBonus  uint32
		wantTotal  uint32
	}{
		{name: "side", definition: side, wantBase: 110, wantTotal: 110},
		{name: "epic child keeps old multiplier", definition: child, wantBase: 330, wantTotal: 330},
		{name: "job change keeps old multiplier", definition: jobChange, wantBase: 330, wantTotal: 330},
		{name: "ignored level keeps old multiplier", definition: ignoredLevel, wantBase: 330, wantTotal: 330},
	} {
		t.Run(test.name, func(t *testing.T) {
			repositories := finishOwnerSeed(t, ctx)
			questID := test.definition.ID
			completionKey := "run-19/op117/426"
			if questID != 3145 {
				completionKey = "run-19/op117/percent-child"
				mainQuestExperienceTestSeedState(t, repositories, questID, completionKey)
			}
			input := finishOwnerInput(finishOwnerProgression(t), finishOwnerAllocator(nil))
			input.QuestID = questID
			input.ExpectedCompletionKey = completionKey
			input.MainQuestExperienceMultiplier = 3
			input.MainQuestExperiencePercent = 15
			owner, err := NewOwner(repositories)
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.ApplyFinishSettlement(ctx, mainQuestExperienceTestCatalog(parent, test.definition), input)
			if err != nil {
				t.Fatal(err)
			}
			if result.BaseExperienceDelta != test.wantBase || result.ExperienceBonusDelta != test.wantBonus ||
				result.ExperienceDelta != test.wantTotal {
				t.Fatalf("excluded result=%+v", result)
			}
		})
	}

	t.Run("no experience", func(t *testing.T) {
		repositories := finishOwnerSeed(t, ctx)
		noExperience := mainQuestPercentDefinition(3145, 1)
		noExperience.NoExperience = true
		input := finishOwnerInput(finishOwnerProgression(t), finishOwnerAllocator(nil))
		input.MainQuestExperiencePercent = 15
		owner, err := NewOwner(repositories)
		if err != nil {
			t.Fatal(err)
		}
		result, err := owner.ApplyFinishSettlement(ctx, mainQuestExperienceTestCatalog(noExperience), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.BaseExperienceDelta != 0 || result.ExperienceBonusDelta != 0 || result.ExperienceDelta != 0 {
			t.Fatalf("no-experience result=%+v", result)
		}
	})
}

func TestMainQuestExperiencePercentCrossesAndSettlesAtMaximumLevel(t *testing.T) {
	ctx := context.Background()
	tables := mainQuestPercentProgression(t, 90)
	definition := mainQuestPercentDefinition(3145, 89)
	threshold89, err := tables.ThresholdToNext(89)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		level      int
		experience int64
		wantLevel  int
	}{
		{name: "89 crosses 90", level: 89, experience: int64(threshold89) - 100, wantLevel: 90},
		{name: "90 remains capped", level: 90, experience: 0, wantLevel: 90},
	} {
		t.Run(test.name, func(t *testing.T) {
			repositories := finishHonorSeed(t, ctx, test.level, test.experience, 0, 0)
			input := finishOwnerInput(tables, finishOwnerAllocator(nil))
			input.MainQuestExperiencePercent = 15
			input.MaximumCharacterLevel = 90
			input.LoadHonorTables = func(context.Context) (*dnfhonor.Tables, error) {
				return finishHonorTables(t), nil
			}
			owner, err := NewOwner(repositories)
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.ApplyFinishSettlement(ctx, mainQuestExperienceTestCatalog(definition), input)
			if err != nil {
				t.Fatal(err)
			}
			if result.BaseExperienceDelta != 42444667 || result.ExperienceBonusDelta != 0 ||
				result.ExperienceDelta != 42444667 || result.NewLevel != test.wantLevel ||
				result.HonorExpertGain == 0 {
				t.Fatalf("maximum-level result=%+v", result)
			}
		})
	}
}

func TestMainQuestExperiencePercentLateFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	repositories := finishOwnerSeed(t, ctx)
	definition := mainQuestPercentDefinition(3145, 1)
	input := finishOwnerInput(finishOwnerProgression(t), finishOwnerAllocator(nil))
	input.MainQuestExperiencePercent = 15
	wantErr := errors.New("late percent reward failure")
	input.AllocateItem = finishOwnerAllocator(wantErr)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.ApplyFinishSettlement(ctx, mainQuestExperienceTestCatalog(definition), input)
	if !errors.Is(err, wantErr) {
		t.Fatalf("late failure error=%v", err)
	}
	character, _, _ := repositories.Character.Load(ctx, "19")
	skill, _, _ := repositories.Skill.Load(ctx, "19")
	quests, _, _ := repositories.Quest.Load(ctx, "19")
	if character.Level != 1 || character.Stats["exp"] != 0 ||
		!reflect.DeepEqual(skill.Points, dnfrepo.SkillPointState{SyncedLevel: 1}) ||
		quests.States[3145].Status != "active" || quests.States[3145].Extra[finishSettlementReceiptKey] != "" {
		t.Fatalf("late failure persisted character=%+v skill=%+v quest=%+v", character, skill.Points, quests.States[3145])
	}
}

func TestMainQuestExperiencePercentParentChildConsumesAndReplaysAssets(t *testing.T) {
	ctx := context.Background()
	repositories := finishOwnerSeed(t, ctx)
	parent := Definition{
		ID: 3145, Path: "n_quest/percent_parent.qst", Type: "[quest clear]", Grade: "[epic]",
		HasLevel: true, LevelMin: 1, LevelMax: 99, Difficulty: "N", IntData: []int64{3146},
	}
	child := Definition{
		ID: 3146, Path: "n_quest/percent_terminal_child.qst", Type: "[seeking]", Grade: "[sub]",
		MainQuestID: 3145, HasLevel: true, LevelMin: 1, LevelMax: 99, Difficulty: "N",
		NoExperience: true, IntData: []int64{9001, 2},
		RewardType: "[item]", RewardItems: []RewardItemRule{{ItemID: 10404, Count: 1}},
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "19",
		States: map[int64]dnfrepo.QuestState{
			3145: {
				Status: "active", ProgressValue: 1,
				Extra: map[string]string{"completion_key": "run-19/op117/percent-parent", "completion_kind": "clear_map", "reward_state": "pending"},
			},
			3146: {Status: "active", ProgressValue: 0},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       map[string]dnfrepo.ItemStack{"0:8": {ItemID: 9001, Count: 3}},
	}); err != nil {
		t.Fatal(err)
	}
	input := finishOwnerInput(finishOwnerProgression(t), finishOwnerAllocator(nil))
	input.MainQuestExperiencePercent = 15
	input.ExpectedCompletionKey = "run-19/op117/percent-parent"
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	catalog := mainQuestExperienceTestCatalog(parent, child)
	first, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.BaseExperienceDelta != 15 || first.ExperienceDelta != 15 ||
		len(first.TerminalChildren) != 1 || len(first.ConsumedItems) != 1 ||
		first.ConsumedItems[0].ItemID != 9001 || first.ConsumedItems[0].Delta != 2 ||
		len(first.Items) != 1 || first.Items[0].ItemID != 10404 {
		t.Fatalf("parent-child settlement=%+v", first)
	}
	replayed, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.ExperienceDelta != 15 ||
		len(replayed.ConsumedItems) != 1 || len(replayed.Items) != 1 {
		t.Fatalf("parent-child replay=%+v", replayed)
	}
	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found || inventory.Slots["0:8"].Count != 1 || inventory.Slots["0:65"].Count != 1 {
		t.Fatalf("parent-child inventory=%+v found=%t err=%v", inventory, found, err)
	}
}

func TestMainQuestExperiencePercentAtCounterLimitStillGrantsHonorOnce(t *testing.T) {
	ctx := context.Background()
	tables := mainQuestPercentProgression(t, 90)
	repositories := finishHonorSeed(t, ctx, 90, math.MaxUint32-10, 0, 0)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	input := finishOwnerInput(tables, finishOwnerAllocator(nil))
	input.MainQuestExperiencePercent = 15
	input.LoadHonorTables = func(context.Context) (*dnfhonor.Tables, error) { return finishHonorTables(t), nil }
	catalog := mainQuestExperienceTestCatalog(mainQuestPercentDefinition(3145, 89))
	entryExperience, err := tables.ThresholdToNext(89)
	if err != nil {
		t.Fatal(err)
	}
	first, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil || first.ExperienceDelta != 42444667 || first.NewExperience != entryExperience || first.NewLevel != 90 || first.HonorExpertGain != 42444667 {
		t.Fatalf("counter limit: result=%+v err=%v", first, err)
	}
	before, _, _ := repositories.Character.Load(ctx, "19")
	input.MainQuestExperiencePercent = 20
	replay, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil || !replay.Replayed || replay.HonorExpertGain != first.HonorExpertGain {
		t.Fatalf("counter replay=%+v err=%v", replay, err)
	}
	after, _, _ := repositories.Character.Load(ctx, "19")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("replay changed capped character")
	}
	below := dnfrepo.CharacterRecord{Level: 89, Stats: map[string]int64{"exp": math.MaxUint32 - 1}}
	skill := dnfrepo.SkillRecord{Points: dnfrepo.SkillPointState{SyncedLevel: 89}}
	if _, err := planFinishProgression(tables, below, skill, 2, 90); !errors.Is(err, progression.ErrExperienceOutOfRange) {
		t.Fatalf("below-cap overflow changed: %v", err)
	}
}

func mainQuestPercentDefinition(id int64, level int) Definition {
	return Definition{
		ID: id, Path: "n_quest/percent_main.qst", Type: "[clear map]", Grade: "[epic]",
		HasLevel: true, LevelMin: level, LevelMax: 99, Difficulty: "N",
		RewardType: "[item]", RewardItems: []RewardItemRule{{ItemID: 10403, Count: 2}},
	}
}

func seedMainQuestPercentCharacter(
	t *testing.T,
	ctx context.Context,
	repositories dnfrepo.Group,
	level int,
	experience int64,
) {
	t.Helper()
	character, _, err := repositories.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	character.Level = level
	character.Stats["exp"] = experience
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	skill, _, err := repositories.Skill.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	skill.Points.SyncedLevel = level
	if err := repositories.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
}

func mainQuestPercentProgression(t *testing.T, levels int) *progression.Tables {
	t.Helper()
	// Explicit interval fixtures are independent of the expected percentage outputs.
	spans := map[int]uint32{1: 1000, 3: 1280, 15: 97040, 35: 1573160, 65: 16272192, 88: 213530540, 89: 282964448}
	var experience strings.Builder
	var cumulative uint64
	for level := 1; level <= levels; level++ {
		increment := uint64(1000)
		if span, exists := spans[level]; exists {
			increment = uint64(span)
		}
		cumulative += increment
		fmt.Fprintf(&experience, "%d ", cumulative)
	}
	var skills strings.Builder
	skills.WriteString("[sp table]\n")
	for level := 1; level <= levels; level++ {
		fmt.Fprintf(&skills, "%d 0\n", level)
	}
	skills.WriteString("[/sp table]\n[tp table]\n")
	for level := 1; level <= levels; level++ {
		fmt.Fprintf(&skills, "%d 0\n", level)
	}
	skills.WriteString("[/tp table]\n")
	tables, err := progression.Load(context.Background(), finishProgressionSource{
		progression.ExperienceTablePath: experience.String(),
		progression.SkillPointTablePath: skills.String(),
		progression.QuestParameterPath:  "[difficulty]\n`N` 100\n[/difficulty]\n[exp reward table]\n100 -1\n[gold reward table]\n10\n[green level penalty]\n80\n[grey level penalty]\n30\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	return tables
}
