package dnfbridge

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const singleLukePartyRewardID uint32 = 10304477
const singleLukeSquadSourceID uint32 = 10303050
const singleLukeCombinedSourceID uint32 = 10304669

// Both shipped compensation descriptions explicitly identify their contents as
// single Luke final rewards. The later combined source includes the separate
// party reward plus exactly the two groups of the earlier squad source.
func loadCurrentSingleLukeRewards(catalog *pvfDungeonDropCatalog, sequence []uint32, now time.Time) (raid.AntonRewardConfiguration, error) {
	if catalog == nil || len(sequence) != 6 || now.IsZero() {
		return raid.AntonRewardConfiguration{}, raid.ErrRewardIneligible
	}
	boxes := make([]currentBoosterDefinition, 0, 3)
	for _, source := range []struct {
		id          uint32
		equivalence string
	}{
		{singleLukePartyRewardID, ""},
		{singleLukeSquadSourceID, "礼盒内容与卢克单人模式卓越圣殿破坏奖励相同"},
		{singleLukeCombinedSourceID, "礼盒内容与单人卢克攻坚战卓越圣殿破坏奖励相同"},
	} {
		box, err := resolveCurrentBoosterDefinition(catalog, source.id, currentBoosterRequestRandom)
		if err != nil || box.MaterialItemID != 0 || box.MaterialCount != 0 {
			return raid.AntonRewardConfiguration{}, fmt.Errorf("single Luke actual reward source %d unavailable: %v", source.id, err)
		}
		if source.equivalence != "" {
			doc, err := parseDungeonCardPVFDocument(catalog.source, box.Source.PVFPath)
			if err != nil || !strings.Contains(magicBoxDocumentText(doc, "explain"), source.equivalence) {
				return raid.AntonRewardConfiguration{}, fmt.Errorf("single Luke reward equivalence missing: %d", source.id)
			}
		}
		boxes = append(boxes, box)
	}
	party, squad, combined := boxes[0].Random.Groups, boxes[1].Random.Groups, boxes[2].Random.Groups
	if len(party) != 1 || len(squad) != 2 || len(combined) != 3 ||
		!reflect.DeepEqual(squad, combined[1:]) || party[0].DrawCount != 1 || combined[0].DrawCount != 1 ||
		len(party[0].Entries) != 1 || len(combined[0].Entries) != 1 ||
		party[0].Entries[0].ItemID != combined[0].Entries[0].ItemID || party[0].Entries[0].Count != combined[0].Entries[0].Count {
		return raid.AntonRewardConfiguration{}, fmt.Errorf("single Luke reward versions disagree")
	}
	// The party groups use weights 100 and 1000, respectively. With one
	// positive-weight entry and one draw they have the same deterministic result.
	if party[0].Entries[0].Weight <= 0 || combined[0].Entries[0].Weight <= 0 {
		return raid.AntonRewardConfiguration{}, raid.ErrRewardReceipt
	}
	// Compatibility policy, as for single Anton: use this raid's normal final
	// display ranks. Actual single sources have no rank-dependent probabilities.
	normal, err := raid.LoadLukeRewardConfiguration(catalog.source)
	if err != nil {
		return raid.AntonRewardConfiguration{}, err
	}
	phase := raid.AntonPhaseRewards{MinimumClears: uint32(len(sequence)), Ranks: append([]raid.AntonRankRange(nil), normal.Phases[1].Ranks...), Choices: []raid.AntonRewardChoice{
		{Type: "party_card", Rank: -1, Weight: 1, ContainerID: singleLukePartyRewardID},
		{Type: "squad_item", Rank: -1, Weight: 1, ContainerID: singleLukeSquadSourceID},
	}}
	config := raid.AntonRewardConfiguration{Phases: []raid.AntonPhaseRewards{phase}}
	if err := validateCurrentAntonRewardDependencies(catalog, config, now); err != nil {
		return raid.AntonRewardConfiguration{}, err
	}
	for _, group := range squad {
		if group.DrawCount != 1 {
			return raid.AntonRewardConfiguration{}, raid.ErrRewardReceipt
		}
		for _, entry := range group.Entries {
			if entry.ItemID <= 0 || entry.ItemID > math.MaxUint32 || entry.Count <= 0 || entry.Count > math.MaxUint16 {
				return raid.AntonRewardConfiguration{}, raid.ErrRewardReceipt
			}
			if _, err := resolveCurrentBoosterRewards(catalog, map[uint32]uint32{uint32(entry.ItemID): uint32(entry.Count)}, nil, repo.ItemStack{}, now.UTC()); err != nil {
				return raid.AntonRewardConfiguration{}, err
			}
		}
	}
	return config, nil
}

func resolveCurrentSingleLukeSquadPlan(catalog *pvfDungeonDropCatalog, phase raid.AntonPhaseRewards, rank byte, roll func(int64) (int64, error), now time.Time) (currentAntonRewardPlan, error) {
	return resolveCurrentSingleRaidSquadPlan(catalog, phase, rank, roll, now, singleLukeSquadSourceID, singleLukeCombinedSourceID)
}
