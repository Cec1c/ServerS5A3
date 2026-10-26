package worldmap

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// rest25-tower-tests：独立候选测试（候选 = 本文件；父集成时把本文件放进
// go-server/internal/modules/dnf/worldmap/ 即可，不需要改产品代码）。
//
// 被钉住的口径：多房间的绝望之塔楼层，起始房既没有坐标归属也没有显式声明的房图时，
// 允许从**普通池**里挑一张「同副本独占 + 文件名无坐标 + 有出生区 + 地图门位与房间邻居
// 一致」的普通图当起始房（父 helper tower_start_resource_pool.go 的 towerStartResourcePool）。
//
// 实现口径（逐条对照父 helper）：
//   - [tower of despair] 元数据 == 1；
//   - dungeons[].Mazes 恰好 1 个且 mazeIndex == 0；
//   - 无 [quest connection]；maze.Start 指向本房间坐标；房间 Start=true 且 Boss=false；
//   - 房间有邻居（Neighbors 非空）；迷宫不是 1x1 单间；
//   - 房间坐标在 bySpecification / byOwnership 里都为空；
//   - 候选取自 byPool[normal]（= 无坐标 + 有 [dungeon] 归属），且
//     Map.DungeonID.Set && Value == 副本 && len(DungeonIDs)==1 && DungeonIDs[0]==副本；
//   - len(DungeonStartArea)==4 且 [2]>0 且 [3]>0；
//   - Greed 里每个非空字符都解析成同一个非零门 mask，且等于房间邻居推出的 mask；
//   - ID 不在本迷宫的三张 specification 列表（含其它坐标）里；
//   - 恰好 1 个候选才返回，且返回 cloneMap 副本。
//
// 需要的 helper API（尽量公共）：
//   worldmap.NewResolver(table)                                           公共
//   worldmap.BuildDungeonTopology(resolver, dungeonID, mazeIndex)          公共
//   worldmap.BuildDungeonLayout(resolver, dungeonID, mazeIndex, chooser)   公共
//   worldmap.Resolver.PoolCandidates(MapPoolRequest)                       公共（前置：start 池为空）
//   (*Resolver).towerStartResourcePool(dungeonID int64, mazeIndex int, room DungeonRoom) ([]Map, bool)
//       —— 同包私有。只在本文件的「返回克隆/不改原数据」与 helper 级形状拒绝两处直接调用，
//          其余断言全部走公共 BuildDungeonLayout。
//   夹具直接构造同包私有 Table（maps/mapByID/mapByPath/dungeons/dungeonByID/dungeonByPath），
//   与同包既有 dungeon_layout_test.go / resolver_test.go 的做法一致。

const (
	towerFloorID      int64 = 11008
	towerNormalMapID  int64 = 20001
	towerBossMapID    int64 = 20002
	towerOwnedStartID int64 = 20003
	towerSpecStartID  int64 = 20004
	towerPoolStartID  int64 = 20005
	towerSecondMapID  int64 = 20006

	towerFloorPath     = "dungeon/Towers/ZTowerOfDespair001.dgn"
	towerNormalMapPath = "map/towerofdespair_down/15130despair001.map"
)

// towerFloorTable 是三间房的绝望之塔楼层：
//
//	Greed "BFE"（宽 3 高 1）⇒ 房间 (0,0) 右门 0x01、(1,0) 左右门 0x05、(2,0) 左门 0x04，
//	起始房 (0,0) 的邻居只有右侧一处，期望门 mask = 0x01。
//
// 起始房没有任何坐标归属/显式声明；boss 房 (2,0) 有坐标归属的 20002；普通池里只有
// 一张合格的塔起始资源 20001。
func towerFloorTable() *Table {
	maps := []Map{
		towerNormalResource(towerNormalMapID, towerNormalMapPath),
		{
			ID:         towerBossMapID,
			Path:       "map/towerofdespair_down/20002(2,0)boss.map",
			DungeonID:  OptionalInt{Value: towerFloorID, Set: true},
			DungeonIDs: []int64{towerFloorID},
		},
	}
	dungeons := []Dungeon{{
		ID:       towerFloorID,
		Path:     towerFloorPath,
		Metadata: DungeonMetadata{IntegerValues: map[string]int64{towerOfDespairSectionKey: 1}},
		Mazes: []Maze{{
			Index:  0,
			Width:  OptionalInt{Value: 3, Set: true},
			Height: OptionalInt{Value: 1, Set: true},
			Greed:  "BFE",
			Start:  &MazePoint{X: 0, Y: 0},
			Boss:   &MazePoint{X: 2, Y: 0},
		}},
	}}
	table := &Table{
		maps:          maps,
		dungeons:      dungeons,
		mapByID:       make(map[int64]int, len(maps)),
		mapByPath:     make(map[string]int, len(maps)),
		dungeonByID:   map[int64]int{towerFloorID: 0},
		dungeonByPath: map[string]int{pathKey(towerFloorPath): 0},
	}
	for pos, mapValue := range table.maps {
		table.mapByID[mapValue.ID] = pos
		table.mapByPath[pathKey(mapValue.Path)] = pos
	}
	return table
}

// towerNormalResource 是 helper 认下的那种普通资源：同副本独占、文件名无坐标、
// 4 个 [dungeon start area] 数字且宽高 > 0、Greed 全 'B'（= 右门 0x01，与 (0,0)
// 只有右侧邻居一致）。
func towerNormalResource(id int64, mapPath string) Map {
	return Map{
		ID:               id,
		Path:             mapPath,
		Type:             "[normal]",
		DungeonID:        OptionalInt{Value: towerFloorID, Set: true},
		DungeonIDs:       []int64{towerFloorID},
		DungeonStartArea: []int64{0, 0, 130, 140},
		Greed:            "BB",
	}
}

func appendTowerFloorMap(table *Table, mapValue Map) {
	pos := len(table.maps)
	table.maps = append(table.maps, mapValue)
	table.mapByID[mapValue.ID] = pos
	table.mapByPath[pathKey(mapValue.Path)] = pos
}

func towerFloorResolver(t *testing.T, table *Table) *Resolver {
	t.Helper()
	resolver, err := NewResolver(table)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	return resolver
}

func towerStartRoom(t *testing.T, resolver *Resolver) DungeonRoom {
	t.Helper()
	topology, err := BuildDungeonTopology(resolver, towerFloorID, 0)
	if err != nil {
		t.Fatalf("build topology: %v", err)
	}
	if topology.Start == nil {
		t.Fatal("fixture bug: the floor has no [start map]")
	}
	room, ok := topology.Room(*topology.Start)
	if !ok {
		t.Fatalf("fixture bug: start room %s is not a room", *topology.Start)
	}
	return room
}

// towerFirstCandidate 让布局里与本次判据无关的歧义房间（例如负例里普通池有两张图）
// 不抢走失败点：它总是选第一个候选，于是唯一可能失败的就是起始房。
func towerFirstCandidate(choice DungeonMapChoice) (int64, error) {
	if len(choice.Candidates) == 0 {
		return 0, errors.New("no candidates")
	}
	return choice.Candidates[0].ID, nil
}

// TestBuildDungeonLayoutTowerStartPicksNormalResourceForThreeRoomFloor 是正向用例：
// 三间房的塔层，起始房没有坐标/显式房图 ⇒ 公共 BuildDungeonLayout 必须用普通池里的
// 20001 把起始房（Start=true/Boss=false）填上（source = ResolutionTypePool）；
// 中间房走普通池，boss 房走自己的坐标归属。
func TestBuildDungeonLayoutTowerStartPicksNormalResourceForThreeRoomFloor(t *testing.T) {
	table := towerFloorTable()
	resolver := towerFloorResolver(t, table)
	if _, err := resolver.PoolCandidates(MapPoolRequest{DungeonID: towerFloorID, MazeIndex: 0, FileType: MapFileStart}); !errors.Is(err, ErrMapPoolEmpty) {
		t.Fatalf("start pool = %v, want %v (the tower fallback must be the only thing that can fill this room)", err, ErrMapPoolEmpty)
	}

	topology, err := BuildDungeonLayout(resolver, towerFloorID, 0, nil)
	if err != nil {
		t.Fatalf("three-room tower floor layout: %v", err)
	}
	start, ok := topology.Room(RoomCoordinate{X: 0, Y: 0})
	if !ok {
		t.Fatal("start room (0,0) absent")
	}
	if !start.Start || start.Boss {
		t.Fatalf("start room flags = start=%t boss=%t, want true/false", start.Start, start.Boss)
	}
	if start.Map == nil || start.Map.Map.ID != towerNormalMapID || start.Map.Source != ResolutionTypePool {
		t.Fatalf("start room map = %+v, want id=%d source=%s", start.Map, towerNormalMapID, ResolutionTypePool)
	}
	if area := start.Map.Map.DungeonStartArea; len(area) != 4 || area[2] != 130 || area[3] != 140 {
		t.Fatalf("start room map lost its [dungeon start area]: %v", area)
	}

	middle, ok := topology.Room(RoomCoordinate{X: 1, Y: 0})
	if !ok || middle.Map == nil || middle.Map.Map.ID != towerNormalMapID || middle.Map.Source != ResolutionTypePool {
		t.Fatalf("middle room = %+v ok=%v, want the normal pool resource %d", middle, ok, towerNormalMapID)
	}
	boss, ok := topology.Room(RoomCoordinate{X: 2, Y: 0})
	if !ok || boss.Map == nil || boss.Map.Map.ID != towerBossMapID || boss.Map.Source != ResolutionDungeonOwnership {
		t.Fatalf("boss room = %+v ok=%v, want id=%d source=%s", boss, ok, towerBossMapID, ResolutionDungeonOwnership)
	}
	if len(topology.UnresolvedRooms()) != 0 || len(topology.resolutionErrors) != 0 {
		t.Fatalf("unresolved=%+v errors=%+v", topology.UnresolvedRooms(), topology.resolutionErrors)
	}
}

// TestBuildDungeonLayoutTowerStartRefusesUnqualifiedResources 逐条钉「不能回退」：
// 每一条都让普通池那张资源失去一个必要条件，公共 BuildDungeonLayout 必须拒绝回退，
// 失败点必须是起始房（0:0）的 ErrMapPoolEmpty。
func TestBuildDungeonLayoutTowerStartRefusesUnqualifiedResources(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*Table)
	}{
		{"ordinary_dungeon_without_tower_marker", func(table *Table) {
			table.dungeons[0].Metadata.IntegerValues = nil
		}},
		{"tower_marker_not_one", func(table *Table) {
			table.dungeons[0].Metadata.IntegerValues = map[string]int64{towerOfDespairSectionKey: 2}
		}},
		{"candidate_without_start_area", func(table *Table) {
			table.maps[table.mapByID[towerNormalMapID]].DungeonStartArea = nil
		}},
		{"candidate_start_area_wrong_arity", func(table *Table) {
			table.maps[table.mapByID[towerNormalMapID]].DungeonStartArea = []int64{0, 0, 130}
		}},
		{"candidate_start_area_zero_width", func(table *Table) {
			table.maps[table.mapByID[towerNormalMapID]].DungeonStartArea = []int64{0, 0, 0, 140}
		}},
		{"candidate_start_area_zero_height", func(table *Table) {
			table.maps[table.mapByID[towerNormalMapID]].DungeonStartArea = []int64{0, 0, 130, 0}
		}},
		{"candidate_door_mask_mismatch", func(table *Table) {
			table.maps[table.mapByID[towerNormalMapID]].Greed = "CC"
		}},
		{"candidate_door_mask_mixed_letters", func(table *Table) {
			table.maps[table.mapByID[towerNormalMapID]].Greed = "BC"
		}},
		{"candidate_greed_without_door_symbols", func(table *Table) {
			table.maps[table.mapByID[towerNormalMapID]].Greed = "   "
		}},
		{"candidate_without_dungeon_id", func(table *Table) {
			table.maps[table.mapByID[towerNormalMapID]].DungeonID = OptionalInt{}
		}},
		{"candidate_foreign_dungeon_id", func(table *Table) {
			table.maps[table.mapByID[towerNormalMapID]].DungeonID = OptionalInt{Value: towerFloorID - 1, Set: true}
		}},
		{"candidate_shared_owner_dungeon_ids", func(table *Table) {
			table.maps[table.mapByID[towerNormalMapID]].DungeonIDs = []int64{towerFloorID, towerFloorID + 1}
		}},
		{"multiple_qualified_candidates", func(table *Table) {
			appendTowerFloorMap(table, towerNormalResource(towerSecondMapID, "map/towerofdespair_down/20006.map"))
		}},
		{"candidate_bound_at_another_room", func(table *Table) {
			table.dungeons[0].Mazes[0].MapSpecifications = []MapSpecification{{
				Type: "map", Coordinate: Point{X: 1, Y: 0}, MapIDs: []int64{towerNormalMapID},
			}}
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			table := towerFloorTable()
			testCase.apply(table)
			resolver := towerFloorResolver(t, table)
			_, err := BuildDungeonLayout(resolver, towerFloorID, 0, towerFirstCandidate)
			if err == nil {
				t.Fatal("layout accepted the tower fallback for an unqualified resource")
			}
			if !errors.Is(err, ErrMapPoolEmpty) {
				t.Fatalf("refusal = %v, want %v", err, ErrMapPoolEmpty)
			}
			if !strings.Contains(err.Error(), "0:0") {
				t.Fatalf("refusal must come from the start room: %v", err)
			}
		})
	}
}

// TestBuildDungeonLayoutTowerStartKeepsExplicitAndStartPoolPrecedence 钉优先级：
// 坐标归属的起始图、显式 specification 声明的起始图、真正的 start 池，
// 都必须排在塔回退之前（塔资源 20001 一次都不能被选中）。
func TestBuildDungeonLayoutTowerStartKeepsExplicitAndStartPoolPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		apply      func(*Table)
		wantID     int64
		wantSource ResolutionSource
	}{
		{
			"coordinate_owned_start_map_wins",
			func(table *Table) {
				appendTowerFloorMap(table, Map{
					ID:         towerOwnedStartID,
					Path:       "map/towerofdespair_down/20003(0,0)start.map",
					DungeonID:  OptionalInt{Value: towerFloorID, Set: true},
					DungeonIDs: []int64{towerFloorID},
				})
			},
			towerOwnedStartID, ResolutionDungeonOwnership,
		},
		{
			"explicit_specification_start_map_wins",
			func(table *Table) {
				appendTowerFloorMap(table, Map{
					ID:         towerSpecStartID,
					Path:       "map/towerofdespair_down/20004.map",
					Type:       "[hidden]",
					DungeonID:  OptionalInt{Value: towerFloorID, Set: true},
					DungeonIDs: []int64{towerFloorID},
				})
				table.dungeons[0].Mazes[0].MapSpecifications = []MapSpecification{{
					Type: "start", Coordinate: Point{X: 0, Y: 0}, MapIDs: []int64{towerSpecStartID},
				}}
			},
			towerSpecStartID, ResolutionMapSpecification,
		},
		{
			"real_start_pool_wins",
			func(table *Table) {
				appendTowerFloorMap(table, Map{
					ID:         towerPoolStartID,
					Path:       "map/towerofdespair_down/20005.map",
					Type:       "[start]",
					DungeonID:  OptionalInt{Value: towerFloorID, Set: true},
					DungeonIDs: []int64{towerFloorID},
				})
			},
			towerPoolStartID, ResolutionTypePool,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			table := towerFloorTable()
			testCase.apply(table)
			resolver := towerFloorResolver(t, table)
			topology, err := BuildDungeonLayout(resolver, towerFloorID, 0, nil)
			if err != nil {
				t.Fatalf("layout: %v", err)
			}
			start, ok := topology.Room(RoomCoordinate{X: 0, Y: 0})
			if !ok || start.Map == nil {
				t.Fatalf("start room = %+v ok=%v", start, ok)
			}
			if start.Map.Map.ID != testCase.wantID || start.Map.Source != testCase.wantSource {
				t.Fatalf("start room = id=%d source=%s, want id=%d source=%s",
					start.Map.Map.ID, start.Map.Source, testCase.wantID, testCase.wantSource)
			}
			if start.Map.Map.ID == towerNormalMapID {
				t.Fatalf("the tower fallback resource %d overrode %s", towerNormalMapID, testCase.name)
			}
			if len(topology.UnresolvedRooms()) != 0 || len(topology.resolutionErrors) != 0 {
				t.Fatalf("unresolved=%+v errors=%+v", topology.UnresolvedRooms(), topology.resolutionErrors)
			}
		})
	}
}

// TestTowerStartResourcePoolShapeGuardsAndClone 直接钉 helper 自身：
// 返回的是克隆（改写返回值不回写表内原图，重复调用逐值一致），
// 以及公共布局路径覆盖不到的房间/楼层形状拒绝。
func TestTowerStartResourcePoolShapeGuardsAndClone(t *testing.T) {
	table := towerFloorTable()
	resolver := towerFloorResolver(t, table)
	room := towerStartRoom(t, resolver)

	original := table.maps[table.mapByID[towerNormalMapID]]
	wantPath, wantGreed := original.Path, original.Greed
	wantAreas := append([]int64(nil), original.DungeonStartArea...)
	wantOwners := append([]int64(nil), original.DungeonIDs...)

	maps, ok := resolver.towerStartResourcePool(towerFloorID, 0, room)
	if !ok || len(maps) != 1 || maps[0].ID != towerNormalMapID {
		t.Fatalf("helper = %+v ok=%v, want the single qualified resource %d", maps, ok, towerNormalMapID)
	}
	maps[0].Path = "map/mutated/0.map"
	maps[0].Greed = "CC"
	maps[0].DungeonIDs[0] = towerFloorID + 1
	maps[0].DungeonStartArea[0] = 9
	stored := table.maps[table.mapByID[towerNormalMapID]]
	if stored.Path != wantPath || stored.Greed != wantGreed ||
		!reflect.DeepEqual(stored.DungeonStartArea, wantAreas) || !reflect.DeepEqual(stored.DungeonIDs, wantOwners) {
		t.Fatalf("helper result shares storage with the table: %+v", stored)
	}
	again, ok := resolver.towerStartResourcePool(towerFloorID, 0, room)
	if !ok || len(again) != 1 || again[0].Path != wantPath || again[0].Greed != wantGreed ||
		!reflect.DeepEqual(again[0].DungeonStartArea, wantAreas) || !reflect.DeepEqual(again[0].DungeonIDs, wantOwners) {
		t.Fatalf("second helper call = %+v ok=%v, want a fresh clone of %+v", again, ok, original)
	}

	guards := []struct {
		name       string
		mazeIndex  int
		apply      func(*Table)
		mutateRoom func(*DungeonRoom)
	}{
		{"maze_index_one", 1, nil, nil},
		{"boss_room", 0, nil, func(room *DungeonRoom) { room.Boss = true }},
		{"room_at_another_coordinate", 0, nil, func(room *DungeonRoom) {
			room.Coordinate = RoomCoordinate{X: 1, Y: 0}
		}},
		{"room_without_neighbors", 0, nil, func(room *DungeonRoom) { room.Neighbors = nil }},
		{"floor_without_tower_marker", 0, func(table *Table) {
			table.dungeons[0].Metadata.IntegerValues = nil
		}, nil},
		{"floor_with_quest_connection", 0, func(table *Table) {
			table.dungeons[0].Mazes[0].QuestConnection = []int64{1}
		}, nil},
		{"floor_with_two_mazes", 0, func(table *Table) {
			dungeon := &table.dungeons[0]
			second := dungeon.Mazes[0]
			second.Index = 1
			dungeon.Mazes = append(dungeon.Mazes, second)
		}, nil},
		{"single_cell_floor", 0, func(table *Table) {
			// 1x1 楼层只有一间房 ⇒ 邻居必为空。这条拒绝同时覆盖「不是单间」与
			// 「房间要有邻居」两条形状约束（单间层走不到多邻居这一支）。
			maze := &table.dungeons[0].Mazes[0]
			maze.Width = OptionalInt{Value: 1, Set: true}
			maze.Height = OptionalInt{Value: 1, Set: true}
			maze.Greed = "B"
			maze.Boss = nil
		}, nil},
	}
	for _, guard := range guards {
		t.Run(guard.name, func(t *testing.T) {
			guardTable := towerFloorTable()
			if guard.apply != nil {
				guard.apply(guardTable)
			}
			guardResolver := towerFloorResolver(t, guardTable)
			guardRoom := towerStartRoom(t, guardResolver)
			if guard.mutateRoom != nil {
				guard.mutateRoom(&guardRoom)
			}
			if pools, ok := guardResolver.towerStartResourcePool(towerFloorID, guard.mazeIndex, guardRoom); ok {
				t.Fatalf("helper accepted the fallback: %+v", pools)
			}
		})
	}
}
