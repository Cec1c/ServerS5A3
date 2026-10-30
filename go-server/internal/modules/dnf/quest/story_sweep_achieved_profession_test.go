package quest

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestOwnerStorySweepAchievedAwakeningPreservesCharacterAndAssets(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	character := repo.CharacterRecord{CharacterID: "17", AccountID: "dnf:1", Job: "0", Level: 90, Stats: map[string]int64{"grow_type": 0x21, "sp": 777, "exp": 12345}}
	inventory := repo.InventoryRecord{CharacterID: "17", Slots: map[string]repo.ItemStack{"0:10": {ItemID: 9001, Count: 3}}}
	skills := repo.SkillRecord{CharacterID: "17", Skills: map[int64]repo.SkillState{1001: {Level: 5, Enabled: true}}}
	for _, err := range []error{repos.Character.Save(ctx, character), repos.Inventory.Save(ctx, inventory), repos.Skill.Save(ctx, skills), repos.Quest.Save(ctx, repo.QuestRecord{CharacterID: "17", States: map[int64]repo.QuestState{100: {Status: "completed"}}})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.ApplyAutoCompleteMain(ctx, achievedAwakeningStoryCatalog(), AutoCompleteMainInput{AccountID: "dnf:1", CharacterID: "17", Eligibility: CharacterEligibility{Level: 90, Job: 0, GrowType: 0x21}, CutoffLevel: 89, CompletedAt: time.Unix(1800000000, 0)})
	if err != nil || !IsCompleted(result.PostCommitQuest, 101) || !IsCompleted(result.PostCommitQuest, 103) {
		t.Fatal("committed story unavailable", err)
	}
	gotCharacter, _, err := repos.Character.Load(ctx, "17")
	if err != nil || !reflect.DeepEqual(gotCharacter, character) {
		t.Fatal("character changed", err)
	}
	gotInventory, _, err := repos.Inventory.Load(ctx, "17")
	if err != nil || !reflect.DeepEqual(gotInventory, inventory) {
		t.Fatal("inventory changed", err)
	}
	gotSkills, _, err := repos.Skill.Load(ctx, "17")
	if err != nil || !reflect.DeepEqual(gotSkills, skills) {
		t.Fatal("skills changed", err)
	}
}

func achievedAwakeningStoryCatalog() *Catalog {
	before := storyOrdinary(100)
	awakening := storyOrdinary(101, 100)
	awakening.JobChangeQuest = 2
	awakening.Job = "[swordman]"
	awakening.HasGrowType = true
	awakening.GrowType = 1
	awakening.RewardType = "[awakening type]"
	awakening.RewardIntData = []int64{1}
	other := awakening
	other.ID = 102
	other.GrowType = 2
	other.CollisionQuests = []int64{101}
	awakening.CollisionQuests = []int64{102}
	dock := storyOrdinary(103, 101)
	return &Catalog{ordered: []Definition{before, awakening, other, dock}, byID: map[int64]Definition{100: before, 101: awakening, 102: other, 103: dock}}
}

func TestStorySweepAlreadyAwakenedCharacterReachesDependentTown(t *testing.T) {
	c := achievedAwakeningStoryCatalog()
	for _, grow := range []int{0x11, 0x21} {
		r := repo.QuestRecord{States: map[int64]repo.QuestState{100: {Status: "completed"}}}
		before := repo.CloneQuest(r)
		p, err := c.PlanAutoCompleteMainQuests(r, CharacterEligibility{Level: 90, Job: 0, GrowType: grow}, 89, 0, "", time.Unix(1800000000, 0))
		if err != nil || !IsCompleted(p.Record, 101) || !IsCompleted(p.Record, 103) {
			t.Fatalf("already achieved awakening blocked town: grow=%x err=%v changed=%v", grow, err, p.ChangedQuestIDs)
		}
		if IsCompleted(p.Record, 102) || !reflect.DeepEqual(r, before) {
			t.Fatal("other branch completed or input modified")
		}
		if p.Record.States[101].Extra["reward_state"] != "granted" {
			t.Fatal("skipped profession may grant reward later")
		}
		repeat, err := c.PlanAutoCompleteMainQuests(p.Record, CharacterEligibility{Level: 90, Job: 0, GrowType: grow}, 89, 0, "", time.Unix(1800000001, 0))
		if err != nil || len(repeat.ChangedFields) != 0 {
			t.Fatal("completion is not idempotent", err)
		}
	}
}

func TestStorySweepDoesNotGrantUnachievedOrConflictingAwakening(t *testing.T) {
	c := achievedAwakeningStoryCatalog()
	for _, grow := range []int{0, 1, 0x12, 0x22, 0x31, -1, 256} {
		r := repo.QuestRecord{States: map[int64]repo.QuestState{100: {Status: "completed"}}}
		p, err := c.PlanAutoCompleteMainQuests(r, CharacterEligibility{Level: 90, Job: 0, GrowType: grow}, 89, 0, "", time.Unix(1800000000, 0))
		if err == nil && (IsCompleted(p.Record, 101) || IsCompleted(p.Record, 103)) {
			t.Fatalf("unachieved/wrong profession bypassed: %x", grow)
		}
	}
	r := repo.QuestRecord{States: map[int64]repo.QuestState{100: {Status: "completed"}, 102: {Status: "active"}}}
	p, err := c.PlanAutoCompleteMainQuests(r, CharacterEligibility{Level: 90, Job: 0, GrowType: 0x21}, 89, 0, "", time.Unix(1800000000, 0))
	if err != nil || IsCompleted(p.Record, 101) || IsCompleted(p.Record, 103) {
		t.Fatal("collision bypassed", err)
	}
}

func TestRealPVFAlreadyAwakenedStorySweepUnlocksWestCoastDock(t *testing.T) {
	c := loadRealPVFQuestCatalog(t)
	r := repo.QuestRecord{States: map[int64]repo.QuestState{6930: {Status: "completed"}}}
	p, err := c.PlanAutoCompleteMainQuests(r, CharacterEligibility{Level: 90, Job: 0, GrowType: 0x21}, 89, 0, "", time.Unix(1800000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{3356, 3368, 3427, 3515, 3779} {
		if !IsCompleted(p.Record, id) {
			d, _ := c.Find(id)
			t.Errorf("real story town gate incomplete: %d %s", id, d.Name)
		}
	}
	for _, id := range []int64{3357, 3369, 3868} {
		if IsCompleted(p.Record, id) {
			t.Errorf("wrong profession or unanswered choice completed: %d", id)
		}
	}
	// Each fixture represents an actual player answer; the sweep itself must
	// not choose a faction. All three legal branches must reach the later gates.
	for answer, branch := range []int64{3870, 3871, 3873} {
		answered := repo.CloneQuest(p.Record)
		answered.States[3868] = repo.QuestState{Status: "completed", Extra: map[string]string{
			questionAnswerExtraKey: strconv.Itoa(answer), questionOptionCountExtraKey: "3", questionAnswerMetadataKey: "1",
		}}
		next, err := c.PlanAutoCompleteMainQuests(answered, CharacterEligibility{Level: 90, Job: 0, GrowType: 0x21}, 89, 0, "", time.Unix(1800000001, 0))
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []int64{branch, 3944, 4840} {
			if !IsCompleted(next.Record, id) {
				d, _ := c.Find(id)
				t.Errorf("answered faction %d still blocks gate %d %s", answer, id, d.Name)
			}
		}
		for _, other := range []int64{3870, 3871, 3873} {
			if other != branch && IsCompleted(next.Record, other) {
				t.Errorf("answered faction %d completed another faction %d", answer, other)
			}
		}
	}
}

func TestStorySweepAchievedAwakeningKeepsOtherSkipBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Definition)
	}{
		{"question", func(d *Definition) { d.Type = "[question]" }},
		{"selection", func(d *Definition) { d.RewardSelectionItems = []RewardItemRule{{ItemID: 1, Count: 1}} }},
		{"malformed", func(d *Definition) { d.RewardParseError = "bad" }},
		{"invalid-stage", func(d *Definition) { d.RewardIntData = []int64{3} }},
		{"future-level", func(d *Definition) { d.LevelMin = 90 }},
		{"missing-prerequisite", func(d *Definition) { d.PreRequiredGroups = [][]int64{{999}} }},
		{"missing-branch", func(d *Definition) { d.HasGrowType = false }},
		{"event", func(d *Definition) { d.IsEvent = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := achievedAwakeningStoryCatalog()
			d := c.byID[101]
			tc.edit(&d)
			c.byID[101] = d
			c.ordered[1] = d
			r := repo.QuestRecord{States: map[int64]repo.QuestState{100: {Status: "completed"}}}
			p, err := c.PlanAutoCompleteMainQuests(r, CharacterEligibility{Level: 90, Job: 0, GrowType: 0x21}, 89, 0, "", time.Unix(1800000000, 0))
			if err != nil || IsCompleted(p.Record, 101) || IsCompleted(p.Record, 103) {
				t.Fatal("guard bypassed", err)
			}
		})
	}
	c := achievedAwakeningStoryCatalog()
	r := repo.QuestRecord{States: map[int64]repo.QuestState{100: {Status: "completed"}, 101: {Status: "active"}}}
	ch := CharacterEligibility{Level: 90, Job: 0, GrowType: 0x21}
	p, err := c.PlanLevelUpTicketQuestClear(r, ch, time.Unix(1800000000, 0))
	if err != nil || IsCompleted(p.Record, 101) || IsCompleted(p.Record, 103) {
		t.Fatal("level-up ticket scope changed", err)
	}
	p, err = c.PlanAutoCompleteMainQuests(r, ch, 89, 101, "", time.Unix(1800000000, 0))
	if err != nil || !IsCompleted(p.Record, 101) || IsCompleted(p.Record, 103) {
		t.Fatal("single-target scope incorrect", err)
	}
}
