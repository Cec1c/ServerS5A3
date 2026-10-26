package worldmap

import (
	"errors"
	"reflect"
	"testing"
)

// rest24-pool-tests：独立候选测试。只走公共 API（Resolver.PoolCandidates /
// Resolver.ResolvePoolChoice / Resolver.Resolve），**不**直接调用 classifyMapPoolType，
// 所以同一个文件既能编译在改动前的 resolver.go（git 7232a947）上，也能编译在父的候选实现上。
//
// 被钉住的口径：只有「文件名不含坐标 + 有 [dungeon] 归属」的 Map 才进 byPool；
// 这批 Map 的分池以已解析 Map.Type 的已知值（normal/start/boss/named/end/hidden/
// quest/default）优先，缺值或未知类型才回落到 ClassifyMapFileType(路径)；
// 带坐标与显式 MapSpecifications 的原有行为保持不变。
const (
	poolTypeDungeonID   int64 = 702
	poolTypeIsolationID int64 = 704
)

func poolTypeFixture() *Table {
	table := resolverTestTable()
	appendPoolTypeDungeon(table, poolTypeDungeonID, "dungeon/pool-types.dgn", []Map{
		// 声明 boss、文件名里完全看不出 boss → 必须进 boss 池（本批要钉的缺陷主体）。
		{ID: 500, Path: "map/pool/500.map", Type: "[boss]"},
		// 声明 normal、文件名像 boss → 必须进 normal 池，且不得污染 boss 池。
		{ID: 501, Path: "map/pool/501boss.map", Type: "[normal]"},
		// Type 缺失 → 回落文件名启发式（s502 → start）。
		{ID: 502, Path: "map/pool/s502.map"},
		// Type 未知（非已知值）→ 回落文件名启发式（q_503 → quest）。
		{ID: 503, Path: "map/pool/q_503.map", Type: "[dummy]"},
		{ID: 504, Path: "map/pool/504.map", Type: "[end]"},
		{ID: 505, Path: "map/pool/505.map", Type: "[hidden]"},
		{ID: 506, Path: "map/pool/506.map", Type: "[quest]"},
		{ID: 507, Path: "map/pool/507.map", Type: "[named]"},
		{ID: 508, Path: "map/pool/508.map", Type: "[default]"},
		{ID: 509, Path: "map/pool/509.map", Type: "[start]"},
		// 带坐标 + 声明 boss：只该进 ownership，任何池里都不该出现。
		{ID: 510, Path: "map/pool/510(0,0).map", Type: "[boss]"},
		// 规范字符串边界：外层空白、大小写、反引号包裹的方括号值。
		{ID: 511, Path: "map/pool/511.map", Type: " [ BOSS ] "},
		{ID: 512, Path: "map/pool/512.map", Type: "BOSS"},
		{ID: 513, Path: "map/pool/513.map", Type: "`[boss]`"},
		// 只有空白 → 等同缺值，回落文件名启发式（514 → normal）。
		{ID: 514, Path: "map/pool/514.map", Type: "   "},
		{ID: 515, Path: "map/pool/s515.map", Type: "[normal]"},
	})
	// 隔离副本：同样是「无坐标 + 声明 boss」，但 [dungeon] 归属另一个副本。
	appendPoolTypeDungeon(table, poolTypeIsolationID, "dungeon/pool-iso.dgn", []Map{
		{ID: 600, Path: "map/iso/600.map", Type: "[boss]"},
	})
	return table
}

func appendPoolTypeDungeon(table *Table, dungeonID int64, dungeonPath string, maps []Map) {
	for _, mapValue := range maps {
		value := mapValue
		value.DungeonIDs = []int64{dungeonID}
		appendResolverTestMap(table, value)
	}
	table.dungeons = append(table.dungeons, Dungeon{
		ID:   dungeonID,
		Path: dungeonPath,
		Mazes: []Maze{{
			Index:  0,
			Width:  OptionalInt{Value: 8, Set: true},
			Height: OptionalInt{Value: 8, Set: true},
		}},
	})
	position := len(table.dungeons) - 1
	table.dungeonByID[dungeonID] = position
	table.dungeonByPath[dungeonPath] = position
}

func poolTypeCandidateIDs(t *testing.T, resolver *Resolver, dungeonID int64, fileType MapFileType) []int64 {
	t.Helper()
	maps, err := resolver.PoolCandidates(MapPoolRequest{DungeonID: dungeonID, MazeIndex: 0, FileType: fileType})
	if err != nil {
		if errors.Is(err, ErrMapPoolEmpty) {
			return nil
		}
		t.Fatalf("pool %s dungeon=%d: %v", fileType, dungeonID, err)
	}
	ids := make([]int64, 0, len(maps))
	for _, mapValue := range maps {
		ids = append(ids, mapValue.ID)
	}
	return ids
}

func poolTypeEqualIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func poolTypeContainsID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestResolverPoolSplitsCoordinateLessOwnedMapsByDeclaredType 钉住：
// 无坐标 + 有 [dungeon] 归属的 Map 按已解析 Map.Type 分池；缺值/未知回落到文件名启发式；
// 带坐标的图不进任何池；不同副本互不串池。
func TestResolverPoolSplitsCoordinateLessOwnedMapsByDeclaredType(t *testing.T) {
	resolver, err := NewResolver(poolTypeFixture())
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	cases := []struct {
		name     string
		fileType MapFileType
		want     []int64
	}{
		{"declared_boss_without_boss_file_name", MapFileBoss, []int64{500, 511, 512, 513}},
		{"declared_normal_overrides_boss_file_name", MapFileNormal, []int64{501, 514}},
		{"declared_start_and_normal_content_entry", MapFileStart, []int64{502, 509, 515}},
		{"declared_end", MapFileEnd, []int64{504}},
		{"declared_hidden", MapFileHidden, []int64{505}},
		{"declared_quest", MapFileQuest, []int64{503, 506}},
		{"declared_named", MapFileNamed, []int64{507}},
		{"declared_default", MapFileDefault, []int64{508}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := poolTypeCandidateIDs(t, resolver, poolTypeDungeonID, test.fileType)
			if !poolTypeEqualIDs(got, test.want) {
				t.Fatalf("pool %s = %v, want %v", test.fileType, got, test.want)
			}
		})
	}

	t.Run("coordinate_bearing_map_never_enters_any_pool", func(t *testing.T) {
		for _, fileType := range []MapFileType{
			MapFileNormal, MapFileStart, MapFileBoss, MapFileNamed,
			MapFileEnd, MapFileHidden, MapFileQuest, MapFileDefault,
		} {
			if poolTypeContainsID(poolTypeCandidateIDs(t, resolver, poolTypeDungeonID, fileType), 510) {
				t.Fatalf("coordinate bearing map 510 leaked into the %s pool", fileType)
			}
		}
		owned, err := resolver.Resolve(ResolveRequest{DungeonID: poolTypeDungeonID, MazeIndex: 0, X: 0, Y: 0})
		if err != nil || owned.Map.ID != 510 || owned.Source != ResolutionDungeonOwnership {
			t.Fatalf("coordinate owned resolution = %+v err=%v", owned, err)
		}
	})

	t.Run("dungeons_stay_isolated", func(t *testing.T) {
		if got := poolTypeCandidateIDs(t, resolver, poolTypeIsolationID, MapFileBoss); !poolTypeEqualIDs(got, []int64{600}) {
			t.Fatalf("isolation dungeon boss pool = %v, want [600]", got)
		}
		if got := poolTypeCandidateIDs(t, resolver, poolTypeDungeonID, MapFileBoss); poolTypeContainsID(got, 600) {
			t.Fatalf("foreign dungeon map leaked into boss pool: %v", got)
		}
		if got := poolTypeCandidateIDs(t, resolver, poolTypeIsolationID, MapFileNormal); len(got) != 0 {
			t.Fatalf("isolation dungeon normal pool = %v, want empty", got)
		}
	})

	t.Run("specifications_without_dungeon_ownership_do_not_create_pools", func(t *testing.T) {
		for _, fileType := range []MapFileType{
			MapFileNormal, MapFileStart, MapFileBoss, MapFileNamed,
			MapFileEnd, MapFileHidden, MapFileQuest, MapFileDefault,
		} {
			if got := poolTypeCandidateIDs(t, resolver, 700, fileType); len(got) != 0 {
				t.Fatalf("dungeon 700 exposed a %s pool without owned coordinate-less maps: %v", fileType, got)
			}
		}
	})
}

// TestResolverPoolChoiceHonorsDeclaredTypeRouting 钉住选择期：
// 声明 boss 的图可以被 boss 池选中；声明 normal 的图在 boss 池里必须被拒；
// 带坐标的图、别的副本的图、不存在的图都必须以 ErrMapPoolChoiceInvalid 拒绝。
func TestResolverPoolChoiceHonorsDeclaredTypeRouting(t *testing.T) {
	resolver, err := NewResolver(poolTypeFixture())
	if err != nil {
		t.Fatal(err)
	}
	boss := MapPoolRequest{DungeonID: poolTypeDungeonID, MazeIndex: 0, FileType: MapFileBoss}
	resolved, err := resolver.ResolvePoolChoice(boss, 500)
	if err != nil || resolved.Map.ID != 500 || resolved.Source != ResolutionTypePool {
		t.Fatalf("declared boss pool choice = %+v err=%v", resolved, err)
	}
	for _, mapID := range []int64{501, 502, 510, 600, 999} {
		if _, err := resolver.ResolvePoolChoice(boss, mapID); !errors.Is(err, ErrMapPoolChoiceInvalid) {
			t.Fatalf("boss pool accepted map %d: %v", mapID, err)
		}
	}
	normal := MapPoolRequest{DungeonID: poolTypeDungeonID, MazeIndex: 0, FileType: MapFileNormal}
	if _, err := resolver.ResolvePoolChoice(normal, 501); err != nil {
		t.Fatalf("declared normal pool choice = %v", err)
	}
	if _, err := resolver.ResolvePoolChoice(normal, 500); !errors.Is(err, ErrMapPoolChoiceInvalid) {
		t.Fatalf("normal pool accepted declared boss map: %v", err)
	}
}

// TestResolverPoolCandidatesReturnIsolatedClones 钉住返回克隆：
// 改写返回值的标量字段与切片字段都不回写表内原始 Map，重复调用结果逐值一致。
func TestResolverPoolCandidatesReturnIsolatedClones(t *testing.T) {
	table := poolTypeFixture()
	resolver, err := NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	request := MapPoolRequest{DungeonID: poolTypeDungeonID, MazeIndex: 0, FileType: MapFileBoss}
	original := cloneMap(table.maps[table.mapByID[500]])

	first, err := resolver.PoolCandidates(request)
	if err != nil {
		t.Fatalf("first pool: %v", err)
	}
	seen := false
	for index := range first {
		if first[index].ID != 500 {
			continue
		}
		seen = true
		first[index].Path = "map/mutated/500.map"
		first[index].Name = "mutated"
		first[index].Type = "[mutated]"
		first[index].DungeonIDs[0] = 9999
	}
	if !seen {
		t.Fatalf("declared boss map 500 absent from boss pool: %+v", first)
	}
	if got := table.maps[table.mapByID[500]]; !reflect.DeepEqual(got, original) {
		t.Fatalf("pool mutation leaked into the table: got=%+v want=%+v", got, original)
	}
	second, err := resolver.PoolCandidates(request)
	if err != nil {
		t.Fatalf("second pool: %v", err)
	}
	for _, candidate := range second {
		if candidate.ID != 500 {
			continue
		}
		if candidate.Path != original.Path || candidate.Name != original.Name || candidate.Type != original.Type {
			t.Fatalf("pool clone leaked scalar mutation: %+v want=%+v", candidate, original)
		}
		if !reflect.DeepEqual(candidate.DungeonIDs, original.DungeonIDs) {
			t.Fatalf("pool clone shares DungeonIDs backing array: %+v want=%+v", candidate.DungeonIDs, original.DungeonIDs)
		}
	}
}
