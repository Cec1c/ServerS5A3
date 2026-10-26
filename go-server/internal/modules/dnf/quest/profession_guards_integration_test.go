package quest

import (
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestIntegratedProfessionGuardsKeepActiveTasksVisible(t *testing.T) {
	catalog := &Catalog{}
	cases := []struct {
		name       string
		reward     string
		marker     int
		grow       int
		acceptable bool
	}{
		{"new class change", "grow type", 1, 0, true},
		{"repeated class change", "grow type", 1, 1, false},
		{"first awakening", "awakening type", 2, 1, true},
		{"repeated awakening", "awakening type", 2, 0x11, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			definition := Definition{ID: 1, ExposedByNPC: 1, Grade: "normal", LevelMin: 1, LevelMax: 90,
				JobChangeQuest: tc.marker, RewardType: tc.reward, RewardIntData: []int64{1}}
			character := CharacterEligibility{Level: 50, Job: 0, GrowType: tc.grow}
			if got := catalog.definitionAcceptable(definition, character, dnfrepo.QuestRecord{}, nil, nil, false); got != tc.acceptable {
				t.Fatalf("new acceptance=%v, want %v", got, tc.acceptable)
			}
			active := map[int64]struct{}{1: {}}
			if !catalog.definitionAcceptable(definition, character, dnfrepo.QuestRecord{}, nil, active, true) {
				t.Fatal("existing task was hidden by integrated profession guards")
			}
			if catalog.definitionAcceptable(definition, character, dnfrepo.QuestRecord{}, nil, active, false) {
				t.Fatal("active task was offered for fresh acceptance")
			}
		})
	}
}
