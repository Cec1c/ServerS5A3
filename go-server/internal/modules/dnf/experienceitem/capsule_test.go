package experienceitem

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/progression"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type capsuleUsePVFSource map[string]string

func (s capsuleUsePVFSource) ReadText(path string) (string, error) {
	value, ok := s[path]
	if !ok {
		return "", fmt.Errorf("capsule test PVF lacks %s", path)
	}
	return value, nil
}

func capsuleUseTables(t *testing.T, thresholds string) *progression.Tables {
	t.Helper()
	var sp, tp strings.Builder
	for level := 1; level <= 12; level++ {
		fmt.Fprintf(&sp, "%d 10\n", level)
		if level >= 5 {
			fmt.Fprintf(&tp, "%d 1\n", level)
		}
	}
	source := capsuleUsePVFSource{
		progression.ExperienceTablePath: thresholds,
		progression.SkillPointTablePath: "[sp table]\n" + sp.String() + "[/sp table]\n[tp table]\n" + tp.String() + "[/tp table]\n",
		progression.QuestParameterPath:  "[difficulty] `N` 100\n[/difficulty]\n[exp reward table]\n100 -1\n[gold reward table]\n10\n[green level penalty]\n80\n[grey level penalty]\n30\n",
	}
	tables, err := progression.Load(context.Background(), source)
	if err != nil {
		t.Fatalf("load capsule progression tables: %v", err)
	}
	return tables
}

func capsuleUseDefinition(kind CapsuleKind, amount uint32) Capsule {
	return Capsule{
		ItemID:       490001001,
		PVFPath:      "stackable/experience_capsule.stk",
		Kind:         kind,
		Amount:       amount,
		MinimumLevel: 1,
		MaximumLevel: 90,
		Jobs:         []string{"[all]"},
	}
}

func capsuleUseFixture(t *testing.T) (repo.Group, *progression.Tables, CapsuleCommand, time.Time) {
	t.Helper()
	ctx := context.Background()
	tables := capsuleUseTables(t, "100 250 450 700 1000 1350 1750 2200 2700 3250 3850 4500")
	g := memory.NewMemoryGroup()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	definition := capsuleUseDefinition(CapsuleFixed, 25)
	for _, err := range []error{
		g.Character.Save(ctx, repo.CharacterRecord{
			CharacterID: "77",
			AccountID:   "capsule-account",
			Job:         "0",
			Level:       4,
			Stats: map[string]int64{
				"exp":         500,
				"grow_type":   0,
				"delete_flag": 0,
				"gold":        456,
				"marker":      9,
			},
			Location: repo.CharacterLocation{TownID: 39},
		}),
		g.Skill.Save(ctx, repo.SkillRecord{
			CharacterID: "77",
			Skills:      map[int64]repo.SkillState{17: {Level: 3, Enabled: true}},
			Points: repo.SkillPointState{
				SyncedLevel: 4,
				TotalSP:     75,
				RemainingSP: 25,
				TotalTP:     3,
				RemainingTP: 1,
			},
		}),
		g.Inventory.Save(ctx, repo.InventoryRecord{
			CharacterID: "77",
			Slots: map[string]repo.ItemStack{
				"0:4": {
					ItemID: definition.ItemID,
					Count:  2,
					Extra:  map[string]string{"pvf_path": definition.PVFPath},
				},
				"0:5": {ItemID: 10002567, Count: 8},
				"1:4": {ItemID: definition.ItemID, Count: 9},
			},
		}),
		g.Quest.Save(ctx, repo.QuestRecord{
			CharacterID: "77",
			States:      map[int64]repo.QuestState{100: {Status: "active"}},
		}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	command := CapsuleCommand{
		AccountID:   "capsule-account",
		CharacterID: "77",
		SlotIndex:   4,
		LevelCap:    10,
		Definition:  definition,
		Now:         now,
		Revalidate:  func() bool { return true },
		ProjectStack: func(stack repo.ItemStack) (repo.ItemStack, error) {
			return stack, nil
		},
		LevelStats: func(repo.CharacterRecord, int) (map[string]int64, error) {
			return nil, nil
		},
	}
	return g, tables, command, now
}

type capsuleUseState struct {
	Character repo.CharacterRecord
	Skill     repo.SkillRecord
	Inventory repo.InventoryRecord
	Quest     repo.QuestRecord
}

func capsuleUseSnapshot(t *testing.T, g repo.Group) capsuleUseState {
	t.Helper()
	ctx := context.Background()
	character, _, err := g.Character.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	skill, _, err := g.Skill.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	inventory, _, err := g.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	quest, _, err := g.Quest.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	return capsuleUseState{
		Character: character,
		Skill:     skill,
		Inventory: inventory,
		Quest:     quest,
	}
}

func capsuleUseCooldownKey(itemID int64) string {
	return fmt.Sprintf("experience_capsule_next_use_ms_%d", itemID)
}

func TestCapsuleUseGainThresholdKindsAndTableBounds(t *testing.T) {
	tables := capsuleUseTables(t, "100 250 450 710 1000")

	fixed := capsuleUseDefinition(CapsuleFixed, 37)
	if gain, err := fixed.Gain(tables, 3); err != nil || gain != 37 {
		t.Fatalf("fixed gain = %d, err = %v", gain, err)
	}

	percent := capsuleUseDefinition(CapsulePercent, 7)
	if gain, err := percent.Gain(tables, 4); err != nil || gain != 18 {
		t.Fatalf("percent gain = %d, err = %v; want floor((710-450)*7/100)=18", gain, err)
	}

	table := capsuleUseDefinition(CapsuleTable, 0)
	table.Bands = []CapsuleBand{
		{Minimum: 2, Maximum: 3, Amount: 10},
		{Minimum: 4, Maximum: 4, Amount: 17},
		{Minimum: 6, Maximum: 6, Amount: 23},
	}
	for _, tc := range []struct {
		name    string
		level   int
		want    uint32
		wantErr bool
	}{
		{name: "below_first_band", level: 1, wantErr: true},
		{name: "first_band_minimum", level: 2, want: 10},
		{name: "first_band_maximum", level: 3, want: 10},
		{name: "middle_band", level: 4, want: 17},
		{name: "gap", level: 5, wantErr: true},
		{name: "last_band_maximum", level: 6, want: 23},
		{name: "above_last_band", level: 7, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := table.Gain(tables, tc.level)
			if tc.wantErr {
				if !errors.Is(err, ErrCapsule) {
					t.Fatalf("Gain(level=%d) error = %v, want ErrCapsule", tc.level, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Gain(level=%d) = %d, err = %v; want %d", tc.level, got, err, tc.want)
			}
		})
	}
}

func TestUseCapsuleCommitsOneUnitAndCrossedLevelEvents(t *testing.T) {
	g, tables, command, now := capsuleUseFixture(t)
	command.Definition.Amount = 500
	before := capsuleUseSnapshot(t, g)

	result, err := UseCapsule(context.Background(), g, tables, command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Character.Level != 6 || result.Character.Stats["exp"] != 1000 {
		t.Fatalf("character progression = level %d exp %d", result.Character.Level, result.Character.Stats["exp"])
	}
	if result.Progression.Experience.Gain != 500 ||
		result.Progression.Experience.LevelsGained != 2 ||
		result.Progression.SkillPoints.SPGain != 20 ||
		result.Progression.SkillPoints.TPGain != 2 {
		t.Fatalf("progression plan = %+v", result.Progression)
	}
	if result.ItemID != command.Definition.ItemID || result.Remaining != 1 {
		t.Fatalf("capsule result item=%d remaining=%d", result.ItemID, result.Remaining)
	}

	if spent := result.Skill.Points.TotalSP - result.Skill.Points.RemainingSP; spent != 50 {
		t.Fatalf("spent SP = %d, want 50", spent)
	}
	if spent := result.Skill.Points.TotalTP - result.Skill.Points.RemainingTP; spent != 2 {
		t.Fatalf("spent TP = %d, want 2", spent)
	}
	if !reflect.DeepEqual(result.Skill.Skills, before.Skill.Skills) {
		t.Fatalf("skill states changed: before=%v after=%v", before.Skill.Skills, result.Skill.Skills)
	}
	for level := 2; level <= 10; level++ {
		want := int64(0)
		if level == 5 || level == 6 {
			want = now.Unix()
		}
		if got := result.Character.Stats[repo.LevelUpEventKey(level)]; got != want {
			t.Fatalf("level event %d = %d, want %d", level, got, want)
		}
	}
	if result.Character.Stats["gold"] != 456 ||
		result.Character.Stats["marker"] != 9 ||
		result.Character.Location.TownID != 39 {
		t.Fatalf("unrelated character state changed: %+v", result.Character)
	}

	afterInventory, _, err := g.Inventory.Load(context.Background(), "77")
	if err != nil {
		t.Fatal(err)
	}
	if afterInventory.Slots["0:4"].Count != 1 ||
		afterInventory.Slots["0:4"].Extra["pvf_path"] != command.Definition.PVFPath {
		t.Fatalf("source stack = %+v", afterInventory.Slots["0:4"])
	}
	if !reflect.DeepEqual(afterInventory.Slots["0:5"], before.Inventory.Slots["0:5"]) ||
		!reflect.DeepEqual(afterInventory.Slots["1:4"], before.Inventory.Slots["1:4"]) {
		t.Fatalf("another inventory slot changed: before=%v after=%v", before.Inventory.Slots, afterInventory.Slots)
	}
	afterQuest, _, err := g.Quest.Load(context.Background(), "77")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterQuest, before.Quest) {
		t.Fatalf("quest changed: before=%+v after=%+v", before.Quest, afterQuest)
	}
}

func TestUseCapsuleStopsAtCrossLevelCap(t *testing.T) {
	g, tables, command, _ := capsuleUseFixture(t)
	command.Definition.Amount = 500
	command.LevelCap = 5

	result, err := UseCapsule(context.Background(), g, tables, command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Character.Level != 5 ||
		result.Character.Stats["exp"] != 700 ||
		result.Progression.Experience.NewLevel != 5 ||
		result.Progression.Experience.NewExperience != 700 {
		t.Fatalf("capped progression = character %+v plan %+v", result.Character, result.Progression.Experience)
	}
	if result.Character.Stats[repo.LevelUpEventKey(5)] == 0 ||
		result.Character.Stats[repo.LevelUpEventKey(6)] != 0 {
		t.Fatalf("capped level events = %v", result.Character.Stats)
	}
}

func TestUseCapsuleRejectsInvalidInputsWithoutMutation(t *testing.T) {
	for _, name := range []string{
		"expired",
		"locked",
		"wrong_account",
		"wrong_character",
		"wrong_item",
		"cooldown",
		"at_level_cap",
	} {
		t.Run(name, func(t *testing.T) {
			g, tables, command, now := capsuleUseFixture(t)
			ctx := context.Background()
			character, _, err := g.Character.Load(ctx, "77")
			if err != nil {
				t.Fatal(err)
			}
			skill, _, err := g.Skill.Load(ctx, "77")
			if err != nil {
				t.Fatal(err)
			}
			inventory, _, err := g.Inventory.Load(ctx, "77")
			if err != nil {
				t.Fatal(err)
			}
			stack := inventory.Slots["0:4"]
			switch name {
			case "expired":
				stack.ExpireAt = now
				inventory.Slots["0:4"] = stack
			case "locked":
				stack.Extra["equipment_lock_state"] = "1"
				inventory.Slots["0:4"] = stack
			case "wrong_account":
				command.AccountID = "another-account"
			case "wrong_character":
				command.CharacterID = "78"
			case "wrong_item":
				stack.ItemID++
				inventory.Slots["0:4"] = stack
			case "cooldown":
				command.Definition.CoolTimeMS = 1000
				character.Stats[capsuleUseCooldownKey(command.Definition.ItemID)] = now.UnixMilli() + 1
			case "at_level_cap":
				character.Level = command.LevelCap
				character.Stats["exp"] = 0
				skill.Points.SyncedLevel = command.LevelCap
			default:
				t.Fatalf("unknown case %q", name)
			}
			for _, err := range []error{
				g.Character.Save(ctx, character),
				g.Skill.Save(ctx, skill),
				g.Inventory.Save(ctx, inventory),
			} {
				if err != nil {
					t.Fatal(err)
				}
			}
			before := capsuleUseSnapshot(t, g)
			if _, err := UseCapsule(ctx, g, tables, command); err == nil {
				t.Fatalf("%s input was accepted", name)
			}
			if after := capsuleUseSnapshot(t, g); !reflect.DeepEqual(after, before) {
				t.Fatalf("%s rejection changed persistent state: before=%+v after=%+v", name, before, after)
			}
		})
	}
}

func TestUseCapsuleLateFailuresRollBackEveryAggregate(t *testing.T) {
	for _, name := range []string{"late_revalidate", "level_stats_error"} {
		t.Run(name, func(t *testing.T) {
			g, tables, command, _ := capsuleUseFixture(t)
			command.Definition.Amount = 500
			var called atomic.Bool
			switch name {
			case "late_revalidate":
				calls := 0
				command.Revalidate = func() bool {
					calls++
					called.Store(true)
					return calls == 1
				}
			case "level_stats_error":
				command.LevelStats = func(repo.CharacterRecord, int) (map[string]int64, error) {
					called.Store(true)
					return nil, errors.New("synthetic level stats failure")
				}
			default:
				t.Fatalf("unknown case %q", name)
			}
			before := capsuleUseSnapshot(t, g)
			if _, err := UseCapsule(context.Background(), g, tables, command); err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if !called.Load() {
				t.Fatalf("%s failure hook was not reached", name)
			}
			if after := capsuleUseSnapshot(t, g); !reflect.DeepEqual(after, before) {
				t.Fatalf("%s did not roll back all aggregates: before=%+v after=%+v", name, before, after)
			}
		})
	}
}

func TestUseCapsuleConcurrentSingleStackConsumesAtMostOnce(t *testing.T) {
	g, tables, command, _ := capsuleUseFixture(t)
	ctx := context.Background()
	inventory, _, err := g.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	stack := inventory.Slots["0:4"]
	stack.Count = 1
	inventory.Slots["0:4"] = stack
	if err := g.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	questBefore, _, err := g.Quest.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}

	const attempts = 16
	var successes, failures atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := UseCapsule(ctx, g, tables, command); err == nil {
				successes.Add(1)
			} else {
				failures.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if successes.Load() != 1 || failures.Load() != attempts-1 {
		t.Fatalf("concurrent single stack successes=%d failures=%d", successes.Load(), failures.Load())
	}
	after, _, err := g.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	if _, found := after.Slots["0:4"]; found {
		t.Fatalf("consumed stack remains: %+v", after.Slots["0:4"])
	}
	questAfter, _, err := g.Quest.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(questAfter, questBefore) {
		t.Fatalf("quest changed under concurrent use: before=%+v after=%+v", questBefore, questAfter)
	}
}
