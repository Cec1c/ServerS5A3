package dnfbridge

import (
	"encoding/binary"
	"testing"
)

func TestQuestCouponAckPreservesActionAcrossRemainingQuantities(t *testing.T) {
	for _, remaining := range []int64{0, 1, 3, 70000} {
		body := buildCurrentQuestClearCouponAck(87, remaining)
		if len(body) < 8 || body[0] != 1 || binary.LittleEndian.Uint16(body[1:3]) != 87 || body[3] != 0 || binary.LittleEndian.Uint32(body[4:8]) != 167 {
			t.Fatalf("remaining=%d decoded op515 header=%x, want result=1 slot=87 list=0 action=167", remaining, body)
		}
	}
}
