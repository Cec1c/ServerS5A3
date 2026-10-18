package worldmap

import "testing"

func singletonRidableSelectionFixture() (Dungeon, *DungeonTopology) {
	coordinate := RoomCoordinate{X: 1, Y: 2}
	dungeon := Dungeon{ID: 700, Mazes: []Maze{{RandomizedObjects: []RandomizedObjectScript{{
		SelectCount: OptionalInt{Set: true, Value: 1}, Regenerate: OptionalInt{Set: true, Value: 99},
		Objects: []RidableObject{{ObjectIndex: OptionalInt{Set: true, Value: 3001}, Team: "neutral", MazeCoordinate: &Point{X: 1, Y: 2}, Position: &Point{X: 210, Y: 240}}},
	}}}}}
	topology := &DungeonTopology{DungeonID: 700, MazeIndex: 0, rooms: map[coordinateKey]DungeonRoom{{x: 1, y: 2}: {Coordinate: coordinate}}}
	return dungeon, topology
}

func TestRandomizedSingletonRidableRetainsRegenerationSource(t *testing.T) {
	dungeon, topology := singletonRidableSelectionFixture()
	plan, err := FreezeRandomizedObjects(dungeon, 0, topology, func(int) (int, error) {
		t.Fatal("singleton must not draw RNG")
		return 0, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := plan.Entries()
	if len(entries) != 1 || entries[0].Regenerate != (OptionalInt{Set: true, Value: 99}) || entries[0].Group != 0 || entries[0].SourceIndex != 0 {
		t.Fatalf("original policy and candidate identity lost: %+v", entries)
	}
	entries[0].Regenerate.Value = 0
	clone := plan.Clone()
	clone.entries[0].Regenerate.Value = 1
	if plan.Entries()[0].Regenerate.Value != 99 {
		t.Fatal("returned copy changed frozen regeneration policy")
	}
	if err := plan.Validate(dungeon, 0); err != nil {
		t.Fatal(err)
	}
	dungeon.Mazes[0].RandomizedObjects[0].Regenerate.Value = 0
	if err := plan.Validate(dungeon, 0); err == nil {
		t.Fatal("changed regeneration source accepted")
	}
}

func TestRandomizedSingletonRidableRejectsUnprovenShapes(t *testing.T) {
	for _, name := range []string{"regenerate1", "regenerate100", "missing_policy", "two_candidates", "two_selected", "hostile", "friendly", "passive", "monster", "unknown_type", "missing_room", "outside_room", "negative_position", "large_position", "missing_id", "unknown_group", "unknown_object", "wrong_topology"} {
		t.Run(name, func(t *testing.T) {
			dungeon, topology := singletonRidableSelectionFixture()
			script := &dungeon.Mazes[0].RandomizedObjects[0]
			object := &script.Objects[0]
			switch name {
			case "regenerate1":
				script.Regenerate.Value = 1
			case "regenerate100":
				script.Regenerate.Value = 100
			case "missing_policy":
				script.Regenerate.Set = false
			case "two_candidates", "two_selected":
				script.Objects = append(script.Objects, *object)
				if name == "two_selected" {
					script.SelectCount.Value = 2
				}
			case "hostile":
				object.Team = "monster"
			case "friendly":
				object.Team = "character"
			case "passive":
				object.ObjectType = "passive object"
			case "monster":
				object.ObjectType = "monster"
			case "unknown_type":
				object.ObjectType = "unknown"
			case "missing_room":
				object.MazeCoordinate = nil
			case "outside_room":
				object.MazeCoordinate.X = 200
			case "negative_position":
				object.Position.X = -1
			case "large_position":
				object.Position.Y = 1 << 32
			case "missing_id":
				object.ObjectIndex.Set = false
			case "unknown_group":
				script.UnknownSections = []RawSection{{Name: "unknown"}}
			case "unknown_object":
				object.UnknownSections = []RawSection{{Name: "unknown"}}
			case "wrong_topology":
				topology.DungeonID++
			}
			if plan, err := FreezeRandomizedObjects(dungeon, 0, topology, nil); err == nil || plan != nil {
				t.Fatalf("unsupported shape published: plan=%+v err=%v", plan, err)
			}
		})
	}
}
