package quest

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	"longheng.io/server/internal/modules/dnf/jobmap"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// The input definitions and progression tables are unchanged resource text.
// Pending objective rows are seeded: this exercises settlement, not combat.
func TestSeekerPercentRealResourceSettlement(t *testing.T) {
	ctx := context.Background()
	var source dnfpvf.Source
	if path := os.Getenv("SEEKER_PERCENT_SETTLEMENT_PVF"); path != "" {
		archive, err := platformpvf.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if archive.Snapshot().Checksum != "9dcd31a3e6001fd5563d36dc65cbaa96bed8b782700cba30625197e27cf1e43c" {
			t.Fatal("unreviewed archive")
		}
		source = archive
	} else if path := os.Getenv("SEEKER_PERCENT_SOURCE_JSON"); path != "" {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var texts finishProgressionSource
		if err := json.Unmarshal(body, &texts); err != nil {
			t.Fatal(err)
		}
		source = texts
	} else {
		t.Skip("set SEEKER_PERCENT_SETTLEMENT_PVF or SEEKER_PERCENT_SOURCE_JSON")
	}
	index, err := dnfpvf.Build(ctx, source, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := progression.Load(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	newFixture := func(t *testing.T, id int64, level int) (dnfrepo.Group, *Owner, FinishCommitInput) {
		t.Helper()
		repos := finishOwnerSeed(t, ctx)
		character, _, _ := repos.Character.Load(ctx, "19")
		character.Level = level
		if level > 1 {
			v, err := tables.ThresholdToNext(level - 1)
			if err != nil {
				t.Fatal(err)
			}
			character.Stats["exp"] = int64(v)
		}
		input := finishOwnerInput(tables, seekerPercentAllocator)
		input.QuestID, input.ExpectedCompletionKey = id, ""
		input.MainQuestExperiencePercent, input.MainQuestExperienceMultiplier, input.ExperienceBonusPercent = 15, 3, 20
		character.Stats["experience_potion_quest_basis_points"] = 10000
		character.Stats["experience_potion_quest_until_ms"] = input.CommittedAt.Add(time.Hour).UnixMilli()
		if err := repos.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
		skill, _, _ := repos.Skill.Load(ctx, "19")
		skill.Points.SyncedLevel = level
		if err := repos.Skill.Save(ctx, skill); err != nil {
			t.Fatal(err)
		}
		states := map[int64]dnfrepo.QuestState{id: {Status: "active", Extra: map[string]string{}}}
		definition := catalog.byID[id]
		if normalizeQuestTag(definition.Type) == "quest clear" {
			for _, child := range definition.IntData {
				states[child] = dnfrepo.QuestState{Status: "completed"}
			}
		}
		if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: states}); err != nil {
			t.Fatal(err)
		}
		owner, err := NewOwner(repos)
		if err != nil {
			t.Fatal(err)
		}
		return repos, owner, input
	}
	for _, tc := range []struct {
		id    int64
		level int
		want  uint32
	}{
		{3146, 3, 192}, {3249, 35, 235974}, {3249, 60, 235974}, {3249, 89, 235974}, {3458, 56, 1622306}, {3971, 88, 32029581}, {3974, 89, 42444667},
	} {
		t.Run(fmt.Sprintf("quest%d_character%d", tc.id, tc.level), func(t *testing.T) {
			repos, owner, input := newFixture(t, tc.id, tc.level)
			result, err := owner.ApplyFinishSettlement(ctx, catalog, input)
			if err != nil {
				t.Fatal(err)
			}
			if result.BaseExperienceDelta != tc.want || result.ExperienceDelta != tc.want || result.ExperienceBonusDelta != 0 {
				t.Fatalf("award=%+v want=%d", result, tc.want)
			}
			character, _, _ := repos.Character.Load(ctx, "19")
			if character.Level != result.NewLevel || character.Stats["exp"] != int64(result.NewExperience) {
				t.Fatal("persisted progression differs")
			}
			inventory, _, _ := repos.Inventory.Load(ctx, "19")
			for _, rule := range catalog.byID[tc.id].RewardItems {
				if seekerPercentCount(inventory, rule.ItemID) != rule.Count {
					t.Fatalf("reward item %d missing: %+v", rule.ItemID, inventory)
				}
			}
			input.MainQuestExperiencePercent = 20
			replay, err := owner.ApplyFinishSettlement(ctx, catalog, input)
			if err != nil || !replay.Replayed || replay.ExperienceDelta != tc.want {
				t.Fatalf("replay=%+v err=%v", replay, err)
			}
			after, _, _ := repos.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(inventory, after) {
				t.Fatal("replay changed inventory")
			}
			t.Logf("quest=%d taskLevel=%d character=%d base=total=%d bonus=0; 20%% replay=%d", tc.id, catalog.byID[tc.id].LevelMin, tc.level, result.ExperienceDelta, replay.ExperienceDelta)
		})
	}
	for _, tc := range []struct {
		id, first, last, item, initial, remaining int64
		level                                     int
		want                                      uint32
	}{
		{3249, 3609, 3610, 10164786, 1, 0, 35, 235974},
		{3458, 3472, 3473, 10164800, 1, 4, 56, 1622306},
		{3971, 3972, 3973, 0, 0, 0, 88, 32029581},
	} {
		t.Run(fmt.Sprintf("parent_child_%d", tc.id), func(t *testing.T) {
			repos, owner, input := newFixture(t, tc.id, tc.level)
			quests, _, _ := repos.Quest.Load(ctx, "19")
			quests.States[tc.first] = dnfrepo.QuestState{Status: "active"}
			quests.States[tc.last] = dnfrepo.QuestState{Status: "active"}
			if err := repos.Quest.Save(ctx, quests); err != nil {
				t.Fatal(err)
			}
			inventory, _, _ := repos.Inventory.Load(ctx, "19")
			if tc.item > 0 {
				if _, err := seekerPercentAllocator(&inventory, FinishItemGrantRequest{ItemID: tc.item, Count: tc.initial}); err != nil {
					t.Fatal(err)
				}
				if err := repos.Inventory.Save(ctx, inventory); err != nil {
					t.Fatal(err)
				}
			}
			input.QuestID = tc.first
			child, err := owner.ApplyFinishSettlement(ctx, catalog, input)
			if err != nil || child.ExperienceDelta != 0 || child.BaseExperienceDelta != 0 || child.ExperienceBonusDelta != 0 {
				t.Fatalf("child=%+v err=%v", child, err)
			}
			input.QuestID = tc.id
			result, err := owner.ApplyFinishSettlement(ctx, catalog, input)
			if err != nil || result.ExperienceDelta != tc.want || len(result.TerminalChildren) != 1 || result.TerminalChildren[0].QuestID != tc.last {
				t.Fatalf("parent=%+v err=%v", result, err)
			}
			inventory, _, _ = repos.Inventory.Load(ctx, "19")
			if tc.item > 0 && seekerPercentCount(inventory, tc.item) != tc.remaining {
				t.Fatalf("item balance=%+v", inventory)
			}
			quests, _, _ = repos.Quest.Load(ctx, "19")
			if !isCompletedQuestStatus(quests.States[tc.last].Status) {
				t.Fatal("terminal child remains active")
			}
			input.MainQuestExperiencePercent = 20
			replay, err := owner.ApplyFinishSettlement(ctx, catalog, input)
			if err != nil || !replay.Replayed || replay.ExperienceDelta != tc.want {
				t.Fatalf("replay=%+v err=%v", replay, err)
			}
			after, _, _ := repos.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(inventory, after) {
				t.Fatal("parent replay changed inventory")
			}
			t.Logf("parent=%d first=%d exp=0 terminal=%d exp=0 parentExp=%d item=%d remaining=%d", tc.id, tc.first, tc.last, result.ExperienceDelta, tc.item, tc.remaining)
		})
	}
	t.Run("missing_letter_rolls_back_then_retries", func(t *testing.T) {
		repos, owner, input := newFixture(t, 3249, 35)
		quests, _, _ := repos.Quest.Load(ctx, "19")
		quests.States[3610] = dnfrepo.QuestState{Status: "active"}
		if err := repos.Quest.Save(ctx, quests); err != nil {
			t.Fatal(err)
		}
		before, _, _ := repos.Character.Load(ctx, "19")
		if _, err := owner.ApplyFinishSettlement(ctx, catalog, input); !errors.Is(err, ErrFinishRequiredItemsMissing) {
			t.Fatalf("missing letter: %v", err)
		}
		after, _, _ := repos.Character.Load(ctx, "19")
		afterQuests, _, _ := repos.Quest.Load(ctx, "19")
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(quests, afterQuests) {
			t.Fatal("failed parent persisted reward or child completion")
		}
		inventory, _, _ := repos.Inventory.Load(ctx, "19")
		if _, err := seekerPercentAllocator(&inventory, FinishItemGrantRequest{ItemID: 10164786, Count: 1}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory.Save(ctx, inventory); err != nil {
			t.Fatal(err)
		}
		result, err := owner.ApplyFinishSettlement(ctx, catalog, input)
		if err != nil || result.Replayed || result.ExperienceDelta != 235974 {
			t.Fatalf("retry=%+v err=%v", result, err)
		}
		inventory, _, _ = repos.Inventory.Load(ctx, "19")
		if seekerPercentCount(inventory, 10164786) != 0 {
			t.Fatal("retry did not consume letter")
		}
	})
	t.Run("legacy_receipt_keeps_original_amount", func(t *testing.T) {
		_, owner, input := newFixture(t, 3974, 89)
		input.MainQuestExperiencePercent = 0
		legacy, err := owner.ApplyFinishSettlement(ctx, catalog, input)
		if err != nil {
			t.Fatal(err)
		}
		input.MainQuestExperiencePercent = 15
		replay, err := owner.ApplyFinishSettlement(ctx, catalog, input)
		if err != nil || !replay.Replayed || replay.ExperienceDelta != legacy.ExperienceDelta || replay.ExperienceBonusDelta != legacy.ExperienceBonusDelta {
			t.Fatalf("legacy=%+v replay=%+v err=%v", legacy, replay, err)
		}
	})
	t.Run("level_cap_and_honor", func(t *testing.T) {
		honorTables, err := dnfhonor.LoadTables(source)
		if err != nil {
			t.Fatal(err)
		}
		threshold, _ := tables.ThresholdToNext(89)
		for _, level := range []int{89, 90} {
			repos, owner, input := newFixture(t, 3974, level)
			character, _, _ := repos.Character.Load(ctx, "19")
			character.Stats["exp"] = int64(threshold)
			wantGain := uint32(42444667)
			if level == 89 {
				character.Stats["exp"] -= 100
				wantGain -= 100
			}
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatal(err)
			}
			input.LoadHonorTables = func(context.Context) (*dnfhonor.Tables, error) { return honorTables, nil }
			result, err := owner.ApplyFinishSettlement(ctx, catalog, input)
			if err != nil || result.NewLevel != 90 || result.ExperienceDelta != 42444667 || result.HonorExpertGain != wantGain {
				t.Fatalf("level%d=%+v err=%v", level, result, err)
			}
			after, _, _ := repos.Character.Load(ctx, "19")
			expected, err := honorTables.AdvanceExpert(dnfhonor.ExpertProgress{}, uint64(wantGain))
			if err != nil || after.Level != 90 || after.Stats[dnfhonor.ExpertLevelStatKey] != int64(expected.Level) || after.Stats[dnfhonor.ExpertProgressExperienceStatKey] != int64(expected.CurrentLevelExperience) {
				t.Fatalf("honor=%+v expected=%+v err=%v", after.Stats, expected, err)
			}
			t.Logf("level=%d->90 reward=42444667 honorGain=%d", level, result.HonorExpertGain)
		}
	})
	t.Run("route_model_88_89", func(t *testing.T) { seekerPercentRouteModel(t, catalog, tables) })
}

func seekerPercentCount(record dnfrepo.InventoryRecord, id int64) int64 {
	var count int64
	for _, stack := range record.Slots {
		if stack.ItemID == id {
			count += stack.Count
		}
	}
	return count
}

func seekerPercentAllocator(record *dnfrepo.InventoryRecord, request FinishItemGrantRequest) ([]FinishCommittedItem, error) {
	if record.Slots == nil {
		record.Slots = map[string]dnfrepo.ItemStack{}
	}
	slot := uint16(65)
	for record.Slots[fmt.Sprintf("0:%d", slot)].ItemID != 0 && record.Slots[fmt.Sprintf("0:%d", slot)].ItemID != request.ItemID {
		slot++
	}
	key := fmt.Sprintf("0:%d", slot)
	count := record.Slots[key].Count + request.Count
	raw := make([]byte, 0x77)
	binary.LittleEndian.PutUint16(raw[0:2], slot)
	binary.LittleEndian.PutUint32(raw[2:6], uint32(request.ItemID))
	binary.LittleEndian.PutUint32(raw[6:10], uint32(count))
	record.Slots[key] = dnfrepo.ItemStack{ItemID: request.ItemID, Count: count, RawEntry: raw}
	return []FinishCommittedItem{{SlotKey: key, SlotIndex: slot, ItemID: request.ItemID, Delta: request.Count, PostCount: count, CountOrSeed: uint32(request.Count), RawEntry: raw}}, nil
}

// Use the production accept planner for PVF prerequisite/collision routing.
// Completion itself is assumed, so counts describe a quest route, not playtime.
func seekerPercentRouteModel(t *testing.T, catalog *Catalog, tables *progression.Tables) {
	t.Helper()
	for faction := int64(3870); faction <= 3874; faction++ {
		var reference map[int][]int64
		for job := 0; job <= jobmap.MaxID; job++ {
			if !jobmap.Valid(job) {
				continue
			}
			record := dnfrepo.QuestRecord{CharacterID: "route", States: map[int64]dnfrepo.QuestState{3944: {Status: "completed"}, faction: {Status: "completed"}}}
			byLevel := map[int][]int64{}
			for changed := true; changed; {
				changed = false
				for _, d := range catalog.ordered {
					if d.LevelMin < 88 || d.LevelMin > 89 || !isPercentMainQuestExperienceDefinition(d) || d.NoExperience {
						continue
					}
					plan, err := catalog.PlanAccept(CharacterEligibility{Level: 90, Job: job, GrowType: 36}, record, d.ID)
					if err != nil {
						continue
					}
					if _, ok := record.States[d.ID]; ok {
						continue
					}
					record.States[d.ID] = dnfrepo.QuestState{Status: "completed"}
					for _, child := range plan.LinkedSubQuests {
						record.States[child.QuestID] = dnfrepo.QuestState{Status: "completed"}
					}
					byLevel[d.LevelMin] = append(byLevel[d.LevelMin], d.ID)
					changed = true
				}
			}
			if len(byLevel[88]) != 25 || len(byLevel[89]) != 55 || !isCompletedQuestStatus(record.States[4872].Status) {
				t.Fatalf("faction=%d job=%d incomplete route: %+v", faction, job, byLevel)
			}
			if reference == nil {
				reference = byLevel
			} else if !reflect.DeepEqual(reference, byLevel) {
				t.Fatalf("job route differs faction=%d job=%d", faction, job)
			}
		}
		for _, level := range []int{88, 89} {
			reward, err := tables.MainQuestExperiencePercent(level, 15)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("faction=%d level=%d route_count=%d reward_each=%d reward_sum=%d ids=%v", faction, level, len(reference[level]), reward, uint64(reward)*uint64(len(reference[level])), reference[level])
		}
	}
}
