package dnfbridge

import (
	"fmt"

	dnfdrops "longheng.io/server/internal/modules/dnf/drop"
)

func currentDungeonConfiguredMaterialItems(catalog *pvfDungeonDropCatalog, rules []dnfdrops.DungeonMaterialRule, rank byte, rng *currentDungeonDropLCG) ([]uint32, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	if rank >= 4 || rng == nil {
		return nil, fmt.Errorf("configured dungeon material: invalid monster rank or random stream")
	}
	// Validate every relevant reference before consuming any random numbers.
	for _, rule := range rules {
		item, err := catalog.ResolveItem(rule.ItemID)
		if err != nil {
			return nil, err
		}
		if normalizeDungeonDropStackableType(item.StackableType) != "[material]" {
			return nil, fmt.Errorf("configured dungeon material %d is not a material", rule.ItemID)
		}
	}
	var items []uint32
	for _, rule := range rules {
		probability := rule.Probability[rank]
		// Disabled ranks leave the existing room random stream intact.
		if probability > 0 && rng.Next(dnfdrops.DungeonMaterialProbabilityBound) < probability {
			items = append(items, rule.ItemID)
		}
	}
	return items, nil
}
