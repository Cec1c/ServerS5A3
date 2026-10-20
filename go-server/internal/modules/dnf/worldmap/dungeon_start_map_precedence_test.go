package worldmap

import (
	"context"
	"errors"
	"os"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件钉住「起始/首领房用哪一份真源解析地图」这条口径（见 resolver.go 的
// StartMapPrecedence 与 ownershipRoomCandidates）。
//
// 现场缺陷：格兰迪发电站 104 / 克雷发电站 101 清完起手房不开下一间的门。
// 真档 PVF 的 `[map specification]` 明确写了起始房的 map（42001 / 42101，
// 两张图都带 `[pathgate recognize range]`），而改动前的文件名启发式把它换成了
// 同目录里"名字像 start"的 15424quest(0,0)start.map / 18000(0,0)S.map ——
// 这两张图**整段没有** `[pathgate recognize range]`，客户端因此拿不到可用门的
// 触发矩形，清房后门不开。

// TestResolverStartRoomSpecificationBeatsFilenameStartHeuristic 是合成用例：
// 一个**非任务连通**迷宫的起始房既有 `[map specification]` 显式声明（type=`map`，
// 文件名里没有 start），又有一张同副本归属、文件名带 start 的兄弟图。
//
// 只有"显式声明那张声明了 `[pathgate recognize range]`、启发式那张没有"这一种
// 形状（= 真档 104/101/102 的形状）才换图：默认档必须取显式声明的 100，
// 回退档（ownership）必须取 300。
func TestResolverStartRoomSpecificationBeatsFilenameStartHeuristic(t *testing.T) {
	table := resolverTestTable()
	// 同副本归属 + 文件名像 start 的兄弟图：这正是真档 15424quest(0,0)start.map
	// 的形状（`[dungeon] 104` + `(0,0)start`）。
	table.maps = append(table.maps,
		Map{ID: 300, Path: "map/alpha/300quest(0,0)start.map", DungeonIDs: []int64{700}},
	)
	table.dungeons[0].Mazes[0].MapSpecifications[0].Type = "map"
	for pos := len(table.maps) - 1; pos < len(table.maps); pos++ {
		table.mapByID[table.maps[pos].ID] = pos
		table.mapByPath[pathKey(table.maps[pos].Path)] = pos
	}
	// 真档证据的两个数据点：显式声明那张有 `[pathgate recognize range]`
	// （`0 0 130 140 0 0 0 0`），被启发式换上的那张整段没有。
	declarePathgateRange(t, table, 100)
	if table.maps[table.mapByID[300]].PathgateRecognizeRange.Set {
		t.Fatal("fixture bug: the heuristic start map must not declare [pathgate recognize range]")
	}

	specification, err := NewResolverWithOptions(table, ResolverOptions{
		StartMapPrecedence: StartMapPrecedenceSpecification,
	})
	if err != nil {
		t.Fatalf("new resolver (specification): %v", err)
	}
	start, err := specification.Resolve(ResolveRequest{DungeonID: 700, MazeIndex: 0, X: 0, Y: 0})
	if err != nil || start.Map.ID != 100 || start.Source != ResolutionMapSpecification {
		t.Fatalf("specification start = %+v err=%v want map=100 source=%s",
			start, err, ResolutionMapSpecification)
	}

	ownership, err := NewResolverWithOptions(table, ResolverOptions{
		StartMapPrecedence: StartMapPrecedenceOwnership,
	})
	if err != nil {
		t.Fatalf("new resolver (ownership): %v", err)
	}
	legacyStart, err := ownership.Resolve(ResolveRequest{DungeonID: 700, MazeIndex: 0, X: 0, Y: 0})
	if err != nil || legacyStart.Map.ID != 300 || legacyStart.Source != ResolutionDungeonOwnership {
		t.Fatalf("ownership start = %+v err=%v want map=300 source=%s",
			legacyStart, err, ResolutionDungeonOwnership)
	}

	// 零值 options 必须与 NewResolver 同结果（默认档 = 修复档）。
	zero, err := NewResolverWithOptions(table, ResolverOptions{})
	if err != nil {
		t.Fatalf("new resolver (zero options): %v", err)
	}
	zeroStart, err := zero.Resolve(ResolveRequest{DungeonID: 700, MazeIndex: 0, X: 0, Y: 0})
	if err != nil || zeroStart.Map.ID != 100 {
		t.Fatalf("zero-options start = %+v err=%v want map=100", zeroStart, err)
	}
	plain, err := NewResolver(table)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	plainStart, err := plain.Resolve(ResolveRequest{DungeonID: 700, MazeIndex: 0, X: 0, Y: 0})
	if err != nil || plainStart.Map.ID != 100 {
		t.Fatalf("NewResolver start = %+v err=%v want map=100", plainStart, err)
	}
}

// TestResolverStartRoomPrecedenceLeavesRoomsWithoutDoorEvidenceAlone 钉住"窄口径"：
// 当**两边都没有** `[pathgate recognize range]` 时（= 真档全量审计里其余 19000+ 个
// 房间的形状），默认档必须**逐字节**等于回退档 —— 修复只吃"缺门声明"这一种可观测缺陷，
// 不顺手推翻别的副本的房间选择。
func TestResolverStartRoomPrecedenceLeavesRoomsWithoutDoorEvidenceAlone(t *testing.T) {
	table := resolverTestTable()
	table.maps = append(table.maps,
		Map{ID: 300, Path: "map/alpha/300quest(0,0)start.map", DungeonIDs: []int64{700}},
	)
	table.dungeons[0].Mazes[0].MapSpecifications[0].Type = "map"
	for pos := len(table.maps) - 1; pos < len(table.maps); pos++ {
		table.mapByID[table.maps[pos].ID] = pos
		table.mapByPath[pathKey(table.maps[pos].Path)] = pos
	}
	// 刻意两边都不声明 `[pathgate recognize range]`（真档 dungeon 8001/8176/… 的形状）。
	specification, err := NewResolverWithOptions(table, ResolverOptions{
		StartMapPrecedence: StartMapPrecedenceSpecification,
	})
	if err != nil {
		t.Fatalf("new resolver (specification): %v", err)
	}
	ownership, err := NewResolverWithOptions(table, ResolverOptions{
		StartMapPrecedence: StartMapPrecedenceOwnership,
	})
	if err != nil {
		t.Fatalf("new resolver (ownership): %v", err)
	}
	request := ResolveRequest{DungeonID: 700, MazeIndex: 0, X: 0, Y: 0}
	fixed, fixedErr := specification.Resolve(request)
	legacy, legacyErr := ownership.Resolve(request)
	if fixedErr != nil || legacyErr != nil {
		t.Fatalf("resolve without door evidence: fix=%+v/%v rollback=%+v/%v", fixed, fixedErr, legacy, legacyErr)
	}
	if fixed.Map.ID != legacy.Map.ID || fixed.Source != legacy.Source {
		t.Fatalf("a room with no door evidence changed: fix=%d/%s rollback=%d/%s",
			fixed.Map.ID, fixed.Source, legacy.Map.ID, legacy.Source)
	}
	if fixed.Map.ID != 300 || fixed.Source != ResolutionDungeonOwnership {
		t.Fatalf("no-door-evidence start = %d/%s want 300/%s (改动前口径)",
			fixed.Map.ID, fixed.Source, ResolutionDungeonOwnership)
	}
}

// declarePathgateRange 给夹具地图补上 `[pathgate recognize range]` 段的存在性
// （真档写法 `0 0 130 140 0 0 0 0` 的首个整数是 0，所以 `Set` 才是"段在不在"的判据）。
func declarePathgateRange(t *testing.T, table *Table, mapID int64) {
	t.Helper()
	pos, ok := table.mapByID[mapID]
	if !ok {
		t.Fatalf("fixture map %d is not indexed", mapID)
	}
	table.maps[pos].PathgateRecognizeRange = OptionalInt{Value: 0, Set: true}
}

// TestStartMapPrecedenceNormalizesOnlyExplicitOwnership 钉住"默认承诺不能被别名破坏"：
// 只有显式 `ownership` 才回退，其它任何取值（含 off/0/legacy/on）都是默认档。
func TestStartMapPrecedenceNormalizesOnlyExplicitOwnership(t *testing.T) {
	if got := (ResolverOptions{StartMapPrecedence: "ownership"}).normalize().StartMapPrecedence; got != StartMapPrecedenceOwnership {
		t.Fatalf("normalize(ownership) = %q", got)
	}
	for _, value := range []StartMapPrecedence{"", "off", "0", "legacy", "on", "specification", "OWNERSHIP-typo"} {
		if got := (ResolverOptions{StartMapPrecedence: value}).normalize().StartMapPrecedence; got != StartMapPrecedenceSpecification {
			t.Fatalf("normalize(%q) = %q want %q", value, got, StartMapPrecedenceSpecification)
		}
	}
	if (ResolverOptions{StartMapPrecedence: " ownership "}).normalize().StartMapPrecedence != StartMapPrecedenceOwnership {
		t.Fatal("normalize must tolerate surrounding whitespace on the explicit rollback value")
	}
	if (ResolverOptions{StartMapPrecedence: StartMapPrecedenceOwnership}).preemptsSpecification() != true {
		t.Fatal("ownership mode must keep the pre-change preemption")
	}
	if (ResolverOptions{}).preemptsSpecification() != false {
		t.Fatal("default mode must not preempt the explicit specification")
	}
}

// TestRealPVFStartRoomMapPrecedenceForGrandineAndKohle 是真档钉子
// （`DNF_WORLDMAP_REAL_PVF_SMOKE` 守卫，与同包其余真档用例同一约定）：
// 格兰迪发电站 104 与克雷发电站 101 的 maze 0 起始房必须解析成 DGN 显式声明的
// 42001 / 42101（两张图带 `[pathgate recognize range]`），而不是改动前的
// 15424 / 18000（两张图整段没有该段 ⇒ 客户端打不开门）。
func TestRealPVFStartRoomMapPrecedenceForGrandineAndKohle(t *testing.T) {
	pvfPath := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNF_WORLDMAP_REAL_PVF_SMOKE to verify the real Grandine/Kohle start-room map")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open real pvf: %v", err)
	}
	table, err := LoadSource(context.Background(), archive, Options{})
	if err != nil {
		t.Fatalf("load real worldmap source: %v", err)
	}
	specification, err := NewResolverWithOptions(table, ResolverOptions{
		StartMapPrecedence: StartMapPrecedenceSpecification,
	})
	if err != nil {
		t.Fatalf("new resolver (specification): %v", err)
	}
	ownership, err := NewResolverWithOptions(table, ResolverOptions{
		StartMapPrecedence: StartMapPrecedenceOwnership,
	})
	if err != nil {
		t.Fatalf("new resolver (ownership): %v", err)
	}

	cases := []struct {
		dungeonID     int64
		dungeonPath   string
		specification int64
		rollback      int64
	}{
		{104, "dungeon/Act9/grandine.dgn", 42001, 15424},
		{101, "dungeon/Act9/KohlePowerStation.dgn", 42101, 18000},
	}
	for _, want := range cases {
		dungeon, ok := table.FindDungeon(want.dungeonID)
		if !ok || dungeon.Path != want.dungeonPath {
			t.Fatalf("real PVF dungeon %d path=%q ok=%t want %q", want.dungeonID, dungeon.Path, ok, want.dungeonPath)
		}
		maze := dungeon.Mazes[0]
		if maze.Start == nil {
			t.Fatalf("dungeon %d maze 0 has no [start map]", want.dungeonID)
		}
		request := ResolveRequest{
			DungeonID: want.dungeonID, MazeIndex: 0,
			X: maze.Start.X, Y: maze.Start.Y,
		}
		start, err := specification.Resolve(request)
		if err != nil {
			t.Fatalf("dungeon %d start(%d,%d) specification resolve: %v",
				want.dungeonID, request.X, request.Y, err)
		}
		if start.Map.ID != want.specification || start.Source != ResolutionMapSpecification {
			t.Fatalf("dungeon %d start(%d,%d) = map=%d path=%s source=%s want map=%d source=%s",
				want.dungeonID, request.X, request.Y, start.Map.ID, start.Map.Path, start.Source,
				want.specification, ResolutionMapSpecification)
		}
		legacy, err := ownership.Resolve(request)
		if err != nil {
			t.Fatalf("dungeon %d start(%d,%d) rollback resolve: %v",
				want.dungeonID, request.X, request.Y, err)
		}
		if legacy.Map.ID != want.rollback || legacy.Source != ResolutionDungeonOwnership {
			t.Fatalf("dungeon %d start(%d,%d) rollback档 = map=%d path=%s source=%s want map=%d source=%s",
				want.dungeonID, request.X, request.Y, legacy.Map.ID, legacy.Map.Path, legacy.Source,
				want.rollback, ResolutionDungeonOwnership)
		}
		// 门能开的地图数据：显式声明那张**有** `[pathgate recognize range]` 段，
		// 被启发式换上的那张**没有**。这就是"清完房间门不开"的 PVF 判据。
		if !start.Map.PathgateRecognizeRange.Set {
			t.Fatalf("dungeon %d start map %d (%s) has no [pathgate recognize range]",
				want.dungeonID, start.Map.ID, start.Map.Path)
		}
		if legacy.Map.PathgateRecognizeRange.Set {
			t.Fatalf("dungeon %d rollback start map %d (%s) unexpectedly declares [pathgate recognize range]; the reported defect evidence changed",
				want.dungeonID, legacy.Map.ID, legacy.Map.Path)
		}
		t.Logf("dungeon %d start(%d,%d): specification map=%d path=%s recognize_range_set=%t | rollback档 map=%d path=%s recognize_range_set=%t",
			want.dungeonID, request.X, request.Y,
			start.Map.ID, start.Map.Path, start.Map.PathgateRecognizeRange.Set,
			legacy.Map.ID, legacy.Map.Path, legacy.Map.PathgateRecognizeRange.Value != 0 || legacy.Map.PathgateRecognizeRange.Set)
	}
}

// TestRealPVFStartMapPrecedenceKeepsEveryResolvableRoomResolvable 是全档影响面审计：
// 完整逐 dungeon/maze/房间比较两档：默认档不得使回退档可解析房间退化，
// 两档换图必须满足缺门形状。FiendWar 4953/4978 显式房间规则同时作用于两档，
// 因而本测试不能检测其相对于修复前源码的变化；另用前后全枚举对照审计。
// 不跳过这两个副本，也不放宽本测试的缺门断言。
func TestRealPVFStartMapPrecedenceKeepsEveryResolvableRoomResolvable(t *testing.T) {
	pvfPath := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNF_WORLDMAP_REAL_PVF_SMOKE to audit the start-map precedence blast radius")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open real pvf: %v", err)
	}
	table, err := LoadSource(context.Background(), archive, Options{})
	if err != nil {
		t.Fatalf("load real worldmap source: %v", err)
	}
	specification, err := NewResolverWithOptions(table, ResolverOptions{
		StartMapPrecedence: StartMapPrecedenceSpecification,
	})
	if err != nil {
		t.Fatalf("new resolver (specification): %v", err)
	}
	ownership, err := NewResolverWithOptions(table, ResolverOptions{
		StartMapPrecedence: StartMapPrecedenceOwnership,
	})
	if err != nil {
		t.Fatalf("new resolver (ownership): %v", err)
	}

	compared := 0
	differences := 0
	regressions := 0
	unexpected := 0
	for _, dungeon := range table.Dungeons() {
		for mazeIndex := range dungeon.Mazes {
			maze := dungeon.Mazes[mazeIndex]
			coordinates := make([]Point, 0, len(maze.MapSpecifications)+2)
			for _, specificationRow := range maze.MapSpecifications {
				coordinates = append(coordinates, specificationRow.Coordinate)
			}
			if maze.Start != nil {
				coordinates = append(coordinates, Point{X: maze.Start.X, Y: maze.Start.Y})
			}
			for _, coordinate := range mazeBossCoordinates(maze.Boss) {
				coordinates = append(coordinates, Point{X: coordinate.X, Y: coordinate.Y})
			}
			for _, coordinate := range coordinates {
				request := ResolveRequest{
					DungeonID: dungeon.ID, MazeIndex: mazeIndex,
					X: coordinate.X, Y: coordinate.Y,
				}
				legacyValue, legacyErr := ownership.Resolve(request)
				value, valueErr := specification.Resolve(request)
				compared++
				if legacyErr == nil && valueErr != nil {
					regressions++
					t.Errorf("dungeon=%d maze=%d room=(%d,%d): rollback resolved map=%d but the fix fails: %v",
						dungeon.ID, mazeIndex, coordinate.X, coordinate.Y, legacyValue.Map.ID, valueErr)
					continue
				}
				if legacyErr != nil || valueErr != nil {
					if !errors.Is(legacyErr, valueErr) {
						t.Logf("dungeon=%d maze=%d room=(%d,%d): both modes fail (rollback=%v fix=%v)",
							dungeon.ID, mazeIndex, coordinate.X, coordinate.Y, legacyErr, valueErr)
					}
					continue
				}
				if legacyValue.Map.ID == value.Map.ID {
					continue
				}
				differences++
				// **窄口径不变式**：换图只允许发生在"改动前那张没有声明任何可用门
				// （`[pathgate recognize range]` 整段缺失）、修复档那张声明了"这一种
				// 形状上。任何别的差异都说明修复越界了。
				if legacyValue.Map.PathgateRecognizeRange.Set || !value.Map.PathgateRecognizeRange.Set {
					unexpected++
					t.Errorf("dungeon=%d maze=%d room=(%d,%d): unexpected map change outside the missing-door shape: rollback map=%d recognize_range_set=%t -> fix map=%d recognize_range_set=%t",
						dungeon.ID, mazeIndex, coordinate.X, coordinate.Y,
						legacyValue.Map.ID, legacyValue.Map.PathgateRecognizeRange.Set,
						value.Map.ID, value.Map.PathgateRecognizeRange.Set)
					continue
				}
				t.Logf("dungeon=%d maze=%d room=(%d,%d): rollback map=%d (%s, recognize_range_set=false) -> fix map=%d (%s, recognize_range_set=true)",
					dungeon.ID, mazeIndex, coordinate.X, coordinate.Y,
					legacyValue.Map.ID, legacyValue.Map.Path, value.Map.ID, value.Map.Path)
			}
		}
	}
	t.Logf("real-PVF start-map precedence audit: compared=%d differing_rooms=%d unexpected_differences=%d regressions=%d",
		compared, differences, unexpected, regressions)
	if compared == 0 {
		t.Fatal("real-PVF start-map precedence audit compared zero rooms")
	}
	if regressions != 0 {
		t.Fatalf("the fix broke %d room(s) that the rollback档 could still resolve", regressions)
	}
	if unexpected != 0 {
		t.Fatalf("the fix changed %d room(s) outside the missing-[pathgate recognize range] shape", unexpected)
	}
}
