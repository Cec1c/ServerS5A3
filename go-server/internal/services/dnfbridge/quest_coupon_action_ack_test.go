package dnfbridge

import (
	"bytes"
	"testing"
)

// Current runtime action table: first awakening=118, gm quest clear=169,
// lv clear=179. Success carries u32 action; failure carries the low action byte.
func TestQuestCouponResultPreservesCurrentNativeActions(t *testing.T) {
	for _, action := range []uint32{
		currentFirstAwakeningCouponActionIndex,
		currentQuestClearCouponActionIndex,
		currentQuestLevelClearActionIndex,
	} {
		for _, list := range []byte{0, 2} {
			r := currentQuestClearCouponRequest{SourceSlotIndex: 87, ListType: list, ActionIndex: action}
			success := buildCurrentQuestCouponResult(r, true)
			wantSuccess := []byte{1, 0x57, 0, list, byte(action), byte(action >> 8), byte(action >> 16), byte(action >> 24)}
			if !bytes.Equal(success, wantSuccess) {
				t.Fatalf("action=%d list=%d success=%x want=%x", action, list, success, wantSuccess)
			}
			failure := buildCurrentQuestCouponResult(r, false)
			wantFailure := []byte{0, 19, 0x57, 0, list, byte(action)}
			if !bytes.Equal(failure, wantFailure) {
				t.Fatalf("action=%d list=%d failure=%x want=%x", action, list, failure, wantFailure)
			}
		}
	}
	if _, matched := decodeCurrentQuestClearCouponRequest(testQuestClearCouponRequestBody(87, 167)); matched {
		t.Fatal("historical action 167 must not match the current coupon action table")
	}
}
