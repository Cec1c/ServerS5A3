package worldmap

import (
	"context"
	"os"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func actualAntonRandomizedSource(t *testing.T) (Dungeon, *DungeonTopology) {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires actual formal PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := LoadSource(context.Background(), a, Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	dungeon, ok := table.FindDungeon(218)
	if !ok {
		t.Fatal("actual energy map missing")
	}
	topology, err := BuildDungeonLayout(resolver, 218, 0, func(choice DungeonMapChoice) (int64, error) { return choice.Candidates[0].ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	return dungeon, topology
}

func TestRealPVFAntonRandomizedObjectsFreezeExactFiveTransmittersAndNamedMonster(t *testing.T) {
	dungeon, topology := actualAntonRandomizedSource(t)
	calls := 0
	plan, err := FreezeRandomizedObjects(dungeon, 0, topology, func(n int) (int, error) { calls++; return 0, nil })
	if err != nil {
		t.Fatal(err)
	}
	entries := plan.Entries()
	if len(entries) != 6 || calls != 7 {
		t.Fatal("actual 5-of-8 plus one named monster", len(entries), calls)
	}
	seen := map[RoomCoordinate]bool{}
	for _, entry := range entries {
		if entry.Position != (Point{X: 459, Y: 256}) || entry.Team != "monster" {
			t.Fatal("actual randomized object data", entry)
		}
		if entry.Group == 0 {
			if entry.ObjectID != 18865 || entry.ObjectType != "passive object" || !entry.MinimapIcon.Set || entry.MinimapIcon.Value != 6 || seen[entry.Map] {
				t.Fatal("actual small transmission selection", entry)
			}
			seen[entry.Map] = true
		} else if entry.Group != 1 || entry.ObjectID != 58530 || entry.Map != (RoomCoordinate{X: 2, Y: 2}) || entry.ObjectType != "" || entry.MinimapIcon.Value != 4 {
			t.Fatal("actual named monster selection", entry)
		}
	}
	if len(seen) != 5 {
		t.Fatal("random selection duplicates")
	}
	entries[0].ObjectID = 1
	if plan.Entries()[0].ObjectID == 1 {
		t.Fatal("returned entries mutate owner")
	}
	clone := plan.Clone()
	clone.entries[0].ObjectID = 2
	if plan.Entries()[0].ObjectID == 2 {
		t.Fatal("follower clone aliases owner")
	}
	if err := plan.Validate(dungeon, 0); err != nil {
		t.Fatal(err)
	}
	changed := cloneDungeon(dungeon)
	changed.Mazes[0].RandomizedObjects[0].Objects[0].ObjectIndex.Value++
	if err := plan.Validate(changed, 0); err == nil {
		t.Fatal("follower accepted changed PVF source")
	}
	changed = cloneDungeon(dungeon)
	changed.ID++
	if err := plan.Validate(changed, 0); err == nil {
		t.Fatal("follower reused another dungeon owner")
	}
}

func TestRealPVFAntonRandomizedObjectsRejectUnknownOrUnownedCandidates(t *testing.T) {
	dungeon, topology := actualAntonRandomizedSource(t)
	for _, name := range []string{"regenerate", "missing_count", "too_many", "unknown_type", "outside_room", "bad_position", "missing_id", "missing_team", "bad_rng", "nil_rng"} {
		t.Run(name, func(t *testing.T) {
			changed := cloneDungeon(dungeon)
			script := &changed.Mazes[0].RandomizedObjects[0]
			object := &script.Objects[0]
			next := func(n int) (int, error) { return 0, nil }
			switch name {
			case "regenerate":
				script.Regenerate.Value = 1
			case "missing_count":
				script.SelectCount.Set = false
			case "too_many":
				script.SelectCount.Value = 9
			case "unknown_type":
				object.ObjectType = "unknown"
			case "outside_room":
				object.MazeCoordinate = &Point{X: 200, Y: 200}
			case "bad_position":
				object.Position = &Point{X: -1, Y: 0}
			case "missing_id":
				object.ObjectIndex.Set = false
			case "missing_team":
				object.Team = ""
			case "bad_rng":
				next = func(n int) (int, error) { return n, nil }
			case "nil_rng":
				next = nil
			}
			if plan, err := FreezeRandomizedObjects(changed, 0, topology, next); err == nil || plan != nil {
				t.Fatal("invalid random source partially published", err)
			}
		})
	}
}
