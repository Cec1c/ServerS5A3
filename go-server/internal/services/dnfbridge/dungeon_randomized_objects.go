package dnfbridge

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type currentDungeonRandomizedEntry struct {
	Source    worldmap.SelectedRandomizedObject
	SpawnMode uint32
	Faction   uint32
	// Only the actual MOB document can classify a frozen row as ridable.
	Ridable bool
}

type currentDungeonRandomizedOwner struct {
	Plan           *worldmap.RandomizedObjectPlan
	Entries        []currentDungeonRandomizedEntry
	entrySignature [32]byte
	// Immutable selection clones for the same run share terminal state, while
	// their source rows and plans remain independently cloned and signed.
	ridableTerminals *currentDungeonRidableTerminalOwner
}

func (o *currentDungeonRandomizedOwner) clone() *currentDungeonRandomizedOwner {
	if o == nil {
		return nil
	}
	return &currentDungeonRandomizedOwner{Plan: o.Plan.Clone(), Entries: append([]currentDungeonRandomizedEntry(nil), o.Entries...), entrySignature: o.entrySignature, ridableTerminals: o.ridableTerminals}
}

func (o *currentDungeonRandomizedOwner) validate(dungeon worldmap.Dungeon, mazeIndex int) error {
	if o == nil {
		return fmt.Errorf("randomized run owner unavailable")
	}
	if err := o.Plan.Validate(dungeon, mazeIndex); err != nil {
		return err
	}
	data, err := json.Marshal(o.Entries)
	if err != nil {
		return err
	}
	if sha256.Sum256(data) != o.entrySignature {
		return fmt.Errorf("randomized namespace/faction selection changed after freeze")
	}
	return nil
}

// Complete the immutable domain selection with actual template namespaces.
// No client-supplied object code, spawn mode or faction chooses these values.
func (s *Service) prepareCurrentDungeonRandomizedOwner(dungeon worldmap.Dungeon, mazeIndex int, topology *worldmap.DungeonTopology, frozen *currentDungeonRandomizedOwner) (*currentDungeonRandomizedOwner, error) {
	if mazeIndex < 0 || mazeIndex >= len(dungeon.Mazes) {
		return nil, fmt.Errorf("randomized maze owner unavailable")
	}
	if len(dungeon.Mazes[mazeIndex].RandomizedObjects) == 0 {
		return nil, nil
	}
	if frozen != nil {
		if err := frozen.validate(dungeon, mazeIndex); err != nil {
			return nil, err
		}
		return frozen.clone(), nil
	}
	plan, err := worldmap.FreezeRandomizedObjects(dungeon, mazeIndex, topology, s.chooseDungeonIndex)
	if err != nil {
		return nil, err
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	passiveText, err := archive.ReadText("passiveobject/passiveobject.lst")
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse("passiveobject/passiveobject.lst", passiveText)
	if err != nil {
		return nil, err
	}
	passives := map[int64]string{}
	for _, entry := range dnfpvf.ParseList(doc) {
		passives[entry.ID] = entry.Path
	}
	monsters, err := s.dungeonMonsterCatalog()
	if err != nil {
		return nil, err
	}
	owner := &currentDungeonRandomizedOwner{Plan: plan}
	roomCounts := map[worldmap.RoomCoordinate]int{}
	for _, entry := range plan.Entries() {
		value := currentDungeonRandomizedEntry{Source: entry}
		switch entry.Team {
		case "monster":
			value.Faction = 100
		case "character":
			value.Faction = 200
		case "neutral":
			value.Faction = 0
		default:
			return nil, fmt.Errorf("randomized faction not from actual source")
		}
		monster, hasMonster, err := monsters.Find(entry.ObjectID)
		if err != nil {
			return nil, err
		}
		passivePath, hasPassive := passives[entry.ObjectID]
		var monsterKind string
		// Ordinary ridable templates can have regenerate=0 and no passive-name
		// collision. Classify every resolved MOB before freezing terminal state.
		if hasMonster {
			text, err := archive.ReadText(monster.Path)
			if err != nil {
				return nil, err
			}
			doc, err := dnfpvf.Parse(monster.Path, text)
			if err != nil {
				return nil, err
			}
			monsterKind, _ = doc.Text("monster object type")
		}
		if entry.Regenerate.Value == 99 && (!hasMonster || strings.Trim(strings.ToLower(monsterKind), "[] \t\r\n") != "ridable object") {
			return nil, fmt.Errorf("regenerating randomized object %d requires an explicit ridable MOB template", entry.ObjectID)
		}
		isPassive := entry.ObjectType == "passive object"
		if entry.ObjectType == "" && hasPassive {
			isPassive = true
			if hasMonster {
				isPassive = !strings.Contains(strings.ToLower(monsterKind), "ridable object")
			}
		}
		if isPassive {
			if !hasPassive {
				return nil, fmt.Errorf("randomized passive template %d absent", entry.ObjectID)
			}
			ref := strings.ToLower(strings.ReplaceAll(passivePath, "\\", "/"))
			if !strings.HasPrefix(ref, "passiveobject/") {
				ref = path.Join("passiveobject", ref)
			}
			text, err := archive.ReadText(ref)
			if err != nil {
				return nil, err
			}
			doc, err := dnfpvf.Parse(ref, text)
			if err != nil || len(doc.Tokens) == 0 {
				return nil, fmt.Errorf("randomized passive template %d unreadable", entry.ObjectID)
			}
			value.SpawnMode = 1
		} else if !hasMonster {
			return nil, fmt.Errorf("randomized monster template %d absent", entry.ObjectID)
		}
		value.Ridable = !isPassive && strings.Trim(strings.ToLower(monsterKind), "[] \t\r\n") == "ridable object"
		if currentDungeonRidableTerminalEligible(value) && owner.ridableTerminals == nil {
			owner.ridableTerminals = &currentDungeonRidableTerminalOwner{}
		}
		roomCounts[entry.Map]++
		if roomCounts[entry.Map] > 255 {
			return nil, fmt.Errorf("randomized room objects exceed native count")
		}
		owner.Entries = append(owner.Entries, value)
	}
	data, err := json.Marshal(owner.Entries)
	if err != nil {
		return nil, err
	}
	owner.entrySignature = sha256.Sum256(data)
	return owner, nil
}

func (o *currentDungeonRandomizedOwner) minimapGroups() [][]currentDungeonInfoPair {
	if o == nil {
		return nil
	}
	var groups [][]currentDungeonInfoPair
	previous := -1
	for _, entry := range o.Entries {
		if !entry.Source.MinimapIcon.Set {
			continue
		}
		if entry.Source.Group != previous {
			groups = append(groups, nil)
			previous = entry.Source.Group
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], currentDungeonInfoPair{First: byte(entry.Source.Map.X), Second: byte(entry.Source.Map.Y)})
	}
	return groups
}

func (o *currentDungeonRandomizedOwner) roomGroups(room worldmap.RoomCoordinate) [][]currentDungeonStartMapRidableEntry {
	if o == nil {
		return nil
	}
	var entries []currentDungeonStartMapRidableEntry
	for _, entry := range o.Entries {
		if entry.Source.Map != room || o.ridableTerminals.retired(entry) {
			continue
		}
		entries = append(entries, currentDungeonStartMapRidableEntry{PositionX: uint32(entry.Source.Position.X), PositionY: uint32(entry.Source.Position.Y), ObjectCode: uint32(entry.Source.ObjectID), Faction: entry.Faction, State: entry.SpawnMode})
	}
	if len(entries) == 0 {
		return nil
	}
	return [][]currentDungeonStartMapRidableEntry{entries}
}
