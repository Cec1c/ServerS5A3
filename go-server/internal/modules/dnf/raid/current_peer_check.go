package raid

import (
	"encoding/binary"
	"fmt"
)

// CurrentPeerCheckRequest is CMD889 from immutable 80f855, 01A55070.
// Current v96 actor caller 034E110A passes channel from 02964680 first,
// target UID second, and advertised server from 02964560 last.
// This is separate from the historical three-byte CheckRaidUserRequest.
type CurrentPeerCheckRequest struct {
	Server        byte
	TargetCharKey uint16
	Channel       byte
}

func DecodeCurrentPeerCheckRequest(body []byte) (CurrentPeerCheckRequest, error) {
	if len(body) != 4 {
		return CurrentPeerCheckRequest{}, fmt.Errorf("current raid peer check requires exactly four bytes")
	}
	return CurrentPeerCheckRequest{Server: body[3], TargetCharKey: binary.LittleEndian.Uint16(body[1:3]), Channel: body[0]}, nil
}

// BuildCurrentUnassignedPeerCheckACK is only valid after actual runtime lookup
// establishes that this target has no raid. ACK1FAE240 consumes u32/u16/4*u8
// after success, sets the inactive cache, reopens the menu, and clears pending.
// Existing-raid tail semantics are not established and must not be faked here.
func BuildCurrentUnassignedPeerCheckACK() []byte { body := make([]byte, 11); body[0] = 1; return body }
