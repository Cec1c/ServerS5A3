package dnfbridge

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// The marker establishes identity; the resource path establishes the floor.
// SAO, Grief, unlisted aliases and a coincidental numeric ID are insufficient.
func currentDespairFloor(dungeon worldmap.Dungeon) (int, error) {
	if dungeon.Metadata.IntegerValues["tower of despair"] != 1 {
		return 0, nil
	}
	name := strings.ToLower(strings.ReplaceAll(dungeon.Path, "\\", "/"))
	base := path.Base(name)
	const prefix = "ztowerofdespair"
	if !strings.HasSuffix(name, "/towers/"+base) || len(base) != len(prefix)+3+4 || !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, ".dgn") {
		return 0, dnfdungeon.ErrSettlementStateInvalid
	}
	floor, err := strconv.Atoi(base[len(prefix) : len(prefix)+3])
	if err != nil || floor < 1 || floor > 100 || base[len(prefix):len(prefix)+3] != fmt.Sprintf("%03d", floor) {
		return 0, dnfdungeon.ErrSettlementStateInvalid
	}
	return floor, nil
}

func (s *Service) resolveCurrentDespairSelection(session *gameSession, requested uint32) (uint32, error) {
	table, _, err := s.dungeonWorldMap()
	if err != nil {
		return 0, err
	}
	dungeon, found := table.FindDungeon(int64(requested))
	if !found || dungeon.Metadata.IntegerValues["tower of despair"] != 1 {
		return requested, nil
	}
	if _, err := currentDespairFloor(dungeon); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	character, err := s.dungeonCharacter(ctx, session.selectedCharacterID, session)
	if err != nil {
		return 0, err
	}
	next, err := dnfdungeon.DespairNextFloor(character.Stats)
	if err != nil {
		return 0, err
	}
	target, found := table.FindDungeonPath(fmt.Sprintf("dungeon/towers/ztowerofdespair%03d.dgn", next))
	if !found || target.ID <= 0 || target.ID > int64(^uint32(0)) {
		return 0, worldmap.ErrDungeonNotIndexed
	}
	floor, err := currentDespairFloor(target)
	if err != nil || floor != next {
		return 0, dnfdungeon.ErrSettlementStateInvalid
	}
	s.logGameEvent(session, "game-dungeon-despair-selection-resolved", "requested_dungeon_id", requested, "dungeon_id", target.ID, "floor", next)
	return uint32(target.ID), nil
}
