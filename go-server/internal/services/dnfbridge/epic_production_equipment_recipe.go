package dnfbridge

import (
	cryptorand "crypto/rand"
	"fmt"
	"math"
	"math/big"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

func currentEpicProductionRecipeTag(value string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), "` []"))
}

func validateCurrentEpicProductionEquipmentRecipeMaterial(source initialEquipmentTextSource, definition dungeonDropItemDefinition, recipe currentEpicProductionCatalyst, material currentEpicProductionCompoundMaterial) error {
	// Only admit the complete equipment predicate form. Incomplete/unknown
	// type-only recipes must not become an unrestricted material wildcard.
	if currentEpicProductionRecipeTag(recipe.NeedItemType) != "equipment" ||
		recipe.NeedItemParts == "" || recipe.NeedItemGrade == "" ||
		recipe.NeedItemMinLevel == 0 || recipe.NeedItemMaxLevel < recipe.NeedItemMinLevel ||
		recipe.NeedCount != 1 || recipe.GroupKey != 0 || recipe.IsMaterialPoint ||
		definition.Kind != dungeonDropItemEquipment || material.Count != 1 {
		return fmt.Errorf("%w: incomplete equipment recipe or material", errCurrentEpicProductionMaterial)
	}
	text, err := source.ReadText(definition.PVFPath)
	if err != nil {
		return fmt.Errorf("%w: read equipment predicate: %v", errCurrentEpicProductionMaterial, err)
	}
	doc, err := dnfpvf.Parse(definition.PVFPath, text)
	if err != nil {
		return fmt.Errorf("%w: parse equipment predicate: %v", errCurrentEpicProductionMaterial, err)
	}
	level, hasLevel := doc.Int("minimum level")
	rarity, hasRarity := doc.Int("rarity")
	if !hasLevel || !hasRarity || level < int64(recipe.NeedItemMinLevel) || level > int64(recipe.NeedItemMaxLevel) ||
		currentEpicProductionRecipeTag(definition.EquipmentType) != currentEpicProductionRecipeTag(recipe.NeedItemParts) ||
		currentUpgradeRarityName(int(rarity)) != currentEpicProductionRecipeTag(recipe.NeedItemGrade) {
		return fmt.Errorf("%w: equipment does not match recipe predicates", errCurrentEpicProductionMaterial)
	}
	// The matching PVF describes sealed OR resealable weapons. This is the
	// template's sealing capability, not the instance's current sealed byte;
	// equipping and then removing a resealable weapon must not disqualify it.
	if recipe.IsSeal && currentEpicProductionRecipeTag(definition.AttachType) != "sealing" {
		return fmt.Errorf("%w: equipment is not sealable", errCurrentEpicProductionMaterial)
	}
	return nil
}

func currentEpicProductionRandomBelow(limit uint32) (uint32, error) {
	if limit == 0 {
		return 0, errCurrentEpicProductionInvalid
	}
	n, err := cryptorand.Int(cryptorand.Reader, new(big.Int).SetUint64(uint64(limit)))
	if err != nil {
		return 0, err
	}
	return uint32(n.Uint64()), nil
}

func currentEpicProductionCompoundOutputCount(plan currentEpicProductionCompoundPlan, units uint64, draw func(uint32) (uint32, error)) (uint32, error) {
	min, max := plan.Recipe.MinMakeCount, plan.Recipe.MaxMakeCount
	if units == 0 || min == 0 || max < min || units > math.MaxUint32/uint64(max) {
		return 0, fmt.Errorf("%w: compound output range", errCurrentEpicProductionMaterial)
	}
	output := units * uint64(min)
	if max == min {
		return uint32(output), nil
	}
	rate, scale := plan.Recipe.MaxItemCountRate, plan.MaxItemCountMaxRate
	// Variable-yield equipment requests contain at most 64 unique items.
	// Reject unsupported ranges before entering the bounded per-unit roll.
	if units > 64 || scale == 0 || rate > scale {
		return 0, fmt.Errorf("%w: compound output probability", errCurrentEpicProductionMaterial)
	}
	for unit := uint64(0); unit < units; unit++ {
		bonus := rate == scale
		if rate != 0 && rate != scale {
			if draw == nil {
				return 0, errCurrentEpicProductionInvalid
			}
			roll, err := draw(scale)
			if err != nil {
				return 0, err
			}
			if roll >= scale {
				return 0, errCurrentEpicProductionInvalid
			}
			bonus = roll < rate
		}
		if bonus {
			output += uint64(max - min)
		}
	}
	return uint32(output), nil
}
