package worldmap

import (
	"errors"
	"testing"
)

func TestFiendWarMultiMazeRejectsForeignRoomFallback(t *testing.T) {
	for _, name := range []string{"missing", "ambiguous", "foreign_owner"} {
		for _, roomX := range []int64{0, 2} {
			t.Run(name, func(t *testing.T) {
				maze := Maze{Start: &MazePoint{}, Boss: &MazePoint{X: 2}}
				d := Dungeon{ID: 4975, Path: "dungeon/gcontents/180621_fiendwar/cosmo_test.dgn", Mazes: []Maze{maze, maze, maze}}
				table := &Table{maps: []Map{{ID: 100, DungeonIDs: []int64{4975}}, {ID: 200, DungeonIDs: []int64{4975}}}}
				key := coordinateKey{x: roomX}
				kind := "boss"
				if roomX == 0 {
					kind = "start"
				}
				index := mazeMapIndex{bySpecification: map[coordinateKey][]indexedMap{key: {{mapPos: 0, specificationType: kind}}}}
				switch name {
				case "missing":
					index.bySpecification[key] = nil
				case "ambiguous":
					index.bySpecification[key] = append(index.bySpecification[key], indexedMap{mapPos: 1, specificationType: kind})
				case "foreign_owner":
					table.maps[0].DungeonIDs = []int64{4967}
				}
				r := &Resolver{table: table}
				if _, handled, err := r.fiendWarDeclaredRoom(d, maze, index, key); !handled || !errors.Is(err, ErrMapReferenceMissing) {
					t.Fatalf("invalid declaration allowed fallback: handled=%t err=%v", handled, err)
				}
			})
		}
	}
}

func TestFiendWarMultiMazeStartWithoutRectangleAndScope(t *testing.T) {
	for _, id := range []int64{4954, 4977} {
		maze := Maze{Start: &MazePoint{}, Boss: &MazePoint{X: 2}}
		d := Dungeon{ID: id, Path: "dungeon/gcontents/180621_fiendwar/cosmo_test.dgn", Mazes: []Maze{maze, maze}}
		table := &Table{maps: []Map{{ID: 100, DungeonIDs: []int64{id}}}}
		index := mazeMapIndex{bySpecification: map[coordinateKey][]indexedMap{{}: {{mapPos: 0, specificationType: "start"}}}}
		r := &Resolver{table: table}
		candidates, handled, err := r.fiendWarDeclaredRoom(d, maze, index, coordinateKey{})
		if err != nil || !handled || len(candidates) != 1 {
			t.Fatalf("declared native start without rectangle rejected: id=%d handled=%t err=%v", id, handled, err)
		}
		for _, change := range []string{"ordinary_path", "single_maze", "outside_id"} {
			altered := d
			switch change {
			case "ordinary_path":
				altered.Path = "dungeon/ordinary.dgn"
			case "single_maze":
				altered.Mazes = []Maze{maze}
			case "outside_id":
				altered.ID = 4979
			}
			if _, handled, _ := r.fiendWarDeclaredRoom(altered, maze, index, coordinateKey{}); handled {
				t.Fatalf("scope escaped: %s", change)
			}
		}
	}
}
