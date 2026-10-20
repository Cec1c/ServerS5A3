package dnfbridge

import (
	"context"
	"encoding/binary"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	mem "longheng.io/server/internal/modules/dnf/repository/memory"
	"strconv"
	"testing"
)

func TestWeaponButtonColdSlot916(t *testing.T) {
	repos := mem.NewMemoryGroup()
	entries := map[string]dnfrepo.EquipmentEntry{}
	for _, slot := range []int16{12, 24} {
		old := 36 - slot
		raw := make([]byte, 119)
		binary.LittleEndian.PutUint16(raw, uint16(old))
		binary.LittleEndian.PutUint32(raw[2:], 101040493)
		binary.LittleEndian.PutUint32(raw[6:], uint32(200000000+int(slot)))
		raw[10] = byte(slot / 3)
		binary.LittleEndian.PutUint16(raw[11:], 48)
		entries[strconv.Itoa(int(slot))] = dnfrepo.EquipmentEntry{SlotIndex: slot, ItemID: 101040493, RawEntry: raw, Extra: map[string]string{"current_exe_runtime_move": "1", "current_exe_equipment_type": strconv.Itoa(int(old)), "equipped_slot": strconv.Itoa(int(old)), "quality_seed": strconv.Itoa(200000000 + int(slot))}}
	}
	if err := repos.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{CharacterID: "6", Entries: entries}); err != nil {
		t.Fatal(err)
	}
	rows := newLiveLoginReader(t, repos, nil).currentMode1EquipmentObjectRows()
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	for _, row := range rows {
		var w packetWriter
		writeCurrentMode1EquipmentCreateRow(&w, row)
		body := w.bytes()
		if body[0] != row.slot || binary.LittleEndian.Uint32(body[5:9]) != 200000000+uint32(row.slot) || body[9] != row.slot/3 {
			t.Errorf("cold slot/quality/reinforce mismatch slot=%d body=%x", row.slot, body)
		}
	}
	untouched := dnfrepo.EquipmentEntry{SlotIndex: 11, ItemID: 101040493, Extra: map[string]string{"source": "pvf_create_equipment_list"}}
	if slot, ok := currentEXEActorEquipmentSlot(untouched); !ok || slot != 12 {
		t.Fatalf("starter translation changed %d %v", slot, ok)
	}
}
