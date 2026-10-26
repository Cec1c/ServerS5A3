package worldmap

import "fmt"

// The two Rosch sides declare their own start maps in the DGN. Their filenames
// contain both Boss and Start, so the legacy filename-type heuristic can select
// a neighbouring dungeon's (0,0)_Start map before considering that declaration.
// Boss filenames also retain an old physical coordinate. Honor both declared rooms
// of these two dungeons; coordinates remain PVF/client owned.
func (r *Resolver) fiendWarDeclaredRoom(d Dungeon, maze Maze, index mazeMapIndex, key coordinateKey) ([]indexedMap, bool, error) {
	start, boss := mazeRoomRoles(maze, key)
	if (d.ID != 4953 && d.ID != 4978) || (!start && !boss) {
		return nil, false, nil
	}
	candidates := selectSpecificationCandidates(index.bySpecification[key], maze, key)
	if len(candidates) != 1 {
		return nil, true, fmt.Errorf("%w: fiendwar dungeon=%d room declarations=%d", ErrMapReferenceMissing, d.ID, len(candidates))
	}
	m := r.table.maps[candidates[0].mapPos]
	if len(m.DungeonIDs) != 1 || m.DungeonIDs[0] != d.ID ||
		(start && (len(m.DungeonStartArea) != 4 || m.DungeonStartArea[2] <= 0 || m.DungeonStartArea[3] <= 0)) {
		return nil, true, fmt.Errorf("%w: fiendwar dungeon=%d map=%d has invalid room ownership/start area", ErrMapReferenceMissing, d.ID, m.ID)
	}
	return candidates, true, nil
}
