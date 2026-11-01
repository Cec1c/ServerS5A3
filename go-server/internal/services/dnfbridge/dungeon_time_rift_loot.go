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
	probability, err := drops.privateSpecialEpicProbability(currentTimeRiftDungeonID)
	if err != nil {
		return nil, err
	}
	definition := &currentRiftLootDefinition{EpicRate: float64(probability) / dnfdrops.EpicClearProbabilityBound, DropCatalog: drops, MonsterCatalog: monsterCatalog, RateSource: hellPartyRewardDropInfoPath + " [" + privateSpecialEpicSection + "]"}
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

// Compatibility entry point; native probabilities and roll counts come from PVF.
func currentNormalAbyssEpicClearReward(request hellPartyRewardRequest) (hellPartyRewardResult, error) {
	return currentNativePVFHellReward(request)
}
