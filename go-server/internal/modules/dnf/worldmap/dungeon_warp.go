package worldmap

import dnfpvf "longheng.io/server/internal/modules/dnf/pvf"

// A PVF warp adds a directed edge within this maze. It never creates a room,
// resolves an unknown map, bypasses clearance, or implies the reverse edge.
func addDeclaredDungeonWarps(topology *DungeonTopology, sections []RawSection) {
	for index, section := range sections {
		if sectionKey(section.Name) != "warp map condition" {
			continue
		}
		pairs, ok := parseDeclaredDungeonWarps(sections[index+1:])
		if !section.Block || len(section.Tokens) != 0 || !ok {
			topology.Diagnostics = append(topology.Diagnostics, TopologyDiagnostic{
				Code: "invalid_warp_map_condition", Message: "expected closed source/destination coordinate pairs",
			})
			continue
		}
		for _, pair := range pairs {
			sourceKey := coordinateKey{x: pair[0].X, y: pair[0].Y}
			source, exists := topology.rooms[sourceKey]
			_, targetExists := topology.rooms[coordinateKey{x: pair[1].X, y: pair[1].Y}]
			if !exists || !targetExists {
				topology.Diagnostics = append(topology.Diagnostics, TopologyDiagnostic{
					Code: "warp_endpoint_not_in_maze", Message: pair[0].String() + " -> " + pair[1].String(),
				})
				continue
			}
			if !roomHasWarpTarget(source, pair[1]) {
				source.WarpTargets = append(source.WarpTargets, pair[1])
				topology.rooms[sourceKey] = source
			}
		}
	}
}

func parseDeclaredDungeonWarps(sections []RawSection) ([][2]RoomCoordinate, bool) {
	var pairs [][2]RoomCoordinate
	var source *RoomCoordinate
	for _, section := range sections {
		key := sectionKey(section.Name)
		if key == "/warp map condition" {
			return pairs, source == nil && len(pairs) > 0 && len(section.Tokens) == 0
		}
		if len(section.Scope) == 0 || sectionKey(section.Scope[len(section.Scope)-1]) != "warp map condition" ||
			section.Block || len(section.Tokens) != 2 {
			return nil, false
		}
		for _, token := range section.Tokens {
			if token.Kind != dnfpvf.TokenInt {
				return nil, false
			}
		}
		point := RoomCoordinate{X: section.Tokens[0].Int, Y: section.Tokens[1].Int}
		switch key {
		case "source grid pos":
			if source != nil {
				return nil, false
			}
			source = &point
		case "dest grid pos":
			if source == nil {
				return nil, false
			}
			// Negative/sentinel and same-room script entries do not authorize
			// a coordinate transition. In particular (-1,-1) is not a wildcard.
			if *source != point && warpCoordinateConcrete(*source) && warpCoordinateConcrete(point) {
				pairs = append(pairs, [2]RoomCoordinate{*source, point})
			}
			source = nil
		default:
			return nil, false
		}
	}
	return nil, false
}

func warpCoordinateConcrete(point RoomCoordinate) bool {
	return point.X >= 0 && point.X <= 255 && point.Y >= 0 && point.Y <= 255
}

func roomHasWarpTarget(room DungeonRoom, target RoomCoordinate) bool {
	for _, coordinate := range room.WarpTargets {
		if coordinate == target {
			return true
		}
	}
	return false
}
