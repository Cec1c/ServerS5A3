package dnfbridge

import (
	"encoding/binary"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// The observed Ekern Warriors client reports a nonzero 0x211 monster
// contribution with owner=ffff. The contributor is not a player ally. Prove
// the two distinct ordinary hostiles and their opposing PVF teams in the
// currently announced room, rather than extending friendly-APC semantics.
// The verified dungeon family is intentional: unequal arbitrary PVF team
// numbers in an unrelated dungeon are not proof of hostility.
func (s *Service) currentEkernFactionDeath(rt *runtimeDungeonState, scene worldmap.DungeonRoomScene, req dungeoncmd.DieMonsterRequest) bool {
	if s.options.currentClientVersion != "official-24.9" || rt == nil || rt.Room == nil ||
		rt.Dungeon.ID != 5097 || normalizeDungeonRuntimePath(rt.Dungeon.Path) != "dungeon/legend/ekern/warriors/warriors.dgn" ||
		isPVFTutorialDungeon(rt) || rt.partyInstance != nil || scene.Boss ||
		req.OwnerObjectKey != ^uint16(0) || req.Layout != dungeoncmd.DieMonsterRequestLayoutVariableCombat ||
		req.CombatEntryCount != 1 || len(req.CombatEntries) != dungeoncmd.Official249DieMonsterEntrySize || len(req.OpaqueTail) != 0 {
		return false
	}
	entry := req.CombatEntries
	if binary.LittleEndian.Uint16(entry) != 0x211 || binary.LittleEndian.Uint64(entry[4:]) == 0 {
		return false
	}
	contributor := uint32(binary.LittleEndian.Uint16(entry[2:]))
	if contributor == req.RuntimeObjectKey {
		return false
	}
	attackerTeam := currentEkernFactionActorTeam(rt, scene, contributor, false)
	targetTeam := currentEkernFactionActorTeam(rt, scene, req.RuntimeObjectKey, true)
	return (attackerTeam == 100 && targetTeam == 101) || (attackerTeam == 101 && targetTeam == 100)
}

func currentEkernFactionActorTeam(rt *runtimeDungeonState, scene worldmap.DungeonRoomScene, key uint32, retryTarget bool) int64 {
	actor, ok := currentDungeonAnnouncedOrdinaryMonsterTarget(rt, scene, key)
	// An authoritative defeat without a recorded response can be replayed.
	// It cannot enter settlement again, and must have the same room binding.
	if !ok && retryTarget && currentDungeonFriendlyDeathTarget(rt, scene, key) {
		if _, sent := rt.Room.DeathResponseBody(key); !sent {
			actor, ok = rt.Room.MonsterByReference(scene.RuntimeObjects[key], runtimeDungeonMonsterDefeated)
			ok = ok && actor.ObjectKey == key
		}
	}
	if !ok || !runtimeSceneContainsHostile(scene.BlockingHostiles, actor.Reference) ||
		currentDungeonOrdinaryMonsterLooksBoss(actor) || currentDungeonClearConditionSource(rt, actor) != "" {
		return 0
	}
	idx := actor.Reference.Index
	if idx < 0 || idx >= len(scene.Map.Map.MonsterTeam) || idx >= len(scene.Map.Map.Monsters) ||
		scene.Map.Map.Monsters[idx].MonsterID != actor.Spawn.MonsterID {
		return 0
	}
	return scene.Map.Map.MonsterTeam[idx]
}
