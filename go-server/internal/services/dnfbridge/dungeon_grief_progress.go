package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const (
	griefFirstFloorID   = 5114
	griefFloorDirectory = "dungeon/towers/grief/"
)

// currentGriefFloor identifies a Tower of Grief resource and derives its floor.
// The marker alone is not enough: the path, object ID and known PVF ID mapping
// must all agree on the same canonical floor.
func currentGriefFloor(dungeon worldmap.Dungeon) (int, error) {
	if dungeon.Metadata.IntegerValues["tower of grief"] != 1 {
		return 0, nil
	}
	name := strings.ToLower(strings.ReplaceAll(dungeon.Path, "\\", "/"))
	if !strings.HasPrefix(name, griefFloorDirectory) {
		return 0, dnfdungeon.ErrSettlementStateInvalid
	}
	base := strings.TrimPrefix(name, griefFloorDirectory)
	if strings.Contains(base, "/") || dungeon.ID <= 0 {
		return 0, dnfdungeon.ErrSettlementStateInvalid
	}
	idPrefix := strconv.FormatInt(dungeon.ID, 10) + "_"
	if !strings.HasPrefix(base, idPrefix) {
		return 0, dnfdungeon.ErrSettlementStateInvalid
	}
	floorPart := strings.TrimPrefix(base, idPrefix)
	if len(floorPart) != len("001.dgn") || !strings.HasSuffix(floorPart, ".dgn") {
		return 0, dnfdungeon.ErrSettlementStateInvalid
	}
	digits := strings.TrimSuffix(floorPart, ".dgn")
	floor, err := strconv.Atoi(digits)
	if err != nil || floor < 1 || floor > 100 || digits != fmt.Sprintf("%03d", floor) {
		return 0, dnfdungeon.ErrSettlementStateInvalid
	}
	if dungeon.ID != int64(griefFirstFloorID+floor) {
		return 0, dnfdungeon.ErrSettlementStateInvalid
	}
	return floor, nil
}

func (s *Service) resolveCurrentGriefSelection(session *gameSession, requested uint32) (uint32, error) {
	table, _, err := s.dungeonWorldMap()
	if err != nil {
		return 0, err
	}
	dungeon, found := table.FindDungeon(int64(requested))
	if !found || dungeon.Metadata.IntegerValues["tower of grief"] != 1 {
		return requested, nil
	}
	if _, err := currentGriefFloor(dungeon); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	character, err := s.dungeonCharacter(ctx, session.selectedCharacterID, session)
	if err != nil {
		return 0, err
	}
	next, err := dnfdungeon.GriefNextFloor(character.Stats)
	if err != nil {
		return 0, err
	}
	targetID := int64(griefFirstFloorID + next)
	target, found := table.FindDungeonPath(fmt.Sprintf("%s%d_%03d.dgn", griefFloorDirectory, targetID, next))
	if !found || target.ID != targetID || target.ID > int64(^uint32(0)) {
		return 0, worldmap.ErrDungeonNotIndexed
	}
	floor, err := currentGriefFloor(target)
	if err != nil || floor != next {
		return 0, dnfdungeon.ErrSettlementStateInvalid
	}
	s.logGameEvent(session, "game-dungeon-grief-selection-resolved", "requested_dungeon_id", requested, "dungeon_id", target.ID, "floor", next)
	return uint32(target.ID), nil
}
