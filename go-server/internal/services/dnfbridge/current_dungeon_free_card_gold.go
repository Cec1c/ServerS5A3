package dnfbridge

import (
	"errors"
	"fmt"
	"math"
)

const (
	currentDungeonFreeCardGoldMaxAmount     int64 = 1<<31 - 1
	currentDungeonFreeCardGoldBaseScale           = 175.0
	currentDungeonFreeCardGoldBaseScaleUnit       = 1000.0
	currentDungeonFreeCardGoldPartyDivisor        = 2.0
)

var errCurrentDungeonFreeCardGoldInputInvalid = errors.New("current dungeon free card gold input is invalid")

// currentDungeonFreeCardGoldAmount calculates the ordinary free-card gold
// reward from already-resolved ledger counts and PVF rates. All ratio work is
// completed in floating point, and the formula is truncated only after the
// full proportional calculation. The pre-variance amount and the final random
// adjustment are both clamped to a non-negative int32 value.
func currentDungeonFreeCardGoldAmount(
	rng *currentDungeonDropLCG,
	reference currentDungeonGoldReference,
	normal, champion, boss int64,
	difficultyRate, partyRate, rankBonus float64,
	partyCount int,
) (int64, error) {
	if normal < 0 || champion < 0 || boss < 0 {
		return 0, fmt.Errorf("%w: negative kill count normal=%d champion=%d boss=%d",
			errCurrentDungeonFreeCardGoldInputInvalid, normal, champion, boss)
	}
	if partyCount <= 0 {
		return 0, fmt.Errorf("%w: party count=%d", errCurrentDungeonFreeCardGoldInputInvalid, partyCount)
	}
	if reference.Base <= 0 {
		return 0, fmt.Errorf("%w: base=%d", errCurrentDungeonFreeCardGoldInputInvalid, reference.Base)
	}
	if reference.VariancePct < 0 || reference.VariancePct > 100 {
		return 0, fmt.Errorf("%w: variance pct=%d", errCurrentDungeonFreeCardGoldInputInvalid, reference.VariancePct)
	}
	if !finitePositiveCurrentDungeonFreeCardGoldRate(difficultyRate) {
		return 0, fmt.Errorf("%w: difficulty rate=%v", errCurrentDungeonFreeCardGoldInputInvalid, difficultyRate)
	}
	if !finitePositiveCurrentDungeonFreeCardGoldRate(partyRate) {
		return 0, fmt.Errorf("%w: party rate=%v", errCurrentDungeonFreeCardGoldInputInvalid, partyRate)
	}
	if math.IsNaN(rankBonus) || math.IsInf(rankBonus, 0) || rankBonus < 0 {
		return 0, fmt.Errorf("%w: rank bonus=%v", errCurrentDungeonFreeCardGoldInputInvalid, rankBonus)
	}

	weightedKills, err := currentDungeonFreeCardWeightedKills(normal, champion, boss)
	if err != nil {
		return 0, err
	}
	if weightedKills == 0 {
		return 0, nil
	}

	// Keep every proportional factor in the double-precision expression. In
	// particular, do not truncate base*175/1000 or weightedKills/2 first.
	exactAmount := math.Trunc(
		(float64(reference.Base) * currentDungeonFreeCardGoldBaseScale / currentDungeonFreeCardGoldBaseScaleUnit) *
			(float64(weightedKills) / currentDungeonFreeCardGoldPartyDivisor) *
			difficultyRate * partyRate * (1 + rankBonus) / float64(partyCount),
	)
	if math.IsNaN(exactAmount) || math.IsInf(exactAmount, -1) || exactAmount < 0 {
		return 0, fmt.Errorf("%w: proportional result=%v", errCurrentDungeonFreeCardGoldInputInvalid, exactAmount)
	}
	amount := clampCurrentDungeonFreeCardGold(exactAmount)

	if amount == 0 || reference.VariancePct == 0 {
		return amount, nil
	}
	if rng == nil {
		return 0, fmt.Errorf("%w: nil rng for variance pct=%d", errCurrentDungeonFreeCardGoldInputInvalid, reference.VariancePct)
	}

	randomRange := reference.VariancePct*2 + 1
	deltaPct := int64(rng.Next(int(randomRange))) - reference.VariancePct
	// amount is non-negative and bounded by MaxInt32, so this multiplication
	// cannot overflow int64. Integer division truncates toward zero as required.
	amount = amount * (100 + deltaPct) / 100
	if amount < 0 {
		amount = 0
	}
	if amount > currentDungeonFreeCardGoldMaxAmount {
		amount = currentDungeonFreeCardGoldMaxAmount
	}
	return amount, nil
}

func finitePositiveCurrentDungeonFreeCardGoldRate(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func currentDungeonFreeCardWeightedKills(normal, champion, boss int64) (int64, error) {
	if champion > math.MaxInt64/2 {
		return 0, fmt.Errorf("%w: champion weight overflows int64: %d", errCurrentDungeonFreeCardGoldInputInvalid, champion)
	}
	championWeight := champion * 2
	if boss > math.MaxInt64/4 {
		return 0, fmt.Errorf("%w: boss weight overflows int64: %d", errCurrentDungeonFreeCardGoldInputInvalid, boss)
	}
	bossWeight := boss * 4

	weighted := normal
	if weighted > math.MaxInt64-championWeight {
		return 0, fmt.Errorf("%w: normal+champion weight overflows int64", errCurrentDungeonFreeCardGoldInputInvalid)
	}
	weighted += championWeight
	if weighted > math.MaxInt64-bossWeight {
		return 0, fmt.Errorf("%w: total kill weight overflows int64", errCurrentDungeonFreeCardGoldInputInvalid)
	}
	return weighted + bossWeight, nil
}

func clampCurrentDungeonFreeCardGold(value float64) int64 {
	if value <= 0 {
		return 0
	}
	if math.IsInf(value, 1) || value >= float64(currentDungeonFreeCardGoldMaxAmount) {
		return currentDungeonFreeCardGoldMaxAmount
	}
	return int64(value)
}
