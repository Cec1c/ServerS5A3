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
func BuildCurrentUnassignedPeerCheckACK() []byte { return BuildCurrentPeerCheckACK(0, 0, 0) }

// ACK1FAE240 stores the first u32 and u16 in the pending target's native
// menu cache at +1B0/+1B4. The native menu reads +1B0 through 1A54B50:
// a real raid key selects its existing join/view action instead of invite.
// The first trailing byte is the real member count, stored at +1B6. The
// native join click (1A67860) silently returns when it is zero and checks it
// against route capacity before opening the application confirmation dialog.
// Only the final three bytes are reserved.
func BuildCurrentPeerCheckACK(raidKey uint32, leader uint16, memberCount byte) []byte {
	body := make([]byte, 11)
	body[0] = 1
	binary.LittleEndian.PutUint32(body[1:5], raidKey)
	binary.LittleEndian.PutUint16(body[5:7], leader)
	body[7] = memberCount
	return body
}
