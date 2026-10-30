package dnfbridge

// These counts represent accepted combat deaths, not the final defeated
// state. Boss cleanup and scripted retirements must not mint extra kills.
type currentDungeonCardKillCounts struct {
	Normal, Champion, Boss int64
}

func (c *currentDungeonCardKillCounts) addActorType(actorType byte) {
	switch actorType {
	case 0, 2, 5, 7:
		c.Normal++
	case 1, 6:
		c.Champion++
	case 3, 8:
		c.Boss++
	}
}

func (r *runtimeDungeonRoom) cardGoldActorTypeLocked(objectKey uint32) (byte, error) {
	if index, ok := r.byObjectKey[objectKey]; ok && index >= 0 && index < len(r.monsters) {
		return currentDungeonMonsterActorType(r.monsters[index].Spawn)
	}
	if index, ok := r.extendedByObjectKey[objectKey]; ok && index >= 0 && index < len(r.extendedActors) {
		return r.extendedActors[index].Packet.Type, nil
	}
	return 0, errDungeonMonsterObjectNotFound
}

// The caller owns session.dungeon.mu. Copy shared room pointers under the
// instance lock, release it, then read each room once. A late joining player's
// per-session Rooms map can omit rooms already cleared by another member.
func (runtime *runtimeDungeonState) currentCardKillCounts() currentDungeonCardKillCounts {
	rooms := make(map[*runtimeDungeonRoom]struct{})
	add := func(room *runtimeDungeonRoom) {
		if room != nil {
			rooms[room] = struct{}{}
		}
	}
	add(runtime.Room)
	for _, visit := range runtime.Rooms {
		if visit != nil {
			add(visit.Room)
		}
	}
	if instance := runtime.partyInstance; instance != nil {
		instance.mu.Lock()
		add(instance.room)
		for _, shared := range instance.rooms {
			add(shared.Room)
		}
		instance.mu.Unlock()
	}
	var counts currentDungeonCardKillCounts
	for room := range rooms {
		room.mu.RLock()
		counts.Normal += room.cardGoldKills.Normal
		counts.Champion += room.cardGoldKills.Champion
		counts.Boss += room.cardGoldKills.Boss
		room.mu.RUnlock()
	}
	return counts
}
