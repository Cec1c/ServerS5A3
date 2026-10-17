package inventory

import (
	"encoding/binary"
	"testing"
)

func TestUpgradeRequestNPCContextTrailer(t *testing.T) {
	// v101 sender 0x25fe206/0x25fe216 writes these two u16 values after
	// the length-prefixed name. Captured Jonathan trailer: dc018700.
	base := make([]byte, 19)
	binary.LittleEndian.PutUint32(base[12:], 3)
	copy(base[16:], "abc")
	for _, tc := range []struct {
		name    string
		tail    []byte
		present bool
		npc     uint16
		slot    int16
	}{
		{"legacy", nil, false, 0xffff, -1},
		{"Jonathan", []byte{0xdc, 1, 0x87, 0}, true, 476, 135},
		{"no NPC", []byte{0xff, 0xff, 0xff, 0xff}, true, 0xffff, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := DecodeUpgradeItemRequest(append(append([]byte(nil), base...), tc.tail...))
			if err != nil {
				t.Fatal(err)
			}
			if r.TargetItemName != "abc" || r.NPCContextPresent != tc.present || r.NPCTemplateID != tc.npc || r.NPCMaterialSlotIndex != tc.slot {
				t.Fatalf("decoded request = %+v", r)
			}
		})
	}
}

func TestUpgradeRequestRejectsTruncatedNPCContext(t *testing.T) {
	for _, n := range []int{1, 2, 3, 5} {
		if _, err := DecodeUpgradeItemRequest(make([]byte, 16+n)); err == nil {
			t.Fatalf("accepted NPC trailer length %d", n)
		}
	}
	for _, n := range []uint32{1, 0x7fffffff, 0xffffffff} {
		body := make([]byte, 16)
		binary.LittleEndian.PutUint32(body[12:], n)
		if _, err := DecodeUpgradeItemRequest(body); err == nil {
			t.Fatalf("accepted unavailable name length %d", n)
		}
	}
}
