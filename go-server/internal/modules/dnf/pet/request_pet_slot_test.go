package pet

import (
	"bytes"
	"testing"
)

// 现场包（角色 3，2026-09-24 晚，宠物更名卡窗口点确认）：
//
//	RECV type=100 body_len=10 hex=...bf000703000000 313233
//	= u16 slot=191 + u8 list=7 + u32 name_len=3 + "123"（第二次 slot=190）
//
// 旧的解码器把槽位限制在 0..139（"creature range"），于是宠物消耗品段 189..239 里的卡
// 每次改名都在解析阶段就被拒（线上日志：slot 191 outside creature range 0..139），
// 玩家看到的就是"窗口能开、点确认没反应"。A21 参考实现校验的正是 189..239。
func TestDecodeRenameCreatureRequestAcceptsPetConsumableSlots(t *testing.T) {
	for _, slot := range []uint16{190, 191, 189, 239} {
		body := make([]byte, 0, 10)
		body = append(body, byte(slot), byte(slot>>8), listTypePet, 3, 0, 0, 0)
		body = append(body, []byte("123")...)
		decoded, err := DecodeRenameCreatureRequest(body)
		if err != nil {
			t.Fatalf("slot %d: %v", slot, err)
		}
		if int16(slot) != decoded.SlotIndex || decoded.ListType != listTypePet || !bytes.Equal(decoded.NameRaw, []byte("123")) {
			t.Fatalf("slot %d decoded %+v", slot, decoded)
		}
	}
	// 140..188（宠物装备段）仍然拒绝，主背包 0..139 继续兼容。
	for _, slot := range []uint16{140, 188} {
		body := make([]byte, 0, 10)
		body = append(body, byte(slot), byte(slot>>8), listTypePet, 1, 0, 0, 0, 'x')
		if _, err := DecodeRenameCreatureRequest(body); err == nil {
			t.Fatalf("slot %d must stay rejected", slot)
		}
	}
	body := make([]byte, 0, 10)
	body = append(body, 5, 0, listTypePet, 1, 0, 0, 0, 'x')
	if _, err := DecodeRenameCreatureRequest(body); err != nil {
		t.Fatalf("legacy main-inventory slot 5 must keep decoding: %v", err)
	}
}
