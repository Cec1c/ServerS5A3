package dnfbridge

import (
	"encoding/binary"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestWeddingCharacterUsesOwnedProfileAndAppearanceSlots(t *testing.T) {
	c := r.CharacterRecord{CharacterID: "23", AccountID: "a", Name: "婚姻角色", Job: "11", Level: 90, Stats: map[string]int64{"grow_type": 0x24}}
	e := r.EquipmentRecord{CharacterID: "23", Entries: map[string]r.EquipmentEntry{"0": {SlotIndex: 0, ItemID: 100}, "12": {SlotIndex: 12, ItemID: 222}, "13": {SlotIndex: 13, ItemID: 333}}}
	const receiverToken = uint32(0x10203)
	b, err := buildCurrentWeddingCharacter(c, e, receiverToken)
	if err != nil || len(b) != 98 || b[0] != 1 {
		t.Fatalf("profile len=%d %v", len(b), err)
	}
	if binary.LittleEndian.Uint32(b[5:]) != receiverToken || b[43] != 90 || b[44] != 11 || b[45] != 0x24 {
		t.Fatalf("header %x", b[:46])
	}
	name, err := decodeCharacterName(b[13:43])
	if err != nil || name != c.Name {
		t.Fatalf("name %q %v", name, err)
	}
	if binary.LittleEndian.Uint32(b[46:]) != 100 || binary.LittleEndian.Uint32(b[94:]) != 222 {
		t.Fatalf("appearance %x", b[46:])
	}
	if _, err = buildCurrentWeddingCharacter(c, r.EquipmentRecord{CharacterID: "24"}, receiverToken); err == nil {
		t.Fatal("foreign equipment accepted")
	}
	c.Name = "bad\x00name"
	if _, err = buildCurrentWeddingCharacter(c, e, receiverToken); err == nil {
		t.Fatal("embedded terminator accepted")
	}
}

func TestWeddingPartnerPreviewPreservesDefaultOutfitAndCloneDisplay(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	c := r.CharacterRecord{CharacterID: "19", AccountID: "a", Name: "伴侣", Job: "11", Level: 90}
	for _, equipped := range []bool{false, true} {
		e := r.EquipmentRecord{CharacterID: "19"}
		if equipped {
			e = cloneAvatarProjectionFixture(t, map[string]string{r.CloneAvatarAppearanceExtraKey: "101580163"})
			e.Entries["12"] = r.EquipmentEntry{SlotIndex: 12, ItemID: 112010001}
		}
		b, err := buildCurrentWeddingCharacter(c, e, 77)
		if err != nil {
			t.Fatal(err)
		}
		for slot := 0; slot < 13; slot++ {
			want := uint32(0xffffffff)
			if equipped && slot == 3 {
				want = 101580163
			}
			if equipped && slot == 12 {
				want = 112010001
			}
			if got := binary.LittleEndian.Uint32(b[46+slot*4:]); got != want {
				t.Fatalf("equipped=%v slot=%d display=%08x want=%08x", equipped, slot, got, want)
			}
		}
	}
}
