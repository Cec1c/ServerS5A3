package expertjob

import (
	"errors"
	"fmt"
	"testing"
)

func TestEnchanterLegendaryProbabilityAndInvalidRarities(t *testing.T) {
	for _, rarity := range []int{-1, 0, 1, 2, 3, 4, 5, 6, 7, 255} {
		t.Run(fmt.Sprintf("rarity%d", rarity), func(t *testing.T) {
			c := beadBatchCatalog()
			recipe := c.enchanter.CardRecipes[100]
			recipe.Qualification = rarity
			c.enchanter.CardRecipes[100] = recipe
			c.enchanter.Cards[200] = EnchanterCard{Qualification: rarity}
			c.enchanter.CardExperienceByLevel[1] = EnchanterCardExperienceRule{
				Level: 1, SuccessRates: [5]int{10, 20, 30, 40, 50}, ExtraRate: 90,
				MinimumExperienceGain: 1, MaximumExperienceGain: 1,
			}
			valid := rarity >= 0 && rarity <= 4 || rarity == 6
			rate := (rarity + 1) * 10
			if rarity == 6 {
				rate = 90
			}
			for roll := 0; roll < 100; roll++ {
				rng := func(int) int { return roll }
				bead, err := c.PlanEnchanterBead(0, 100, 200, rng)
				store, storeErr := c.PlanEnchanterStore(0, 100, 200, 1, rng)
				if !valid {
					if !errors.Is(err, ErrRecipeUnavailable) || !errors.Is(storeErr, ErrRecipeUnavailable) {
						t.Fatalf("invalid rarity accepted: %v %v", err, storeErr)
					}
				} else if err != nil || storeErr != nil || bead.Success != (roll < rate) || store.Success != (roll < rate) {
					t.Fatalf("roll=%d rate=%d bead=%+v err=%v store=%+v err=%v", roll, rate, bead, err, store, storeErr)
				}
			}
		})
	}
}
