package dnfbridge

import (
	"encoding/binary"

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
	room := runtime.Room.Snapshot()
	if room.Coordinate != scene.Coordinate || room.MapID != scene.Map.Map.ID {
		return false
	}
	reference, bound := scene.RuntimeObjects[request.RuntimeObjectKey]
	if !bound || reference.Kind != worldmap.HostileMonster || !runtimeSceneContainsHostile(scene.ExpectedHostiles, reference) {
		return false
	}
	if reference.Index >= 0 && reference.Index < len(scene.Map.Map.MonsterTeam) && scene.Map.Map.MonsterTeam[reference.Index] == 0 {
		return false
	}
	target, found := runtime.Room.MonsterByReference(reference, runtimeDungeonMonsterAnnounced, runtimeDungeonMonsterDefeated)
	if !found || target.ObjectKey != request.RuntimeObjectKey {
		return false
	}
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
		if currentDungeonRoomContainsFriendlyContributor(runtime.Room, scene, kind, key) {
			return true
		}
		// Followers retain the original key and faction after leaving their
		// spawning room. Use that visit's PVF scene, not the destination's rows.
		for _, visit := range runtime.Rooms {
			if visit != nil && visit.Room != nil && visit.Room != runtime.Room &&
				currentDungeonRoomContainsFriendlyContributor(visit.Room, visit.Scene, kind, key) {
				return true
			}
		}
	}
	return false
}

func currentDungeonRoomContainsFriendlyContributor(room *runtimeDungeonRoom, scene worldmap.DungeonRoomScene, kind uint16, key uint32) bool {
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
		reference, bound := scene.RuntimeObjects[key]
		return bound && reference == monster.Reference
	default:
		return false
	}
}
