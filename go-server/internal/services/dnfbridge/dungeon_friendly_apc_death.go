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
		// 0x111 is the native character kind observed for friendly APCs. Do not
		// interpret the remaining opaque combat fields as authoritative damage.
		if binary.LittleEndian.Uint16(entry) != 0x111 || binary.LittleEndian.Uint64(entry[4:]) == 0 {
			continue
		}
		key := uint32(binary.LittleEndian.Uint16(entry[2:]))
		if runtime.Room.ContainsAnnouncedNonHostileAICharacterObjectKey(key) {
			return true
		}
		// Friendly followers retain their original announced key across rooms.
		for _, visit := range runtime.Rooms {
			if visit != nil && visit.Room != nil && visit.Room != runtime.Room && visit.Room.ContainsAnnouncedNonHostileAICharacterObjectKey(key) {
				return true
			}
		}
	}
	return false
}
