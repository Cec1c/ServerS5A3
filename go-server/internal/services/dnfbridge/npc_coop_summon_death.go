package dnfbridge

import (
	"encoding/binary"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type storyNPCDeathTargetKind uint8

const (
	storyNPCOrdinaryHostile storyNPCDeathTargetKind = iota
	storyNPCSpawn
	storyNPCLocalSpawnWithLocalHit
)

// A client-local attack object has no durable server identity. Resolve its
// parent on this death only, using a positive APC contribution or an explicit
// owner and the exact NPC announced in the current map.
func storyNPCDeathSource(room *runtimeDungeonRoom, scene worldmap.DungeonRoomScene, request dungeoncmd.DieMonsterRequest, target storyNPCDeathTargetKind) bool {
	if room == nil || request.Layout != dungeoncmd.DieMonsterRequestLayoutVariableCombat || len(request.OpaqueTail) != 0 || len(request.CombatEntries) != int(request.CombatEntryCount)*dungeoncmd.Official249DieMonsterEntrySize {
		return false
	}
	snapshot := room.Snapshot()
	if snapshot.MapID != scene.Map.Map.ID || snapshot.Coordinate != scene.Coordinate {
		return false
	}
	for _, actor := range snapshot.ExtendedActors {
		if actor.Kind != runtimeDungeonActorAICharacter || actor.HostileReference != nil || actor.Packet.Blocking != 0 || actor.AICharacter == nil {
			continue
		}
		faction := normalizeDungeonPVFSymbol(actor.AICharacter.Faction)
		if faction == "monster" {
			continue
		}
		// Preserve the existing ordinary-kill ownership rules. For retiring a
		// spawn, a map-declared ally in this room supplies the same evidence
		// irrespective of its character code.
		switch actor.AICharacter.Code {
		case 2001, 2012, 6514, 6516, 6517: // Bhan and Paris story variants
		default:
			if target == storyNPCOrdinaryHostile || faction != "character" {
				continue
			}
		}
		matches := actor.ObjectKey == uint32(request.OwnerObjectKey) || (target == storyNPCLocalSpawnWithLocalHit && actor.State == runtimeDungeonMonsterAnnounced)
		for i := 0; !matches && i < len(request.CombatEntries); i += dungeoncmd.Official249DieMonsterEntrySize {
			row := request.CombatEntries[i:]
			matches = binary.LittleEndian.Uint16(row) == currentDungeonPuppetContributionKindAICharacter && uint32(binary.LittleEndian.Uint16(row[2:])) == actor.ObjectKey && binary.LittleEndian.Uint64(row[4:]) != 0
		}
		if matches && (actor.State == runtimeDungeonMonsterAnnounced || currentDungeonRoomConfirmedRetiredFriendlyAPC(room, scene, actor.ObjectKey)) {
			return true
		}
	}
	return false
}

func roomHasRegisteredNPCDeathTarget(room *runtimeDungeonRoom, key uint32) bool {
	room.mu.RLock()
	defer room.mu.RUnlock()
	_, ordinary := room.byObjectKey[key]
	_, extended := room.extendedByObjectKey[key]
	return ordinary || extended
}

// Locally spawned monsters have no server actor to settle. Project their removal
// only to the reporting client. Server-owned actors always use normal settlement.
func (s *Service) notifyStoryNPCClientMonsterDeath(player *gameSession, r *runtimeDungeonState, scene worldmap.DungeonRoomScene, req dungeoncmd.DieMonsterRequest) (bool, error) {
	key := req.RuntimeObjectKey
	if s.options.currentClientVersion != "official-24.9" || key == 0 || key >= uint32(^uint16(0)) || key == uint32(req.OwnerObjectKey) || key == uint32(currentSceneActorObjectKey(player.selectedCharacterID)) || (currentDungeonPathUsesTutorialNamespace(r.Dungeon.Path) && !isPVFTutorialDungeon(r)) {
		return false, nil
	}
	if _, bound := scene.RuntimeObjects[key]; bound || !currentDungeonSceneDeclaresFriendlyAICharacter(scene) {
		return false, nil
	}
	room := r.Room
	if roomHasRegisteredNPCDeathTarget(room, key) {
		return false, nil
	}
	// Only a notification for a non-server target can rely on the presence of
	// a live story NPC and a local player/summon hit. This never settles a kill.
	localHit := req.OwnerObjectKey == currentSceneActorObjectKey(player.selectedCharacterID)
	if req.OwnerObjectKey == ^uint16(0) && req.CombatEntryCount == 1 && len(req.CombatEntries) == dungeoncmd.Official249DieMonsterEntrySize {
		row := req.CombatEntries
		source := uint32(binary.LittleEndian.Uint16(row[2:]))
		localHit = binary.LittleEndian.Uint16(row) == currentDungeonPuppetContributionKindMonster && source != 0 && source != key && binary.LittleEndian.Uint64(row[4:]) != 0 && !room.ContainsAnnouncedActorObjectKey(source)
	}
	inferred := !storyNPCDeathSource(room, scene, req, storyNPCSpawn)
	if inferred {
		// Scripted modes require the killer or a contribution to identify the NPC.
		if !localHit || r.raidEntry != nil || isPVFTutorialDungeon(r) || !storyNPCDeathSource(room, scene, req, storyNPCLocalSpawnWithLocalHit) {
			return false, nil
		}
	}
	// A delayed report for an earlier room is not a new client-local spawn.
	for _, visit := range r.Rooms {
		if visit == nil || visit.Room == nil || visit.Room == room {
			continue
		}
		if _, bound := visit.Scene.RuntimeObjects[key]; bound || roomHasRegisteredNPCDeathTarget(visit.Room, key) {
			return false, nil
		}
	}
	inst := r.lockPartyDungeonInstance()
	reserved := r.DropOwner != nil && r.DropOwner.byObjectKey[key] != nil
	if inst != nil {
		_, isMember := inst.members[uint16(key)]
		reserved = reserved || isMember || (inst.dropOwner != nil && inst.dropOwner.byObjectKey[key] != nil)
	}
	unlockPartyDungeonInstance(inst)
	if reserved {
		return false, nil
	}
	if r.storyNPCNotificationRoom != room {
		r.storyNPCNotificationRoom = room
		r.storyNPCNotificationCount = 0
	}
	// The room budget bounds inferred local hits. An explicit NPC owner or
	// contribution remains verifiable even in long encounters.
	if inferred && r.storyNPCNotificationCount >= currentDungeonPuppetNotificationOnlyLimit {
		return true, nil
	}
	body, err := buildCurrentDungeonDeathNotificationBodyWithDrops(key, currentDungeonDeathResponseMonster, nil)
	if err != nil {
		return true, err
	}
	if err = s.sendGameUpperRawClass(player, uint16(dnfenum.CmdPacketNotifyDieMonster), body, 0); err != nil {
		return true, err
	}
	if inferred {
		r.storyNPCNotificationCount++
	}
	return true, nil
}

// A retained special-monster child uses its existing retirement handler.
// Its map-owned parent and every room blocker keep their original lifecycles.
func storyNPCSpecialChildDeath(r *runtimeDungeonState, scene worldmap.DungeonRoomScene, req dungeoncmd.DieMonsterRequest) bool {
	if currentDungeonPathUsesTutorialNamespace(r.Dungeon.Path) && !isPVFTutorialDungeon(r) {
		return false
	}
	room := r.Room
	room.mu.RLock()
	index, found := room.extendedByObjectKey[req.RuntimeObjectKey]
	child := false
	if found && index >= 0 && index < len(room.extendedActors) {
		a := room.extendedActors[index]
		child = a.ObjectKey == req.RuntimeObjectKey && a.Kind == runtimeDungeonActorSpecialMonster && a.HostileReference == nil && a.State != runtimeDungeonMonsterPlanned
	}
	room.mu.RUnlock()
	return child && storyNPCDeathSource(room, scene, req, storyNPCSpawn)
}
