package dnfbridge

import (
	"fmt"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
)

func (s *Service) currentEpicRoadLootEnabled(id int64) bool {
	_, supported := dnfdungeon.EpicEntryTicketCount(id)
	return s != nil && s.options.currentClientVersion == "official-24.9" && supported
}

// These event bosses are ordinary actors, so they never reach the abyss-wave
// reward path. Their own particularhellparty pool and approved probability are frozen
// before admission; dungeon 316's multi-epic policy and fragment guarantee do
// not apply to the event's three separately declared pools.
func (s *Service) prepareCurrentEpicRoadLoot(id int64) (*currentRiftLootDefinition, error) {
	if !s.currentEpicRoadLootEnabled(id) {
		return nil, fmt.Errorf("unsupported epic road loot dungeon %d", id)
	}
	monsters, err := s.dungeonMonsterCatalog()
	if err != nil {
		return nil, err
	}
	text, err := monsters.source.ReadText("etc/particularhellparty.etc")
	if err != nil {
		return nil, err
	}
	definition, err := parseCurrentParticularHellLoot(text, id)
	if err != nil {
		return nil, err
	}
	// Paid Epic Road tiers have distinct encounter probabilities. The shared
	// inferno-weapon rate belongs to the particular-hell configuration; it
	// must not make all three ticket prices award epics at the same 50% rate.
	switch id {
	case 6063: // 30 invitations
		definition.EpicRate = 0.10
	case 6064: // 45 invitations
		definition.EpicRate = 0.15
	case 6065: // 60 invitations
		definition.EpicRate = 0.20
	default:
		return nil, fmt.Errorf("unsupported epic road probability dungeon %d", id)
	}
	drops, err := monsters.DropCatalog()
	if err != nil {
		return nil, err
	}
	for _, id := range definition.Equipment {
		item, err := drops.ResolveItem(id)
		if err != nil {
			return nil, err
		}
		if item.Kind != dungeonDropItemEquipment || item.Rarity != hellPartyRewardRarityEpic {
			return nil, fmt.Errorf("epic road pool item %d is not epic equipment", id)
		}
	}
	definition.DropCatalog, definition.MonsterCatalog = drops, monsters
	return &definition, nil
}
