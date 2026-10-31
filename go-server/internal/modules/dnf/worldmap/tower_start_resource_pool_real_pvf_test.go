package worldmap

// rest25-tower-real: 绝望之塔多层楼层（11022/11032/11062）起始房真档 smoke 候选。
//
// 只走公共 API（platformpvf.Open / LoadSource / NewResolver / BuildDungeonLayout /
// (*Resolver).PoolCandidates），不引用任何窄回退 helper，因此在父写
// towerStartResourcePool 前后的产品树上都能编译，用于红/绿对照。
//
// 真档事实来源（只读审计 rest25-tower-missing，DGN 与 MAP 原文档）：
//
//	11022 3x1 start(0,0) boss(2,0) spec(1,0)=15180 normal{15144,15180} boss{15181} start池空
//	11032 4x1 start(3,0) boss(0,0) spec(1,0)=15250 (2,0)=15251 normal{15154,15250,15251} boss{15252} start池空
//	11062 2x1 start(0,0) boss(1,0) 无 spec normal{15204} boss{15182} start池空
//
// 起始房是 start-only 房：全塔 start 池为空，改动前没有任何回退能兜住它。
// 起始图与中间图按真实资源 ID 断言；首领房的图只从 boss 池取（不按文件名或
// 路径猜图源），并钉住它落在 type_pool 来源。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 配对档案：client-v106-single-luke-20261016/client-release/patch/client/Script.pvf
const rest25RealPVFSHA256 = "ad7d05c70421278f7385729bcba3e81e37d99d0f3df85a87a9f0dc19c92504cb"

type rest25RealFloor struct {
	dungeonID  int64
	start      RoomCoordinate
	startMap   int64
	middle     map[RoomCoordinate]int64
	boss       RoomCoordinate
	bossPool   []int64
	normalPool []int64
	rooms      []RoomCoordinate
}

var rest25RealFloors = []rest25RealFloor{
	{
		dungeonID:  11022,
		start:      RoomCoordinate{X: 0, Y: 0},
		startMap:   15144,
		middle:     map[RoomCoordinate]int64{{X: 1, Y: 0}: 15180},
		boss:       RoomCoordinate{X: 2, Y: 0},
		bossPool:   []int64{15181},
		normalPool: []int64{15144, 15180},
		rooms:      []RoomCoordinate{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 2, Y: 0}},
	},
	{
		dungeonID:  11032,
		start:      RoomCoordinate{X: 3, Y: 0},
		startMap:   15154,
		middle:     map[RoomCoordinate]int64{{X: 1, Y: 0}: 15250, {X: 2, Y: 0}: 15251},
		boss:       RoomCoordinate{X: 0, Y: 0},
		bossPool:   []int64{15252},
		normalPool: []int64{15154, 15250, 15251},
		rooms:      []RoomCoordinate{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 2, Y: 0}, {X: 3, Y: 0}},
	},
	{
		dungeonID:  11062,
		start:      RoomCoordinate{X: 0, Y: 0},
		startMap:   15204,
		middle:     map[RoomCoordinate]int64{},
		boss:       RoomCoordinate{X: 1, Y: 0},
		bossPool:   []int64{15182},
		normalPool: []int64{15204},
		rooms:      []RoomCoordinate{{X: 0, Y: 0}, {X: 1, Y: 0}},
	},
}

func rest25RealPVFHash(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open pvf %s: %v", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatalf("hash pvf %s: %v", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// rest25RealFirstCandidate 固定取首候选（与只读审计同一口径），让结果与 map 迭代顺序无关。
func rest25RealFirstCandidate(choice DungeonMapChoice) (int64, error) {
	if len(choice.Candidates) == 0 {
		return 0, fmt.Errorf("room %s has no candidates", choice.Coordinate)
	}
	return choice.Candidates[0].ID, nil
}

func rest25RealPoolIDs(t *testing.T, resolver *Resolver, dungeonID int64, fileType MapFileType) []int64 {
	t.Helper()
	values, err := resolver.PoolCandidates(MapPoolRequest{DungeonID: dungeonID, MazeIndex: 0, FileType: fileType})
	if err != nil {
		t.Fatalf("dungeon %d %s pool: %v", dungeonID, fileType, err)
	}
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		ids = append(ids, value.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func rest25RealSameIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// rest25RealDungeonOwned 只认「声明 [dungeon_id] 且 DungeonIDs 恰好只有本层」的真实归属。
func rest25RealDungeonOwned(mapValue Map, dungeonID int64) bool {
	return mapValue.DungeonID.Set && mapValue.DungeonID.Value == dungeonID &&
		len(mapValue.DungeonIDs) == 1 && mapValue.DungeonIDs[0] == dungeonID
}

// TestRealPVFTowerStartResourceFloors 真档 smoke：全塔 100 层可布局 + 3 个多层楼层逐房钉住。
func TestRealPVFTowerStartResourceFloors(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNF_WORLDMAP_REAL_PVF_SMOKE to the paired Script.pvf")
	}
	if got := rest25RealPVFHash(t, pvfPath); got != rest25RealPVFSHA256 {
		t.Fatalf("paired PVF mismatch: sha256=%s want=%s path=%s", got, rest25RealPVFSHA256, pvfPath)
	}
	checkRealPVFTowerStartResourceFloors(t, pvfPath)
}

func checkRealPVFTowerStartResourceFloors(t *testing.T, pvfPath string) {
	t.Helper()
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open real Script.pvf: %v", err)
	}
	table, err := LoadSource(context.Background(), archive, Options{SkipAreas: true})
	if err != nil {
		t.Fatalf("load real worldmap table: %v", err)
	}
	resolver, err := NewResolver(table)
	if err != nil {
		t.Fatalf("build resolver: %v", err)
	}

	// 1) 全塔：11008..11107 共 100 层，每层 BuildDungeonLayout 都要成功。
	failures := make([]string, 0, 4)
	for id := int64(11008); id <= 11107; id++ {
		if _, layoutErr := BuildDungeonLayout(resolver, id, 0, rest25RealFirstCandidate); layoutErr != nil {
			failures = append(failures, fmt.Sprintf("%d: %v", id, layoutErr))
		}
	}
	if len(failures) != 0 {
		t.Fatalf("tower 11008..11107: %d/100 floors failed BuildDungeonLayout:\n%s",
			len(failures), strings.Join(failures, "\n"))
	}
	t.Logf("tower 11008..11107: 100/100 BuildDungeonLayout ok")

	// 2) 多层楼层：起始房、显式中间房、首领房逐房核对。
	for _, floor := range rest25RealFloors {
		floor := floor
		t.Run(fmt.Sprintf("dungeon_%d", floor.dungeonID), func(t *testing.T) {
			topology, layoutErr := BuildDungeonLayout(resolver, floor.dungeonID, 0, rest25RealFirstCandidate)
			if layoutErr != nil {
				t.Fatalf("dungeon %d BuildDungeonLayout: %v", floor.dungeonID, layoutErr)
			}
			rooms := topology.Rooms()
			if len(rooms) != len(floor.rooms) {
				t.Fatalf("dungeon %d rooms: got %d want %d", floor.dungeonID, len(rooms), len(floor.rooms))
			}
			for _, coordinate := range floor.rooms {
				if _, ok := topology.Room(coordinate); !ok {
					t.Fatalf("dungeon %d room %s absent", floor.dungeonID, coordinate)
				}
			}

			// 池保持：normal / boss 池逐 ID 与真档一致；start 池本来就是空的。
			if got := rest25RealPoolIDs(t, resolver, floor.dungeonID, MapFileNormal); !rest25RealSameIDs(got, floor.normalPool) {
				t.Errorf("dungeon %d normal pool: got %v want %v", floor.dungeonID, got, floor.normalPool)
			}
			bossPool := rest25RealPoolIDs(t, resolver, floor.dungeonID, MapFileBoss)
			if !rest25RealSameIDs(bossPool, floor.bossPool) {
				t.Errorf("dungeon %d boss pool: got %v want %v", floor.dungeonID, bossPool, floor.bossPool)
			}
			if len(bossPool) != 1 {
				t.Fatalf("dungeon %d boss pool not unique: %v", floor.dungeonID, bossPool)
			}
			if _, startErr := resolver.PoolCandidates(MapPoolRequest{DungeonID: floor.dungeonID, MazeIndex: 0, FileType: MapFileStart}); startErr == nil {
				t.Errorf("dungeon %d start pool became non-empty", floor.dungeonID)
			}

			// 起始房：真实楼层本体资源（无文件名坐标、同副本独有、来自 normal 池），来源是 type_pool。
			startRoom, ok := topology.Room(floor.start)
			if !ok {
				t.Fatalf("dungeon %d start room %s absent", floor.dungeonID, floor.start)
			}
			if !startRoom.Start || startRoom.Boss {
				t.Errorf("dungeon %d room %s roles: start=%t boss=%t", floor.dungeonID, floor.start, startRoom.Start, startRoom.Boss)
			}
			if startRoom.Map == nil {
				t.Fatalf("dungeon %d start room %s unresolved", floor.dungeonID, floor.start)
			}
			if startRoom.Map.Map.ID != floor.startMap {
				t.Errorf("dungeon %d start room %s map: got %d want %d",
					floor.dungeonID, floor.start, startRoom.Map.Map.ID, floor.startMap)
			}
			if startRoom.Map.Source != ResolutionTypePool {
				t.Errorf("dungeon %d start room %s source: got %q want %q",
					floor.dungeonID, floor.start, startRoom.Map.Source, ResolutionTypePool)
			}
			if _, _, coordinate := ParseMapFileCoordinate(startRoom.Map.Map.Path); coordinate {
				t.Errorf("dungeon %d start room %s map %d has a filename coordinate: %s",
					floor.dungeonID, floor.start, startRoom.Map.Map.ID, startRoom.Map.Map.Path)
			}
			if !rest25RealDungeonOwned(startRoom.Map.Map, floor.dungeonID) {
				t.Errorf("dungeon %d start room %s map %d is not uniquely owned by the floor: dungeon_id=%+v ids=%v",
					floor.dungeonID, floor.start, startRoom.Map.Map.ID, startRoom.Map.Map.DungeonID, startRoom.Map.Map.DungeonIDs)
			}
			startPooled := false
			for _, id := range floor.normalPool {
				if id == startRoom.Map.Map.ID {
					startPooled = true
				}
			}
			if !startPooled {
				t.Errorf("dungeon %d start room %s map %d is not in the normal pool %v",
					floor.dungeonID, floor.start, startRoom.Map.Map.ID, floor.normalPool)
			}

			// 显式中间房：与 DGN 里 [map specification] 声明的坐标-地图 ID 逐条一致，
			// 且来源仍是 map_specification（没有被起始房改动挤掉）。
			dungeon, found := table.FindDungeon(floor.dungeonID)
			if !found || len(dungeon.Mazes) == 0 {
				t.Fatalf("dungeon %d not indexed", floor.dungeonID)
			}
			declared := make(map[RoomCoordinate]int64)
			for _, specification := range dungeon.Mazes[0].MapSpecifications {
				for _, mapID := range specification.MapIDs {
					declared[RoomCoordinate{X: specification.Coordinate.X, Y: specification.Coordinate.Y}] = mapID
				}
			}
			if len(declared) != len(floor.middle) {
				t.Errorf("dungeon %d declared middle rooms: got %d want %d", floor.dungeonID, len(declared), len(floor.middle))
			}
			for coordinate, wantID := range floor.middle {
				if gotID, ok := declared[coordinate]; !ok || gotID != wantID {
					t.Errorf("dungeon %d declared middle %s: got %d present=%t want %d", floor.dungeonID, coordinate, gotID, ok, wantID)
				}
				room, ok := topology.Room(coordinate)
				if !ok {
					t.Errorf("dungeon %d middle room %s absent", floor.dungeonID, coordinate)
					continue
				}
				if room.Start || room.Boss {
					t.Errorf("dungeon %d middle room %s roles changed: start=%t boss=%t", floor.dungeonID, coordinate, room.Start, room.Boss)
				}
				if room.Map == nil {
					t.Errorf("dungeon %d middle room %s unresolved", floor.dungeonID, coordinate)
					continue
				}
				if room.Map.Map.ID != wantID {
					t.Errorf("dungeon %d middle room %s map: got %d want %d", floor.dungeonID, coordinate, room.Map.Map.ID, wantID)
				}
				if room.Map.Source != ResolutionMapSpecification {
					t.Errorf("dungeon %d middle room %s source: got %q want %q",
						floor.dungeonID, coordinate, room.Map.Source, ResolutionMapSpecification)
				}
			}

			// 首领房：图源只认 boss 池的唯一成员（不按文件名猜），来源是 type_pool。
			bossRoom, ok := topology.Room(floor.boss)
			if !ok {
				t.Fatalf("dungeon %d boss room %s absent", floor.dungeonID, floor.boss)
			}
			if !bossRoom.Boss || bossRoom.Start {
				t.Errorf("dungeon %d room %s roles: start=%t boss=%t", floor.dungeonID, floor.boss, bossRoom.Start, bossRoom.Boss)
			}
			if bossRoom.Map == nil {
				t.Fatalf("dungeon %d boss room %s unresolved", floor.dungeonID, floor.boss)
			}
			if bossRoom.Map.Map.ID != bossPool[0] {
				t.Errorf("dungeon %d boss room %s map: got %d want boss-pool member %d",
					floor.dungeonID, floor.boss, bossRoom.Map.Map.ID, bossPool[0])
			}
			if bossRoom.Map.Source != ResolutionTypePool {
				t.Errorf("dungeon %d boss room %s source: got %q want %q",
					floor.dungeonID, floor.boss, bossRoom.Map.Source, ResolutionTypePool)
			}
			if !rest25RealDungeonOwned(bossRoom.Map.Map, floor.dungeonID) {
				t.Errorf("dungeon %d boss room %s map %d is not uniquely owned by the floor: dungeon_id=%+v ids=%v",
					floor.dungeonID, floor.boss, bossRoom.Map.Map.ID, bossRoom.Map.Map.DungeonID, bossRoom.Map.Map.DungeonIDs)
			}
		})
	}
}
