package aikenupgrade

import (
	"encoding/binary"
	"errors"
)

// ErrSetRequest rejects an op1341 body whose wire shape is not exactly the
// current client's u8 options array followed by a u8/u16 target array.
var ErrSetRequest = errors.New("aiken set request rejected")

// SetRequest is the current EXE's class1 op1341 body. The options and targets
// are independent wire arrays; their business pairing is deliberately left to
// a caller that has receiver evidence.
type SetRequest struct {
	Options []byte
	Targets []uint16
}

// DecodeSetRequest decodes the current v151 op1341 body:
//
//	u8 optionCount; optionCount * u8 optionID
//	u8 targetCount; targetCount * u16 targetSlot
//
// The body must end exactly after targetCount targets. The target values are
// main-bag (wire list type 0) slot indices as established by the current EXE;
// this decoder only preserves them so that endpoint and equipment validation
// remain in the asset owner.
func DecodeSetRequest(body []byte) (SetRequest, error) {
	if len(body) < 2 {
		return SetRequest{}, ErrSetRequest
	}

	optionCount := int(body[0])
	targetCountOffset := 1 + optionCount
	if targetCountOffset >= len(body) {
		return SetRequest{}, ErrSetRequest
	}

	targetCount := int(body[targetCountOffset])
	remaining := len(body) - targetCountOffset - 1
	if remaining != targetCount*2 {
		return SetRequest{}, ErrSetRequest
	}

	req := SetRequest{
		Options: append([]byte(nil), body[1:targetCountOffset]...),
		Targets: make([]uint16, targetCount),
	}
	targets := body[targetCountOffset+1:]
	for i := range req.Targets {
		req.Targets[i] = binary.LittleEndian.Uint16(targets[i*2:])
	}
	return req, nil
}
