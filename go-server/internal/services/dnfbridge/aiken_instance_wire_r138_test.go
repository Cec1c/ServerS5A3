package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Synthetic instance bytes exercise projection only; this is not a legal upgrade fixture.
// The template ID/path/type are from the frozen PVF extraction, not invented resource rules.
func TestAikenInstanceWireR138ProjectionRoundTrip(t *testing.T) {
	for _, tc := range []struct{ name, tailExtra, modeExtra string }{
		{"raw_only", "", ""}, {"tail_alias", "01027ffd03", ""},
		{"explicit_mode1", "01027ffd03", "01027ffd03"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := testRepositoryGroup()
			raw := make([]byte, 119)
			copy(raw[0x72:], []byte{1, 2, 127, 253, 3})
			// Non-custom opaque state must survive persistence and projection.
			copy(raw[0x1a:0x20], []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66})
			extra := map[string]string{"item_kind": "equipment", "equipment_type": "[coat]", "current_exe_equipment_type": "14", "mode1_create_enabled": "1", "pvf_path": "equipment/character/common/jacket/cloth/100050746.equ"}
			if tc.tailExtra != "" {
				extra["tail_data_72"] = tc.tailExtra
			}
			if tc.modeExtra != "" {
				extra["mode1_raw_e_hex"] = tc.modeExtra
			}
			equip := dnfrepo.EquipmentEntry{SlotIndex: 13, ItemID: 100050746, RawEntry: raw, Extra: extra}
			inv := dnfrepo.InventoryRecord{CharacterID: "wire-only", Slots: map[string]dnfrepo.ItemStack{"0:9": {ItemID: equip.ItemID, Count: 1, RawEntry: raw, Extra: extra}}}
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "wire-only", Entries: map[string]dnfrepo.EquipmentEntry{"13": equip}}); err != nil {
				t.Fatal(err)
			}
			// Reload through repository API; no production database or player is touched.
			loaded, found, err := repos.Inventory.Load(ctx, "wire-only")
			if err != nil || !found {
				t.Fatal(found, err)
			}
			eq, found, err := repos.Equipment.Load(ctx, "wire-only")
			if err != nil || !found {
				t.Fatal(found, err)
			}
			if !reflect.DeepEqual(loaded.Slots, inv.Slots) || !reflect.DeepEqual(eq.Entries["13"], equip) {
				t.Fatal("persistence changed instance")
			}
			bag := currentItemListEntryFromStack(0, 9, loaded.Slots["0:9"])
			worn, ok := currentItemListEntryFromEquipment(eq.Entries["13"])
			if !ok {
				t.Fatal("worn row rejected")
			}
			for _, entry := range []currentItemListEntry{bag, worn} {
				if !bytes.Equal(entry.data[0x72:], raw[0x72:]) {
					t.Fatalf("tail lost: %x", entry.data[0x72:])
				}
				if !bytes.Equal(entry.data[0x1a:0x20], raw[0x1a:0x20]) {
					t.Fatal("opaque non-custom bytes changed")
				}
			}
			noti13 := buildCurrentItemListBody(0, []currentItemListEntry{bag}, dnfrepo.CharacterContainerState{MainSlotCount: 56})
			noti14 := buildCurrentItemUpdateBody(0, []currentItemListEntry{bag})
			if len(noti13) != 124 || len(noti14) != 122 || !bytes.Equal(noti13[5:], bag.data[:]) || !bytes.Equal(noti14[3:], bag.data[:]) {
				t.Fatal("119B frame mismatch")
			}
			service := Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			reader := csharpLegacyUserInfoReader{ctx: ctx, service: &service, characterID: "wire-only"}
			rows := reader.currentMode1EquipmentObjectRows()
			if len(rows) != 1 {
				t.Fatalf("rows %d", len(rows))
			}
			var w packetWriter
			writeCurrentMode1EquipmentCreateRow(&w, rows[0])
			wire := w.bytes()
			if dir := os.Getenv("AIKEN_WIRE_OUTPUT"); dir != "" {
				evidence := map[string]string{"noti13": hex.EncodeToString(noti13), "noti14": hex.EncodeToString(noti14), "mode1_create_row": hex.EncodeToString(wire), "mode1_tail": hex.EncodeToString(rows[0].lateRawE), "raw_tail": hex.EncodeToString(raw[0x72:])}
				data, _ := json.MarshalIndent(evidence, "", "  ")
				if err := os.WriteFile(filepath.Join(dir, tc.name+".json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(rows[0].lateRawE, raw[0x72:]) {
				t.Fatalf("cold-login custom block=%x, durable 119B tail=%x; bag NOTI13/14 preserve it", rows[0].lateRawE, raw[0x72:])
			}
			// The final raw block is length-prefixed (u32=5), then the exact five custom bytes.
			if !bytes.Equal(wire[len(wire)-9:], append([]byte{5, 0, 0, 0}, raw[0x72:]...)) {
				t.Fatal("mode1 final block mismatch")
			}
		})
	}
}
