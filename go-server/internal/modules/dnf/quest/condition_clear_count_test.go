package quest

import (
	"context"
	"os"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFMultiClearConditionInitialCounts(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF not configured")
	}
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	i, err := dnfpvf.Build(context.Background(), a, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, def := range c.byID {
		if normalizeQuestTag(def.Type) != "condition under clear" || def.SubType != 6 || len(def.IntData) != 3 || def.IntData[2] <= 1 {
			continue
		}
		matched++
		if got := definitionInitialTrigger(def, nil); int64(got) != def.IntData[2] {
			t.Fatalf("quest %d initial=%d PVF=%d", def.ID, got, def.IntData[2])
		}
	}
	if matched != 25 {
		t.Fatalf("real PVF multi-clear count=%d want25", matched)
	}
	for id, want := range map[int64]uint32{5477: 2, 5478: 3, 8512: 6, 8524: 8, 20668: 5} {
		def, ok := c.Find(id)
		if !ok || definitionInitialTrigger(def, nil) != want {
			t.Fatalf("quest %d count=%d want%d", id, definitionInitialTrigger(def, nil), want)
		}
	}
}

func TestConditionClearInitialCountAndUntouchedLegacyRepair(t *testing.T) {
	def := Definition{ID: 5478, Path: "n_quest/test.qst", Type: "[condition under clear]", SubType: 6, IntData: []int64{-1, 0, 3}}
	if got := definitionInitialTrigger(def, nil); got != 3 {
		t.Fatalf("initial=%d want3", got)
	}
	c := &Catalog{byID: map[int64]Definition{5478: def}}
	for _, scenario := range []string{"untouched", "partial", "completed", "imported", "already_correct"} {
		state := dnfrepo.QuestState{Status: "active", ProgressValue: 1, Extra: map[string]string{"pvf_path": def.Path, "quest_type": def.Type}}
		switch scenario {
		case "partial":
			state.Extra["last_set_trigger_type"] = "0"
		case "completed":
			state.Status = "completed"
			state.ProgressValue = 0
		case "imported":
			state.Extra = nil
		case "already_correct":
			state.ProgressValue = 3
		}
		record := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{5478: state}}
		plan, err := c.PlanLegacySaturatedActiveTriggerRepair(record, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if scenario == "untouched" {
			if len(plan.Repairs) != 1 || plan.Record.States[5478].ProgressValue != 3 {
				t.Fatal("untouched initial corruption not repaired")
			}
		} else if len(plan.Repairs) != 0 || plan.Record.States[5478].ProgressValue != state.ProgressValue {
			t.Fatalf("%s legitimate progress changed", scenario)
		}
	}
}
