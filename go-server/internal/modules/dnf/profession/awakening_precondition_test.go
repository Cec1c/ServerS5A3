package profession

import (
	"errors"
	"testing"
)

// TestCheckAwakeningPreconditionMirrorsPlanTransitionState 固定接取预检与结算共用的觉醒前置：
// 目标必须正好是当前觉醒阶段 +1；chain type 与目标范围不合法同样按同一档拒绝。
func TestCheckAwakeningPreconditionMirrorsPlanTransitionState(t *testing.T) {
	first := Request{Kind: KindAwakening, ChainType: 2, GrowNumber: 1}
	if err := CheckAwakeningPrecondition(0x01, first); err != nil {
		t.Fatalf("first awakening from stage 0 must be allowed: %v", err)
	}
	if err := CheckAwakeningPrecondition(0x05, first); err != nil {
		t.Fatalf("first awakening for another first-branch must be allowed: %v", err)
	}
	second := Request{Kind: KindAwakening, ChainType: 2, GrowNumber: 2}
	if err := CheckAwakeningPrecondition(0x11, second); err != nil {
		t.Fatalf("second awakening from stage 1 must be allowed: %v", err)
	}
	for _, tc := range []struct {
		current byte
		request Request
	}{
		{0x11, first},
		{0x21, first},
		{0x01, second},
		{0x21, second},
		{0x01, Request{Kind: KindAwakening, ChainType: 1, GrowNumber: 1}},
		{0x01, Request{Kind: KindAwakening, ChainType: 2, GrowNumber: 0}},
		{0x01, Request{Kind: KindAwakening, ChainType: 2, GrowNumber: 3}},
		{0x01, Request{Kind: KindClassChange, ChainType: 1, GrowNumber: 1}},
	} {
		if err := CheckAwakeningPrecondition(tc.current, tc.request); !errors.Is(err, ErrTransitionOutOfStep) {
			t.Fatalf("current=0x%02x request=%+v err=%v, want ErrTransitionOutOfStep", tc.current, tc.request, err)
		}
	}
}
