package equip

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	mem "longheng.io/server/internal/modules/dnf/repository/memory"
	"reflect"
	"strconv"
	"testing"
)

func TestWeaponButtonSlotIdentity916(t *testing.T) {
	for _, slots := range [][]int16{{12, 24}, {12}, {24}} {
		t.Run(strconv.Itoa(int(slots[0]))+"-"+strconv.Itoa(len(slots)), func(t *testing.T) {
			ctx := context.Background()
			repos := mem.NewMemoryGroup()
			saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{}})
			entries := map[string]dnfrepo.EquipmentEntry{}
			for _, slot := range slots {
				raw := make([]byte, 119)
				binary.LittleEndian.PutUint16(raw, uint16(slot))
				binary.LittleEndian.PutUint32(raw[2:], 101040493)
				binary.LittleEndian.PutUint32(raw[6:], uint32(200000000+int(slot)))
				raw[10] = byte(slot / 3)
				raw[0x57] = byte(slot / 6)
				key := strconv.Itoa(int(slot))
				entries[key] = dnfrepo.EquipmentEntry{SlotIndex: slot, ItemID: 101040493, RawEntry: raw, Extra: map[string]string{"current_exe_runtime_move": "1", "current_exe_equipment_type": key, "equipment_slot": key, "equipped_slot": key, "raw_entry_hex": hex.EncodeToString(raw), "quality_seed": strconv.Itoa(200000000 + int(slot)), "byte_57": strconv.Itoa(int(slot / 6)), "enchant_card_id": "10007563", "equipment_emblem_data": "02ffffffffffffffff"}}
			}
			saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{CharacterID: "77", Entries: entries})
			before := loadTestEquipment(t, ctx, repos, "77")
			inv := loadTestInventory(t, ctx, repos, "77")
			owner, err := NewOwnerWithPlacementValidator(repos, &recordingPlacementValidator{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.Move(ctx, MoveCommand{SelectedCharacterID: 77, SourceListType: 3, SourceSlotIndex: 12, DestinationListType: 3, DestinationSlotIndex: 24, MoveCount: 0})
			if err != nil {
				t.Fatal(err)
			}
			if result.SourceSlotIndex != 12 || result.DestinationSlotIndex != 24 {
				t.Fatalf("ACK endpoints changed: %+v", result)
			}
			after := loadTestEquipment(t, ctx, repos, "77")
			if len(after.Entries) != len(before.Entries) {
				t.Fatal("equipment count changed")
			}
			for _, old := range before.Entries {
				slot := int16(36) - old.SlotIndex
				key := strconv.Itoa(int(slot))
				got := after.Entries[key]
				if got.SlotIndex != slot || got.ItemID != old.ItemID || !bytes.Equal(got.RawEntry[2:], old.RawEntry[2:]) {
					t.Fatalf("instance changed old=%+v got=%+v", old, got)
				}
				if binary.LittleEndian.Uint16(got.RawEntry) != uint16(slot) {
					t.Errorf("persisted raw points at old slot: %x", got.RawEntry[:2])
				}
				for _, field := range []string{"current_exe_equipment_type", "equipped_slot", "equipment_slot"} {
					if got.Extra[field] != key {
						t.Errorf("slot %s %s=%q", key, field, got.Extra[field])
					}
				}
				if got.Extra["raw_entry_hex"] != hex.EncodeToString(got.RawEntry) {
					t.Error("raw mirror differs")
				}
				for field, value := range old.Extra {
					if field == "current_exe_equipment_type" || field == "equipped_slot" || field == "equipment_slot" || field == "raw_entry_hex" {
						continue
					}
					if got.Extra[field] != value {
						t.Errorf("instance extra changed %s", field)
					}
				}
			}
			if !reflect.DeepEqual(inv, loadTestInventory(t, ctx, repos, "77")) {
				t.Fatal("swap changed inventory")
			}
		})
	}
}
