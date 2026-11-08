package adventuregroup

import (
	"math"
	"testing"
)

func TestCombinedExperienceRetainsRosterLevelAndFraction(t *testing.T) {
	table := &Tables{
		pointRanges:      []pointRange{{minLevel: 1, maxLevel: 90, point: 10}},
		manageThresholds: []uint64{10, 40, 100, 200, 500, 1000}, manageLevelMax: 6,
		optionByLevel: map[int]uint64{3: 15, 4: 20},
		runtime:       RuntimeConfig{Experience: ExperienceConfig{Costs: []uint64{0, 100, 200, 300, 400, 500}, RateNumerator: 3, RateDenominator: 10}},
	}
	for _, tc := range []struct {
		name     string
		rows     []Character
		earned   uint64
		level    int
		progress uint64
	}{
		{"empty", nil, 0, 1, 0},
		{"roster only", []Character{{Level: 10}}, 0, 3, 0},
		{"roster fraction", []Character{{Level: 10}, {Level: 5}}, 0, 3, 150},
		{"both", []Character{{Level: 10}, {Level: 5}}, 337, 3, 251},
		{"cross boundary", []Character{{Level: 10}, {Level: 5}}, 500, 4, 0},
		{"more than one level", []Character{{Level: 10}}, 2400, 5, 20},
		{"deleted", []Character{{Level: 10}, {Level: 90, Deleted: true}}, 337, 3, 101},
		{"saturate", []Character{{Level: 10}}, math.MaxUint64, 6, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := table.CalculateCombinedExperience(tc.rows, tc.earned)
			if err != nil || got.ManageLevel != tc.level || got.TotalPoint != tc.progress {
				t.Fatalf("got=%+v err=%v want=%d/%d", got, err, tc.level, tc.progress)
			}
			if got.Benefits != table.BenefitsForLevel(tc.level) {
				t.Fatal("benefits do not match combined level")
			}
		})
	}
	// Character points and earned EXP are different units. Preserve the existing
	// roster level and its fraction, then apply the modern EXP rate once.
	for level := 1; level <= 90; level++ {
		rows := []Character{{Level: level}}
		legacy, err := table.Calculate(rows)
		if err != nil {
			t.Fatal(err)
		}
		got, err := table.CalculateCombinedExperience(rows, 0)
		if err != nil {
			t.Fatal(err)
		}
		want := legacy.ManageLevel
		if want < 1 {
			want = 1
		}
		if got.ManageLevel != want {
			t.Fatalf("character level=%d legacy=%d combined=%d", level, want, got.ManageLevel)
		}
	}
}

func TestCombinedExperienceLegacyFallbackAndWideArithmetic(t *testing.T) {
	table := &Tables{pointRanges: []pointRange{{minLevel: 1, maxLevel: 1, point: math.MaxUint64 - 1}}, manageThresholds: []uint64{1, math.MaxUint64}, manageLevelMax: 2}
	rows := []Character{{Level: 1}}
	want, _ := table.Calculate(rows)
	got, err := table.CalculateCombinedExperience(rows, math.MaxUint64)
	if err != nil || got != want {
		t.Fatal("legacy table changed", got, err)
	}
	table.runtime.Experience = ExperienceConfig{Costs: []uint64{0, math.MaxUint64}, RateNumerator: 1, RateDenominator: 1}
	got, err = table.CalculateCombinedExperience(rows, 0)
	if err != nil || got.ManageLevel != 1 || got.TotalPoint != math.MaxUint64-2 {
		t.Fatal("wide interpolation overflow", got, err)
	}
	got, err = table.CalculateCombinedExperience(rows, 2)
	if err != nil || got.ManageLevel != 2 || got.TotalPoint != 0 {
		t.Fatal("wide crossing overflow", got, err)
	}
	if _, err := table.CalculateCombinedExperience([]Character{{Level: 0}}, 0); err == nil {
		t.Fatal("invalid roster accepted")
	}
}
