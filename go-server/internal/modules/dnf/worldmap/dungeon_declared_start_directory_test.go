package worldmap

import (
	"reflect"
	"testing"
)

func declaredStartDirectoryFixture() *Table {
	table := resolverTestTable()
	table.dungeons[0].Path = "dungeon/alpha.dgn"
	table.dungeons[0].Mazes[0].Width = OptionalInt{Set: true, Value: 2}
	table.dungeons[0].Mazes[0].Height = OptionalInt{Set: true, Value: 1}
	table.dungeons[0].Mazes[0].Greed = "BBEE"
	table.maps[table.mapByID[100]].Greed = "BB BB"
	table.maps[table.mapByID[105]].Greed = "BB BB"
	appendResolverTestMap(table, Map{ID: 300, Path: "map/obsolete/300(0,0)start.map", DungeonIDs: []int64{700}, Greed: "DD DD"})
	return table
}

// 同一副本编号可以保留旧目录；当前DGN的显式房间不能被旧目录的start文件名覆盖。
// 两侧均没有pathgate recognize range，模拟北门普通首房的实际形状。
func TestResolverDeclaredStartDirectoryOverridesForeignStart(t *testing.T) {
	for _, mode := range []StartMapPrecedence{StartMapPrecedenceSpecification, StartMapPrecedenceOwnership} {
		t.Run(string(mode), func(t *testing.T) {
			table := declaredStartDirectoryFixture()
			resolver, err := NewResolverWithOptions(table, ResolverOptions{StartMapPrecedence: mode})
			if err != nil {
				t.Fatal(err)
			}
			got, err := resolver.Resolve(ResolveRequest{DungeonID: 700, MazeIndex: 0, X: 0, Y: 0})
			want, source := int64(100), ResolutionMapSpecification
			if mode == StartMapPrecedenceOwnership {
				want, source = 300, ResolutionDungeonOwnership
			}
			if err != nil || got.Map.ID != want || got.Source != source {
				t.Fatalf("起始房=%d/%s err=%v，预期%d/%s", got.Map.ID, got.Source, err, want, source)
			}
		})
	}
}

func TestResolverDeclaredStartDirectoryPreservesExistingChoices(t *testing.T) {
	for _, scenario := range []string{"同目录", "候选含有效目录", "旧目录在其它房间被声明", "未声明起始房", "团本", "首领房", "DGN目录不对应", "旧图出口匹配", "显式图出口不匹配", "未知旧图出口", "未知迷宫出口", "显式图出口不一致"} {
		t.Run(scenario, func(t *testing.T) {
			table := declaredStartDirectoryFixture()
			request := ResolveRequest{DungeonID: 700, MazeIndex: 0, X: 0, Y: 0}
			switch scenario {
			case "DGN目录不对应":
				table.dungeons[0].Path = "dungeon/another.dgn"
			case "旧图出口匹配":
				table.maps[table.mapByID[300]].Greed = "BB BB"
			case "显式图出口不匹配":
				table.maps[table.mapByID[100]].Greed = "JJ JJ"
			case "未知旧图出口":
				table.maps[table.mapByID[300]].Greed = ""
			case "未知迷宫出口":
				table.dungeons[0].Mazes[0].Greed = ""
			case "显式图出口不一致":
				table.maps[table.mapByID[100]].Greed = "BB DD"
			case "同目录":
				table.maps[table.mapByID[300]].Path = "map/alpha/300(0,0)start.map"
			case "候选含有效目录":
				appendResolverTestMap(table, Map{ID: 301, Path: "map/alpha/301(0,0)start.map", DungeonIDs: []int64{700}})
			case "旧目录在其它房间被声明":
				appendResolverTestMap(table, Map{ID: 302, Path: "map/obsolete/302.map"})
				table.dungeons[0].Mazes[0].MapSpecifications = append(table.dungeons[0].Mazes[0].MapSpecifications, MapSpecification{Type: "map", Coordinate: Point{X: 0, Y: 1}, MapIDs: []int64{302}})
			case "未声明起始房":
				table.dungeons[0].Mazes[0].MapSpecifications = table.dungeons[0].Mazes[0].MapSpecifications[1:]
			case "团本":
				table.dungeons[0].SourceSections = []RawSection{{Name: "raid dungeon"}}
			case "首领房":
				request.X = 1
				appendResolverTestMap(table, Map{ID: 303, Path: "map/obsolete/303(1,0)boss.map", DungeonIDs: []int64{700}})
			}
			modern, err := NewResolver(table)
			if err != nil {
				t.Fatal(err)
			}
			legacy, err := NewResolverWithOptions(table, ResolverOptions{StartMapPrecedence: StartMapPrecedenceOwnership})
			if err != nil {
				t.Fatal(err)
			}
			got, err := modern.ResolveCandidates(request)
			if err != nil {
				t.Fatal(err)
			}
			want, err := legacy.ResolveCandidates(request)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("改变了既有候选：got=%+v want=%+v", got, want)
			}
		})
	}
}

func TestResolverDeclaredStartDirectoryPreservesExplicitPoolAndQuest(t *testing.T) {
	for _, quest := range []bool{false, true} {
		table := declaredStartDirectoryFixture()
		table.dungeons[0].Mazes[0].MapSpecifications[0].MapIDs = []int64{100, 105}
		if quest {
			table.dungeons[0].Mazes[0].QuestConnection = []int64{0, 99, 1}
		}
		r, err := NewResolver(table)
		if err != nil {
			t.Fatal(err)
		}
		values, err := r.ResolveCandidates(ResolveRequest{DungeonID: 700, MazeIndex: 0, X: 0, Y: 0})
		if err != nil {
			t.Fatal(err)
		}
		if len(values) != 2 || values[0].Map.ID != 100 || values[1].Map.ID != 105 || values[0].Source != ResolutionMapSpecification {
			t.Fatalf("显式候选池被替换或裁剪：%+v", values)
		}
	}
}
