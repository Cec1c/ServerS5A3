package dnfbridge

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	currentDungeonRidableRequestLength      = 7
	currentDungeonRidableNotificationLength = 20

	currentDungeonRidableStateReleased  byte = 1
	currentDungeonRidableStateMounted   byte = 2
	currentDungeonRidableStateDestroyed byte = 3
)

var (
	errDungeonRidableRequestLength = errors.New("dnf ridable request body must be exactly 7 bytes")
	errDungeonRidableRequestState  = errors.New("dnf ridable request state must be 1, 2, or 3")
)

// currentDungeonRidableRequest is the C2S316 lifecycle notification. Keep the
// definition key and room bytes raw: a missing room is sent as FF FF and must
// remain available to the future lifecycle owner.
type currentDungeonRidableRequest struct {
	State            byte
	DefinitionKeyRaw uint32
	RoomX            byte
	RoomY            byte
}

func decodeCurrentDungeonRidableRequest(body []byte) (currentDungeonRidableRequest, error) {
	if len(body) != currentDungeonRidableRequestLength {
		return currentDungeonRidableRequest{}, fmt.Errorf(
			"%w: got=%d want=%d",
			errDungeonRidableRequestLength,
			len(body),
			currentDungeonRidableRequestLength,
		)
	}
	request := currentDungeonRidableRequest{
		State:            body[0],
		DefinitionKeyRaw: binary.LittleEndian.Uint32(body[1:5]),
		RoomX:            body[5],
		RoomY:            body[6],
	}
	switch request.State {
	case currentDungeonRidableStateReleased,
		currentDungeonRidableStateMounted,
		currentDungeonRidableStateDestroyed:
		return request, nil
	default:
		return currentDungeonRidableRequest{}, fmt.Errorf("%w: state=%d", errDungeonRidableRequestState, request.State)
	}
}

// buildCurrentDungeonRidableNotification builds the fixed S2C NOTI296 body.
// Its five u32 values intentionally share the START_MAP ridable-row layout;
// State is the wire SpawnKind here, not the C2S316 lifecycle state.
func buildCurrentDungeonRidableNotification(entry currentDungeonStartMapRidableEntry) []byte {
	var writer packetWriter
	writeCurrentDungeonStartMapRidableEntry(&writer, entry)
	return writer.bytes()
}
