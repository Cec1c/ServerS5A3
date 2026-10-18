package quest

import (
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// V124's material-satisfied branch emits one decrement for every remaining
// low-channel count. Material quantities belong to its item/count predicates;
// the channel tracks whether that whole predicate still needs satisfying.
func TestSeekAndMeetNPCInitialTriggerIsOneMaterialPredicate(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []int64
		want uint32
	}{
		{"one-item", []int64{1001, 1, 79}, 513},
		{"two-hundred-items", []int64{1001, 200, 79}, 513},
		{"four-kinds-qst3763", []int64{3033, 200, 3034, 200, 3035, 200, 3036, 200, 79}, 513},
		{"npc-is-last-field", []int64{1001, 10, 1002, 20, 0}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := definitionInitialTrigger(Definition{Type: "[seek n meet npc]", IntData: tc.data}, nil)
			if got != tc.want {
				t.Fatalf("trigger=%d (material channel=%d), want %d", got, got&0x1ff, tc.want)
			}
		})
	}
}

func TestLegacySeekAndMeetTriggerRepairPreservesEarnedChannels(t *testing.T) {
	definition := Definition{ID: 3763, Path: "n_quest/seek.qst", Type: "[seek n meet npc]", IntData: []int64{3033, 200, 3034, 200, 3035, 200, 3036, 200, 79}}
	for _, tc := range []struct {
		name          string
		before, after int64
		status        string
		metadata      bool
		mutate        func(*Definition, *dnfrepo.QuestState)
	}{
		{name: "untouched", before: 712, after: 513, metadata: true},
		{name: "partly-decremented", before: 611, after: 513, metadata: true},
		{name: "npc-already-met", before: 200, after: 1, metadata: true},
		{name: "last-material-step", before: 513, after: 513, metadata: true},
		{name: "materials-satisfied", before: 512, after: 512, metadata: true},
		{name: "all-satisfied", before: 0, after: 0, metadata: true},
		{name: "only-materials-pending", before: 1, after: 1, metadata: true},
		{name: "completed", before: 712, after: 712, metadata: true, status: "completed"},
		{name: "no-provenance", before: 712, after: 712},
		{name: "wrong-path", before: 712, after: 712, metadata: true, mutate: func(_ *Definition, s *dnfrepo.QuestState) { s.Extra["pvf_path"] = "n_quest/other.qst" }},
		{name: "wrong-type", before: 712, after: 712, metadata: true, mutate: func(_ *Definition, s *dnfrepo.QuestState) { s.Extra["quest_type"] = "[seek item]" }},
		{name: "other-quest", before: 712, after: 712, metadata: true, mutate: func(d *Definition, _ *dnfrepo.QuestState) { d.Type = "[meet npc]" }},
		{name: "even-fields", before: 712, after: 712, metadata: true, mutate: func(d *Definition, _ *dnfrepo.QuestState) { d.IntData = []int64{3033, 200, 3034, 200} }},
		{name: "invalid-pair", before: 712, after: 712, metadata: true, mutate: func(d *Definition, _ *dnfrepo.QuestState) { d.IntData = []int64{3033, 200, 0, 200, 79} }},
		{name: "negative", before: -1, after: -1, metadata: true},
		{name: "out-of-u32", before: 1 << 32, after: 1 << 32, metadata: true},
		{name: "extra-channel", before: 1<<18 | 712, after: 1<<18 | 712, metadata: true},
		{name: "too-many-npc", before: 2<<9 | 200, after: 2<<9 | 200, metadata: true},
		{name: "beyond-old-initial", before: 713, after: 713, metadata: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := definition
			s := dnfrepo.QuestState{Status: "active", ProgressValue: tc.before}
			if tc.status != "" {
				s.Status = tc.status
			}
			if tc.metadata {
				s.Extra = map[string]string{"pvf_path": d.Path, "quest_type": d.Type}
			}
			if tc.mutate != nil {
				tc.mutate(&d, &s)
			}
			catalog := &Catalog{byID: map[int64]Definition{3763: d}}
			record := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{3763: s}, Progress: map[int64]dnfrepo.QuestState{3763: s}}
			before := dnfrepo.CloneQuest(record)
			now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
			plan, err := catalog.PlanLegacySaturatedActiveTriggerRepair(record, now)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(record, before) {
				t.Fatal("planner mutated input")
			}
			for _, states := range []map[int64]dnfrepo.QuestState{plan.Record.States, plan.Record.Progress} {
				if got := states[3763].ProgressValue; got != tc.after {
					t.Fatalf("trigger=%d want=%d", got, tc.after)
				}
			}
			if tc.before == tc.after {
				if len(plan.Repairs) != 0 || !reflect.DeepEqual(plan.Record, before) {
					t.Fatalf("untouched state changed: %+v", plan)
				}
			} else {
				if len(plan.Repairs) != 2 || len(plan.ChangedFields) != 2 {
					t.Fatalf("repair fields=%+v", plan)
				}
				if plan.Record.States[3763].Extra["legacy_trigger_repair_kind"] != "pvf_seek_meet_material_count_to_predicate" {
					t.Fatal("missing repair provenance")
				}
			}
			again, err := catalog.PlanLegacySaturatedActiveTriggerRepair(plan.Record, now.Add(time.Second))
			if err != nil || len(again.Repairs) != 0 || !reflect.DeepEqual(again.Record, plan.Record) {
				t.Fatalf("repair not idempotent: %+v err=%v", again, err)
			}
		})
	}
}
