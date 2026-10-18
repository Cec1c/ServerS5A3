package dnfbridge

import (
	"encoding/binary"
	"strings"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// The official native writer (0x02758b4b) writes the killer key independently
// of its contribution list. A friendly APC kill can therefore have owner=ffff
// and one or more 14-byte contributions. Prove both the contributor and target
// against this run; a nonempty list or sentinel alone grants no authority.
// Story allies can be AI characters (native kind 0x111) or ordinary monsters
// explicitly assigned to the player side by PVF [monster team] (kind 0x211).
func (s *Service) currentDungeonFriendlyAPCDeath(runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene, request dungeoncmd.DieMonsterRequest) bool {
	if s.options.currentClientVersion != "official-24.9" ||
		request.OwnerObjectKey != ^uint16(0) || request.Layout != dungeoncmd.DieMonsterRequestLayoutVariableCombat ||
		request.CombatEntryCount == 0 || len(request.CombatEntries) != int(request.CombatEntryCount)*dungeoncmd.Official249DieMonsterEntrySize ||
		runtime == nil || runtime.Room == nil || runtime.Session == nil ||
		isPVFTutorialDungeon(runtime) || currentDungeonPathUsesTutorialNamespace(runtime.Dungeon.Path) {
		return false
	}
	if !currentDungeonFriendlyDeathTarget(runtime, scene, request.RuntimeObjectKey) {
		return false
	}
	target, _ := runtime.Room.MonsterByReference(
		scene.RuntimeObjects[request.RuntimeObjectKey],
		runtimeDungeonMonsterAnnounced,
		runtimeDungeonMonsterDefeated,
	)
	if target.State == runtimeDungeonMonsterAnnounced {
		if _, ok := currentDungeonOwnerSentinelAnnouncedOrdinaryMonster(runtime, scene, request.RuntimeObjectKey); !ok {
			return false
		}
	}
	for offset := 0; offset < len(request.CombatEntries); offset += dungeoncmd.Official249DieMonsterEntrySize {
		entry := request.CombatEntries[offset:]
		// Native kind selects the server-owned actor table; it is not proof of
		// allegiance. Do not use the opaque contribution value to settle damage.
		if binary.LittleEndian.Uint64(entry[4:]) == 0 {
			continue
		}
		kind := binary.LittleEndian.Uint16(entry)
		key := uint32(binary.LittleEndian.Uint16(entry[2:]))
		if currentDungeonRoomContainsFriendlyContributor(runtime.Room, scene, kind, key, false) {
			return true
		}
		// Followers retain the original key and faction after leaving their
		// spawning room. Use that visit's PVF scene, not the destination's rows.
		for _, visit := range runtime.Rooms {
			if visit != nil && visit.Room != nil && visit.Room != runtime.Room &&
				currentDungeonRoomContainsFriendlyContributor(visit.Room, visit.Scene, kind, key, false) {
				return true
			}
		}
	}
	return false
}

// currentDungeonFriendlyOwnerDeath proves the real object key path written by
// the current client. The target remains a live or already-authoritative
// ordinary hostile in the current room. A current-room friend may be an
// announced non-hostile AI character or team=0 monster; a monster visiting
// from an earlier room must also carry the same-index PVF follow declaration.
func (s *Service) currentDungeonFriendlyOwnerDeath(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	request dungeoncmd.DieMonsterRequest,
) bool {
	if s.options.currentClientVersion != "official-24.9" ||
		request.OwnerObjectKey == 0 || request.OwnerObjectKey == ^uint16(0) ||
		request.Layout != dungeoncmd.DieMonsterRequestLayoutVariableCombat ||
		runtime == nil || runtime.Room == nil || runtime.Session == nil ||
		isPVFTutorialDungeon(runtime) || currentDungeonPathUsesTutorialNamespace(runtime.Dungeon.Path) ||
		!currentDungeonFriendlyDeathTarget(runtime, scene, request.RuntimeObjectKey) {
		return false
	}
	owner := uint32(request.OwnerObjectKey)
	if currentDungeonRoomContainsFriendlyOwner(runtime.Room, scene, owner, false) {
		return true
	}
	for _, visit := range runtime.Rooms {
		if visit == nil || visit.Room == nil || visit.Room == runtime.Room {
			continue
		}
		if currentDungeonRoomContainsFriendlyOwner(visit.Room, visit.Scene, owner, true) {
			return true
		}
	}
	return false
}

func currentDungeonFriendlyDeathTarget(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	targetKey uint32,
) bool {
	if runtime == nil || runtime.Room == nil || targetKey == 0 {
		return false
	}
	room := runtime.Room.Snapshot()
	if room.Coordinate != scene.Coordinate || room.MapID != scene.Map.Map.ID {
		return false
	}
	reference, bound := scene.RuntimeObjects[targetKey]
	if !bound || reference.Kind != worldmap.HostileMonster ||
		!runtimeSceneContainsHostile(scene.ExpectedHostiles, reference) ||
		reference.Index < 0 || reference.Index >= len(scene.Map.Map.Monsters) {
		return false
	}
	// A missing [monster team] row means hostile by default. Only an explicit
	// same-index team=0 declaration proves that this target is friendly.
	if reference.Index < len(scene.Map.Map.MonsterTeam) &&
		scene.Map.Map.MonsterTeam[reference.Index] == 0 {
		return false
	}
	target, found := runtime.Room.MonsterByReference(
		reference,
		runtimeDungeonMonsterAnnounced,
		runtimeDungeonMonsterDefeated,
	)
	return found && target.ObjectKey == targetKey
}

func currentDungeonRoomContainsFriendlyOwner(
	room *runtimeDungeonRoom,
	scene worldmap.DungeonRoomScene,
	objectKey uint32,
	requireMonsterFollow bool,
) bool {
	return currentDungeonRoomContainsFriendlyContributor(room, scene, 0x111, objectKey, false) ||
		currentDungeonRoomContainsFriendlyContributor(room, scene, 0x211, objectKey, requireMonsterFollow)
}

func currentDungeonRoomContainsFriendlyContributor(
	room *runtimeDungeonRoom,
	scene worldmap.DungeonRoomScene,
	kind uint16,
	key uint32,
	requireMonsterFollow bool,
) bool {
	switch kind {
	case 0x111:
		return room.ContainsAnnouncedNonHostileAICharacterObjectKey(key)
	case 0x211:
		monster, ok := room.AnnouncedMonster(key)
		if !ok || monster.Reference.Kind != worldmap.HostileMonster {
			return false
		}
		snapshot := room.Snapshot()
		index := monster.Reference.Index
		// A missing team row means hostile by default. Neither a nonblocking
		// flag nor a client-reported kind is enough to establish a friendly NPC.
		if snapshot.MapID != scene.Map.Map.ID || snapshot.Coordinate != scene.Coordinate ||
			index < 0 || index >= len(scene.Map.Map.MonsterTeam) || scene.Map.Map.MonsterTeam[index] != 0 ||
			index >= len(scene.Map.Map.Monsters) || scene.Map.Map.Monsters[index].MonsterID != monster.Spawn.MonsterID {
			return false
		}
		if requireMonsterFollow &&
			(index >= len(scene.Map.Map.MonsterFollowType) ||
				!strings.EqualFold(strings.TrimSpace(scene.Map.Map.MonsterFollowType[index]), "follow")) {
			return false
		}
		reference, bound := scene.RuntimeObjects[key]
		return bound && reference == monster.Reference
	default:
		return false
	}
}
