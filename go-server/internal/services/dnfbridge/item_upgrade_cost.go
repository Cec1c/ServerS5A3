package dnfbridge

import (
	"fmt"
	"math"
)

// nativeUpgradeGoldCost follows the v101 calculator's integer boundaries:
// base[level] -> type -> rarity -> current upgrade level. Each multiplication
// uses a stored float32 weight and truncates after adding 0.5. Multiplying all
// weights first loses the intermediate rounding and can change payment.
// Native equipment kinds are 12..25; kind 24 aliases the weapon weight.
func nativeUpgradeGoldCost(table *currentUpgradeTable, equipmentLevel, rarity, equipmentKind, upgradeLevel int) (int64, error) {
	if table == nil || equipmentLevel < 0 || equipmentLevel >= len(table.Costs) || rarity < 0 || rarity >= len(table.CostWeightsByRarity) || upgradeLevel < 0 {
		return 0, fmt.Errorf("upgrade cost metadata outside table")
	}
	typeIndex := equipmentKind - 12
	if equipmentKind == 24 {
		typeIndex = 0
	}
	if equipmentKind == 25 {
		typeIndex = 12
	}
	if typeIndex < 0 || typeIndex >= len(table.TypeWeights) || equipmentKind > 25 {
		return 0, fmt.Errorf("upgrade cost equipment kind %d unsupported", equipmentKind)
	}
	if upgradeLevel > 18 {
		upgradeLevel = 18
	}
	levelWeights, ok := table.CostWeightsByUpgradeLevel[upgradeLevel]
	if !ok {
		return 0, fmt.Errorf("upgrade cost level %d missing", upgradeLevel)
	}
	levelWeight := levelWeights[1]
	if equipmentKind == 12 {
		levelWeight = levelWeights[0]
	}
	cost := int64(table.Costs[equipmentLevel])
	if cost < 0 {
		return 0, fmt.Errorf("negative upgrade base cost")
	}
	for _, weight := range []float64{table.TypeWeights[typeIndex], table.CostWeightsByRarity[rarity], levelWeight} {
		value := float64(cost)*float64(float32(weight)) + 0.5
		if weight < 0 || math.IsNaN(value) || math.IsInf(value, 0) || value >= float64(math.MaxInt32)+1 {
			return 0, fmt.Errorf("upgrade cost weight or result invalid")
		}
		cost = int64(value)
	}
	return cost, nil
}
