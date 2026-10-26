package worldmap

import (
	"context"
	"errors"
	"os"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFNorthgateDeclaredStartMap(t *testing.T) {
	filename := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if filename == "" {
		t.Skip("设置DNF_WORLDMAP_REAL_PVF_SMOKE以核对真实北门首房")
	}
	archive, err := platformpvf.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	table, err := LoadSource(context.Background(), archive, Options{})
	if err != nil {
		t.Fatal(err)
	}
	dungeon, ok := table.FindDungeon(88)
	if !ok || pathKey(dungeon.Path) != "dungeon/act7/gentnorthgate_r.dgn" {
		t.Fatalf("北门定义不符：%+v", dungeon)
	}
	maze := dungeon.Mazes[0]
	if mazeQuestConnected(maze) || maze.Start == nil || maze.Start.X != 0 || maze.Start.Y != 1 {
		t.Fatalf("普通迷宫起点不符：%+v", maze.Start)
	}
	modern, err := NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := NewResolverWithOptions(table, ResolverOptions{StartMapPrecedence: StartMapPrecedenceOwnership})
	if err != nil {
		t.Fatal(err)
	}
	request := ResolveRequest{DungeonID: 88, MazeIndex: 0, X: 0, Y: 1}
	start, err := modern.Resolve(request)
	if err != nil || start.Map.ID != 22428 || start.Source != ResolutionMapSpecification || pathKey(start.Map.Path) != "map/gentnorthgate_r/22428.map" {
		t.Fatalf("实际首房=%d/%s/%s err=%v，须服从当前DGN显式22428", start.Map.ID, start.Source, start.Map.Path, err)
	}
	old, err := legacy.ResolveCandidates(request)
	if err != nil || len(old) == 0 {
		t.Fatalf("旧目录对照失败：%v", err)
	}
	for _, value := range old {
		if value.Map.ID == 22428 {
			t.Fatal("旧目录缺陷对照失去区分度")
		}
		t.Logf("旧起点候选：%d %s", value.Map.ID, value.Map.Path)
	}
	topology, err := BuildDungeonLayout(modern, 88, 0, func(choice DungeonMapChoice) (int64, error) { return choice.Candidates[0].ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	run, err := NewDungeonRun(topology)
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewDungeonSession(run)
	if err != nil {
		t.Fatal(err)
	}
	scene, ok := session.Scene()
	if !ok || scene.Map.Map.ID != 22428 || len(scene.BlockingHostiles) != 5 || scene.Cleared {
		t.Fatalf("真实首房演员不符：map=%d blockers=%d cleared=%t", scene.Map.Map.ID, len(scene.BlockingHostiles), scene.Cleared)
	}
	if _, err := session.MoveByte(1, 1); !errors.Is(err, ErrRunCurrentRoomNotCleared) {
		t.Fatalf("清怪前过门守卫=%v", err)
	}
	for i, reference := range scene.BlockingHostiles {
		key := uint32(8000 + i)
		if err := session.BindHostileObject(reference, key); err != nil {
			t.Fatal(err)
		}
		if _, err := session.MarkHostileDefeated(key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := session.MoveByte(1, 1); err != nil {
		t.Fatalf("清完首房无法过门：%v", err)
	}
	scene, _ = session.Scene()
	if scene.Map.Map.ID != 22430 || scene.Coordinate != (RoomCoordinate{X: 1, Y: 1}) {
		t.Fatalf("下一房=%d/%s，预期22430/(1,1)", scene.Map.Map.ID, scene.Coordinate)
	}
	t.Log("真实普通北门：22428首房五怪各自死亡后进入22430，保持清怪门禁")
}
