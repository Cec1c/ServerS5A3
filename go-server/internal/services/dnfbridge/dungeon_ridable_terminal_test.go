package dnfbridge

import (
	"sync"
	"sync/atomic"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

func ridableTerminalTestOwner() *currentDungeonRandomizedOwner {
	return &currentDungeonRandomizedOwner{
		ridableTerminals: &currentDungeonRidableTerminalOwner{},
		Entries: []currentDungeonRandomizedEntry{{
			Source:  worldmap.SelectedRandomizedObject{Group: 0, SourceIndex: 0, Map: worldmap.RoomCoordinate{X: 2, Y: 1}, ObjectID: 69000, Regenerate: worldmap.OptionalInt{Set: true, Value: 0}},
			Ridable: true,
		}},
	}
}

func TestCurrentRidableTerminalOwnerPreservesFrozenSourceAndDoesNotRevive(t *testing.T) {
	o := ridableTerminalTestOwner()
	clone := o.clone()
	room := worldmap.RoomCoordinate{X: 2, Y: 1}
	for _, state := range []byte{2, 1} {
		changed, err := o.applyRidableReport(room, currentDungeonRidableRequest{State: state, DefinitionKeyRaw: 69000, RoomX: 2, RoomY: 1})
		if err != nil || changed || len(clone.roomGroups(room)) != 1 {
			t.Fatal("local mount/release changed source", changed, err)
		}
	}
	for i, state := range []byte{3, 3, 2, 1, 3} {
		changed, err := clone.applyRidableReport(room, currentDungeonRidableRequest{State: state, DefinitionKeyRaw: 69000, RoomX: 2, RoomY: 1})
		if err != nil || changed != (i == 0) {
			t.Fatal("terminal transition", i, changed, err)
		}
		if len(o.roomGroups(room)) != 0 || len(o.Entries) != 1 || o.Entries[0].Source.ObjectID != 69000 {
			t.Fatal("terminal filter mutated or revived frozen source")
		}
	}
	if len(ridableTerminalTestOwner().roomGroups(room)) != 1 {
		t.Fatal("new run inherited prior run terminal state")
	}
}

func TestCurrentRidableTerminalOwnerRejectsAmbiguousOrUnprovenSource(t *testing.T) {
	for _, name := range []string{"ambiguous", "nonridable", "passive", "regen99", "missing_policy", "missing_room", "wrong_room", "unknown_key", "invalid_state"} {
		t.Run(name, func(t *testing.T) {
			o := ridableTerminalTestOwner()
			room := worldmap.RoomCoordinate{X: 2, Y: 1}
			report := currentDungeonRidableRequest{State: 3, DefinitionKeyRaw: 69000, RoomX: 2, RoomY: 1}
			switch name {
			case "ambiguous":
				other := o.Entries[0]
				other.Source.SourceIndex = 1
				o.Entries = append(o.Entries, other)
			case "nonridable":
				o.Entries[0].Ridable = false
			case "passive":
				o.Entries[0].SpawnMode = 1
			case "regen99":
				o.Entries[0].Source.Regenerate.Value = 99
			case "missing_policy":
				o.Entries[0].Source.Regenerate.Set = false
			case "missing_room":
				report.RoomX, report.RoomY = 255, 255
			case "wrong_room":
				report.RoomX = 3
			case "unknown_key":
				report.DefinitionKeyRaw = 0x10010d88
			case "invalid_state":
				report.State = 4
			}
			changed, err := o.applyRidableReport(room, report)
			if err == nil || changed || len(o.ridableTerminals.destroyed) != 0 {
				t.Fatal("unproven source accepted", changed, err)
			}
		})
	}
}

func TestCurrentRidableTerminalOwnerConcurrentClonesRetireOnce(t *testing.T) {
	o := ridableTerminalTestOwner()
	var changed atomic.Int32
	var failures atomic.Int32
	var wait sync.WaitGroup
	for i := 0; i < 64; i++ {
		clone := o.clone()
		wait.Add(1)
		go func() {
			defer wait.Done()
			ok, err := clone.applyRidableReport(worldmap.RoomCoordinate{X: 2, Y: 1}, currentDungeonRidableRequest{State: 3, DefinitionKeyRaw: 69000, RoomX: 2, RoomY: 1})
			if err != nil {
				failures.Add(1)
			}
			if ok {
				changed.Add(1)
			}
			_ = clone.roomGroups(worldmap.RoomCoordinate{X: 2, Y: 1})
		}()
	}
	wait.Wait()
	if changed.Load() != 1 || failures.Load() != 0 {
		t.Fatal("shared terminal owner forked", changed.Load(), failures.Load())
	}
}
