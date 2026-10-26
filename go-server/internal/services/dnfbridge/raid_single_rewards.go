package dnfbridge

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// The shipped raid ETC has no single reward pool. Its shipped compensation
// items explicitly state that their contents equal a successful single clear.
// Use that equivalence only after checking both versions and the separate party
// reward agree. Never give the compensation box itself or use a normal pool.
const singleAntonPartyRewardID uint32 = 10304476
const singleAntonSquadSourceID uint32 = 10304671
const singleAntonCombinedSourceID uint32 = 10303189

func loadCurrentSingleAntonRewards(catalog *pvfDungeonDropCatalog, sequence []uint32, now time.Time) (raid.AntonRewardConfiguration, error) {
	if catalog == nil || len(sequence) != 6 || now.IsZero() {
		return raid.AntonRewardConfiguration{}, raid.ErrRewardIneligible
	}
	boxes := make([]currentBoosterDefinition, 0, 3)
	for _, id := range []uint32{singleAntonPartyRewardID, singleAntonSquadSourceID, singleAntonCombinedSourceID} {
		box, err := resolveCurrentBoosterDefinition(catalog, id, currentBoosterRequestRandom)
		if err != nil || box.MaterialItemID != 0 || box.MaterialCount != 0 {
			return raid.AntonRewardConfiguration{}, fmt.Errorf("single Anton actual reward source %d unavailable: %v", id, err)
		}
		if id != singleAntonPartyRewardID {
			doc, err := parseDungeonCardPVFDocument(catalog.source, box.Source.PVFPath)
			if err != nil || !strings.Contains(magicBoxDocumentText(doc, "explain"), "礼盒内容与安徒恩单人模式灭杀奖励相同") {
				return raid.AntonRewardConfiguration{}, fmt.Errorf("single Anton reward equivalence missing: %d", id)
			}
		}
		boxes = append(boxes, box)
	}
	party, squad, combined := boxes[0].Random.Groups, boxes[1].Random.Groups, boxes[2].Random.Groups
	if len(party) != 1 || len(squad) != 1 || len(combined) != 2 ||
		!reflect.DeepEqual(party[0], combined[0]) || !reflect.DeepEqual(squad[0], combined[1]) ||
		party[0].DrawCount != 1 || squad[0].DrawCount != 1 {
		return raid.AntonRewardConfiguration{}, fmt.Errorf("single Anton reward versions disagree")
	}
	// Death ranks are display-only here. The current single result reader uses
	// Anton's final result screen; reuse its actual final death-rank table as a
	// compatibility policy. Single reward probabilities have no rank branches.
	normal, err := raid.LoadAntonRewardConfiguration(catalog.source)
	if err != nil {
		return raid.AntonRewardConfiguration{}, err
	}
	phase := raid.AntonPhaseRewards{MinimumClears: uint32(len(sequence)), Ranks: append([]raid.AntonRankRange(nil), normal.Phases[1].Ranks...), Choices: []raid.AntonRewardChoice{
		{Type: "party_card", Rank: -1, Weight: 1, ContainerID: singleAntonPartyRewardID},
		{Type: "squad_item", Rank: -1, Weight: 1, ContainerID: singleAntonSquadSourceID},
	}}
	config := raid.AntonRewardConfiguration{Phases: []raid.AntonPhaseRewards{phase}}
	if err := validateCurrentAntonRewardDependencies(catalog, config, now); err != nil {
		return raid.AntonRewardConfiguration{}, err
	}
	// The squad compensation source must be expanded, so validate every direct
	// leaf before admission, including rare branches that a zero draw misses.
	for _, group := range squad {
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

// Expand only the explicitly equivalent outer single reward source. A rare
// child booster remains its actual unopened item, matching the normal owner.
func resolveCurrentSingleAntonSquadPlan(catalog *pvfDungeonDropCatalog, phase raid.AntonPhaseRewards, rank byte, roll func(int64) (int64, error), now time.Time) (currentAntonRewardPlan, error) {
	return resolveCurrentSingleRaidSquadPlan(catalog, phase, rank, roll, now, singleAntonSquadSourceID, singleAntonCombinedSourceID)
}

func resolveCurrentSingleRaidSquadPlan(catalog *pvfDungeonDropCatalog, phase raid.AntonPhaseRewards, rank byte, roll func(int64) (int64, error), now time.Time, sourceID, combinedID uint32) (currentAntonRewardPlan, error) {
	choices, total, err := phase.Pool("squad_item", rank)
	if err != nil || total != 1 || len(choices) != 1 || choices[0].ContainerID != sourceID || choices[0].Flags != 0 || roll == nil || now.IsZero() {
		return currentAntonRewardPlan{}, raid.ErrRewardReceipt
	}
	box, err := resolveCurrentBoosterDefinition(catalog, sourceID, currentBoosterRequestRandom)
	if err != nil {
		return currentAntonRewardPlan{}, err
	}
	amounts, options, err := resolveCurrentBoosterRewardAmounts(box, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom}, roll)
	if err != nil {
		return currentAntonRewardPlan{}, err
	}
	rewards, err := resolveCurrentBoosterRewards(catalog, amounts, options, repo.ItemStack{}, now.UTC())
	if err != nil || len(rewards) == 0 || len(rewards) > 255 {
		return currentAntonRewardPlan{}, raid.ErrRewardReceipt
	}
	for _, reward := range rewards {
		if reward.Count == 0 || reward.Count > math.MaxUint16 || reward.Definition.ItemID == sourceID || reward.Definition.ItemID == combinedID {
			return currentAntonRewardPlan{}, raid.ErrRewardReceipt
		}
	}
	encoded, err := json.Marshal(struct {
		Choice  raid.AntonRewardChoice
		Rewards []currentBoosterReward
	}{choices[0], rewards})
	if err != nil {
		return currentAntonRewardPlan{}, err
	}
	return currentAntonRewardPlan{Choice: choices[0], Rewards: rewards, Fingerprint: sha256.Sum256(encoded)}, nil
}
