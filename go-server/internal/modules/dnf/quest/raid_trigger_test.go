package quest

import (
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestRaidPhaseClearStartsWithEveryPVFCountRemaining(t *testing.T) {
	definition := Definition{Type: "[raid phase clear]", IntData: []int64{0, 0, 5, -1, 1, 0, 5, -1}}
	got := definitionInitialTrigger(definition, nil)
	if got&0x1ff != 5 || (got>>9)&0x1ff != 5 || got>>18 != 0 {
		t.Fatalf("new raid quest remaining counters=%08x; must display 0/5 and 0/5", got)
	}
	definition.IntData = []int64{1, 0, 7, 1, 1, 1, 3, 1, 1, 2, 2, 1}
	got = definitionInitialTrigger(definition, nil)
	if got&0x1ff != 7 || (got>>9)&0x1ff != 3 || (got>>18)&0x1ff != 2 {
		t.Fatalf("raid requirements must come from each PVF row: %08x", got)
	}
}

func TestLegacyRaidInitialRepairPreservesProgressAndIsIdempotent(t *testing.T) {
	definition := Definition{Path: "n_quest/anton/anton_epic_acce_01.qst", Type: "[raid phase clear]", IntData: []int64{0, 0, 5, -1, 1, 0, 5, -1}}
	catalog := &Catalog{byID: map[int64]Definition{12830: definition}}
	initial := dnfrepo.QuestState{Status: "active", ProgressValue: 1, Extra: map[string]string{"pvf_path": definition.Path, "quest_type": definition.Type}}
	for _, variant := range []string{"untouched", "event", "completed", "imported", "partial"} {
		t.Run(variant, func(t *testing.T) {
			state := initial
			state.Extra = map[string]string{"pvf_path": definition.Path, "quest_type": definition.Type}
			switch variant {
			case "event":
				state.Extra["last_set_trigger_type"] = "16"
			case "completed":
				state.Status = "completed"
			case "imported":
				state.Extra = nil
			case "partial":
				state.ProgressValue = int64(packTriggerChannels(3, 2))
			}
			record := dnfrepo.QuestRecord{States: map[int64]dnfrepo.QuestState{12830: state}}
			plan, err := catalog.PlanLegacySaturatedActiveTriggerRepair(record, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if variant == "untouched" {
				if len(plan.Repairs) != 1 || plan.Record.States[12830].ProgressValue != int64(packTriggerChannels(5, 5)) {
					t.Fatalf("incorrect raid initialization not repaired: %+v", plan)
				}
				second, err := catalog.PlanLegacySaturatedActiveTriggerRepair(plan.Record, time.Now())
				if err != nil || len(second.Repairs) != 0 {
					t.Fatalf("repair repeats: %+v err=%v", second, err)
				}
			} else if len(plan.Repairs) != 0 || !reflect.DeepEqual(plan.Record.States[12830], state) {
				t.Fatalf("legitimate state altered: %+v", plan)
			}
			if !reflect.DeepEqual(record.States[12830], state) {
				t.Fatal("planner mutated input")
			}
		})
	}
}
