package quest

import (
	"context"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strings"
	"testing"
)

func TestPlayerIssuesForestEvidenceRealPVF(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.Open(pvfPath)
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
	count := 0
	for _, d := range catalog.ordered {
		if !strings.Contains(d.Name, "守护森林的战斗") {
			continue
		}
		count++
		t.Logf("FOREST id=%d path=%q name=%q grade=%q type=%q levels=%d..%d job=%q grow=%d hasGrow=%v jobChange=%d target=%+v parent=%d pre=%v collision=%v dependItems=%v", d.ID, d.Path, d.Name, d.Grade, d.Type, d.LevelMin, d.LevelMax, d.Job, d.GrowType, d.HasGrowType, d.JobChangeQuest, d.TargetCharacterRules, d.MainQuestID, d.PreRequiredGroups, d.CollisionQuests, d.HasDependGiveItem)
	}
	if count == 0 {
		t.Fatal("reported forest quest absent from actual catalog")
	}
	t.Run("P80_forest_transfer_branch", func(t *testing.T) {
		d, ok := catalog.Find(6931)
		if !ok || d.Name != "守护森林的战斗 - 剑魂" || len(d.TargetCharacterRules) != 1 ||
			d.TargetCharacterRules[0].FirstGrowType != 0 || d.TargetCharacterRules[0].AwakeningStage != -1 {
			t.Fatalf("actual forest target changed: %+v", d)
		}
		character := CharacterEligibility{Level: 15, Job: 0, GrowType: 0}
		record := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{3153: {Status: "completed"}}}
		if decision, err := catalog.DecideAccept(character, record, d.ID); err != nil || decision != AcceptNew {
			t.Fatalf("actual forest branch unavailable to base swordman after PVF prerequisite: decision=%v err=%v", decision, err)
		}
		listed := false
		for _, id := range catalog.QuestList(character, record).IDs {
			if int64(id) == d.ID {
				listed = true
			}
		}
		if !listed {
			t.Fatal("forest branch absent from client list")
		}
		if _, err := catalog.PlanAccept(character, record, d.ID); err != nil {
			t.Fatalf("forest branch accept plan: %v", err)
		}
		for _, control := range []struct {
			name      string
			character CharacterEligibility
			record    dnfrepo.QuestRecord
		}{
			{"wrong_job", CharacterEligibility{Level: 15, Job: 2}, record},
			{"already_transferred", CharacterEligibility{Level: 15, Job: 0, GrowType: 1}, record},
			{"missing_prerequisite", character, dnfrepo.QuestRecord{}},
			{"collision", character, dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{3153: {Status: "completed"}, 6930: {Status: "completed"}}}},
		} {
			t.Run(control.name, func(t *testing.T) {
				if decision, err := catalog.DecideAccept(control.character, control.record, d.ID); err == nil || decision != AcceptRejected {
					t.Fatalf("guard lost: decision=%v err=%v", decision, err)
				}
			})
		}
	})
	t.Run("P80_forest_existing_profession_branch", func(t *testing.T) {
		d, ok := catalog.Find(6930)
		if !ok || len(d.TargetCharacterRules) != 1 || d.TargetCharacterRules[0].FirstGrowType != -1 || d.TargetCharacterRules[0].AwakeningStage != -1 {
			t.Fatalf("actual existing profession branch changed: %+v", d)
		}
		record := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{3153: {Status: "completed"}}}
		if decision, err := catalog.DecideAccept(CharacterEligibility{Level: 15, Job: 0, GrowType: 1}, record, d.ID); err != nil || decision != AcceptNew {
			t.Fatalf("existing profession forest branch decision=%v err=%v", decision, err)
		}
	})
}
