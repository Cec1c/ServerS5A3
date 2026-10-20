package worldmap

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

var (
	ErrResolverTableRequired  = errors.New("dnf worldmap resolver table is required")
	ErrDungeonNotIndexed      = errors.New("dnf dungeon is not indexed")
	ErrMazeNotFound           = errors.New("dnf dungeon maze is not found")
	ErrMapReferenceMissing    = errors.New("dnf dungeon map reference is missing")
	ErrMapUnresolved          = errors.New("dnf dungeon map is unresolved")
	ErrMapAmbiguous           = errors.New("dnf dungeon map is ambiguous")
	ErrMapPoolEmpty           = errors.New("dnf dungeon map pool is empty")
	ErrMapPoolChoiceInvalid   = errors.New("dnf dungeon map pool choice is invalid")
	ErrLayeredSpecMissing     = errors.New("dnf dungeon layered map specification is missing")
	ErrLayeredSpecAmbiguous   = errors.New("dnf dungeon layered map specification is ambiguous")
	ErrLayeredIndexInvalid    = errors.New("dnf dungeon layered map index is invalid")
	ErrStoryStageMissing      = errors.New("dnf dungeon story stage is missing")
	ErrStoryStageIndexInvalid = errors.New("dnf dungeon story stage index is invalid")
)

type MapFileType string

const (
	MapFileNormal  MapFileType = "normal"
	MapFileStart   MapFileType = "start"
	MapFileBoss    MapFileType = "boss"
	MapFileNamed   MapFileType = "named"
	MapFileEnd     MapFileType = "end"
	MapFileHidden  MapFileType = "hidden"
	MapFileQuest   MapFileType = "quest"
	MapFileDefault MapFileType = "default"
)

type ResolutionSource string

const (
	ResolutionMapSpecification ResolutionSource = "map_specification"
	ResolutionDungeonOwnership ResolutionSource = "dungeon_ownership"
	ResolutionDirectoryPath    ResolutionSource = "directory_coordinate"
	ResolutionTypePool         ResolutionSource = "type_pool"
	// ResolutionHellSealDoor 是**按房间覆盖房图**的来源：深渊房间那一间要用
	// 深渊门那张图（PVF `[seal door map index]`），不是迷宫 `[map specification]`
	// 里那张。厂商口径见 `Network\Handlers\Dungeon\DungeonMapHandler.cs:370-388`
	// 的 `effectiveOverrideMapId == hellRoomInfo.MapId`。
	ResolutionHellSealDoor ResolutionSource = "hell_seal_door"
)

type ResolveRequest struct {
	DungeonID int64 `json:"dungeon_id"`
	MazeIndex int   `json:"maze_index"`
	X         int64 `json:"x"`
	Y         int64 `json:"y"`
}

type ResolvedMap struct {
	Map               Map              `json:"map"`
	Source            ResolutionSource `json:"source"`
	SpecificationType string           `json:"specification_type,omitempty"`
}

// DungeonStoryStage is one client-side PVF map-candidate advance in an
// activity story maze. Index and map order are derived from the repeated
// [boss map] coordinate sequence and the matching [map specification] rows.
type DungeonStoryStage struct {
	Index             int            `json:"index"`
	Coordinate        RoomCoordinate `json:"coordinate"`
	MapID             int64          `json:"map_id"`
	SpecificationType string         `json:"specification_type"`
}

type DungeonMapIndexSnapshot struct {
	DungeonID         int64 `json:"dungeon_id"`
	Mazes             int   `json:"mazes"`
	Specifications    int   `json:"specifications"`
	OwnershipEntries  int   `json:"ownership_entries"`
	CoordinateEntries int   `json:"coordinate_entries"`
	PathEntries       int   `json:"path_entries"`
}

// StartMapPrecedence 选择"非任务连通迷宫的起始房/首领房"用哪一份真源。
//
// 现场缺陷（操作者报「7 格蓝迪 克雷没有修复」= 格兰迪发电站 104 / 克雷发电站 101
// 清完房间不开下一间的门）：这两张 DGN 的 maze 0 都**显式**声明了
//
//	[map specification] `map` 0 0 42001 …      （104，dungeon/Act9/grandine.dgn:121）
//	[map specification] `map` 0 0 42101 …      （101，dungeon/Act9/KohlePowerStation.dgn:475）
//
// 而改动前的 `resolveCandidateSet`（本文件 `ownershipRoomCandidates` 那段）对
// **非任务连通迷宫的起始/首领房**把"同目录 + 同 `[dungeon]` + 文件名像 start/boss"的
// 启发式池排在 `[map specification]` **前面**，于是起始房被解析成
// `map/grandine/15424quest(0,0)start.map`（地图 id 15424）/
// `map/KohlePowerStation/18000(0,0)S.map`（id 18000）—— 这两张图**没有**
// `[pathgate recognize range]` 段（真档 42001/42101 有 `0 0 130 140 0 0 0 0`），
// 客户端因此拿不到该房间可用门的触发矩形，清房后门不开。
//
// 默认档 = 显式声明优先（修复档）；`ownership` = 改动前口径（一键回退）。
type StartMapPrecedence string

const (
	// StartMapPrecedenceSpecification 是默认档（也是零值）：PVF 的
	// `[map specification]` / `[boss map]` 显式声明的 map 优先，
	// 文件名启发式只在显式声明读不到时兜底。
	StartMapPrecedenceSpecification StartMapPrecedence = "specification"
	// StartMapPrecedenceOwnership 是改动前口径：非任务连通迷宫的起始/首领房
	// 先按同副本/同目录的文件名启发式解析，逐字节回退用。
	StartMapPrecedenceOwnership StartMapPrecedence = "ownership"
)

// ResolverOptions 是 Resolver 的**显式**配置。零值 = 默认档（显式声明优先），
// 也是所有 `NewResolver` 调用者的今天行为之外的新默认。
type ResolverOptions struct {
	// StartMapPrecedence 归一化后的起始/首领房判据真源。
	StartMapPrecedence StartMapPrecedence
}

// normalize 把未设置/非法值收敛到默认档，只有显式 `ownership` 才回退。
func (o ResolverOptions) normalize() ResolverOptions {
	if strings.EqualFold(strings.TrimSpace(string(o.StartMapPrecedence)),
		string(StartMapPrecedenceOwnership)) {
		o.StartMapPrecedence = StartMapPrecedenceOwnership
		return o
	}
	o.StartMapPrecedence = StartMapPrecedenceSpecification
	return o
}

// preemptsSpecification 报告"文件名启发式是否排在显式声明之前"（= 回退档）。
func (o ResolverOptions) preemptsSpecification() bool {
	return o.StartMapPrecedence == StartMapPrecedenceOwnership
}

type Resolver struct {
	table     *Table
	byDungeon map[int64]dungeonMapIndex
	options   ResolverOptions
}

type coordinateKey struct {
	x int64
	y int64
}

type indexedMap struct {
	mapPos            int
	fileType          MapFileType
	specificationType string
}

type mazeMapIndex struct {
	bySpecification map[coordinateKey][]indexedMap
	byOwnership     map[coordinateKey][]indexedMap
	byCoordinate    map[coordinateKey][]indexedMap
	byPool          map[MapFileType][]indexedMap
	byPath          map[string]indexedMap
	layeredByRoom   map[coordinateKey]map[int]struct{}
	layeredMaps     map[int]struct{}
	storyStages     []indexedStoryStage
	storyMapsByRoom map[coordinateKey]map[int]struct{}
	specifications  int
}

type indexedStoryStage struct {
	coordinate        coordinateKey
	mapPos            int
	specificationType string
}

type dungeonMapIndex struct {
	dungeonPos int
	mazes      []mazeMapIndex
}

var mapCoordinatePattern = regexp.MustCompile(`\((-?\d+)[,.](-?\d+)\)`)

func NewResolver(table *Table) (*Resolver, error) {
	return NewResolverWithOptions(table, ResolverOptions{})
}

// NewResolverWithOptions 与 `NewResolver` 的唯一差别是显式传入口径档位。
// 零值 options 与 `NewResolver` 逐字节同结果（默认 = 显式声明优先）。
func NewResolverWithOptions(table *Table, options ResolverOptions) (*Resolver, error) {
	if table == nil {
		return nil, ErrResolverTableRequired
	}
	options = options.normalize()
	resolver := &Resolver{
		table:     table,
		byDungeon: make(map[int64]dungeonMapIndex, len(table.dungeons)),
		options:   options,
	}
	mapsByDirectory := indexMapsByDirectory(table.maps)
	mapsByDungeon := indexMapsByDungeon(table.maps)
	for dungeonPos, dungeon := range table.dungeons {
		if _, exists := resolver.byDungeon[dungeon.ID]; exists {
			continue
		}
		index := dungeonMapIndex{dungeonPos: dungeonPos, mazes: make([]mazeMapIndex, len(dungeon.Mazes))}
		for mazePos, maze := range dungeon.Mazes {
			mazeIndex, err := buildMazeMapIndex(table, dungeon, maze, mapsByDirectory, mapsByDungeon[dungeon.ID])
			if err != nil {
				return nil, err
			}
			index.mazes[mazePos] = mazeIndex
		}
		resolver.byDungeon[dungeon.ID] = index
	}
	return resolver, nil
}

func buildMazeMapIndex(
	table *Table,
	dungeon Dungeon,
	maze Maze,
	mapsByDirectory map[string][]int,
	ownedMaps []int,
) (mazeMapIndex, error) {
	index := mazeMapIndex{
		bySpecification: make(map[coordinateKey][]indexedMap),
		byOwnership:     make(map[coordinateKey][]indexedMap),
		byCoordinate:    make(map[coordinateKey][]indexedMap),
		byPool:          make(map[MapFileType][]indexedMap),
		byPath:          make(map[string]indexedMap),
		layeredByRoom:   make(map[coordinateKey]map[int]struct{}),
		layeredMaps:     make(map[int]struct{}),
		storyMapsByRoom: make(map[coordinateKey]map[int]struct{}),
	}
	if err := buildMazeStoryStageIndex(table, dungeon, maze, &index); err != nil {
		return mazeMapIndex{}, err
	}
	directories := make(map[string]struct{})
	for _, specification := range maze.MapSpecifications {
		if isLayeredSpecification(specification) {
			if err := addLayeredSpecificationToMazeIndex(table, dungeon, maze, specification, &index, directories); err != nil {
				return mazeMapIndex{}, err
			}
			continue
		}
		if err := addSpecificationToMazeIndex(table, dungeon, maze, specification, &index, directories); err != nil {
			return mazeMapIndex{}, err
		}
	}
	for _, specification := range maze.BossSpecifications {
		if isLayeredSpecification(specification) {
			if err := addLayeredSpecificationToMazeIndex(table, dungeon, maze, specification, &index, directories); err != nil {
				return mazeMapIndex{}, err
			}
			continue
		}
		if err := addSpecificationToMazeIndex(table, dungeon, maze, specification, &index, directories); err != nil {
			return mazeMapIndex{}, err
		}
	}
	for _, specification := range maze.LayeredSpecifications {
		if err := addLayeredSpecificationToMazeIndex(table, dungeon, maze, specification, &index, directories); err != nil {
			return mazeMapIndex{}, err
		}
	}
	filterStoryStageSpecificationCandidates(&index)
	for directory := range directories {
		for _, mapPos := range mapsByDirectory[directory] {
			mapValue := table.maps[mapPos]
			ref := indexedMap{mapPos: mapPos, fileType: ClassifyMapFileType(mapValue.Path)}
			index.byPath[pathKey(mapValue.Path)] = ref
			x, y, ok := ParseMapFileCoordinate(mapValue.Path)
			if !ok {
				continue
			}
			key := coordinateKey{x: x, y: y}
			if index.isLayeredMapAt(key, mapPos) {
				continue
			}
			if index.isStoryMapAt(key, mapPos) {
				continue
			}
			index.byCoordinate[key] = appendUniqueIndexedMap(index.byCoordinate[key], ref)
		}
	}
	for _, mapPos := range ownedMaps {
		mapValue := table.maps[mapPos]
		ref := indexedMap{mapPos: mapPos, fileType: ClassifyMapFileType(mapValue.Path)}
		index.byPath[pathKey(mapValue.Path)] = ref
		x, y, ok := ParseMapFileCoordinate(mapValue.Path)
		if !ok {
			if _, layered := index.layeredMaps[mapPos]; layered {
				continue
			}
			index.byPool[ref.fileType] = appendUniqueIndexedMap(index.byPool[ref.fileType], ref)
			continue
		}
		key := coordinateKey{x: x, y: y}
		if index.isLayeredMapAt(key, mapPos) {
			continue
		}
		if index.isStoryMapAt(key, mapPos) {
			continue
		}
		index.byOwnership[key] = appendUniqueIndexedMap(index.byOwnership[key], ref)
	}
	return index, nil
}

func buildMazeStoryStageIndex(
	table *Table,
	dungeon Dungeon,
	maze Maze,
	index *mazeMapIndex,
) error {
	coordinates := mazeBossCoordinates(maze.Boss)
	if len(coordinates) < 2 || !hasRepeatedRoomCoordinate(coordinates) || !mazeQuestConnected(maze) {
		return nil
	}
	specificationsByRoom := make(map[coordinateKey][]MapSpecification)
	appendSpecifications := func(specifications []MapSpecification) {
		for _, specification := range specifications {
			if isLayeredSpecification(specification) || len(specification.MapIDs) == 0 {
				continue
			}
			key := coordinateKey{x: specification.Coordinate.X, y: specification.Coordinate.Y}
			specificationsByRoom[key] = append(specificationsByRoom[key], specification)
		}
	}
	appendSpecifications(maze.MapSpecifications)
	appendSpecifications(maze.BossSpecifications)
	used := make(map[coordinateKey]int)
	stages := make([]indexedStoryStage, 0, len(coordinates))
	for _, coordinate := range coordinates {
		key := coordinateKey{x: coordinate.X, y: coordinate.Y}
		position := used[key]
		specifications := specificationsByRoom[key]
		if position >= len(specifications) {
			return nil
		}
		specification := specifications[position]
		used[key] = position + 1
		mapID := specification.MapIDs[0]
		mapPos, ok := table.mapByID[mapID]
		if !ok {
			return fmt.Errorf(
				"%w: dungeon=%d maze=%d story_stage=%d coordinate=(%d,%d) map_id=%d",
				ErrMapReferenceMissing, dungeon.ID, maze.Index, len(stages), key.x, key.y, mapID,
			)
		}
		stages = append(stages, indexedStoryStage{
			coordinate: key, mapPos: mapPos, specificationType: specification.Type,
		})
	}
	for _, stage := range stages {
		if index.storyMapsByRoom[stage.coordinate] == nil {
			index.storyMapsByRoom[stage.coordinate] = make(map[int]struct{})
		}
		index.storyMapsByRoom[stage.coordinate][stage.mapPos] = struct{}{}
	}
	index.storyStages = stages
	return nil
}

func hasRepeatedRoomCoordinate(coordinates []RoomCoordinate) bool {
	seen := make(map[RoomCoordinate]struct{}, len(coordinates))
	for _, coordinate := range coordinates {
		if _, ok := seen[coordinate]; ok {
			return true
		}
		seen[coordinate] = struct{}{}
	}
	return false
}

func filterStoryStageSpecificationCandidates(index *mazeMapIndex) {
	if index == nil || len(index.storyStages) == 0 {
		return
	}
	firstStageByRoom := make(map[coordinateKey]int)
	for _, stage := range index.storyStages {
		if _, exists := firstStageByRoom[stage.coordinate]; !exists {
			firstStageByRoom[stage.coordinate] = stage.mapPos
		}
	}
	for key, storyMaps := range index.storyMapsByRoom {
		candidates := index.bySpecification[key]
		ordinary := make([]indexedMap, 0, len(candidates))
		for _, candidate := range candidates {
			if _, story := storyMaps[candidate.mapPos]; !story {
				ordinary = appendUniqueIndexedMap(ordinary, candidate)
			}
		}
		if len(ordinary) != 0 {
			index.bySpecification[key] = ordinary
			continue
		}
		fallbackMapPos, ok := firstStageByRoom[key]
		if !ok {
			continue
		}
		for _, candidate := range candidates {
			if candidate.mapPos == fallbackMapPos {
				index.bySpecification[key] = []indexedMap{candidate}
				break
			}
		}
	}
}

func isLayeredSpecification(specification MapSpecification) bool {
	return sectionKey(strings.Trim(specification.Type, "[]")) == "layered"
}

func addLayeredSpecificationToMazeIndex(
	table *Table,
	dungeon Dungeon,
	maze Maze,
	specification MapSpecification,
	index *mazeMapIndex,
	directories map[string]struct{},
) error {
	key := coordinateKey{x: specification.Coordinate.X, y: specification.Coordinate.Y}
	index.specifications++
	if index.layeredByRoom[key] == nil {
		index.layeredByRoom[key] = make(map[int]struct{})
	}
	for _, mapID := range specification.MapIDs {
		mapPos, ok := table.mapByID[mapID]
		if !ok {
			return fmt.Errorf(
				"%w: dungeon=%d maze=%d coordinate=(%d,%d) map_id=%d",
				ErrMapReferenceMissing, dungeon.ID, maze.Index, key.x, key.y, mapID,
			)
		}
		index.layeredByRoom[key][mapPos] = struct{}{}
		index.layeredMaps[mapPos] = struct{}{}
		directory := pathKey(path.Dir(strings.ReplaceAll(table.maps[mapPos].Path, "\\", "/")))
		if directory != "" && directory != "." {
			directories[directory] = struct{}{}
		}
	}
	return nil
}

func (index mazeMapIndex) isLayeredMapAt(key coordinateKey, mapPos int) bool {
	_, ok := index.layeredByRoom[key][mapPos]
	return ok
}

func (index mazeMapIndex) isStoryMapAt(key coordinateKey, mapPos int) bool {
	_, ok := index.storyMapsByRoom[key][mapPos]
	return ok
}

func indexMapsByDirectory(maps []Map) map[string][]int {
	out := make(map[string][]int)
	for mapPos, mapValue := range maps {
		directory := pathKey(path.Dir(strings.ReplaceAll(mapValue.Path, "\\", "/")))
		out[directory] = append(out[directory], mapPos)
	}
	return out
}

func indexMapsByDungeon(maps []Map) map[int64][]int {
	out := make(map[int64][]int)
	for mapPos, mapValue := range maps {
		for _, dungeonID := range mapValue.DungeonIDs {
			out[dungeonID] = append(out[dungeonID], mapPos)
		}
	}
	return out
}

func addSpecificationToMazeIndex(
	table *Table,
	dungeon Dungeon,
	maze Maze,
	specification MapSpecification,
	index *mazeMapIndex,
	directories map[string]struct{},
) error {
	key := coordinateKey{x: specification.Coordinate.X, y: specification.Coordinate.Y}
	index.specifications++
	for _, mapID := range specification.MapIDs {
		mapPos, ok := table.mapByID[mapID]
		if !ok {
			return fmt.Errorf(
				"%w: dungeon=%d maze=%d coordinate=(%d,%d) map_id=%d",
				ErrMapReferenceMissing, dungeon.ID, maze.Index, key.x, key.y, mapID,
			)
		}
		mapValue := table.maps[mapPos]
		ref := indexedMap{
			mapPos: mapPos, fileType: specificationFileType(specification.Type),
			specificationType: specification.Type,
		}
		index.bySpecification[key] = appendUniqueIndexedMap(index.bySpecification[key], ref)
		index.byPath[pathKey(mapValue.Path)] = ref
		directory := pathKey(path.Dir(strings.ReplaceAll(mapValue.Path, "\\", "/")))
		if directory != "" && directory != "." {
			directories[directory] = struct{}{}
		}
	}
	return nil
}

func (r *Resolver) Resolve(request ResolveRequest) (ResolvedMap, error) {
	candidates, source, err := r.resolveCandidateSet(request)
	if err != nil {
		return ResolvedMap{}, err
	}
	return r.resolveCandidates(request, candidates, source)
}

// ResolveLayered resolves exactly one PVF-declared layered map at a maze
// coordinate. layerIndex is positional in the specification's MapIDs list;
// directory ownership and filename-coordinate fallbacks are deliberately not
// consulted. Both inline `layered` rows in [map specification] and typed
// [layered map specification] rows are explicit PVF declarations.
func (r *Resolver) ResolveLayered(
	dungeonID int64,
	mazeIndex int,
	coordinate RoomCoordinate,
	layerIndex int,
) (ResolvedMap, error) {
	if r == nil || r.table == nil {
		return ResolvedMap{}, ErrResolverTableRequired
	}
	dungeonIndex, ok := r.byDungeon[dungeonID]
	if !ok {
		return ResolvedMap{}, fmt.Errorf("%w: dungeon=%d", ErrDungeonNotIndexed, dungeonID)
	}
	if mazeIndex < 0 || mazeIndex >= len(dungeonIndex.mazes) {
		return ResolvedMap{}, fmt.Errorf("%w: dungeon=%d maze=%d", ErrMazeNotFound, dungeonID, mazeIndex)
	}
	dungeon := r.table.dungeons[dungeonIndex.dungeonPos]
	maze := dungeon.Mazes[mazeIndex]
	specifications := explicitLayeredSpecificationsAt(maze, coordinate)
	if len(specifications) == 0 {
		return ResolvedMap{}, fmt.Errorf(
			"%w: dungeon=%d maze=%d coordinate=%s",
			ErrLayeredSpecMissing,
			dungeonID,
			mazeIndex,
			coordinate,
		)
	}
	if len(specifications) != 1 {
		return ResolvedMap{}, fmt.Errorf(
			"%w: dungeon=%d maze=%d coordinate=%s specifications=%d",
			ErrLayeredSpecAmbiguous,
			dungeonID,
			mazeIndex,
			coordinate,
			len(specifications),
		)
	}
	specification := specifications[0]
	if layerIndex < 0 || layerIndex >= len(specification.MapIDs) {
		return ResolvedMap{}, fmt.Errorf(
			"%w: dungeon=%d maze=%d coordinate=%s layer=%d count=%d",
			ErrLayeredIndexInvalid,
			dungeonID,
			mazeIndex,
			coordinate,
			layerIndex,
			len(specification.MapIDs),
		)
	}
	mapID := specification.MapIDs[layerIndex]
	mapPos, ok := r.table.mapByID[mapID]
	if !ok {
		return ResolvedMap{}, fmt.Errorf(
			"%w: dungeon=%d maze=%d coordinate=%s layer=%d map_id=%d",
			ErrMapReferenceMissing,
			dungeonID,
			mazeIndex,
			coordinate,
			layerIndex,
			mapID,
		)
	}
	return ResolvedMap{
		Map:               cloneMap(r.table.maps[mapPos]),
		Source:            ResolutionMapSpecification,
		SpecificationType: "layered",
	}, nil
}

// StoryStages returns the ordered activity-story candidate chain declared by
// the active maze. A maze is treated as staged only when its quest-connected
// [boss map] sequence repeats a coordinate and every occurrence can be paired
// with a matching non-layered map specification in declaration order.
func (r *Resolver) StoryStages(dungeonID int64, mazeIndex int) ([]DungeonStoryStage, error) {
	index, err := r.mazeIndex(dungeonID, mazeIndex)
	if err != nil {
		return nil, err
	}
	if len(index.storyStages) == 0 {
		return nil, fmt.Errorf("%w: dungeon=%d maze=%d", ErrStoryStageMissing, dungeonID, mazeIndex)
	}
	out := make([]DungeonStoryStage, len(index.storyStages))
	for stageIndex, stage := range index.storyStages {
		out[stageIndex] = DungeonStoryStage{
			Index:             stageIndex,
			Coordinate:        RoomCoordinate{X: stage.coordinate.x, Y: stage.coordinate.y},
			MapID:             r.table.maps[stage.mapPos].ID,
			SpecificationType: stage.specificationType,
		}
	}
	return out, nil
}

func (r *Resolver) ResolveStoryStage(dungeonID int64, mazeIndex int, stageIndex int) (ResolvedMap, DungeonStoryStage, error) {
	index, err := r.mazeIndex(dungeonID, mazeIndex)
	if err != nil {
		return ResolvedMap{}, DungeonStoryStage{}, err
	}
	if len(index.storyStages) == 0 {
		return ResolvedMap{}, DungeonStoryStage{}, fmt.Errorf(
			"%w: dungeon=%d maze=%d", ErrStoryStageMissing, dungeonID, mazeIndex,
		)
	}
	if stageIndex < 0 || stageIndex >= len(index.storyStages) {
		return ResolvedMap{}, DungeonStoryStage{}, fmt.Errorf(
			"%w: dungeon=%d maze=%d stage=%d count=%d",
			ErrStoryStageIndexInvalid, dungeonID, mazeIndex, stageIndex, len(index.storyStages),
		)
	}
	stage := index.storyStages[stageIndex]
	descriptor := DungeonStoryStage{
		Index:             stageIndex,
		Coordinate:        RoomCoordinate{X: stage.coordinate.x, Y: stage.coordinate.y},
		MapID:             r.table.maps[stage.mapPos].ID,
		SpecificationType: stage.specificationType,
	}
	return ResolvedMap{
		Map:               cloneMap(r.table.maps[stage.mapPos]),
		Source:            ResolutionMapSpecification,
		SpecificationType: stage.specificationType,
	}, descriptor, nil
}

func explicitLayeredSpecificationsAt(maze Maze, coordinate RoomCoordinate) []MapSpecification {
	specifications := make([]MapSpecification, 0, 1)
	appendMatching := func(values []MapSpecification, requireLayeredType bool) {
		for _, specification := range values {
			if requireLayeredType && sectionKey(strings.Trim(specification.Type, "[]")) != "layered" {
				continue
			}
			if specification.Coordinate.X != coordinate.X || specification.Coordinate.Y != coordinate.Y {
				continue
			}
			specifications = append(specifications, specification)
		}
	}
	appendMatching(maze.MapSpecifications, true)
	appendMatching(maze.LayeredSpecifications, false)
	return specifications
}

func (r *Resolver) ResolveCandidates(request ResolveRequest) ([]ResolvedMap, error) {
	candidates, source, err := r.resolveCandidateSet(request)
	if err != nil {
		return nil, err
	}
	candidates = uniqueIndexedMaps(candidates)
	out := make([]ResolvedMap, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, ResolvedMap{
			Map:               cloneMap(r.table.maps[candidate.mapPos]),
			Source:            source,
			SpecificationType: candidate.specificationType,
		})
	}
	return out, nil
}

func (r *Resolver) resolveCandidateSet(request ResolveRequest) ([]indexedMap, ResolutionSource, error) {
	if r == nil || r.table == nil {
		return nil, "", ErrResolverTableRequired
	}
	dungeonIndex, ok := r.byDungeon[request.DungeonID]
	if !ok {
		return nil, "", fmt.Errorf("%w: dungeon=%d", ErrDungeonNotIndexed, request.DungeonID)
	}
	if request.MazeIndex < 0 || request.MazeIndex >= len(dungeonIndex.mazes) {
		return nil, "", fmt.Errorf("%w: dungeon=%d maze=%d", ErrMazeNotFound, request.DungeonID, request.MazeIndex)
	}
	dungeon := r.table.dungeons[dungeonIndex.dungeonPos]
	maze := dungeon.Mazes[request.MazeIndex]
	index := dungeonIndex.mazes[request.MazeIndex]
	key := coordinateKey{x: request.X, y: request.Y}
	expectedType := expectedMapFileType(maze, key)

	// 起始/首领房的两条真源之间的优先级。
	//
	// **修复档只吃一种形状**：改动前那条文件名启发式要发的房间地图
	// **整段没有** `[pathgate recognize range]`（= 这个房间里一扇可用门都没声明），
	// 而 PVF 为同一房间**显式声明**的地图里恰好有一张声明了该段。
	// 真档证据（`DNF_WORLDMAP_REAL_PVF_SMOKE`）：
	//
	//	dungeon 104（dungeon/Act9/grandine.dgn）maze 0 room (0,0)：
	//	  启发式 = 15424 `grandine/15424quest(0,0)start.map`（**无**该段）
	//	  显式声明 = 42001 `grandine/42001.map`（`0 0 130 140 0 0 0 0`）
	//	dungeon 101（dungeon/Act9/KohlePowerStation.dgn）maze 0 room (0,0)：
	//	  启发式 = 18000 `KohlePowerStation/18000(0,0)S.map`（**无**该段）
	//	  显式声明 = 42101 `KohlePowerStation/42101.map`（`0 0 130 140 0 0 0 0`）
	//
	// 客户端靠这张表的矩形才能走进下一间房：发了没有该段的图，清完房间门就不开
	// （操作者「7 格蓝迪 克雷没有修复」）。其余每一间**逐字节保持改动前口径**，
	// 由真档全量审计 `TestRealPVFStartMapPrecedenceKeepsEveryResolvableRoomResolvable`
	// 钉住（改动前后没有任何房间从可解析变成不可解析，且只有"缺门声明"这一种形状换图）。
	if !r.options.preemptsSpecification() {
		if candidates, ok := r.doorBearingSpecificationRoom(index, maze, key, expectedType); ok {
			return candidates, ResolutionMapSpecification, nil
		}
	}
	if candidates, source, ok := ownershipRoomCandidates(index, maze, key, expectedType); ok {
		return candidates, source, nil
	}
	if candidates := selectSpecificationCandidates(index.bySpecification[key], maze, key); len(candidates) > 0 {
		return candidates, ResolutionMapSpecification, nil
	}
	if candidates := exactFileTypeCandidates(index.byOwnership[key], expectedType); len(candidates) > 0 {
		return candidates, ResolutionDungeonOwnership, nil
	}
	var exact []indexedMap
	for _, candidate := range index.byCoordinate[key] {
		if candidate.fileType == expectedType {
			exact = appendUniqueIndexedMap(exact, candidate)
		}
	}
	if len(exact) > 0 {
		return exact, ResolutionDirectoryPath, nil
	}
	start, boss := mazeRoomRoles(maze, key)
	for _, fallbackType := range startBossCoordinateFallbackFileTypes(expectedType, start, boss) {
		if candidates := exactFileTypeCandidates(index.byOwnership[key], fallbackType); len(candidates) > 0 {
			return candidates, ResolutionDungeonOwnership, nil
		}
		if candidates := exactFileTypeCandidates(index.byCoordinate[key], fallbackType); len(candidates) > 0 {
			return candidates, ResolutionDirectoryPath, nil
		}
	}
	return nil, "", fmt.Errorf(
		"%w: dungeon=%d maze=%d coordinate=(%d,%d) expected_type=%s",
		ErrMapUnresolved, request.DungeonID, request.MazeIndex, request.X, request.Y, expectedType,
	)
}

type MapPoolRequest struct {
	DungeonID int64       `json:"dungeon_id"`
	MazeIndex int         `json:"maze_index"`
	FileType  MapFileType `json:"file_type"`
}

func (r *Resolver) PoolCandidates(request MapPoolRequest) ([]Map, error) {
	index, err := r.mazeIndex(request.DungeonID, request.MazeIndex)
	if err != nil {
		return nil, err
	}
	refs := uniqueIndexedMaps(index.byPool[request.FileType])
	if len(refs) == 0 {
		return nil, fmt.Errorf(
			"%w: dungeon=%d maze=%d file_type=%s",
			ErrMapPoolEmpty, request.DungeonID, request.MazeIndex, request.FileType,
		)
	}
	out := make([]Map, 0, len(refs))
	for _, ref := range refs {
		out = append(out, cloneMap(r.table.maps[ref.mapPos]))
	}
	return out, nil
}

func (r *Resolver) ResolvePoolChoice(request MapPoolRequest, mapID int64) (ResolvedMap, error) {
	candidates, err := r.PoolCandidates(request)
	if err != nil {
		return ResolvedMap{}, err
	}
	for _, candidate := range candidates {
		if candidate.ID == mapID {
			return ResolvedMap{Map: candidate, Source: ResolutionTypePool}, nil
		}
	}
	return ResolvedMap{}, fmt.Errorf(
		"%w: dungeon=%d maze=%d file_type=%s map_id=%d",
		ErrMapPoolChoiceInvalid, request.DungeonID, request.MazeIndex, request.FileType, mapID,
	)
}

func (r *Resolver) mazeIndex(dungeonID int64, mazeIndex int) (mazeMapIndex, error) {
	if r == nil || r.table == nil {
		return mazeMapIndex{}, ErrResolverTableRequired
	}
	dungeonIndex, ok := r.byDungeon[dungeonID]
	if !ok {
		return mazeMapIndex{}, fmt.Errorf("%w: dungeon=%d", ErrDungeonNotIndexed, dungeonID)
	}
	if mazeIndex < 0 || mazeIndex >= len(dungeonIndex.mazes) {
		return mazeMapIndex{}, fmt.Errorf("%w: dungeon=%d maze=%d", ErrMazeNotFound, dungeonID, mazeIndex)
	}
	return dungeonIndex.mazes[mazeIndex], nil
}

// doorBearingSpecificationRoom 是修复档的**唯一**覆盖判据：
// 「改动前那条文件名启发式要发的房间地图一张都没声明 `[pathgate recognize range]`
// （= 玩家在这个房间里没有任何可走通的门），而 PVF 为同一房间显式声明的地图里
// 恰好有一张声明了该段」⇒ 用显式声明的那张。
//
// 判据里两个条件都必要：
//   - 只对非任务连通迷宫的起始/首领房生效（与 `ownershipRoomCandidates` 同一适用范围）；
//   - 启发式侧必须**全部**缺该段，否则连"缺门声明"这个可观测缺陷都不存在，
//     没有理由推翻改动前的选择。
//
// 地图没加载（`Options{SkipMaps: true}` 之类）时两边的 `Set` 都是 false ⇒
// 判据不成立 ⇒ 逐字节走改动前路径。这是刻意的 fail-safe：读不到门数据就不改行为。
func (r *Resolver) doorBearingSpecificationRoom(
	index mazeMapIndex,
	maze Maze,
	key coordinateKey,
	expectedType MapFileType,
) ([]indexedMap, bool) {
	heuristic, _, ok := ownershipRoomCandidates(index, maze, key, expectedType)
	if !ok {
		return nil, false
	}
	for _, candidate := range heuristic {
		if r.mapDeclaresPathgateRange(candidate) {
			return nil, false
		}
	}
	var withDoors []indexedMap
	for _, candidate := range selectSpecificationCandidates(index.bySpecification[key], maze, key) {
		if r.mapDeclaresPathgateRange(candidate) {
			withDoors = appendUniqueIndexedMap(withDoors, candidate)
		}
	}
	if len(withDoors) != 1 {
		return nil, false
	}
	return withDoors, true
}

// mapDeclaresPathgateRange 报告这张图有没有声明 `[pathgate recognize range]`。
// 该段就是"这个房间里哪几扇门可用"的矩形表；整段缺失 = 一扇可用门都没有。
func (r *Resolver) mapDeclaresPathgateRange(candidate indexedMap) bool {
	if r == nil || r.table == nil || candidate.mapPos < 0 || candidate.mapPos >= len(r.table.maps) {
		return false
	}
	return r.table.maps[candidate.mapPos].PathgateRecognizeRange.Set
}

// ownershipRoomCandidates 是"同副本 + 同目录文件名"启发式池，**只**对非任务连通
// 迷宫的起始房/首领房生效（`expectedType` 为 start/boss 且 maze 没有 `[quest connection]`）。
//
// 这段启发式从改动前就在，默认档把它从"显式声明之前"挪到"显式声明之后"：
// 104/101 的起始房正是被它从 PVF 显式声明的 42001/42101 抢走，换成同目录里
// 文件名含 start 的 15424/18000（两张图都缺 `[pathgate recognize range]`，
// 客户端因此打不开门）。原实现整段照抄在下面两个分支里，回退档逐字节同旧行为。
func ownershipRoomCandidates(
	index mazeMapIndex,
	maze Maze,
	key coordinateKey,
	expectedType MapFileType,
) ([]indexedMap, ResolutionSource, bool) {
	if mazeQuestConnected(maze) || (expectedType != MapFileStart && expectedType != MapFileBoss) {
		return nil, "", false
	}
	if candidates := exactFileTypeCandidates(index.byOwnership[key], expectedType); len(candidates) > 0 {
		return candidates, ResolutionDungeonOwnership, true
	}
	if candidates := exactFileTypeCandidates(index.byCoordinate[key], expectedType); len(candidates) > 0 {
		return candidates, ResolutionDirectoryPath, true
	}
	return nil, "", false
}

func exactFileTypeCandidates(candidates []indexedMap, expectedType MapFileType) []indexedMap {
	var exact []indexedMap
	for _, candidate := range candidates {
		if candidate.fileType == expectedType {
			exact = appendUniqueIndexedMap(exact, candidate)
		}
	}
	return exact
}

func (r *Resolver) TryResolve(request ResolveRequest) (ResolvedMap, bool) {
	resolved, err := r.Resolve(request)
	return resolved, err == nil
}

func (r *Resolver) ResolvePath(dungeonID int64, mazeIndex int, mapPath string) (Map, bool) {
	if r == nil || r.table == nil {
		return Map{}, false
	}
	dungeonIndex, ok := r.byDungeon[dungeonID]
	if !ok || mazeIndex < 0 || mazeIndex >= len(dungeonIndex.mazes) {
		return Map{}, false
	}
	ref, ok := dungeonIndex.mazes[mazeIndex].byPath[pathKey(mapPath)]
	if !ok {
		return Map{}, false
	}
	return cloneMap(r.table.maps[ref.mapPos]), true
}

func (r *Resolver) Snapshot(dungeonID int64) (DungeonMapIndexSnapshot, bool) {
	if r == nil {
		return DungeonMapIndexSnapshot{}, false
	}
	index, ok := r.byDungeon[dungeonID]
	if !ok {
		return DungeonMapIndexSnapshot{}, false
	}
	snapshot := DungeonMapIndexSnapshot{DungeonID: dungeonID, Mazes: len(index.mazes)}
	for _, maze := range index.mazes {
		snapshot.Specifications += maze.specifications
		for _, entries := range maze.byOwnership {
			snapshot.OwnershipEntries += len(entries)
		}
		for _, entries := range maze.byCoordinate {
			snapshot.CoordinateEntries += len(entries)
		}
		snapshot.PathEntries += len(maze.byPath)
	}
	return snapshot, true
}

func (r *Resolver) resolveCandidates(request ResolveRequest, candidates []indexedMap, source ResolutionSource) (ResolvedMap, error) {
	candidates = uniqueIndexedMaps(candidates)
	if len(candidates) != 1 {
		ids := make([]int64, 0, len(candidates))
		for _, candidate := range candidates {
			ids = append(ids, r.table.maps[candidate.mapPos].ID)
		}
		return ResolvedMap{}, fmt.Errorf(
			"%w: dungeon=%d maze=%d coordinate=(%d,%d) map_ids=%v",
			ErrMapAmbiguous, request.DungeonID, request.MazeIndex, request.X, request.Y, ids,
		)
	}
	candidate := candidates[0]
	return ResolvedMap{
		Map: cloneMap(r.table.maps[candidate.mapPos]), Source: source,
		SpecificationType: candidate.specificationType,
	}, nil
}

func selectSpecificationCandidates(candidates []indexedMap, maze Maze, key coordinateKey) []indexedMap {
	if len(candidates) == 0 {
		return nil
	}
	expected := expectedMapFileType(maze, key)
	var typed []indexedMap
	for _, candidate := range candidates {
		if candidate.fileType == expected {
			typed = appendUniqueIndexedMap(typed, candidate)
		}
	}
	if len(typed) > 0 {
		return typed
	}
	return uniqueIndexedMaps(candidates)
}

func expectedMapFileType(maze Maze, key coordinateKey) MapFileType {
	if maze.Boss != nil && maze.Boss.X == key.x && maze.Boss.Y == key.y {
		return MapFileBoss
	}
	if maze.Start != nil && maze.Start.X == key.x && maze.Start.Y == key.y {
		return MapFileStart
	}
	if mazeQuestConnected(maze) {
		return MapFileQuest
	}
	return MapFileNormal
}

func mazeRoomRoles(maze Maze, key coordinateKey) (start bool, boss bool) {
	start = maze.Start != nil && maze.Start.X == key.x && maze.Start.Y == key.y
	for _, coordinate := range mazeBossCoordinates(maze.Boss) {
		if coordinate.X == key.x && coordinate.Y == key.y {
			boss = true
			break
		}
	}
	return start, boss
}

func startBossCoordinateFallbackFileTypes(fileType MapFileType, start bool, boss bool) []MapFileType {
	if fileType != MapFileBoss || !start || !boss {
		return nil
	}
	// The production training-room PVF marks its only coordinate as both
	// start and boss while the only coordinate-owned map is explicitly named
	// start. Exact boss/specification matches have already won before this
	// narrow compatibility fallback.
	return []MapFileType{MapFileStart}
}

func mazeQuestConnected(maze Maze) bool {
	return len(maze.QuestConnection) >= 2
}

func specificationFileType(value string) MapFileType {
	switch sectionKey(strings.Trim(value, "[]")) {
	case "start":
		return MapFileStart
	case "boss":
		return MapFileBoss
	case "named":
		return MapFileNamed
	case "end":
		return MapFileEnd
	case "hidden":
		return MapFileHidden
	case "quest":
		return MapFileQuest
	case "default":
		return MapFileDefault
	default:
		return MapFileNormal
	}
}

func ParseMapFileCoordinate(mapPath string) (int64, int64, bool) {
	stem := strings.TrimSuffix(path.Base(strings.ReplaceAll(mapPath, "\\", "/")), path.Ext(mapPath))
	match := mapCoordinatePattern.FindStringSubmatch(stem)
	if len(match) != 3 {
		return 0, 0, false
	}
	var x, y int64
	if _, err := fmt.Sscan(match[1], &x); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscan(match[2], &y); err != nil {
		return 0, 0, false
	}
	return x, y, true
}

func ClassifyMapFileType(mapPath string) MapFileType {
	stem := strings.TrimSuffix(path.Base(strings.ReplaceAll(mapPath, "\\", "/")), path.Ext(mapPath))
	lower := strings.ToLower(stem)
	if strings.HasPrefix(lower, "q_") || strings.HasPrefix(lower, "quest") || prefixedDigit(lower, "q") {
		return MapFileQuest
	}
	if strings.HasPrefix(lower, "bn") && len(lower) > 2 && isASCIIDigit(lower[2]) {
		return MapFileNamed
	}
	if strings.Contains(lower, "boss") || prefixedDigit(lower, "b") || typedSuffix(lower, 'b') {
		return MapFileBoss
	}
	if strings.Contains(lower, "start") || prefixedDigit(lower, "s") || typedSuffix(lower, 's') {
		return MapFileStart
	}
	if prefixedDigit(lower, "e") {
		return MapFileEnd
	}
	if prefixedDigit(lower, "h") {
		return MapFileHidden
	}
	if prefixedDigit(lower, "d") {
		return MapFileDefault
	}
	return MapFileNormal
}

func prefixedDigit(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) > len(prefix) && isASCIIDigit(value[len(prefix)])
}

func typedSuffix(value string, suffix byte) bool {
	if len(value) < 2 || value[len(value)-1] != suffix {
		return false
	}
	previous := value[len(value)-2]
	return isASCIIDigit(previous) || previous == ')'
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func appendUniqueIndexedMap(target []indexedMap, candidate indexedMap) []indexedMap {
	for _, existing := range target {
		if existing.mapPos == candidate.mapPos && existing.fileType == candidate.fileType {
			return target
		}
	}
	return append(target, candidate)
}

func uniqueIndexedMaps(values []indexedMap) []indexedMap {
	var out []indexedMap
	seen := make(map[int]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value.mapPos]; ok {
			continue
		}
		seen[value.mapPos] = struct{}{}
		out = append(out, value)
	}
	return out
}
