package dnfbridge

import (
	"errors"
	"fmt"
)

// currentDespairResultMsgID is the message id of the despair-tower floor result
// notice. The verified v111 registration pairs opcode 348 with the NOTI
// (class0) table helper 0x251E280 and handler 0x1C577A0, so this notice is a
// class0 notification rather than a COMMAND response.
const currentDespairResultMsgID uint16 = 348

// Verified v111 handler 0x1C577A0 consumes one fixed prefix followed by one
// repeated row per reward:
//
//	u32 clear time in milliseconds
//	u16 cleared floor (the tower's own 1..100 floor domain)
//	u8  reward count
//	repeated { u32 item id, u32 quantity }
const (
	currentDespairResultMinimumFloor = 1
	currentDespairResultMaximumFloor = 100
	currentDespairResultMaximumItems = 0xff
)

var (
	errCurrentDespairResultFloor = errors.New("current despair result cleared floor is out of range")
	errCurrentDespairResultCount = errors.New("current despair result reward count exceeds one byte")
	errCurrentDespairResultItem  = errors.New("current despair result reward item id or quantity is zero")
)

// currentDespairResultItem is one repeated reward row of the verified body:
// u32 item id followed by u32 quantity. The quantity is a granted stack count,
// not a container slot.
type currentDespairResultItem struct {
	ItemID   uint32
	Quantity uint32
}

// buildCurrentDespairResultBody serializes only the verified v111 body. It
// neither sends the notice nor awards anything: an authoritative owner must
// have committed the reward before calling this builder, and the caller keeps
// responsibility for the frame wrapper and for the class0 registration that
// currentDespairResultMsgID names.
func buildCurrentDespairResultBody(clearTimeMS uint32, clearedFloor uint16, items []currentDespairResultItem) ([]byte, error) {
	if clearedFloor < currentDespairResultMinimumFloor || clearedFloor > currentDespairResultMaximumFloor {
		return nil, fmt.Errorf("%w: cleared_floor=%d", errCurrentDespairResultFloor, clearedFloor)
	}
	if len(items) > currentDespairResultMaximumItems {
		return nil, fmt.Errorf("%w: reward_count=%d", errCurrentDespairResultCount, len(items))
	}
	var writer packetWriter
	writer.writeUint32(clearTimeMS)
	writer.writeUint16(clearedFloor)
	writer.writeByte(byte(len(items)))
	for index, item := range items {
		if item.ItemID == 0 || item.Quantity == 0 {
			return nil, fmt.Errorf(
				"%w: row=%d item=%d quantity=%d",
				errCurrentDespairResultItem,
				index,
				item.ItemID,
				item.Quantity,
			)
		}
		writer.writeUint32(item.ItemID)
		writer.writeUint32(item.Quantity)
	}
	return writer.bytes(), nil
}
