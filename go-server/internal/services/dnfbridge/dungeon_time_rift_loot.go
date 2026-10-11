package dnfbridge

import (
	"fmt"

	dnfdrops "longheng.io/server/internal/modules/dnf/drop"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const currentTimeRiftDungeonID int64 = 7539

func (s *Service) currentTimeRiftLootEnabled(dungeonID int64) bool {
	return s != nil && s.options.currentClientVersion == "official-24.9" && dungeonID == currentTimeRiftDungeonID
}

// Inferno_MetroCenter1.dgn uses ordinary boss maps 80303/80304, not a
// hell-party wave. Freeze its real level-scoped .equ pool before admission.
func (s *Service) prepareCurrentTimeRiftLoot(dungeon worldmap.Dungeon, difficulty byte) (*currentRiftLootDefinition, error) {
	monsterCatalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		return nil, err
	}
	drops, err := monsterCatalog.DropCatalog()
	if err != nil {
		return nil, err
	}
	pool, err := drops.hellPartyRewardEquipmentPool()
	if err != nil {
		return nil, err
	}
	if !dungeon.Metadata.MinimumRequiredLevel.Set || !dungeon.Metadata.BasisLevel.Set {
		return nil, errDungeonHellPartyRewardLevels
	}
	low, high, ok := hellPartyRewardGradeRangeForLevels(dungeon.Metadata.MinimumRequiredLevel.Value, dungeon.Metadata.BasisLevel.Value)
	if !ok {
		return nil, errDungeonHellPartyRewardLevels
	}
	pieces, err := s.epicPieceCatalog()
	if err != nil || pieces == nil {
		return nil, fmt.Errorf("time rift epic piece catalog unavailable: %v", err)
	}
	definition := &currentRiftLootDefinition{EpicRate: float64(dnfdrops.TimeRiftEpicProbability) / dnfdrops.EpicClearProbabilityBound, DropCatalog: drops, MonsterCatalog: monsterCatalog}
	for _, item := range pool.candidates(low, high, hellPartyRewardRarityEpic) {
		if item.ItemID == 0 || item.Weight == 0 {
			continue
		}
		definition.Equipment = append(definition.Equipment, item.ItemID)
		definition.EquipmentWeights = append(definition.EquipmentWeights, item.Weight)
		if piece, found := pieces.pieceByOutput[item.ItemID]; found {
			if _, err := drops.ResolveItem(piece); err != nil {
				return nil, err
			}
			definition.Pieces = append(definition.Pieces, piece)
		}
	}
	if len(definition.Equipment) == 0 || len(definition.Pieces) == 0 {
		return nil, fmt.Errorf("time rift real epic/piece pool empty")
	}
	amounts, err := abyssEpicFragmentAmountsFromEnv()
	if err != nil {
		return nil, err
	}
	definition.FragmentCount, _ = amounts.amountFor(int(difficulty), 'A')
	return definition, nil
}

// All groups of the final shared wave must be retired. This gives each
// recipient exactly one 13% trial for the encounter, even with several groups.
func currentNormalAbyssEpicClearReady(runtime *runtimeDungeonState, groupID int64) bool {
	wave, _, ok := abyssEpicFragmentFinalWaveFor(runtime, abyssEpicFragmentFinalWaveRoom)
	if !ok || !wave.containsGroup(groupID) {
		return false
	}
	for _, id := range wave.GroupIDs {
		if dungeonHellPartyGroupAliveCount(runtime.Room, id) != 0 {
			return false
		}
	}
	return true
}

func currentNormalAbyssEpicClearReward(request hellPartyRewardRequest) (hellPartyRewardResult, error) {
	result := hellPartyRewardResult{GroupID: request.GroupID, RewardRollCount: 1, Rate: 13, Chance: dnfdrops.NormalAbyssEpicProbability, RateSource: "operator_per_completed_abyss", Rolls: 1, EpicWindow: dnfdrops.NormalAbyssEpicProbability, EpicWindowSource: "operator_per_completed_abyss", EpicWindowActive: true, EpicExpectationPPM: dnfdrops.NormalAbyssEpicProbability, RarityMode: "epic", RarityAllowed: "4", Pool: "equ"}
	if request.rng == nil || request.dropCatalog == nil {
		return result, errDungeonHellPartyRewardDropRule
	}
	pool, err := request.dropCatalog.hellPartyRewardEquipmentPool()
	if err != nil {
		return result, err
	}
	low, high, ok := hellPartyRewardGradeRangeForLevels(request.DungeonMinimumLevel, request.DungeonBasisLevel)
	if !ok {
		return result, errDungeonHellPartyRewardLevels
	}
	result.GradeMin, result.GradeMax = low, high
	result.CandidateCount = len(pool.candidates(low, high, hellPartyRewardRarityEpic))
	if result.CandidateCount == 0 {
		return result, fmt.Errorf("normal abyss real epic pool empty")
	}
	if !dnfdrops.EpicClearProbabilityHit(request.rng.Next(dnfdrops.EpicClearProbabilityBound), dnfdrops.NormalAbyssEpicProbability) {
		result.Misses = 1
		return result, nil
	}
	selection, err := pool.selectEquipmentWithinRarities(request.rng, request.DungeonMinimumLevel, request.DungeonBasisLevel, []int64{hellPartyRewardRarityEpic}, false)
	if err != nil {
		return result, err
	}
	if !selection.Selected {
		return result, fmt.Errorf("normal abyss real epic selection failed")
	}
	result.RaritySelected[hellPartyRewardRarityEpic] = 1
	result.Selected = []hellPartyRewardItem{{ItemID: selection.ItemID, Amount: 1}}
	return result, nil
}
