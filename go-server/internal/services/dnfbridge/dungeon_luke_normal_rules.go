package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Sequence 42, not the raid entrance quest, owns ordinary Mechanical Throne.
func loadCurrentLukeThronePrerequisites(source dnfpvf.Source) ([]uint32, error) {
	const ref = "etc/sequential_dungeon_info.etc"
	text, err := source.ReadText(ref)
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse(ref, text)
	if err != nil {
		return nil, err
	}
	luke := false
	for _, section := range doc.Sections {
		values := doc.Tokens[section.Start:section.End]
		switch strings.ToLower(strings.TrimSpace(section.Name)) {
		case "sequential dungeon":
			luke = len(values) == 1 && values[0].Kind == dnfpvf.TokenInt && values[0].Int == 42
		case "dungeon index check":
			if !luke {
				continue
			}
			var prerequisites []uint32
			seen := map[uint32]bool{}
			for _, token := range values {
				if token.Kind != dnfpvf.TokenInt || token.Int <= 0 || token.Int > int64(^uint32(0)) || seen[uint32(token.Int)] {
					return nil, fmt.Errorf("Luke sequence invalid")
				}
				id := uint32(token.Int)
				seen[id] = true
				if id == 5106 {
					if len(prerequisites) == 0 {
						return nil, fmt.Errorf("Luke throne prerequisites missing")
					}
					return prerequisites, nil
				}
				prerequisites = append(prerequisites, id)
			}
		}
	}
	return nil, fmt.Errorf("Luke throne sequence unavailable")
}

// validateCurrentLukeNormalThroneEntry admits ordinary Mechanical Throne (5106,
// dungeon/luke_normal/solium.dgn) only when the character has the sequence-42
// prerequisites 5101..5105 recorded. The prerequisite set is read from the real
// etc/sequential_dungeon_info.etc sequence 42 [dungeon index check]; 5106 itself
// is the entrance and is excluded.
//
// LIMITATION: the clear test uses the persisted lifetime
// DungeonPermission.ClearState. etc/sequential_dungeon_info.etc sequence 42 has
// no reset/day field and no etc/ table references 5101..5106, so no service-day
// (06:00) reset is applied. This is a tracked limitation, not a claim of daily
// fidelity; do not add a day boundary without reset evidence.
func (s *Service) validateCurrentLukeNormalThroneEntry(ctx context.Context, session *gameSession, dungeon worldmap.Dungeon) error {
	if dungeon.ID != 5106 || !strings.EqualFold(strings.ReplaceAll(dungeon.Path, "\\", "/"), "dungeon/luke_normal/solium.dgn") {
		return nil
	}
	if session == nil || session.selectedCharacterID == 0 {
		return errDungeonCharacterRequired
	}
	s.initialEquipmentMu.Lock()
	source, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return err
	}
	prerequisites, err := loadCurrentLukeThronePrerequisites(source)
	if err != nil {
		return err
	}
	repos, ok := s.repositoryGroupFor(ctx)
	if !ok || repos.DungeonPermission == nil {
		return fmt.Errorf("Luke throne clear repository unavailable")
	}
	record, found, err := repos.DungeonPermission.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil {
		return err
	}
	if !found || record.CharacterID != strconv.Itoa(int(session.selectedCharacterID)) {
		return fmt.Errorf("Luke throne prerequisites incomplete")
	}
	cleared := map[uint32]bool{}
	for _, entry := range record.Entries {
		if entry.ClearState != 0 {
			cleared[entry.DungeonID] = true
		}
	}
	for _, id := range prerequisites {
		if !cleared[id] {
			return fmt.Errorf("Luke throne prerequisite %d incomplete", id)
		}
	}
	return nil
}

// Keep both separately authored quest-drop bosses alive until each reports
// death. This rule is limited to ordinary Luke; raid and story actors retain
// their existing owners and forced-end behavior.
func currentLukeNormalBossesPending(runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene) bool {
	if runtime == nil || runtime.Room == nil || runtime.Dungeon.ID < 5101 || runtime.Dungeon.ID > 5106 {
		return false
	}
	room := runtime.Room.Snapshot()
	if room.Coordinate != scene.Coordinate || room.MapID != scene.Map.Map.ID {
		return false
	}
	blocking := map[worldmap.HostileReference]bool{}
	for _, ref := range scene.BlockingHostiles {
		blocking[ref] = true
	}
	count, pending := 0, false
	for _, monster := range room.Monsters {
		boss := normalizeDungeonPVFSymbol(monster.Spawn.Rank) == "boss" || normalizeDungeonPVFSymbol(monster.Spawn.SuffixMarker) == "boss" || normalizeDungeonPVFSymbol(monster.Definition.Rank) == "boss"
		if !boss || !blocking[monster.Reference] {
			continue
		}
		count++
		pending = pending || monster.State != runtimeDungeonMonsterDefeated
	}
	return count > 1 && pending
}
