package quest

import (
	"context"
	"fmt"
	"os"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestDebugDailyMissionJobDiff reproduces the live "accepted daily mission is not
// in the op21 list" symptom locally: the committed regression test uses
// CharacterEligibility{Level:90, Job:2, GrowType:36} and passes, while the live
// characters are job=0 (char 1/6) and the quests are absent from the built list.
// This test prints every predicate for both jobs so the difference is named, not
// guessed. It is a diagnostic harness; the permanent regression test follows once
// the cause is pinned.
func TestDebugDailyMissionJobDiff(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(context.Background(), archive, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	record := dnfrepo.QuestRecord{CharacterID: "1", States: map[int64]dnfrepo.QuestState{
		4872:  {Status: "completed"},
		10019: {Status: "active", ProgressValue: 1},
		2901:  {Status: "active", ProgressValue: 1},
	}}
	completed, active := questStateSets(record)

	for _, id := range []int64{10019, 2901} {
		definition, found := catalog.Find(id)
		if !found {
			t.Fatalf("quest %d not in catalog", id)
		}
		t.Logf("=== quest %d path=%s ===", id, definition.Path)
		t.Logf("  grade=%q(type=%q subtype=%d) name=%q", definition.Grade, definition.Type, definition.SubType, definition.Name)
		t.Logf("  level=[%d..%d] jobTag=%q targetCharacter=%q targetRules=%v jobChangeQuest=%d",
			definition.LevelMin, definition.LevelMax, definition.Job, definition.TargetCharacter,
			definition.TargetCharacterRules, definition.JobChangeQuest)
		t.Logf("  growType=%d hasGrowType=%v exposedByNPC=%d isEvent=%v creatureReq=%v expertReq=%v",
			definition.GrowType, definition.HasGrowType, definition.ExposedByNPC, definition.IsEvent,
			definition.HasCreatureRequirement, definition.HasExpertRequirement)
		t.Logf("  preRequiredGroups=%v answers=%v collisions=%v hasDependGiveItem=%v",
			definition.PreRequiredGroups, definition.PreRequiredAnswers, definition.CollisionQuests,
			definition.HasDependGiveItem)
		grade := normalizeQuestTag(definition.Grade)
		t.Logf("  selectableGrade=%v repeatableGrade=%v", selectableGrade(grade), repeatableGrade(grade))

		for _, job := range []int{0, 2} {
			character := CharacterEligibility{Level: 90, Job: job, GrowType: 36}
			list := catalog.QuestList(character, record)
			inList := false
			for _, listed := range list.IDs {
				if int64(listed) == id {
					inList = true
				}
			}
			step1 := definition.ExposedByNPC != 0 && !definition.IsEvent &&
				!definition.HasCreatureRequirement && !definition.HasExpertRequirement
			step2 := selectableGrade(grade) && grade != "training"
			step3 := character.Level >= definition.LevelMin && character.Level <= definition.LevelMax
			step4 := definitionMatchesCharacter(definition, character)
			step5 := true
			if _, exists := active[id]; exists {
				step5 = false // includeActive=false path
			}
			step6 := true
			if _, exists := completed[id]; exists && !repeatableGrade(grade) {
				step6 = false
			}
			step7 := true
			if len(definition.PreRequiredGroups) > 0 {
				step7 = false
				for _, group := range definition.PreRequiredGroups {
					if completedAll(group, completed) {
						step7 = true
					}
				}
			}
			step8 := len(definition.PreRequiredAnswers) == 0
			step9 := true
			for _, collision := range definition.CollisionQuests {
				if collision > 0 {
					if _, exists := completed[collision]; exists {
						step9 = false
					}
				}
			}
			preflight := list.PreflightExcluded
			t.Logf("  job=%d -> inList=%v listLen=%d preflightExcluded=%d | exposed=%v grade=%v level=%v char=%v notActive=%v notDone=%v prereq=%v answers=%v collision=%v",
				job, inList, len(list.IDs), preflight, step1, step2, step3, step4, step5, step6, step7, step8, step9)
			fmt.Printf("JOBDIFF quest=%d job=%d inList=%v charMatch=%v exposed=%v level=%v active=%v completed=%v prereq=%v\n",
				id, job, inList, step4, step1, step3, step5, step6, step7)
		}
	}
}
