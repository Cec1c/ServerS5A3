package inventory

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// 现场包（HK 线上，角色 3 领取春节欢庆爆竹后右键）：
//
//	RECV type=251 ENUM_CMDPACKET_USE_SHARED_EFFECT_ITEM body_len=6
//	hex=...a2833f1d4100 -> u32 itemCode=490701730 + u16 slot=65
//
// 客户端对带共享特效的消耗品走 op251 而不是 op44；此前服务端没有 handler，
// 一个包都不回，玩家看到的就是"右键没消耗、没特效，之后背包只能看不能点"。
func TestDecodeUseSharedEffectItemMatchesLiveBody(t *testing.T) {
	req, err := DecodeUseSharedEffectItemRequest([]byte{0xA2, 0x83, 0x3F, 0x1D, 0x41, 0x00})
	if err != nil {
		t.Fatalf("decode live op251 body: %v", err)
	}
	if req.ItemCode != 490701730 || req.SlotIndex != 65 || req.ListType != listTypeMain {
		t.Fatalf("decoded %+v, want item=490701730 slot=65 list=%d", req, listTypeMain)
	}
}

func TestDecodeUseSharedEffectItemRejectsOtherShapes(t *testing.T) {
	if _, err := DecodeUseSharedEffectItemRequest([]byte{0x01, 0x02, 0x03}); err == nil {
		t.Fatal("short body must be rejected")
	}
	if _, err := DecodeUseSharedEffectItemRequest([]byte{0x00, 0x00, 0x00, 0x00, 0x41, 0x00}); err == nil {
		t.Fatal("zero item code must be rejected")
	}
}

func TestSharedEffectResultReleasesNativeLockAndCarriesPVFIndex(t *testing.T) {
	r := ownerAppliedUseStackableResult(251, Command{SelectedCharacterID: 6}, UseStackableResult{ItemID: 490701730, SlotIndex: 65, ListType: 0, HasRandomRewardEffect: true, RandomRewardEffectIndex: 1})
	if len(r.UpperResponses) != 2 {
		t.Fatalf("responses=%+v", r.UpperResponses)
	}
	ack, fx := r.UpperResponses[0], r.UpperResponses[1]
	if ack.MsgID != 251 || ack.Classification != 1 || !bytes.Equal(ack.Body, []byte{1, 65, 0, 0}) {
		t.Fatalf("native unlock ACK=%+v", ack)
	}
	if fx.MsgID != 219 || fx.Classification != 0 || len(fx.Body) != 7 || binary.LittleEndian.Uint16(fx.Body) != 6 || binary.LittleEndian.Uint32(fx.Body[2:]) != 490701730 || fx.Body[6] != 1 {
		t.Fatalf("native PVF effect=%+v", fx)
	}
	if len(r.ItemSlotRefreshes) != 1 || r.ItemSlotRefreshes[0].SlotIndex != 65 {
		t.Fatalf("source row not refreshed: %+v", r.ItemSlotRefreshes)
	}
}
