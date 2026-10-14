package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 当前 EXE 的 0/2 mode1 和 mode3 都将属性块后的栏位状态写到
// actor.vfunc_0xAC4()+0x1AC；光环幻化槽11通过0x2961950读取其中的0x08位。
func auraSlotNativeState(t *testing.T, body []byte) byte {
	t.Helper()
	if len(body) < 23 || binary.LittleEndian.Uint16(body[1:3]) != 1 {
		t.Fatalf("native actor header=%x", body)
	}
	var lengthOffset int
	switch body[0] {
	case 1:
		lengthOffset = 27
	case 3:
		lengthOffset = 19
	default:
		t.Fatalf("unexpected actor mode=%d", body[0])
	}
	if len(body) < lengthOffset+4 {
		t.Fatal("native stat length truncated")
	}
	statLength := binary.LittleEndian.Uint32(body[lengthOffset : lengthOffset+4])
	if statLength != currentMode1StatBlobWireSize {
		t.Fatalf("stat length=%d", statLength)
	}
	stateOffset := lengthOffset + 4 + int(statLength)
	if stateOffset >= len(body) {
		t.Fatal("native slot state truncated")
	}
	return body[stateOffset]
}

func assertAuraSlotNativeState(t *testing.T, body []byte, want byte) {
	t.Helper()
	if got := auraSlotNativeState(t, body); got != want {
		t.Fatalf("native mode%d slot state=%02x want %02x", body[0], got, want)
	}
}

func TestAuraSkinNativeActorStateRestoresOnlyCommittedEntitlement(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stats map[string]int64
		want  byte
	}{
		{name: "新角色默认锁定", want: 0},
		{name: "未开启不能借其他栏位开光环", stats: map[string]int64{"ex_equip_slot_stat": 0x1b}, want: 0x13},
		{name: "已扣材料开启且不误开耳环", stats: map[string]int64{"aura_flag": 1}, want: 0x08},
		{name: "保留已有三种装备栏", stats: map[string]int64{"ex_equip_slot_stat": 0x13, "aura_flag": 1}, want: 0x1b},
		{name: "锁定光环时保留已有装备栏", stats: map[string]int64{"ex_equip_slot_stat": 0x13, "aura_flag": 0}, want: 0x13},
	} {
		t.Run(tc.name, func(t *testing.T) {
			character := dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 1, Stats: tc.stats}
			service := &Service{}
			for _, body := range [][]byte{
				service.buildCurrentSelectedUserInfoMode1Body(context.Background(), nil, nil, character, true, 19),
				service.buildCurrentSelectedUserInfoMode3Body(context.Background(), nil, nil, character, true, 19),
			} {
				assertAuraSlotNativeState(t, body, tc.want)
			}
		})
	}
}
