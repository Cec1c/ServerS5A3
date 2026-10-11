package worldmap

import "unicode"

// A raid's only typed start resource can retain an old physical filename
// coordinate outside its current logical maze (actual BlackVolcano 19206).
// Keep explicit/exact/pool precedence; do not choose a normal or foreign map.
func (r *Resolver) raidStartResourcePool(dungeonID int64, mazeIndex int, room DungeonRoom) ([]Map, bool) {
	if r == nil || r.table == nil || !room.Start || room.Boss {
		return nil, false
	}
	di, ok := r.byDungeon[dungeonID]
	if !ok {
		return nil, false
	}
	d := r.table.dungeons[di.dungeonPos]
	if len(d.Mazes) != 1 || mazeIndex != 0 {
		return nil, false
	}
	maze := d.Mazes[0]
	if len(maze.QuestConnection) != 0 || !maze.Width.Set || !maze.Height.Set {
		return nil, false
	}
	declared := false
	for _, section := range d.SourceSections {
		if section.Name == "raid dungeon" && len(section.Tokens) == 1 && section.Tokens[0].Value == "1" {
			declared = true
		}
	}
	if !declared {
		return nil, false
	}
	key := coordinateKey{room.Coordinate.X, room.Coordinate.Y}
	index := di.mazes[0]
	if len(index.bySpecification[key]) != 0 {
		return nil, false
	}
	var candidates []indexedMap
	for _, refs := range index.byOwnership {
		for _, ref := range refs {
			if ref.fileType == MapFileStart {
				candidates = appendUniqueIndexedMap(candidates, ref)
			}
		}
	}
	if len(candidates) != 1 {
		return nil, false
	}
	ref := candidates[0]
	m := r.table.maps[ref.mapPos]
	if !m.DungeonID.Set || m.DungeonID.Value != dungeonID || len(m.DungeonIDs) != 1 || m.DungeonIDs[0] != dungeonID {
		return nil, false
	}
	x, y, ok := ParseMapFileCoordinate(m.Path)
	if !ok || (x >= 0 && y >= 0 && x < maze.Width.Value && y < maze.Height.Value) {
		return nil, false
	}
	var expected uint8
	for _, n := range room.Neighbors {
		for _, delta := range topologyDeltas {
			if n.Direction == delta.direction {
				expected |= delta.mask
			}
		}
	}
	var mask uint8
	seen := false
	for _, c := range m.Greed {
		if unicode.IsSpace(c) {
			continue
		}
		value, ok := greedRoomMask(c)
		if !ok || value == 0 || (seen && value != mask) {
			return nil, false
		}
		mask = value
		seen = true
	}
	if !seen || expected == 0 || mask != expected {
		return nil, false
	}
	return []Map{cloneMap(m)}, true
}
