package worldmap

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

type SelectedRandomizedObject struct {
	Group       int
	SourceIndex int
	Map         RoomCoordinate
	Position    Point
	ObjectID    int64
	ObjectType  string
	Team        string
	MinimapIcon OptionalInt
}

// One immutable selection belongs to one prepared run. A follower validates
// the exact source signature and uses the leader's choices without new RNG.
type RandomizedObjectPlan struct {
	dungeonID       int64
	mazeIndex       int
	sourceSignature [32]byte
	entries         []SelectedRandomizedObject
}

func randomizedObjectSourceSignature(dungeon Dungeon, mazeIndex int) ([32]byte, error) {
	if dungeon.ID <= 0 || mazeIndex < 0 || mazeIndex >= len(dungeon.Mazes) {
		return [32]byte{}, fmt.Errorf("randomized dungeon/maze owner unavailable")
	}
	data, err := json.Marshal(struct {
		ID      int64
		Maze    int
		Scripts []RandomizedObjectScript
	}{dungeon.ID, mazeIndex, dungeon.Mazes[mazeIndex].RandomizedObjects})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(data), nil
}

func FreezeRandomizedObjects(dungeon Dungeon, mazeIndex int, topology *DungeonTopology, next func(int) (int, error)) (*RandomizedObjectPlan, error) {
	signature, err := randomizedObjectSourceSignature(dungeon, mazeIndex)
	if err != nil {
		return nil, err
	}
	if topology == nil || topology.DungeonID != dungeon.ID || topology.MazeIndex != mazeIndex {
		return nil, fmt.Errorf("randomized objects require matching room topology")
	}
	plan := &RandomizedObjectPlan{dungeonID: dungeon.ID, mazeIndex: mazeIndex, sourceSignature: signature}
	scripts := dungeon.Mazes[mazeIndex].RandomizedObjects
	if len(scripts) > 255 {
		return nil, fmt.Errorf("randomized object groups exceed native count")
	}
	for group, script := range scripts {
		if !script.SelectCount.Set || script.SelectCount.Value <= 0 || script.SelectCount.Value > int64(len(script.Objects)) || !script.Regenerate.Set || script.Regenerate.Value != 0 || len(script.UnknownSections) != 0 {
			return nil, fmt.Errorf("unsupported randomized object group %d", group)
		}
		if script.MinimapIcon.Set && (script.MinimapIcon.Value < 0 || script.MinimapIcon.Value > 255) {
			return nil, fmt.Errorf("randomized minimap icon invalid")
		}
		if len(script.Objects) > 255 {
			return nil, fmt.Errorf("randomized candidates exceed native count")
		}
		indices := make([]int, len(script.Objects))
		for index, object := range script.Objects {
			indices[index] = index
			kind := strings.ToLower(strings.TrimSpace(object.ObjectType))
			if kind != "" && kind != "monster" && kind != "passive object" && kind != "ridable object" {
				return nil, fmt.Errorf("unknown randomized object type %q", kind)
			}
			if object.MazeCoordinate == nil || object.Position == nil || !object.ObjectIndex.Set || object.ObjectIndex.Value <= 0 || object.ObjectIndex.Value > int64(^uint32(0)) || len(object.UnknownSections) != 0 {
				return nil, fmt.Errorf("incomplete randomized object %d/%d", group, index)
			}
			coordinate := RoomCoordinate{X: object.MazeCoordinate.X, Y: object.MazeCoordinate.Y}
			if coordinate.X < 0 || coordinate.X > 255 || coordinate.Y < 0 || coordinate.Y > 255 {
				return nil, fmt.Errorf("randomized maze coordinate exceeds native byte")
			}
			if _, ok := topology.Room(coordinate); !ok {
				return nil, fmt.Errorf("randomized object map %s is not a real dungeon room", coordinate)
			}
			if object.Position.X < 0 || object.Position.X > int64(^uint32(0)) || object.Position.Y < 0 || object.Position.Y > int64(^uint32(0)) {
				return nil, fmt.Errorf("randomized position exceeds native range")
			}
			if object.Team != "monster" && object.Team != "character" && object.Team != "neutral" {
				return nil, fmt.Errorf("randomized faction unavailable")
			}
		}
		if int(script.SelectCount.Value) < len(indices) {
			if next == nil {
				return nil, fmt.Errorf("randomized selection requires owned RNG")
			}
			for n := len(indices) - 1; n > 0; n-- {
				chosen, err := next(n + 1)
				if err != nil {
					return nil, err
				}
				if chosen < 0 || chosen > n {
					return nil, fmt.Errorf("randomized selector returned out of range")
				}
				indices[n], indices[chosen] = indices[chosen], indices[n]
			}
		}
		indices = indices[:int(script.SelectCount.Value)]
		for _, index := range indices {
			object := script.Objects[index]
			plan.entries = append(plan.entries, SelectedRandomizedObject{Group: group, SourceIndex: index, Map: RoomCoordinate{X: object.MazeCoordinate.X, Y: object.MazeCoordinate.Y}, Position: *object.Position, ObjectID: object.ObjectIndex.Value, ObjectType: strings.ToLower(strings.TrimSpace(object.ObjectType)), Team: object.Team, MinimapIcon: script.MinimapIcon})
		}
	}
	return plan, nil
}

func (p *RandomizedObjectPlan) Validate(dungeon Dungeon, mazeIndex int) error {
	if p == nil || p.dungeonID != dungeon.ID || p.mazeIndex != mazeIndex {
		return fmt.Errorf("randomized plan belongs to another run selection")
	}
	signature, err := randomizedObjectSourceSignature(dungeon, mazeIndex)
	if err != nil {
		return err
	}
	if signature != p.sourceSignature {
		return fmt.Errorf("randomized source changed after selection")
	}
	return nil
}

func (p *RandomizedObjectPlan) Clone() *RandomizedObjectPlan {
	if p == nil {
		return nil
	}
	v := *p
	v.entries = append([]SelectedRandomizedObject(nil), p.entries...)
	return &v
}
func (p *RandomizedObjectPlan) Entries() []SelectedRandomizedObject {
	if p == nil {
		return nil
	}
	return append([]SelectedRandomizedObject(nil), p.entries...)
}
