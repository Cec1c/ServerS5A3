package dnfbridge

import (
	"fmt"
	"math"

	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const currentDungeonFreeCardGoldRulesPath = currentDungeonClearRewardRulePath

type currentDungeonFreeCardGoldInputs struct {
	Kills                                currentDungeonCardKillCounts
	PartyCount                           int
	DifficultyRate, PartyRate, RankBonus float64
}

func loadCurrentDungeonFreeCardGoldInputs(source dnfpvf.Source, runtime *runtimeDungeonState) (currentDungeonFreeCardGoldInputs, error) {
	if runtime == nil || runtime.entryPartyCount < 1 || runtime.entryPartyCount > 4 ||
		!runtime.settlementPlayResultReceived || len(runtime.settlementPlayResultBody) <= currentDungeonPlayResultClientRankPointOffset {
		return currentDungeonFreeCardGoldInputs{}, errCurrentDungeonSettlementPVFInvalid
	}
	document, err := parseDungeonCardPVFDocument(source, currentDungeonFreeCardGoldRulesPath)
	if err != nil {
		return currentDungeonFreeCardGoldInputs{}, err
	}
	readRate := func(section string, index int) (float64, error) {
		tokens, found := document.Section(section)
		values := document.Numbers(section)
		if !found || len(tokens) != len(values) || index < 0 || index >= len(values) {
			return 0, fmt.Errorf("%w: %s index=%d", errDungeonCardPVFSectionShape, section, index)
		}
		for _, value := range values {
			if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
				return 0, fmt.Errorf("%w: %s rate=%v", errDungeonCardPVFSectionShape, section, value)
			}
		}
		return values[index], nil
	}
	difficultyRate, err := readRate("dungeon difficulty gold drop bonusrate", int(runtime.Request.Difficulty))
	if err != nil {
		return currentDungeonFreeCardGoldInputs{}, err
	}
	partyRate, err := readRate("party member drop bonusrate", runtime.entryPartyCount-1)
	if err != nil {
		return currentDungeonFreeCardGoldInputs{}, err
	}
	sources, err := progression.LoadMonsterExperienceSources(source)
	if err != nil {
		return currentDungeonFreeCardGoldInputs{}, err
	}
	ranks, err := loadCurrentDungeonClearRankCatalog(source)
	if err != nil {
		return currentDungeonFreeCardGoldInputs{}, err
	}
	rates := sources.RawModifiers().ClearRankRates
	if len(rates) == 0 || len(rates) > len(ranks.gradeThresholds) {
		return currentDungeonFreeCardGoldInputs{}, errCurrentDungeonSettlementPVFInvalid
	}
	point := runtime.settlementPlayResultBody[currentDungeonPlayResultClientRankPointOffset]
	rankBonus := 0.0
	// Rank grades also contain lower display-only grades. Only the leading
	// thresholds have reward rates, in the reverse table order.
	for index, threshold := range ranks.gradeThresholds[:len(rates)] {
		if point >= threshold {
			rankBonus = rates[len(rates)-1-index]
			break
		}
	}
	if rankBonus < 0 || math.IsNaN(rankBonus) || math.IsInf(rankBonus, 0) {
		return currentDungeonFreeCardGoldInputs{}, errCurrentDungeonSettlementPVFInvalid
	}
	return currentDungeonFreeCardGoldInputs{
		Kills: runtime.currentCardKillCounts(), PartyCount: runtime.entryPartyCount,
		DifficultyRate: difficultyRate, PartyRate: partyRate, RankBonus: rankBonus,
	}, nil
}

func (input currentDungeonFreeCardGoldInputs) amount(rng *currentDungeonDropLCG, reference currentDungeonGoldReference) (int64, error) {
	return currentDungeonFreeCardGoldAmount(rng, reference,
		input.Kills.Normal, input.Kills.Champion, input.Kills.Boss,
		input.DifficultyRate, input.PartyRate, input.RankBonus, input.PartyCount)
}
