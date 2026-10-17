package expertjob

import (
	"math"
	"reflect"
	"testing"
)

func beadBatchCatalog() *Catalog {
	recipes := &Config{JobType: EnchanterType, ExperienceThresholds: []int64{5, 20}, Recipes: map[int64]Recipe{}}
	return &Catalog{configs: map[byte]*Config{EnchanterType: recipes}, enchanter: &EnchanterConfig{
		Recipes:     recipes,
		CardRecipes: map[int64]EnchanterCardRecipe{100: {Qualification: 1, RecipeItemID: 100, RequiredLevel: 1, Materials: []RecipeEntry{{ItemID: 300, Count: 2}}}},
		Cards:       map[int64]EnchanterCard{200: {Qualification: 1}}, BeadByCard: map[int64]int64{200: 400},
		CardExperienceByLevel: map[int]EnchanterCardExperienceRule{
			1: {Level: 1, SuccessRates: [5]int{100, 50}, MinimumExperienceGain: 6, MaximumExperienceGain: 6},
			2: {Level: 2, SuccessRates: [5]int{100, 100}, MinimumExperienceGain: 1, MaximumExperienceGain: 1},
		},
	}}
}

func TestEnchanterBeadBatchIndependentRollsAndOneExperienceCommit(t *testing.T) {
	c := beadBatchCatalog()
	rolls := []int{49, 50, 0}
	n := 0
	plan, err := c.PlanEnchanterBeads(0, 100, 200, 3, func(limit int) int {
		if limit != 100 {
			t.Fatal(limit)
		}
		r := rolls[n]
		n++
		return r
	})
	if err != nil || n != 3 || plan.SuccessCount != 2 || plan.FailureCount != 1 || plan.FinalExperience != 12 || plan.ExperienceGain != 12 || !plan.LevelChanged || plan.GoldCost != 0 || !reflect.DeepEqual(plan.Materials, []RecipeEntry{{ItemID: 300, Count: 6}}) || !reflect.DeepEqual(plan.AttemptedOutputs, []RecipeEntry{{ItemID: 400, Count: 3}}) || !reflect.DeepEqual(plan.Rewards, []RecipeEntry{{ItemID: 400, Count: 2}}) {
		t.Fatalf("plan=%+v calls=%d err=%v", plan, n, err)
	}
}

func TestEnchanterBeadBatchAllFailuresStillConsumesAttempts(t *testing.T) {
	plan, err := beadBatchCatalog().PlanEnchanterBeads(0, 100, 200, 4, func(int) int { return 99 })
	if err != nil || plan.SuccessCount != 0 || plan.FailureCount != 4 || plan.ExperienceGain != 0 || plan.FinalExperience != 0 || plan.LevelChanged || len(plan.Rewards) != 0 || plan.Materials[0].Count != 8 || plan.AttemptedOutputs[0].Count != 4 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

func TestEnchanterBeadBatchValidationAndMaximum(t *testing.T) {
	c := beadBatchCatalog()
	for _, count := range []int{-1, 0, 65536} {
		if _, err := c.PlanEnchanterBeads(0, 100, 200, count, nil); err == nil {
			t.Fatal(count)
		}
	}
	for _, pair := range [][2]int64{{101, 200}, {100, 201}} {
		if _, err := c.PlanEnchanterBeads(0, pair[0], pair[1], 1, nil); err == nil {
			t.Fatal(pair)
		}
	}
	p, err := c.PlanEnchanterBeads(0, 100, 200, 65535, func(int) int { return 99 })
	if err != nil || p.FailureCount != 65535 || p.Materials[0].Count != 131070 {
		t.Fatalf("max=%+v %v", p, err)
	}
	r := c.enchanter.CardRecipes[100]
	r.Materials[0].Count = math.MaxInt64
	c.enchanter.CardRecipes[100] = r
	if _, err := c.PlanEnchanterBeads(0, 100, 200, 2, func(int) int { return 99 }); err == nil {
		t.Fatal("material multiplication overflow accepted")
	}
}
