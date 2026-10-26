package dnfbridge

import (
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Protected by runtimeDungeonRoom.mu; party members share this exact state.
type currentGuildPracticeRoom struct {
	guildID uint32
	targets map[uint32]currentGuildPracticeTarget
}

type currentGuildPracticeTarget struct {
	MonsterID   uint32
	PlacementID uint32
	X, Y        int32
	Local       bool
	Defeated    bool
}

func (s *Service) prepareCurrentGuildPracticeRoom(session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene) error {
	if !currentFriendlyGuildPractice(runtime) {
		return nil
	}
	return s.prepareCurrentGuildPracticeTargetRoom(runtime, runtime.Room, scene)
}

func (s *Service) prepareCurrentGuildPracticeTargetRoom(runtime *runtimeDungeonState, room *runtimeDungeonRoom, scene worldmap.DungeonRoomScene) error {
	if !currentFriendlyGuildPractice(runtime) {
		return nil
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return err
	}
	catalog, err := guild.LoadPracticeMonsterCatalog(items.source)
	if err != nil {
		return err
	}
	return prepareCurrentGuildPracticeRoomTargets(runtime.guildPractice, room, scene, catalog)
}

// Keep the client-generated rows out of op29. V177 01076B50 scans the frozen
// 64 slots in their wire order, appends mapped monsters and assigns local
// combat keys. Row+28=1 is inverted into actor+5208 by 0263329D/02823F50;
// the native NOTI38 predicates 0251E3F0/0251E500 exclude those actors from
// live/present blockers. They do not extend the MAP's static hostile list.
func prepareCurrentGuildPracticeRoomTargets(owner *currentGuildPracticeRun, room *runtimeDungeonRoom, scene worldmap.DungeonRoomScene, catalog guild.PracticeMonsterCatalog) error {
	if owner == nil || room == nil {
		return guild.ErrPracticeCatalog
	}
	area, mirror, err := currentGuildPracticeMapProjection(scene.Map.Map)
	if err != nil {
		return err
	}
	room.mu.Lock()
	defer room.mu.Unlock()
	if room.coordinate != scene.Coordinate || room.mapID != scene.Map.Map.ID {
		return errDungeonActorOwnerConflict
	}
	if room.guildPractice != nil {
		if room.guildPractice.guildID != owner.GuildID {
			return errDungeonActorOwnerConflict
		}
		return nil
	}
	state := &currentGuildPracticeRoom{guildID: owner.GuildID, targets: make(map[uint32]currentGuildPracticeTarget)}
	for _, monster := range room.monsters {
		// Native collision scanning is only proved for the production table:
		// server-owned keys 400+, followed by locally appended 0,1,2,... .
		if monster.ObjectKey < firstDungeonMonsterObjectKey {
			return errDungeonActorOwnerConflict
		}
		typ, err := currentDungeonMonsterActorType(monster.Spawn)
		if err != nil {
			return err
		}
		state.targets[monster.ObjectKey] = currentGuildPracticeTarget{MonsterID: owner.Guardian.ReplaceMonster(uint32(monster.Spawn.MonsterID), uint32(typ))}
	}
	if len(room.extendedActors) != 0 || len(room.opaqueHostiles) != 0 {
		return fmt.Errorf("guild practice unproven extended actors")
	}
	slots := 0
	key := uint32(0)
	for _, placement := range owner.Placements {
		if area < 0 || int64(placement.Area) != area {
			continue
		}
		slots++
		if slots > 64 {
			return guild.ErrPracticeCatalog
		}
		id, mapped := catalog[placement.ObjectID]
		if !mapped {
			continue
		}
		for {
			if _, exists := state.targets[key]; !exists {
				break
			}
			key++
		}
		x := int32(placement.X)
		if mirror > 0 {
			x = int32(mirror) - x
		}
		state.targets[key] = currentGuildPracticeTarget{MonsterID: id, PlacementID: placement.ID, X: x, Y: int32(placement.Y), Local: true}
		key++
	}
	room.guildPractice = state
	return nil
}

func currentGuildPracticeMapProjection(m worldmap.Map) (area, mirror int64, err error) {
	area = -1 // native maps without a placement area have no slots
	seen := make(map[string]bool)
	for _, section := range m.SourceSections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		if name != "agitwar area index" && name != "agit obejcts xpos reverse" {
			continue
		}
		if seen[name] || len(section.Tokens) != 1 || section.Tokens[0].Kind != pvf.TokenInt {
			return 0, 0, guild.ErrPracticeCatalog
		}
		seen[name] = true
		value := section.Tokens[0].Int
		if name == "agitwar area index" {
			if value < 0 || value > 7 {
				return 0, 0, guild.ErrPracticeCatalog
			}
			area = value
		} else {
			if value < 0 || value > 32767 {
				return 0, 0, guild.ErrPracticeCatalog
			}
			mirror = value
		}
	}
	return area, mirror, nil
}
