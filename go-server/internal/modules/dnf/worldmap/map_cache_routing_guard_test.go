package worldmap

import (
	"errors"
	"testing"
)

func TestMapCacheMetadataPreservesFiendWarStartGuard(t *testing.T) {
	for _, id := range []int64{4953, 4978} {
		full := Map{ID: 100, DungeonIDs: []int64{id}, DungeonStartArea: []int64{0, 0, 130, 140}}
		projected := mapRoutingMetadata(full)
		full.DungeonStartArea[2] = 0
		if len(projected.DungeonStartArea) != 4 || projected.DungeonStartArea[2] != 130 {
			t.Fatal("起始区域投影缺失或与输入共享可变切片")
		}
		table := &Table{maps: []Map{projected}}
		resolver := &Resolver{table: table}
		key := coordinateKey{0, 0}
		index := mazeMapIndex{bySpecification: map[coordinateKey][]indexedMap{key: {{mapPos: 0, fileType: MapFileStart}}}}
		maze := Maze{Start: &MazePoint{X: 0, Y: 0}}
		if candidates, handled, err := resolver.fiendWarDeclaredRoom(Dungeon{ID: id}, maze, index, key); !handled || err != nil || len(candidates) != 1 {
			t.Fatalf("缓存元数据使合法起点被拒绝：dungeon=%d handled=%t err=%v", id, handled, err)
		}
		table.maps[0].DungeonStartArea = nil
		if _, handled, err := resolver.fiendWarDeclaredRoom(Dungeon{ID: id}, maze, index, key); !handled || !errors.Is(err, ErrMapReferenceMissing) {
			t.Fatal("缺失区域绕过既有守卫", err)
		}
	}
}
