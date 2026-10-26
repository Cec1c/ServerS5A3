package dnfbridge

import (
	"fmt"
	"sort"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Bind the sequence announced by op386 to the already constructed actor
// owners, preserving mixed path order rather than grouping by actor kind.
// Called before the room is installed or announced to any session.
func bindTournamentRoomPath(room *runtimeDungeonRoom, tournament *tournamentDungeonRuntime, scene worldmap.DungeonRoomScene, first uint32) error {
	if tournament == nil {
		return nil
	}
	order := scene.Map.Map.TournamentActorOrder
	if len(order) != len(tournament.PathActors) || len(order) == 0 {
		return fmt.Errorf("tournament actor order is missing or incomplete")
	}
	if room == nil {
		return fmt.Errorf("tournament room is missing")
	}
	room.mu.Lock()
	defer room.mu.Unlock()
	if len(room.announcedSessions) != 0 {
		return errDungeonActorAlreadyAnnounced
	}
	total := len(room.monsters) + len(room.extendedActors)
	if first == 0 || uint64(first)+uint64(total) > 65536 {
		return errDungeonExtendedActorObjectKeyRange
	}
	keys := make(map[worldmap.HostileReference]uint32, len(order))
	for index, reference := range order {
		if _, duplicate := keys[reference]; duplicate {
			return fmt.Errorf("duplicate tournament path reference: %+v", reference)
		}
		keys[reference] = first + uint32(index)
	}
	counts := make(map[worldmap.HostileReference]int, len(order))
	for _, actor := range room.monsters {
		if _, found := keys[actor.Reference]; found {
			counts[actor.Reference]++
		}
	}
	for _, actor := range room.extendedActors {
		if actor.Kind == runtimeDungeonActorAICharacter && actor.HostileReference != nil {
			if _, found := keys[*actor.HostileReference]; found {
				counts[*actor.HostileReference]++
			}
		}
	}
	for reference := range keys {
		if counts[reference] != 1 {
			return fmt.Errorf("tournament path owner count=%d for %+v", counts[reference], reference)
		}
	}
	if !tournament.tryBindFirstActorSequence(uint16(first)) {
		return fmt.Errorf("tournament first actor sequence rejected")
	}
	next := first + uint32(len(order))
	room.byObjectKey = make(map[uint32]int, len(room.monsters))
	room.extendedByObjectKey = make(map[uint32]int, len(room.extendedActors))
	for index := range room.monsters {
		actor := &room.monsters[index]
		key, found := keys[actor.Reference]
		if !found {
			key, next = next, next+1
		}
		actor.ObjectKey = key
		room.byObjectKey[key] = index
	}
	for index := range room.extendedActors {
		actor := &room.extendedActors[index]
		key, found := uint32(0), false
		if actor.Kind == runtimeDungeonActorAICharacter && actor.HostileReference != nil {
			key, found = keys[*actor.HostileReference]
		}
		if !found {
			key, next = next, next+1
		}
		actor.ObjectKey, actor.Packet.ObjectKey = key, uint16(key)
		room.extendedByObjectKey[key] = index
	}
	return nil
}

func tournamentDefeatedObjectKeys(snapshot runtimeDungeonRoomSnapshot) []uint32 {
	var keys []uint32
	for _, actor := range snapshot.Monsters {
		if actor.State == runtimeDungeonMonsterDefeated {
			keys = append(keys, actor.ObjectKey)
		}
	}
	for _, actor := range snapshot.ExtendedActors {
		if actor.Kind == runtimeDungeonActorAICharacter && actor.HostileReference != nil && actor.State == runtimeDungeonMonsterDefeated {
			keys = append(keys, actor.ObjectKey)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
