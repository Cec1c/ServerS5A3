package worldmap

import (
	"path"
	"strings"
)

// 同一副本编号可能同时归属旧版和当前版本的地图。普通起始房已有有效显式
// 候选，且文件名启发式的全部候选均来自本迷宫从未声明的目录时，核对当前
// DGN名称和迷宫连通形状：显式目录须对应DGN，所有显式图出口吻合而旧图均不吻合。
// 不要求可选的pathgate recognize range段存在，不猜测缺省门矩形。
// 同目录、迷宫内确有声明的其它目录、无显式候选、剧情、首领和团本保持原路径。
// 调用方只在默认specification档启用，ownership档仍保留原始回退。
func (r *Resolver) declaredStartDirectoryRoom(
	dungeon Dungeon,
	index mazeMapIndex,
	maze Maze,
	key coordinateKey,
	expectedType MapFileType,
) ([]indexedMap, bool) {
	if expectedType != MapFileStart || mazeQuestConnected(maze) {
		return nil, false
	}
	for _, section := range dungeon.SourceSections {
		if sectionKey(section.Name) == "raid dungeon" {
			return nil, false
		}
	}
	heuristic, _, ok := ownershipRoomCandidates(index, maze, key, expectedType)
	if !ok {
		return nil, false
	}
	explicit := selectSpecificationCandidates(index.bySpecification[key], maze, key)
	if len(explicit) == 0 {
		return nil, false
	}
	directory := func(candidate indexedMap) string {
		return pathKey(path.Dir(strings.ReplaceAll(r.table.maps[candidate.mapPos].Path, "\\", "/")))
	}
	startDirectory := directory(explicit[0])
	dungeonPath := pathKey(strings.ReplaceAll(dungeon.Path, "\\", "/"))
	dungeonName := strings.TrimSuffix(path.Base(dungeonPath), path.Ext(dungeonPath))
	if path.Base(startDirectory) != dungeonName {
		return nil, false
	}
	for _, candidate := range explicit[1:] {
		if directory(candidate) != startDirectory {
			return nil, false
		}
	}
	declared := make(map[string]struct{})
	for _, candidates := range index.bySpecification {
		for _, candidate := range candidates {
			declared[directory(candidate)] = struct{}{}
		}
	}
	for _, candidate := range heuristic {
		if _, used := declared[directory(candidate)]; used {
			return nil, false
		}
	}
	// 与既有拓扑解码共用DGN的[greed]语义；未知/不规则形状不猜测。
	diagnostic := &DungeonTopology{}
	masks, valid := addGreedCells(diagnostic, maze, make(map[coordinateKey]struct{}))
	expected := masks[key]
	if !valid || expected == 0 || len(diagnostic.Diagnostics) != 0 {
		return nil, false
	}
	for _, candidate := range explicit {
		mask, ok := uniformStartRoomMask(r.table.maps[candidate.mapPos].Greed)
		if !ok || mask != expected {
			return nil, false
		}
	}
	for _, candidate := range heuristic {
		mask, ok := uniformStartRoomMask(r.table.maps[candidate.mapPos].Greed)
		if !ok || mask == expected {
			return nil, false
		}
	}
	return explicit, true
}

func uniformStartRoomMask(greed string) (uint8, bool) {
	var mask uint8
	for _, row := range normalizedGreedRows(greed) {
		for _, symbol := range row {
			value, valid := greedRoomMask(symbol)
			if !valid || value == 0 || mask != 0 && value != mask {
				return 0, false
			}
			mask = value
		}
	}
	return mask, mask != 0
}
