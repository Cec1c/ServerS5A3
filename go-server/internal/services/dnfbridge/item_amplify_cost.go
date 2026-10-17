package dnfbridge

import (
	"fmt"
	"math"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// AmplifyUpgrade.etc initializes its own native table (25e9420 -> 3a7e560).
// It does not inherit reinforcement weights. An absent current-level entry
// skips the final multiplication (3a3d6c5/3a3d8b7 -> 3a3da69).
func nativeAmplifyGoldCost(table *currentUpgradeTable, grade, rarity, kind, level int) (int64, error) {
	if table == nil {
		return 0, fmt.Errorf("amplify cost table unavailable")
	}
	if table.GoldCostMetadataError != nil {
		return 0, table.GoldCostMetadataError
	}
	if grade < 0 || grade >= len(table.Costs) || rarity < 0 || rarity >= len(table.CostWeightsByRarity) || level < 0 {
		return 0, fmt.Errorf("amplify cost metadata outside table")
	}
	index := kind - 12
	if kind == 24 {
		index = 0
	}
	if kind == 25 {
		index = 12
	}
	if index < 0 || index >= len(table.TypeWeights) || kind > 25 {
		return 0, fmt.Errorf("amplify cost equipment kind %d unsupported", kind)
	}
	weights := []float64{table.TypeWeights[index], table.CostWeightsByRarity[rarity]}
	if level > 18 {
		level = 18
	}
	if row, found := table.CostWeightsByUpgradeLevel[level]; found {
		w := row[1]
		if kind == 12 {
			w = row[0]
		}
		weights = append(weights, w)
	}
	cost := int64(table.Costs[grade])
	if cost < 0 || cost > math.MaxInt32 {
		return 0, fmt.Errorf("amplify base cost invalid")
	}
	for _, weight := range weights {
		value := float64(cost)*float64(float32(weight)) + .5
		if weight < 0 || math.IsNaN(value) || math.IsInf(value, 0) || value >= float64(math.MaxInt32)+1 {
			return 0, fmt.Errorf("amplify cost weight or result invalid")
		}
		cost = int64(value)
	}
	return cost, nil
}

// The permissive gameplay parser predates gold quotation. Do not let malformed
// fee tokens look like an intentionally absent native level-weight section.
// This diagnostic is enforced only by the new amplification quote.
func validateAmplifyGoldCostMetadata(doc *dnfpvf.Document) error {
	for _, name := range []string{"cost", "type", "cost weights by rarity", "cost weight by upgrade level"} {
		tokens, found := doc.Section(name)
		if name == "cost weight by upgrade level" && !found {
			continue
		}
		if !found || len(tokens) == 0 {
			return fmt.Errorf("amplify cost section %q missing or empty", name)
		}
		for _, token := range tokens {
			var n float64
			switch token.Kind {
			case dnfpvf.TokenInt:
				n = float64(token.Int)
			case dnfpvf.TokenFloat:
				if name == "cost" {
					return fmt.Errorf("amplify cost must contain integers")
				}
				n = token.Float
			default:
				return fmt.Errorf("amplify cost section %q has nonnumeric token", name)
			}
			if n < 0 || math.IsNaN(n) || math.IsInf(n, 0) || math.IsInf(float64(float32(n)), 0) {
				return fmt.Errorf("amplify cost section %q has invalid value", name)
			}
			if name == "cost" && n > math.MaxInt32 {
				return fmt.Errorf("amplify base cost overflows")
			}
		}
		if name == "cost weight by upgrade level" {
			if len(tokens)%3 != 0 {
				return fmt.Errorf("amplify level weights require triples")
			}
			seen := make(map[int64]bool)
			for i := 0; i < len(tokens); i += 3 {
				token := tokens[i]
				if token.Kind != dnfpvf.TokenInt || token.Int > 255 || seen[token.Int] {
					return fmt.Errorf("amplify level weight key invalid or repeated")
				}
				seen[token.Int] = true
			}
		}
	}
	return nil
}
