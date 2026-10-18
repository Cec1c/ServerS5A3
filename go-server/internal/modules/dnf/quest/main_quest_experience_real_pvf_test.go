package quest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// This verifies settlement against unchanged real quest definitions and tables.
// The character has a synthetic pending completion; it is not a playthrough.
func TestRealPVFMainQuestExperienceTripleSettlement(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify main quest settlement against the runtime PVF")
	}
	source, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
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
	for _, band := range [][2]int{{10, 20}, {35, 50}, {65, 80}} {
		var definition Definition
		for _, q := range catalog.ordered {
			if normalizeQuestTag(q.Grade) != "epic" || (q.Job != "" && normalizeQuestTag(q.Job) != "all") ||
				!strings.Contains(q.Path, "/quest_renewal/epic_quest/") ||
				q.LevelMin < band[0] || q.LevelMin > band[1] || q.NoExperience || q.MainQuestID != 0 ||
				normalizeQuestTag(q.Type) != "meet npc" || q.RewardParseError != "" ||
				len(q.RewardItems) != 0 || len(q.RewardSelectionItems) != 0 || q.JobChangeQuest != 0 {
				continue
			}
			r, err := catalog.PlanFinishReward(CharacterEligibility{Level: q.LevelMin, Job: 2}, q.ID, ^uint16(0), false)
			if err != nil || r.HasProfession || r.HasExpertJob || r.HasSlotExpansion {
				continue
			}
			definition = q
			break
		}
		if definition.ID == 0 {
			t.Fatalf("no unchanged epic meet-npc fixture in band %v", band)
		}
		t.Run(fmt.Sprintf("quest%d_level%d", definition.ID, definition.LevelMin), func(t *testing.T) {
			threshold, err := tables.ThresholdToNext(definition.LevelMin)
			if err != nil || threshold == 0 {
				t.Fatalf("next-level threshold=%d err=%v", threshold, err)
			}
			settle := func(multiplier int) FinishCommitResult {
				t.Helper()
				repos := finishOwnerSeed(t, ctx)
				character, _, _ := repos.Character.Load(ctx, "19")
				character.Level = definition.LevelMin
				character.Stats["exp"] = int64(threshold - 1)
				if err := repos.Character.Save(ctx, character); err != nil {
					t.Fatal(err)
				}
				skill, _, _ := repos.Skill.Load(ctx, "19")
				skill.Points.SyncedLevel = definition.LevelMin
				if err := repos.Skill.Save(ctx, skill); err != nil {
					t.Fatal(err)
				}
				if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
					definition.ID: {Status: "active", ProgressValue: 0, Extra: map[string]string{"completion_key": "real-pvf-rate-test", "completion_kind": "clear_map", "reward_state": "pending"}},
				}}); err != nil {
					t.Fatal(err)
				}
				owner, err := NewOwner(repos)
				if err != nil {
					t.Fatal(err)
				}
				input := finishOwnerInput(tables, finishOwnerAllocator(nil))
				input.QuestID = definition.ID
				input.ExpectedCompletionKey = "real-pvf-rate-test"
				input.MainQuestExperienceMultiplier = multiplier
				result, err := owner.ApplyFinishSettlement(ctx, catalog, input)
				if err != nil {
					t.Fatal(err)
				}
				got, _, err := repos.Character.Load(ctx, "19")
				if err != nil {
					t.Fatal(err)
				}
				if got.Level != result.NewLevel || got.Stats["exp"] != int64(result.NewExperience) ||
					result.NewExperience != threshold-1+result.ExperienceDelta || result.NewLevel <= definition.LevelMin {
					t.Fatalf("persisted progression differs: %+v / %+v", got, result)
				}
				gotSkill, _, err := repos.Skill.Load(ctx, "19")
				if err != nil || gotSkill.Points.SyncedLevel != result.NewLevel || result.SPDelta <= 0 {
					t.Fatalf("persisted skill points differ: %+v / %+v err=%v", gotSkill.Points, result, err)
				}
				input.MainQuestExperienceMultiplier = 1
				replay, err := owner.ApplyFinishSettlement(ctx, catalog, input)
				if err != nil {
					t.Fatal(err)
				}
				if !replay.Replayed || replay.ExperienceDelta != result.ExperienceDelta || replay.NewExperience != result.NewExperience {
					t.Fatalf("replay changed award: %+v / %+v", result, replay)
				}
				return result
			}
			one, three := settle(1), settle(3)
			if one.ExperienceDelta == 0 || uint64(three.ExperienceDelta) != uint64(one.ExperienceDelta)*3 || uint64(three.BaseExperienceDelta) != uint64(one.BaseExperienceDelta)*3 {
				t.Fatalf("ratio: one=%+v three=%+v", one, three)
			}
			t.Logf("quest=%d path=%s level=%d experience=%d -> %d, new level=%d, total experience=%d, SP=%d", definition.ID, definition.Path, definition.LevelMin, one.ExperienceDelta, three.ExperienceDelta, three.NewLevel, three.NewExperience, three.SPDelta)
		})
	}
}
