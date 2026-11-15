package expertjob

import "math"

// groupDisjointerTierBonus is the private server's level 8-11 balance policy.
// Apply it to the individually rounded PVF result, before a batch is merged.
// It neither rewrites cached PVF rules nor draws additional random numbers.
func groupDisjointerTierBonus(config *DisjointerConfig, machineGrade, equipmentGrade int, warriorsDungeon bool, rule DisjointerResultRule, base int64) (int64, int, error) {
	chance := max(0, min(100, rule.BigWinChancePercent))
	if machineGrade < 8 || machineGrade > 11 {
		return base, chance, nil
	}
	step := machineGrade - 7
	if rule.ItemID == 3037 { // Clear cubes only; souls and other materials are independent.
		if base < 1 || base > math.MaxUint32 {
			return 0, 0, ErrExtractionInvalid
		}
		// base <= uint32, so multiplying by at most 120 is safe in int64.
		base = base * int64(100+step*5) / 100
		if base > math.MaxUint32 {
			return 0, 0, ErrExtractionInvalid
		}
	}
	if chance > 0 && !warriorsDungeon {
		for _, selected := range config.BigWinResults[rule.BigWinTable] {
			if equipmentGrade >= selected.MinimumGrade && equipmentGrade <= selected.MaximumGrade && selected.ItemID > 0 && selected.Weight > 0 {
				chance = min(100, chance+step)
				break
			}
		}
	}
	return base, chance, nil
}
