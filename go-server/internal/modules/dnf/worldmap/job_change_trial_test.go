package worldmap

import (
	"errors"
	"testing"
)

func TestJobChangeTrialResolvesOwnedEntranceAtSharedStartBoss(t *testing.T) {
	for _, name := range []string{"actual", "wrong_owner", "wrong_coordinate", "ambiguous"} {
		t.Run(name, func(t *testing.T) {
			table := resolverTestTable()
			entrance := Map{ID: 76032, Path: "map/Cataclysm/ChangeClass/76032_ChangeClass_S_(0,0).map", DungeonIDs: []int64{7142}}
			switch name {
			case "wrong_owner":
				entrance.DungeonIDs = []int64{7143}
			case "wrong_coordinate":
				entrance.Path = "map/Cataclysm/ChangeClass/76032_ChangeClass_S_(1,0).map"
			}
			addMapToTable(table, entrance)
			// 同目录的下一张图和7143同坐标资源都不能成为7142入口。
			addMapToTable(table, Map{ID: 76033, Path: "map/Cataclysm/ChangeClass/76033_ChangeClass_B_(1,0).map", DungeonIDs: []int64{7142}})
			addMapToTable(table, Map{ID: 76115, Path: "map/Cataclysm/ChangeClass/76115_ChangeClass_S_(0,0).map", DungeonIDs: []int64{7143}})
			if name == "ambiguous" {
				addMapToTable(table, Map{ID: 76034, Path: "map/Cataclysm/ChangeClass/76034_ChangeClass_S_(0,0).map", DungeonIDs: []int64{7142}})
			}
			addDungeonToTable(table, Dungeon{ID: 7142, Path: "dungeon/Cataclysm/ChangeClass/7142_ChangeClass.dgn", Mazes: []Maze{{Index: 0, Start: &MazePoint{X: 0, Y: 0}, Boss: &MazePoint{X: 0, Y: 0}, Width: OptionalInt{Set: true, Value: 1}, Height: OptionalInt{Set: true, Value: 1}}}})
			resolver, err := NewResolver(table)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := resolver.Resolve(ResolveRequest{DungeonID: 7142, MazeIndex: 0, X: 0, Y: 0})
			if name == "ambiguous" {
				if !errors.Is(err, ErrMapAmbiguous) {
					t.Fatalf("ambiguous error=%v", err)
				}
				return
			}
			if name != "actual" {
				if !errors.Is(err, ErrMapUnresolved) {
					t.Fatalf("foreign map accepted: %+v err=%v", resolved, err)
				}
				return
			}
			if err != nil || resolved.Map.ID != 76032 || resolved.Source != ResolutionDungeonOwnership {
				t.Fatalf("entrance=%+v error=%v", resolved, err)
			}
			layout, err := BuildDungeonLayout(resolver, 7142, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			room, ok := layout.Room(RoomCoordinate{X: 0, Y: 0})
			if !ok || room.Map == nil || room.Map.Map.ID != 76032 {
				t.Fatalf("room=%+v found=%t", room, ok)
			}
		})
	}
}
