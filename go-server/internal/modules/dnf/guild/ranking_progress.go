package guild

import (
	"math"
	"time"
)

// RankingDungeonPoints combines the highest unlocked conquest tier and the
// sum of each dungeon's purification percentage. Conquest rows are alternatives,
// including the two-predecessor branch; they are not cumulative awards.
func RankingDungeonPoints(g *Guild, policy *DungeonPolicy, rules *RankingRules, catalog *DungeonCatalog, now time.Time) (float32, error) {
	if g == nil || rules == nil || catalog == nil || catalog.Rules == nil || now.IsZero() {
		return 0, ErrRankingRules
	}
	cycle, err := policy.Cycle(now)
	if err != nil {
		return 0, err
	}
	counts, err := DungeonCounts(g, cycle)
	if err != nil {
		return 0, err
	}
	ratios := make(map[uint32]uint32, len(catalog.Rules.byID))
	var purify float64
	for id := range catalog.Rules.byID {
		ratio, err := catalog.Rules.PurificationRatio(id, counts[id])
		if err != nil {
			return 0, err
		}
		ratios[id] = ratio
		purify += float64(ratio) * rules.Dungeon.Rate
	}
	conquest := rules.MinimumConquestPoint
	for _, row := range rules.Conquests {
		first, a := catalog.Rules.byID[row.First]
		second, b := catalog.Rules.byID[row.Second]
		if !a || !b {
			return 0, ErrRankingRules
		}
		if ratios[row.First] >= first.threshold && ratios[row.Second] >= second.threshold {
			conquest = math.Max(conquest, row.Points)
		}
	}
	return float32(math.Min(rules.Dungeon.Maximum, conquest+purify)), nil
}

// RankingBenefitPoints counts the original gameplay benefits, once per kind.
// Capacity expansion and the later gold contract are not welfare icon slots.
// Support soldiers (kind 2) have no entitlement producer and receive no points.
func RankingBenefitPoints(g *Guild, rules *RankingRules, now time.Time) (float32, error) {
	if g == nil || rules == nil || now.IsZero() {
		return 0, ErrRankingRules
	}
	var count int
	for _, kind := range []byte{1, 3, 5, 7} {
		if b, ok := g.Benefits[kind]; ok && b.Type == kind && b.Active(now) {
			count++
		}
	}
	if g.Cargo != nil {
		if err := ValidateCargo(g.Cargo); err != nil {
			return 0, err
		}
		if g.Cargo.Capacity > 0 {
			count++
		}
	}
	return float32(math.Min(rules.Contents.Maximum, float64(count)*rules.Contents.Rate)), nil
}

func CalculateRankingPeriod(reg Registry, facts map[string]RankingMemberFacts, rules *RankingRules, catalog *DungeonCatalog, now time.Time) (*RankingPeriod, error) {
	factors := make(map[uint32]RankingFactors, len(reg.Guilds))
	for id, g := range reg.Guilds {
		f, err := RankingMemberFactors(g, facts, rules, now)
		if err != nil {
			return nil, err
		}
		if f.Dungeon, err = RankingDungeonPoints(g, reg.DungeonPolicy, rules, catalog, now); err != nil {
			return nil, err
		}
		if f.Contents, err = RankingBenefitPoints(g, rules, now); err != nil {
			return nil, err
		}
		if f.PartyClear, err = RankingPartyPoints(g, now); err != nil {
			return nil, err
		}
		// Event ID 0 is inactive. War points await their actual battle owner;
		// essence income and guardian ownership do not represent a war victory.
		factors[id] = f
	}
	period, err := BuildRankingPeriod(now, factors)
	if err != nil {
		return nil, err
	}
	for _, fact := range facts {
		if fact.Online && fact.Character.Stats["delete_flag"] == 0 {
			period.Online = append(period.Online, fact.Owner)
		}
	}
	return period, nil
}
