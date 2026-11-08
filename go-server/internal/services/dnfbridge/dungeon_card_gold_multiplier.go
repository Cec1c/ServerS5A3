package dnfbridge

import (
	"fmt"
	"math"
)

const currentDungeonCardFinalGoldMultiplier int64 = 3

// Apply to the fully resolved bundle before it is frozen or shared. Replaying
// a selection uses that same plan and never applies the multiplier again.
// Cost and item rewards have separate contracts and are not multiplied.
func applyCurrentDungeonCardFinalGoldMultiplier(bundle *dungeonCardRewardBundle) error {
	if bundle == nil || bundle.Gold < 0 || bundle.Gold > math.MaxInt64/currentDungeonCardFinalGoldMultiplier {
		return fmt.Errorf("%w: final card gold multiplier overflow", errDungeonCardPlanInvalid)
	}
	bundle.Gold *= currentDungeonCardFinalGoldMultiplier
	return nil
}
