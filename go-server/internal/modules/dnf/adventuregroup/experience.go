package adventuregroup

import (
	"fmt"
	"math/big"
	"strconv"

	"longheng.io/server/internal/modules/dnf/pvf"
)

// ExperienceConfig is the 2018 account progression table. Costs are the EXP
// consumed when entering each level, not cumulative thresholds. Index zero is
// level one, whose cost is zero. The wire carries the current level's remainder.
type ExperienceConfig struct {
	Costs           []uint64
	RateNumerator   uint64
	RateDenominator uint64
}

func parseExperienceConfig(doc *pvf.Document) (ExperienceConfig, error) {
	var out ExperienceConfig
	names := []string{"max adventure level", "adventure exp rate", "adventure exp table"}
	parts := make([][]pvf.Token, len(names))
	found := 0
	for i, name := range names {
		for _, section := range doc.Sections {
			if normalizedPVFName(section.Name) == name {
				if parts[i] != nil {
					return out, fmt.Errorf("%w: duplicate [%s]", ErrTableMalformed, name)
				}
				parts[i] = append([]pvf.Token{}, sectionTokens(doc, section)...)
				found++
			}
		}
	}
	// Legacy documents and narrow synthetic fixtures retain their old calculator.
	// A partially present modern table must never silently fall back to it.
	if found == 0 {
		return out, nil
	}
	if found != 3 || len(parts[0]) != 1 || parts[0][0].Kind != pvf.TokenInt ||
		parts[0][0].Int < 1 || parts[0][0].Int > 1000 || len(parts[1]) != 1 {
		return out, fmt.Errorf("%w: adventure experience header", ErrTableMalformed)
	}
	max := int(parts[0][0].Int)
	if len(parts[2]) < max*2 || len(parts[2])%2 != 0 || len(parts[2]) > 2000 {
		return out, fmt.Errorf("%w: adventure experience levels", ErrTableMalformed)
	}
	rateToken := parts[1][0]
	if rateToken.Kind != pvf.TokenFloat && rateToken.Kind != pvf.TokenInt {
		return out, fmt.Errorf("%w: adventure experience rate", ErrTableMalformed)
	}
	rate, ok := new(big.Rat).SetString(rateToken.Raw)
	if !ok || rate.Sign() <= 0 || !rate.Num().IsUint64() || !rate.Denom().IsUint64() {
		return out, fmt.Errorf("%w: adventure experience rate", ErrTableMalformed)
	}
	out.RateNumerator, out.RateDenominator = rate.Num().Uint64(), rate.Denom().Uint64()
	// Production PVF includes costs above its enabled level cap (30 of 50).
	// Validate every row, but only enable levels up to the configured maximum.
	for i := 0; i < len(parts[2])/2; i++ {
		level, costToken := parts[2][i*2], parts[2][i*2+1]
		if level.Kind != pvf.TokenInt || level.Int != int64(i+1) ||
			(costToken.Kind != pvf.TokenString && costToken.Kind != pvf.TokenInt) {
			return ExperienceConfig{}, fmt.Errorf("%w: adventure experience row %d", ErrTableMalformed, i)
		}
		cost, err := strconv.ParseUint(costToken.Value, 10, 64)
		if err != nil || (i == 0 && cost != 0) || (i > 0 && cost == 0) {
			return ExperienceConfig{}, fmt.Errorf("%w: adventure experience cost %d", ErrTableMalformed, i)
		}
		if i < max {
			out.Costs = append(out.Costs, cost)
		}
	}
	return out, nil
}

func (t *Tables) UsesAccountExperience() bool {
	return t != nil && len(t.runtime.Experience.Costs) != 0
}

// CalculateAccountExperience reads the existing unscaled, committed HonorExp
// account ledger. No new award, conversion write, or inferred historical grant
// is needed. Applying the PVF rate to the cumulative ledger retains fractions
// across small awards and is independent of how kill batches were grouped.
func (t *Tables) CalculateAccountExperience(earned uint64) (Summary, error) {
	if !t.UsesAccountExperience() {
		return Summary{}, ErrTableEmpty
	}
	c := t.runtime.Experience
	if c.RateNumerator == 0 || c.RateDenominator == 0 {
		return Summary{}, ErrTableMalformed
	}
	remaining := new(big.Int).SetUint64(earned)
	remaining.Mul(remaining, new(big.Int).SetUint64(c.RateNumerator))
	remaining.Quo(remaining, new(big.Int).SetUint64(c.RateDenominator))
	level := 1
	for level < len(c.Costs) {
		cost := new(big.Int).SetUint64(c.Costs[level])
		if remaining.Cmp(cost) < 0 {
			break
		}
		remaining.Sub(remaining, cost)
		level++
	}
	progress := uint64(0)
	if level < len(c.Costs) {
		progress = remaining.Uint64() // strictly below the next uint64 cost
	}
	return Summary{ManageLevel: level, TotalPoint: progress, Benefits: t.BenefitsForLevel(level)}, nil
}
