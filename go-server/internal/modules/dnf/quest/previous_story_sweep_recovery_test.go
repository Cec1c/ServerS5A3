package quest

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type previousStorySweepFixture struct {
	ctx       context.Context
	repos     repo.Group
	owner     *Owner
	catalog   *Catalog
	character repo.CharacterRecord
}

type previousStorySweepSnapshot struct {
	quest     repo.QuestRecord
	character repo.CharacterRecord
	inventory repo.InventoryRecord
	skills    repo.SkillRecord
}

func newPreviousStorySweepFixture(t *testing.T, job, growType, level int, record repo.QuestRecord) previousStorySweepFixture {
	t.Helper()

	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	character := repo.CharacterRecord{
		CharacterID: "17",
		AccountID:   "dnf:1",
		Job:         strconv.Itoa(job),
		Level:       level,
		Stats:       map[string]int64{"grow_type": int64(growType), "sp": 777, "exp": 12345},
	}
	inventory := repo.InventoryRecord{
		CharacterID: "17",
		Slots:       map[string]repo.ItemStack{"0:10": {ItemID: 9001, Count: 3}},
	}
	skills := repo.SkillRecord{
		CharacterID: "17",
		Skills:      map[int64]repo.SkillState{1001: {Level: 5, Enabled: true}},
	}
	record.CharacterID = character.CharacterID
	if record.States == nil {
		record.States = map[int64]repo.QuestState{}
	}

	saves := []struct {
		name string
		err  error
	}{
		{"character", repos.Character.Save(ctx, character)},
		{"inventory", repos.Inventory.Save(ctx, inventory)},
		{"skills", repos.Skill.Save(ctx, skills)},
		{"quests", repos.Quest.Save(ctx, record)},
	}
	for _, save := range saves {
		if save.err != nil {
			t.Fatalf("save %s: %v", save.name, save.err)
		}
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	return previousStorySweepFixture{
		ctx:       ctx,
		repos:     repos,
		owner:     owner,
		catalog:   achievedAwakeningStoryCatalog(),
		character: character,
	}
}

func previousStorySweepInput(character repo.CharacterRecord, cutoff int) AutoCompleteMainInput {
	job, _ := strconv.Atoi(character.Job)
	return AutoCompleteMainInput{
		AccountID:   character.AccountID,
		CharacterID: character.CharacterID,
		Eligibility: CharacterEligibility{
			Level:    character.Level,
			Job:      job,
			GrowType: int(character.Stats["grow_type"]),
		},
		CutoffLevel: cutoff,
		CompletedAt: time.Unix(1800000000, 0),
	}
}

func previousStorySweepMarkers(cutoff string) map[string]string {
	return map[string]string{
		"reward_state":               finishRewardGranted,
		"auto_completed":             "true",
		"auto_complete_reason":       "main_story_level_clear_no_reward",
		"auto_complete_level_cutoff": cutoff,
	}
}

func previousStorySweepMarkedState(status, cutoff string) repo.QuestState {
	return repo.QuestState{Status: status, Extra: previousStorySweepMarkers(cutoff)}
}

func (f previousStorySweepFixture) snapshot(t *testing.T) previousStorySweepSnapshot {
	t.Helper()

	questRecord, found, err := f.repos.Quest.Load(f.ctx, "17")
	if err != nil || !found {
		t.Fatalf("load quest: found=%v err=%v", found, err)
	}
	character, found, err := f.repos.Character.Load(f.ctx, "17")
	if err != nil || !found {
		t.Fatalf("load character: found=%v err=%v", found, err)
	}
	inventory, found, err := f.repos.Inventory.Load(f.ctx, "17")
	if err != nil || !found {
		t.Fatalf("load inventory: found=%v err=%v", found, err)
	}
	skills, found, err := f.repos.Skill.Load(f.ctx, "17")
	if err != nil || !found {
		t.Fatalf("load skills: found=%v err=%v", found, err)
	}
	return previousStorySweepSnapshot{
		quest:     questRecord,
		character: character,
		inventory: inventory,
		skills:    skills,
	}
}

func (before previousStorySweepSnapshot) requireEqual(t *testing.T, after previousStorySweepSnapshot) {
	t.Helper()
	if !reflect.DeepEqual(after.quest, before.quest) {
		t.Fatal("quest snapshot changed")
	}
	if !reflect.DeepEqual(after.character, before.character) {
		t.Fatal("character snapshot changed")
	}
	if !reflect.DeepEqual(after.inventory, before.inventory) {
		t.Fatal("inventory snapshot changed")
	}
	if !reflect.DeepEqual(after.skills, before.skills) {
		t.Fatal("skills snapshot changed")
	}
}

func requirePreviousStorySweepCompleted(t *testing.T, record repo.QuestRecord, ids ...int64) {
	t.Helper()
	for _, id := range ids {
		state, found := questStateFor(record, id)
		if !found || !isCompletedQuestStatus(state.Status) {
			t.Fatalf("quest %d is not completed: %+v", id, state)
		}
	}
}

func requirePreviousStorySweepMarker(t *testing.T, record repo.QuestRecord, id int64, cutoff string) {
	t.Helper()
	state, found := questStateFor(record, id)
	if !found {
		t.Fatalf("quest %d missing", id)
	}
	if state.Extra["auto_completed"] != "true" ||
		state.Extra["auto_complete_reason"] != "main_story_level_clear_no_reward" ||
		state.Extra["auto_complete_level_cutoff"] != cutoff {
		t.Fatalf("quest %d marker = %+v", id, state.Extra)
	}
	if state.Extra["reward_state"] != finishRewardGranted {
		t.Fatalf("quest %d reward state = %q", id, state.Extra["reward_state"])
	}
}

func setPreviousStorySweepAwakeningLevel(catalog *Catalog, level int) {
	definition := catalog.byID[101]
	definition.LevelMin = level
	catalog.byID[101] = definition
	for index := range catalog.ordered {
		if catalog.ordered[index].ID == 101 {
			catalog.ordered[index] = definition
		}
	}
}

func TestRecoverPreviousMainStorySweepRestoresAuthorizedAwakeningAndTown(t *testing.T) {
	fixture := newPreviousStorySweepFixture(t, 0, 0x21, 90, repo.QuestRecord{
		States: map[int64]repo.QuestState{
			100: previousStorySweepMarkedState("completed", "89"),
		},
	})
	before := fixture.snapshot(t)

	result, err := fixture.owner.RecoverPreviousMainStorySweep(
		fixture.ctx,
		fixture.catalog,
		previousStorySweepInput(fixture.character, 89),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Idempotent || result.CutoffLevel != 89 {
		t.Fatalf("result = %+v", result)
	}
	requirePreviousStorySweepCompleted(t, result.PostCommitQuest, 101, 103)
	for _, id := range []int64{101, 103} {
		requirePreviousStorySweepMarker(t, result.PostCommitQuest, id, "89")
	}
	if IsCompleted(result.PostCommitQuest, 102) {
		t.Fatal("recovery completed the other profession branch")
	}
	if !reflect.DeepEqual(result.PostCommitQuest.States[100], before.quest.States[100]) {
		t.Fatal("already-completed quest was rewritten or rewarded again")
	}
	if !reflect.DeepEqual(result.PostCommitCharacter, before.character) {
		t.Fatal("character changed during recovery")
	}

	after := fixture.snapshot(t)
	if !reflect.DeepEqual(after.character, before.character) {
		t.Fatal("persisted character changed during recovery")
	}
	if !reflect.DeepEqual(after.inventory, before.inventory) {
		t.Fatal("persisted inventory changed during recovery")
	}
	if !reflect.DeepEqual(after.skills, before.skills) {
		t.Fatal("persisted skills changed during recovery")
	}
	if !reflect.DeepEqual(after.quest, result.PostCommitQuest) {
		t.Fatal("verified transaction snapshot differs from persisted quest")
	}
}

func TestRecoverPreviousMainStorySweepSecondCallIsIdempotent(t *testing.T) {
	fixture := newPreviousStorySweepFixture(t, 0, 0x21, 90, repo.QuestRecord{
		States: map[int64]repo.QuestState{
			100: previousStorySweepMarkedState("completed", "89"),
		},
	})
	first, err := fixture.owner.RecoverPreviousMainStorySweep(
		fixture.ctx,
		fixture.catalog,
		previousStorySweepInput(fixture.character, 89),
	)
	if err != nil {
		t.Fatal(err)
	}
	requirePreviousStorySweepCompleted(t, first.PostCommitQuest, 101, 103)
	firstSnapshot := fixture.snapshot(t)
	if !reflect.DeepEqual(first.PostCommitQuest, firstSnapshot.quest) {
		t.Fatal("first verified snapshot differs from persisted quest")
	}

	secondInput := previousStorySweepInput(fixture.character, 89)
	secondInput.CompletedAt = time.Unix(1800000001, 0)
	second, err := fixture.owner.RecoverPreviousMainStorySweep(
		fixture.ctx,
		fixture.catalog,
		secondInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Idempotent || len(second.ChangedQuestIDs) != 0 || len(second.ChangedLinkedSubIDs) != 0 {
		t.Fatalf("second call changed quests: %+v", second)
	}
	if second.CutoffLevel != first.CutoffLevel {
		t.Fatalf("cutoff changed from %d to %d", first.CutoffLevel, second.CutoffLevel)
	}
	if !reflect.DeepEqual(second.PostCommitQuest, first.PostCommitQuest) {
		t.Fatal("second verified quest snapshot changed")
	}
	if !reflect.DeepEqual(second.PostCommitCharacter, first.PostCommitCharacter) {
		t.Fatal("second verified character snapshot changed")
	}
	firstSnapshot.requireEqual(t, fixture.snapshot(t))
}

func TestRecoverPreviousMainStorySweepUsesHistoricalMaximumWithinInputCap(t *testing.T) {
	fixture := newPreviousStorySweepFixture(t, 0, 0x21, 90, repo.QuestRecord{
		States: map[int64]repo.QuestState{
			100: previousStorySweepMarkedState("completed", "82"),
			90:  previousStorySweepMarkedState("completed", "80"),
			91:  previousStorySweepMarkedState("completed", "86"),
		},
	})
	setPreviousStorySweepAwakeningLevel(fixture.catalog, 85)

	result, err := fixture.owner.RecoverPreviousMainStorySweep(
		fixture.ctx,
		fixture.catalog,
		previousStorySweepInput(fixture.character, 85),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.CutoffLevel != 85 {
		t.Fatalf("effective cutoff = %d, want historic max capped to 85", result.CutoffLevel)
	}
	requirePreviousStorySweepCompleted(t, result.PostCommitQuest, 101, 103)
	requirePreviousStorySweepMarker(t, result.PostCommitQuest, 101, "85")
	requirePreviousStorySweepMarker(t, result.PostCommitQuest, 103, "85")
}

func TestRecoverPreviousMainStorySweepDoesNotRaiseHistoricCutoffToInput(t *testing.T) {
	fixture := newPreviousStorySweepFixture(t, 0, 0x21, 90, repo.QuestRecord{
		States: map[int64]repo.QuestState{
			100: previousStorySweepMarkedState("completed", "88"),
		},
	})
	setPreviousStorySweepAwakeningLevel(fixture.catalog, 89)
	before := fixture.snapshot(t)

	result, err := fixture.owner.RecoverPreviousMainStorySweep(
		fixture.ctx,
		fixture.catalog,
		previousStorySweepInput(fixture.character, 89),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.CutoffLevel != 88 || !result.Idempotent || len(result.ChangedQuestIDs) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if IsCompleted(result.PostCommitQuest, 101) || IsCompleted(result.PostCommitQuest, 103) {
		t.Fatal("recovery raised an 88 cutoff to the input level 89")
	}
	before.requireEqual(t, fixture.snapshot(t))
}

func TestRecoverPreviousMainStorySweepRequiresCanonicalCompletedMarker(t *testing.T) {
	cases := []struct {
		name  string
		state repo.QuestState
	}{
		{
			name:  "no marker",
			state: repo.QuestState{Status: "completed"},
		},
		{
			name: "ordinary manual completion",
			state: repo.QuestState{
				Status: "completed",
				Extra:  map[string]string{"reward_state": finishRewardGranted},
			},
		},
		{
			name:  "active marker",
			state: previousStorySweepMarkedState("active", "89"),
		},
		{
			name: "wrong reason",
			state: func() repo.QuestState {
				markers := previousStorySweepMarkers("89")
				markers["auto_complete_reason"] = "manual_completion"
				return repo.QuestState{Status: "completed", Extra: markers}
			}(),
		},
	}
	for _, cutoff := range []string{"", "0", "-1", "90", "not-a-number"} {
		cases = append(cases, struct {
			name  string
			state repo.QuestState
		}{
			name:  "invalid cutoff " + strconv.Quote(cutoff),
			state: previousStorySweepMarkedState("completed", cutoff),
		})
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPreviousStorySweepFixture(t, 0, 0x21, 90, repo.QuestRecord{
				States: map[int64]repo.QuestState{100: testCase.state},
			})
			before := fixture.snapshot(t)

			result, err := fixture.owner.RecoverPreviousMainStorySweep(
				fixture.ctx,
				fixture.catalog,
				previousStorySweepInput(fixture.character, 89),
			)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Idempotent || len(result.ChangedQuestIDs) != 0 {
				t.Fatalf("non-canonical marker authorized writes: %+v", result)
			}
			before.requireEqual(t, fixture.snapshot(t))
		})
	}
}

func TestRecoverPreviousMainStorySweepKeepsAwakeningBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		job    int
		grow   int
		record func() repo.QuestRecord
	}{
		{
			name: "other profession",
			job:  1,
			grow: 0x22,
			record: func() repo.QuestRecord {
				return repo.QuestRecord{States: map[int64]repo.QuestState{
					100: previousStorySweepMarkedState("completed", "89"),
				}}
			},
		},
		{
			name: "not awakened",
			job:  0,
			grow: 0,
			record: func() repo.QuestRecord {
				return repo.QuestRecord{States: map[int64]repo.QuestState{
					100: previousStorySweepMarkedState("completed", "89"),
				}}
			},
		},
		{
			name: "collision",
			job:  0,
			grow: 0x21,
			record: func() repo.QuestRecord {
				return repo.QuestRecord{States: map[int64]repo.QuestState{
					100: previousStorySweepMarkedState("completed", "89"),
					102: {Status: "active"},
				}}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPreviousStorySweepFixture(t, testCase.job, testCase.grow, 90, testCase.record())
			before := fixture.snapshot(t)

			result, err := fixture.owner.RecoverPreviousMainStorySweep(
				fixture.ctx,
				fixture.catalog,
				previousStorySweepInput(fixture.character, 89),
			)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Idempotent || len(result.ChangedQuestIDs) != 0 {
				t.Fatalf("boundary authorized recovery: %+v", result)
			}
			if IsCompleted(result.PostCommitQuest, 101) || IsCompleted(result.PostCommitQuest, 103) {
				t.Fatal("boundary completed an ineligible awakening chain")
			}
			before.requireEqual(t, fixture.snapshot(t))
		})
	}
}

func TestRecoverPreviousMainStorySweepRollsBackTransactionFailure(t *testing.T) {
	fixture := newPreviousStorySweepFixture(t, 0, 0x21, 90, repo.QuestRecord{
		States: map[int64]repo.QuestState{
			100: previousStorySweepMarkedState("completed", "89"),
		},
	})
	before := fixture.snapshot(t)
	failure := errors.New("previous story sweep commit failure")
	wrapped := fixture.repos
	wrapped.CharacterSettlement = storyFailSettlement{
		inner: fixture.repos.CharacterSettlement,
		err:   failure,
	}
	owner, err := NewOwner(wrapped)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := owner.RecoverPreviousMainStorySweep(
		fixture.ctx,
		fixture.catalog,
		previousStorySweepInput(fixture.character, 89),
	); !errors.Is(err, failure) {
		t.Fatalf("error = %v, want %v", err, failure)
	}
	before.requireEqual(t, fixture.snapshot(t))
}
