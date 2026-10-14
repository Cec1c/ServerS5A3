package inventory

import (
	"encoding/binary"
	"testing"
)

// Current EXE dispatch consumes result, then 0x24f3580 reads u16 slot,
// u8 list and u32 action. The old quantity-based body dispatched count<<8.
func TestDamageFontAckPreservesActionAcrossRemainingQuantities(t *testing.T) {
	for _, remaining := range []int64{0, 1, 3, 70000} {
		body := buildUseStackableActionSuccessAck(DamageFontUnlockResult{SourceSlotIndex: 87, RemainingCount: remaining})
		if len(body) < 8 || body[0] != 1 || binary.LittleEndian.Uint16(body[1:3]) != 87 || body[3] != 0 || binary.LittleEndian.Uint32(body[4:8]) != 162 {
			t.Fatalf("remaining=%d decoded op515 header=%x, want result=1 slot=87 list=0 action=162", remaining, body)
		}
	}
}
