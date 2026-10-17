package expertjob

import "math"

// PlanEnchanterBeads keeps one batch on its starting qualification/rate, as
// ordinary expert-job compound does. Every card has an independent roll;
// experience is credited once after the batch completes.
func (c *Catalog) PlanEnchanterBeads(experience, recipeID, cardID int64, count int, rng RandomFunc) (CompoundPlan, error) {
	if count <= 0 || count > math.MaxUint16 {
		return CompoundPlan{}, ErrRecipeUnavailable
	}
	var plan CompoundPlan
	var beadID int64
	for i := 0; i < count; i++ {
		bead, err := c.PlanEnchanterBead(experience, recipeID, cardID, rng)
		if err != nil {
			return CompoundPlan{}, err
		}
		if i == 0 {
			beadID = bead.BeadItemID
			plan.AttemptedOutputs = []RecipeEntry{{ItemID: beadID, Count: int64(count)}}
			for _, m := range bead.Recipe.Materials {
				if m.ItemID <= 0 || m.Count <= 0 || m.Count > math.MaxInt64/int64(count) {
					return CompoundPlan{}, ErrRecipeUnavailable
				}
				plan.Materials = append(plan.Materials, RecipeEntry{ItemID: m.ItemID, Count: m.Count * int64(count)})
			}
		}
		if bead.Success {
			plan.SuccessCount++
			plan.ExperienceGain = saturatingAdd(plan.ExperienceGain, bead.ExperienceGain)
		}
	}
	plan.FailureCount = count - plan.SuccessCount
	if plan.SuccessCount > 0 {
		plan.Rewards = []RecipeEntry{{ItemID: beadID, Count: int64(plan.SuccessCount)}}
	}
	plan.FinalExperience = saturatingAdd(experience, plan.ExperienceGain)
	config, _ := c.Enchanter()
	plan.LevelChanged = config.Recipes.Level(experience) != config.Recipes.Level(plan.FinalExperience)
	return plan, nil
}
