package expertjob

import (
	"errors"
	"fmt"
	"testing"
)

func groupTierTestCatalog(grade, chance int) *Catalog {
	return &Catalog{disjointer: &DisjointerConfig{
		BaseConst: 150,
		Results: map[disjointerResultKey]DisjointerResultRule{
			{grade - 1, 2, 0}: {ItemID: 3037, Multiplier: 1, AdditionalTable: 1, BigWinTable: 2, BigWinChancePercent: chance},
		},
		AdditionalResults: map[int][]DisjointerSelectionRule{1: {{MinimumGrade: 1, MaximumGrade: 99, ItemID: 3166, Weight: 1, CountDivisor: 10}}},
		BigWinResults:     map[int][]DisjointerSelectionRule{2: {{MinimumGrade: 1, MaximumGrade: 99, ItemID: 3262, Weight: 1, CountDivisor: 1}}},
		SelfServiceChance: 80, SelfServiceItemID: 2610045, SelfServiceItemCount: 1,
		EnduranceReduceMin: 2, EnduranceReduceMax: 2,
		ExperienceGainMin: 1, ExperienceGainMax: 1,
	}}
}

func groupTierTestEquipment() EquipmentMetadata {
	return EquipmentMetadata{Rarity: 2, Grade: 90, Level: 90, SellGold: 15000}
}

func groupTierTestRandom(roll int) RandomFunc {
	return func(n int) int {
		if n == 10000 {
			return roll
		}
		return 0
	}
}

func TestGroupDisjointerTierCubeAndCriticalBoundaries(t *testing.T) {
	counts := []int64{110, 110, 110, 110, 110, 110, 110, 115, 121, 126, 132}
	chances := []int{9, 9, 9, 9, 9, 9, 9, 10, 11, 12, 13}
	for grade := 1; grade <= 11; grade++ {
		for _, self := range []bool{false, true} {
			for _, critical := range []bool{false, true} {
				t.Run(fmt.Sprintf("grade%d/self%t/critical%t", grade, self, critical), func(t *testing.T) {
					roll := chances[grade-1] * 100
					if critical {
						roll--
					}
					plan, err := groupTierTestCatalog(grade, 9).PlanDisjointer(500, grade, groupTierTestEquipment(), self, groupTierTestRandom(roll))
					if err != nil {
						t.Fatal(err)
					}
					if got := disjointerQuantityMaterialCount(plan.Materials, 3037); got != counts[grade-1] {
						t.Fatalf("clear cubes = %d, want %d", got, counts[grade-1])
					}
					material, count, excluded := int64(3166), int64(9), int64(3262)
					if critical {
						material, count, excluded = 3262, 90, 3166
					}
					if disjointerQuantityMaterialCount(plan.Materials, material) != count || disjointerQuantityMaterialCount(plan.Materials, excluded) != 0 {
						t.Fatalf("normal/critical rewards must replace, without quantity scaling: %+v", plan)
					}
					parts := int64(0)
					if self {
						parts = 1
					}
					if disjointerQuantityMaterialCount(plan.Materials, 2610045) != parts || plan.EnduranceReduction != 2 || plan.ExperienceGain != 1 || plan.FinalExperience != 501 {
						t.Fatalf("unrelated rewards or machine state changed: %+v", plan)
					}
				})
			}
		}
	}
}

func TestGroupDisjointerTierKeepsDisabledCriticalAndAiken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		chance  int
		aiken   bool
		missing bool
	}{
		{name: "disabled", chance: 0},
		{name: "aiken", chance: 9, aiken: true},
		{name: "aiken forced critical", chance: 100, aiken: true},
		{name: "no eligible big-win row", chance: 9, missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := groupTierTestCatalog(11, tc.chance)
			equipment := groupTierTestEquipment()
			equipment.WarriorsDungeon = tc.aiken
			roll := 0
			if tc.missing {
				catalog.disjointer.BigWinResults[2][0].MinimumGrade = 91
				roll = 900 // The original 9% misses; absent rewards must not gain 4%.
			}
			plan, err := catalog.PlanDisjointer(0, 11, equipment, false, groupTierTestRandom(roll))
			if err != nil {
				t.Fatal(err)
			}
			if disjointerQuantityMaterialCount(plan.Materials, 3037) != 132 || disjointerQuantityMaterialCount(plan.Materials, 3166) != 9 || disjointerQuantityMaterialCount(plan.Materials, 3262) != 0 {
				t.Fatalf("disabled/Aiken critical changed: %+v", plan)
			}
		})
	}
}

func TestGroupDisjointerTierRoundsPerEquipmentAndOnlyScalesClearCubes(t *testing.T) {
	catalog := groupTierTestCatalog(8, 0)
	catalog.disjointer.BaseConst = 100
	rule := catalog.disjointer.Results[disjointerResultKey{7, 2, 0}]
	rule.Multiplier = 3.5
	catalog.disjointer.Results[disjointerResultKey{7, 2, 0}] = rule
	equipment := groupTierTestEquipment()
	equipment.SellGold = 101 // floor(111*3.5/100)=3; then floor(3*105/100)=3, not 4.
	var total int64
	for i := 0; i < 20; i++ {
		plan, err := catalog.PlanDisjointer(0, 8, equipment, false, groupTierTestRandom(9999))
		if err != nil {
			t.Fatal(err)
		}
		total += disjointerQuantityMaterialCount(plan.Materials, 3037)
	}
	if total != 60 {
		t.Fatalf("twenty individually rounded items = %d, want 60", total)
	}
	catalog = groupTierTestCatalog(11, 0)
	rule = catalog.disjointer.Results[disjointerResultKey{10, 2, 0}]
	rule.ItemID = 7777
	catalog.disjointer.Results[disjointerResultKey{10, 2, 0}] = rule
	plan, err := catalog.PlanDisjointer(0, 11, groupTierTestEquipment(), false, groupTierTestRandom(9999))
	if err != nil || disjointerQuantityMaterialCount(plan.Materials, 7777) != 110 {
		t.Fatalf("non-clear primary reward changed: %+v err=%v", plan, err)
	}
}

func TestGroupDisjointerTierRejectsPostBonusOverflow(t *testing.T) {
	catalog := groupTierTestCatalog(11, 0)
	equipment := groupTierTestEquipment()
	equipment.SellGold = 550_000_000_000 // Baseline 4,033,333,333 fits uint32; 120% does not.
	if _, err := catalog.PlanDisjointer(0, 11, equipment, false, groupTierTestRandom(9999)); !errors.Is(err, ErrExtractionInvalid) {
		t.Fatalf("overflow error = %v, want %v", err, ErrExtractionInvalid)
	}
	// Clamp the bonus to 100%; valid always-critical rows remain always critical.
	catalog = groupTierTestCatalog(11, 99)
	plan, err := catalog.PlanDisjointer(0, 11, groupTierTestEquipment(), false, groupTierTestRandom(9999))
	if err != nil || disjointerQuantityMaterialCount(plan.Materials, 3262) != 90 {
		t.Fatalf("capped critical failed: %+v err=%v", plan, err)
	}
}
