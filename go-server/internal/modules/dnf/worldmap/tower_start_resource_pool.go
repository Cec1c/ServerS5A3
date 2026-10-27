package worldmap

import "unicode"

// Multi-room despair floors use a normal MAP for the start room. Only an
// unbound, uniquely owned resource with a spawn area and matching doors is
// eligible; ordinary dungeons and ambiguous resources keep their old refusal.
func (r *Resolver) towerStartResourcePool(dungeonID int64, mazeIndex int, room DungeonRoom) ([]Map, bool) {
	values, ok, _ := r.towerStartResourcePoolRead(dungeonID, mazeIndex, room)
	return values, ok
}

func (r *Resolver) towerStartResourcePoolRead(dungeonID int64, mazeIndex int, room DungeonRoom) ([]Map, bool, error) {
	if r == nil || r.table == nil || !room.Start || room.Boss || len(room.Neighbors) == 0 {
		return nil, false, nil
	}
	di, ok := r.byDungeon[dungeonID]
	if !ok {
		return nil, false, nil
	}
	d := r.table.dungeons[di.dungeonPos]
	if d.Metadata.IntegerValues["tower of despair"] != 1 || len(d.Mazes) != 1 || mazeIndex != 0 {
		return nil, false, nil
	}
	maze := d.Mazes[0]
	if len(maze.QuestConnection) != 0 || maze.Start == nil || maze.Start.X != room.Coordinate.X || maze.Start.Y != room.Coordinate.Y {
		return nil, false, nil
	}
	if !maze.Width.Set || !maze.Height.Set || maze.Width.Value < 1 || maze.Height.Value < 1 || (maze.Width.Value == 1 && maze.Height.Value == 1) {
		return nil, false, nil
	}
	index := di.mazes[0]
	key := coordinateKey{room.Coordinate.X, room.Coordinate.Y}
	if len(index.bySpecification[key]) != 0 || len(index.byOwnership[key]) != 0 {
		return nil, false, nil
	}
	bound := make(map[int64]bool)
	for _, specifications := range [][]MapSpecification{maze.MapSpecifications, maze.BossSpecifications, maze.LayeredSpecifications} {
		for _, specification := range specifications {
			for _, id := range specification.MapIDs {
				bound[id] = true
			}
		}
	}
	var expected uint8
	for _, neighbor := range room.Neighbors {
		for _, delta := range topologyDeltas {
			if neighbor.Direction == delta.direction {
				expected |= delta.mask
			}
		}
	}
	if expected == 0 {
		return nil, false, nil
	}
	var candidates []indexedMap
	for _, ref := range index.byPool[MapFileNormal] {
		m := r.table.maps[ref.mapPos]
		if bound[m.ID] || !m.DungeonID.Set || m.DungeonID.Value != dungeonID || len(m.DungeonIDs) != 1 || m.DungeonIDs[0] != dungeonID {
			continue
		}
		if _, _, coordinate := ParseMapFileCoordinate(m.Path); coordinate || len(m.DungeonStartArea) != 4 || m.DungeonStartArea[2] <= 0 || m.DungeonStartArea[3] <= 0 {
			continue
		}
		seen, matches := false, true
		for _, c := range m.Greed {
			if unicode.IsSpace(c) {
				continue
			}
			mask, valid := greedRoomMask(c)
			if !valid || mask != expected {
				matches = false
				break
			}
			seen = true
		}
		if seen && matches {
			candidates = append(candidates, ref)
		}
	}
	if len(candidates) != 1 {
		return nil, false, nil
	}
	value, err := r.table.readMap(candidates[0].mapPos)
	if err != nil {
		return nil, false, err
	}
	return []Map{value}, true, nil
}
