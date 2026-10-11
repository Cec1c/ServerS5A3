package dnfbridge

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	dnfdrops "longheng.io/server/internal/modules/dnf/drop"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type currentRiftLootDefinition struct {
	Equipment        []uint32
	EquipmentWeights []uint32
	Pieces           []uint32
	EpicRate         float64
	MultiEpicCount   bool
	FragmentCount    int
	DropCatalog      *pvfDungeonDropCatalog
	MonsterCatalog   *pvfDungeonMonsterCatalog
}

// PVF scopes separate item pools with [dungeon]/[index]/[item list]. Never
// borrow another dungeon's pool or synthesize an equipment/piece identifier.
func parseCurrentRiftLoot(text string) (currentRiftLootDefinition, error) {
	doc, err := dnfpvf.Parse("etc/particularhellparty.etc", text)
	if err != nil {
		return currentRiftLootDefinition{}, err
	}
	rate, ok := doc.Number("inferno weapon creation rate")
	if !ok || math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 || rate > 1 {
		return currentRiftLootDefinition{}, fmt.Errorf("rift epic probability unavailable")
	}
	definition := currentRiftLootDefinition{EpicRate: rate}
	index := int64(0)
	seen := map[uint32]bool{}
	for _, section := range doc.Sections {
		switch strings.ToLower(strings.TrimSpace(section.Name)) {
		case "index":
			index = 0
			for _, token := range dungeonPVFSectionTokens(doc, section) {
				if token.Kind == dnfpvf.TokenInt {
					index = token.Int
					break
				}
			}
		case "item list":
			if index != currentRiftDungeonID {
				continue
			}
			for _, token := range dungeonPVFSectionTokens(doc, section) {
				if token.Kind != dnfpvf.TokenInt || token.Int <= 0 || token.Int > math.MaxUint32 {
					return definition, fmt.Errorf("invalid rift equipment pool")
				}
				id := uint32(token.Int)
				if !seen[id] {
					seen[id] = true
					definition.Equipment = append(definition.Equipment, id)
				}
			}
		}
	}
	if len(definition.Equipment) == 0 {
		return definition, fmt.Errorf("rift dungeon 316 equipment pool missing")
	}
	return definition, nil
}

func (s *Service) prepareCurrentRiftLoot(difficulty byte) (*currentRiftLootDefinition, error) {
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	if archive == nil {
		return nil, fmt.Errorf("rift PVF archive unavailable")
	}
	text, _, err := readInitialPVFText(archive, "etc/particularhellparty.etc")
	if err != nil {
		return nil, err
	}
	definition, err := parseCurrentRiftLoot(text)
	if err != nil {
		return nil, err
	}
	definition.MultiEpicCount = true // Only the dungeon-316 preparation path.
	if override := strings.TrimSpace(os.Getenv("DNFBRIDGE_RIFT_EPIC_RATE")); override != "" {
		rate, err := strconv.ParseFloat(override, 64)
		if err != nil || math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 || rate > 1 {
			return nil, fmt.Errorf("rift epic rate override invalid")
		}
		definition.EpicRate = rate
	}
	monsterCatalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		return nil, err
	}
	dropCatalog, err := monsterCatalog.DropCatalog()
	if err != nil {
		return nil, err
	}
	pieces, err := s.epicPieceCatalog()
	if err != nil || pieces == nil {
		return nil, fmt.Errorf("rift epic piece catalog unavailable: %w", err)
	}
	for _, id := range definition.Equipment {
		item, err := dropCatalog.ResolveItem(id)
		if err != nil {
			return nil, err
		}
		if item.Kind != dungeonDropItemEquipment {
			return nil, fmt.Errorf("rift pool %d is not equipment", id)
		}
		if piece, ok := pieces.pieceByOutput[id]; ok {
			if _, err := dropCatalog.ResolveItem(piece); err != nil {
				return nil, err
			}
			definition.Pieces = append(definition.Pieces, piece)
		}
	}
	if len(definition.Pieces) == 0 {
		return nil, fmt.Errorf("rift equipment pool has no real epic piece mapping")
	}
	amounts, err := abyssEpicFragmentAmountsFromEnv()
	if err != nil {
		return nil, err
	}
	definition.FragmentCount, _ = amounts.amountFor(int(difficulty), 'A')
	if definition.FragmentCount <= 0 {
		return nil, fmt.Errorf("rift fragment guarantee count invalid")
	}
	definition.DropCatalog, definition.MonsterCatalog = dropCatalog, monsterCatalog
	return &definition, nil
}

func (definition *currentRiftLootDefinition) selectItems(rng *currentDungeonDropLCG) []hellPartyRewardItem {
	items := make([]hellPartyRewardItem, 0, definition.FragmentCount+1)
	for i := 0; i < definition.FragmentCount; i++ {
		items = append(items, hellPartyRewardItem{ItemID: definition.Pieces[rng.Next(len(definition.Pieces))], Amount: 1})
	}
	epics := 0
	roll := rng.Next(dnfdrops.EpicClearProbabilityBound)
	if definition.MultiEpicCount {
		epics = dnfdrops.DemonRiftEpicCount(roll)
	} else if float64(roll) < definition.EpicRate*1000000 {
		epics = 1
	}
	for i := 0; i < epics; i++ {
		index := 0
		if len(definition.EquipmentWeights) == len(definition.Equipment) {
			total := uint32(0)
			for _, weight := range definition.EquipmentWeights {
				total += weight
			}
			roll := uint32(rng.Next(int(total)))
			for i, weight := range definition.EquipmentWeights {
				if roll < weight {
					index = i
					break
				}
				roll -= weight
			}
		} else {
			index = rng.Next(len(definition.Equipment))
		}
		items = append(items, hellPartyRewardItem{ItemID: definition.Equipment[index], Amount: 1})
	}
	return items
}

// Ordinary authoritative boss retirement calls this once. The existing room
// ledger rejects replayed deaths, and the normal ground-drop pickup owner
// credits the resulting pieces to the real epic book.
func (s *Service) planCurrentRiftBossDrops(session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene) ([]currentDungeonDeathDropWire, error) {
	definition := runtime.riftLoot
	if definition == nil {
		return nil, fmt.Errorf("rift loot was not frozen before admission")
	}
	visit, err := runtime.currentDungeonRoomVisit(scene)
	if err != nil {
		return nil, err
	}
	rng := newCurrentDungeonDropLCG(visit.DropRNG)
	var wires []currentDungeonDeathDropWire
	for _, recipient := range partyDungeonDropRecipients(runtime, session.selectedCharacterID, currentSceneActorObjectKey(session.selectedCharacterID)) {
		selected := definition.selectItems(rng)
		memberWires, err := s.registerHellPartyRewardDrops(runtime, scene, hellPartyRewardRequest{dropCatalog: definition.DropCatalog, dungeonMonsterCatalog: definition.MonsterCatalog, rng: rng}, selected, recipient.ActorKey, recipient.CharacterID)
		visit.DropRNG = rng.Seed()
		if err != nil {
			return nil, err
		}
		wires = append(wires, memberWires...)
		s.logGameEvent(session, "game-rift-boss-loot-registered", "dungeon_id", runtime.Dungeon.ID, "recipient_char_id", recipient.CharacterID, "fragment_count", definition.FragmentCount, "epic_probability", definition.EpicRate, "multi_epic_count", definition.MultiEpicCount, "items", hellPartyRewardItemList(selected), "source", "particularhellparty_pool_and_epicpieceinfo_mapping")
	}
	return wires, nil
}
