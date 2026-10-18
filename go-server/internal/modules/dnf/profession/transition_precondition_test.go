package profession

import (
	"errors"
	"testing"
)

// TestCheckClassChangePreconditionMirrorsPlanTransitionState 固定接取预检与结算共用的
// 转职前置：未转职未觉醒才成立；chain type、目标编号与 kind 不合法都按同一档拒绝。
func TestCheckClassChangePreconditionMirrorsPlanTransitionState(t *testing.T) {
	classChange := Request{Kind: KindClassChange, ChainType: 1, GrowNumber: 2}
	if err := CheckClassChangePrecondition(0x00, classChange); err != nil {
		t.Fatalf("fresh character must satisfy the class-change precondition: %v", err)
	}
	for _, current := range []byte{0x01, 0x02, 0x03, 0x04, 0x11, 0x21} {
		if err := CheckClassChangePrecondition(current, classChange); !errors.Is(err, ErrTransitionOutOfStep) {
			t.Fatalf("current=0x%02x err=%v, want ErrTransitionOutOfStep", current, err)
		}
	}
	for _, bad := range []Request{
		{Kind: KindClassChange, ChainType: 2, GrowNumber: 1},
		{Kind: KindClassChange, ChainType: 1, GrowNumber: 0},
		{Kind: KindAwakening, ChainType: 2, GrowNumber: 1},
	} {
		if err := CheckClassChangePrecondition(0x00, bad); !errors.Is(err, ErrTransitionOutOfStep) {
			t.Fatalf("request=%+v err=%v, want ErrTransitionOutOfStep", bad, err)
		}
	}
	// 觉醒目标不受影响：同一函数只拒绝转职链，不改变 awakening 的前置判定。
	awakening := Request{Kind: KindAwakening, ChainType: 2, GrowNumber: 1}
	if err := CheckClassChangePrecondition(0x01, awakening); err == nil {
		t.Fatal("the class-change precondition must not accept an awakening request")
	}
}
