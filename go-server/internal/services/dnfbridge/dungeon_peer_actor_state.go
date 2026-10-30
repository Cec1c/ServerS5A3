package dnfbridge

import (
	"encoding/binary"
	"fmt"
)

const (
	currentDungeonPeerActorMode0KeyOffset        = 5 + 0x47
	currentDungeonPeerActorMode0NameLengthOffset = currentDungeonPeerActorMode0KeyOffset + 2
	currentDungeonPeerActorMode0NameOffset       = currentDungeonPeerActorMode0NameLengthOffset + 4
	currentDungeonPeerActorMode0StateTailOffset  = 5
)

// projectCurrentDungeonPeerActorState returns a copy of one mode0 actor whose
// state byte is set to 1. The source body is never modified.
func projectCurrentDungeonPeerActorState(body []byte, objectKey uint16) ([]byte, error) {
	if len(body) < currentDungeonPeerActorMode0NameOffset {
		return nil, fmt.Errorf("dungeon peer actor mode0 header is truncated")
	}
	if body[0] != currentCrossSceneMode0Subtype {
		return nil, fmt.Errorf("dungeon peer actor body is not mode0")
	}
	if binary.LittleEndian.Uint16(body[1:3]) != 1 {
		return nil, fmt.Errorf("dungeon peer actor mode0 must contain exactly one actor")
	}
	// The advertised route can be nonzero (the live service uses 1).
	// The strict builder has already bound it to the receiver's server.
	if body[3] >= 250 {
		return nil, fmt.Errorf("dungeon peer actor mode0 route is invalid")
	}
	if binary.LittleEndian.Uint16(body[currentDungeonPeerActorMode0KeyOffset:currentDungeonPeerActorMode0KeyOffset+2]) != objectKey {
		return nil, fmt.Errorf("dungeon peer actor mode0 object key differs")
	}

	nameLength := binary.LittleEndian.Uint32(body[currentDungeonPeerActorMode0NameLengthOffset:currentDungeonPeerActorMode0NameOffset])
	if nameLength > uint32(rosterNameMaxBytes) {
		return nil, fmt.Errorf("dungeon peer actor mode0 name is too long")
	}
	tailStart := currentDungeonPeerActorMode0NameOffset + int(nameLength)
	if tailStart > len(body) || len(body)-tailStart < currentDungeonPeerActorMode0StateTailOffset+1 {
		return nil, fmt.Errorf("dungeon peer actor mode0 tail is truncated")
	}

	projected := append([]byte(nil), body...)
	projected[tailStart+currentDungeonPeerActorMode0StateTailOffset] = 1
	return projected, nil
}
