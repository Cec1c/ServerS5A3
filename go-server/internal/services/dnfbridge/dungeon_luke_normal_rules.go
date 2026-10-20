package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const currentLukeNormalSequenceID int64 = 42

type currentLukeNormalSequence struct {
	Dungeons  []uint32
	AutoEnter bool
}

func (s *Service) currentDungeonRuleSource() (initialEquipmentTextSource, error) {
	if s == nil {
		return nil, fmt.Errorf("dungeon rule source unavailable")
	}
	s.initialEquipmentMu.Lock()
	defer s.initialEquipmentMu.Unlock()
	if s.dungeonRuleSource != nil {
		return s.dungeonRuleSource, nil
	}
	return s.initialEquipmentArchiveLocked()
}

func loadCurrentLukeNormalSequence(source initialEquipmentTextSource) (currentLukeNormalSequence, error) {
	const ref = "etc/sequential_dungeon_info.etc"
	text, err := source.ReadText(ref)
	if err != nil {
		return currentLukeNormalSequence{}, err
	}
	doc, err := dnfpvf.Parse(ref, text)
	if err != nil {
		return currentLukeNormalSequence{}, err
	}
	var sequence currentLukeNormalSequence
	inLuke := false
	selected := false
	for _, section := range doc.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		values := doc.Tokens[section.Start:section.End]
		switch name {
		case "sequential dungeon":
			inLuke = len(values) == 1 && values[0].Kind == dnfpvf.TokenInt &&
				values[0].Int == currentLukeNormalSequenceID
			if inLuke {
				if selected {
					return currentLukeNormalSequence{}, fmt.Errorf("Luke sequence duplicated")
				}
				selected = true
			}
			continue
		case "/sequential dungeon":
			if inLuke {
				inLuke = false
			}
			continue
		}
		if !inLuke {
			continue
		}
		switch name {
		case "dungeon index check":
			if len(sequence.Dungeons) != 0 {
				return currentLukeNormalSequence{}, fmt.Errorf("Luke sequence dungeon list duplicated")
			}
			seen := map[uint32]bool{}
			for _, token := range values {
				if token.Kind != dnfpvf.TokenInt || token.Int <= 0 || token.Int > int64(^uint32(0)) {
					return currentLukeNormalSequence{}, fmt.Errorf("Luke sequence invalid")
				}
				id := uint32(token.Int)
				if seen[id] {
					return currentLukeNormalSequence{}, fmt.Errorf("Luke sequence contains duplicate dungeon %d", id)
				}
				seen[id] = true
				sequence.Dungeons = append(sequence.Dungeons, id)
			}
		case "next dungeon auto enter":
			if len(values) != 0 {
				return currentLukeNormalSequence{}, fmt.Errorf("Luke auto-enter marker invalid")
			}
			sequence.AutoEnter = true
		}
	}
	if selected {
		return validateCurrentLukeNormalSequence(sequence)
	}
	return currentLukeNormalSequence{}, fmt.Errorf("Luke sequence 42 unavailable")
}

func validateCurrentLukeNormalSequence(sequence currentLukeNormalSequence) (currentLukeNormalSequence, error) {
	if len(sequence.Dungeons) == 0 {
		return currentLukeNormalSequence{}, fmt.Errorf("Luke sequence dungeon list missing")
	}
	return sequence, nil
}

// Sequence 42, not the raid entrance quest, owns ordinary Mechanical Throne.
func loadCurrentLukeThronePrerequisites(source initialEquipmentTextSource) ([]uint32, error) {
	sequence, err := loadCurrentLukeNormalSequence(source)
	if err != nil {
		return nil, err
	}
	for index, id := range sequence.Dungeons {
		if id == 5106 {
			if index == 0 {
				return nil, fmt.Errorf("Luke throne prerequisites missing")
			}
			return append([]uint32(nil), sequence.Dungeons[:index]...), nil
		}
	}
	return nil, fmt.Errorf("Luke throne sequence unavailable")
}

func currentLukeNormalDungeon(runtime *runtimeDungeonState) bool {
	if runtime == nil || runtime.raidEntry != nil || runtime.Dungeon.ID < 5101 || runtime.Dungeon.ID > 5106 {
		return false
	}
	path := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(runtime.Dungeon.Path, "\\", "/")))
	return strings.HasPrefix(path, "dungeon/luke_normal/")
}

// resolveCurrentLukeNormalNextDungeon reads approved sequence 42 after the
// completed run but before any runtime retirement. A read or definition error
// therefore leaves the settlement available for another click. An absent next
// item is the final-map retry case and is not an error.
func (s *Service) resolveCurrentLukeNormalNextDungeon(runtime *runtimeDungeonState) (uint32, bool, error) {
	if !currentLukeNormalDungeon(runtime) {
		return 0, false, nil
	}
	source, err := s.currentDungeonRuleSource()
	if err != nil {
		return 0, true, err
	}
	sequence, err := loadCurrentLukeNormalSequence(source)
	if err != nil {
		return 0, true, err
	}
	if !sequence.AutoEnter {
		return 0, false, nil
	}
	current := uint32(runtime.Dungeon.ID)
	index := -1
	for i, id := range sequence.Dungeons {
		if id == current {
			index = i
			break
		}
	}
	if index < 0 {
		return 0, true, fmt.Errorf("Luke completed dungeon %d missing from sequence", current)
	}
	if index+1 >= len(sequence.Dungeons) {
		return 0, false, nil
	}
	next := sequence.Dungeons[index+1]
	table, _, err := s.dungeonWorldMap()
	if err != nil {
		return 0, true, err
	}
	if _, ok := table.FindDungeon(int64(next)); !ok {
		return 0, true, fmt.Errorf("Luke next dungeon %d unavailable", next)
	}
	return next, true, nil
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
	source, err := s.currentDungeonRuleSource()
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
