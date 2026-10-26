package dnfbridge

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestTownReturnDirectionR128UsesFrozenMovementCode(t *testing.T) {
	const (
		characterID        = uint16(47)
		frozenDirection    = byte(6)
		persistedDirection = byte(2)
		areaState          = byte(3)
	)

	transition := currentDungeonTownTransition{
		TownID:         19,
		AreaID:         2,
		ActorObjectKey: currentSceneActorObjectKey(characterID),
		Direction:      persistedDirection,
		AreaState:      areaState,
	}
	snapshot := currentTownPositionSnapshot{
		CharacterID:     characterID,
		TownID:          transition.TownID,
		AreaID:          transition.AreaID,
		PositionX:       640,
		PositionY:       240,
		MovementCode:    frozenDirection,
		OpaqueScaledU16: 987,
		PositionValid:   true,
	}

	got, err := applyCurrentTownPositionSnapshotToTransition(
		transition,
		snapshot,
		characterID,
		"current_exe_op35_runtime_origin_snapshot",
	)
	if err != nil {
		t.Fatal(err)
	}

	want := []byte{
		19, 2, 1, 0,
		byte(characterID), byte(characterID >> 8),
		0x80, 0x02,
		0xf0, 0x00,
		frozenDirection,
		areaState,
		1,
	}
	if !bytes.Equal(got.Body, want) {
		t.Fatalf("op24 body=%x want frozen direction %d and existing area state %d: %x",
			got.Body, frozenDirection, areaState, want)
	}
	if got.Direction != frozenDirection {
		t.Fatalf("transition direction=%d want frozen movement code %d (persisted transition input was %d)",
			got.Direction, frozenDirection, persistedDirection)
	}
	if got.AreaState != areaState {
		t.Fatalf("transition area state=%d want existing value %d", got.AreaState, areaState)
	}
}

func TestTownReturnDirectionR128PreservesEveryLegalFrozenDirection(t *testing.T) {
	for _, frozenDirection := range []byte{2, 3, 4, 5, 6, 8, 9} {
		frozenDirection := frozenDirection
		t.Run(fmt.Sprintf("direction_%d", frozenDirection), func(t *testing.T) {
			transition := currentDungeonTownTransition{
				TownID:         19,
				AreaID:         2,
				ActorObjectKey: 47,
				Direction:      2,
				AreaState:      3,
			}
			snapshot := currentTownPositionSnapshot{
				CharacterID:   47,
				TownID:        19,
				AreaID:        2,
				PositionX:     640,
				PositionY:     240,
				MovementCode:  frozenDirection,
				PositionValid: true,
			}

			got, err := applyCurrentTownPositionSnapshotToTransition(
				transition,
				snapshot,
				snapshot.CharacterID,
				"current_exe_op35_runtime_origin_snapshot",
			)
			if err != nil {
				t.Fatal(err)
			}
			if got.Direction != frozenDirection || len(got.Body) != 13 || got.Body[10] != frozenDirection || got.Body[11] != transition.AreaState {
				t.Fatalf("frozen=%d direction=%d body=%x want direction=%d area_state=%d",
					frozenDirection, got.Direction, got.Body, frozenDirection, transition.AreaState)
			}
		})
	}
}

func TestTownReturnDirectionR128ZeroUsesPersistedFallback(t *testing.T) {
	transition := currentDungeonTownTransition{TownID: 19, AreaID: 2, ActorObjectKey: 47, Direction: 2, AreaState: 3}
	snapshot := currentTownPositionSnapshot{CharacterID: 47, TownID: 19, AreaID: 2, PositionX: 640, PositionY: 240, PositionValid: true}
	got, err := applyCurrentTownPositionSnapshotToTransition(transition, snapshot, 47, "persisted_origin")
	if err != nil {
		t.Fatal(err)
	}
	if got.Direction != 2 || len(got.Body) != 13 || got.Body[10] != 2 || got.Body[11] != 3 {
		t.Fatalf("zero movement lost persisted fallback: direction=%d body=%x", got.Direction, got.Body)
	}
}

func TestTownReturnDirectionR128RejectsInvalidOrigin(t *testing.T) {
	transition := currentDungeonTownTransition{
		TownID:         19,
		AreaID:         2,
		ActorObjectKey: 47,
		Direction:      2,
		AreaState:      3,
	}
	for _, tc := range []struct {
		name string
		snap currentTownPositionSnapshot
	}{
		{
			name: "position invalid",
			snap: currentTownPositionSnapshot{
				CharacterID: 47, TownID: 19, AreaID: 2, MovementCode: 6,
			},
		},
		{
			name: "character mismatch",
			snap: currentTownPositionSnapshot{
				CharacterID: 48, TownID: 19, AreaID: 2, MovementCode: 6, PositionValid: true,
			},
		},
		{
			name: "scene mismatch",
			snap: currentTownPositionSnapshot{
				CharacterID: 47, TownID: 19, AreaID: 1, MovementCode: 6, PositionValid: true,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := applyCurrentTownPositionSnapshotToTransition(
				transition,
				tc.snap,
				47,
				"current_exe_op35_runtime_origin_snapshot",
			)
			if !errors.Is(err, errCurrentDungeonTownReturnOriginUnavailable) {
				t.Fatalf("invalid origin error=%v want unavailable-origin sentinel", err)
			}
		})
	}
}
