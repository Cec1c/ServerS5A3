package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Exercise the selected-character mode1 boundary, including repository loading,
// current-item normalization, create-row selection and the production serializer.
func TestBuildCurrentSelectedUserInfoMode1ForgeState120P38Regression917(t *testing.T) {
	t.Setenv("DNFBRIDGE_MODE1_EQUIPMENT_RAW_BLOCKS", "evidence")
	// P38 character119 rows from p38-cold915.json. Only slot bytes and the two
	// slot aliases are normalized to the persisted 12/24 to isolate forge.
	fixtures := []struct {
		slot      int16
		quality   uint32
		reinforce byte
		ampType   byte
		ampValue  uint16
		rawHex    string
		extra     map[string]string
	}{
		{
			slot: 12, quality: 346360515, reinforce: 9, ampType: 3, ampValue: 7,
			rawHex: "18006DC10506C30AA514003000006DC105060000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
			extra: map[string]string{
				"amplification_type":          "3",
				"amplification_value":         "7",
				"amplify_type":                "3",
				"amplify_value":               "7",
				"byte_13":                     "3",
				"byte_57":                     "4",
				"current_exe_equipment_type":  "12",
				"current_exe_inventory_count": "1",
				"current_exe_runtime_move":    "1",
				"durability":                  "48",
				"equipment_type":              "[weapon]",
				"equipped_slot":               "12",
				"ext_data0":                   "9",
				"item_kind":                   "equipment",
				"marker16":                    "7",
				"marker_16":                   "7",
				"max_durability":              "48",
				"pvf_path":                    "equipment/character/swordman/weapon/beamsword/101040493.equ",
				"quality_seed":                "346360515",
				"reinforce":                   "9",
				"value_13":                    "3",
				"value_57":                    "4",
				"value_c":                     "3",
				"value_d":                     "7",
			},
		},
		{
			slot: 24, quality: 999999998, reinforce: 7, ampType: 0, ampValue: 0,
			rawHex: "0C006DC10506FEC99A3B003000006DC105060000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
			extra: map[string]string{
				"byte_57":                     "2",
				"current_exe_equipment_type":  "24",
				"current_exe_inventory_count": "1",
				"current_exe_runtime_move":    "1",
				"durability":                  "48",
				"equipment_type":              "[weapon]",
				"equipped_slot":               "24",
				"ext_data0":                   "7",
				"item_kind":                   "equipment",
				"max_durability":              "48",
				"pvf_path":                    "equipment/character/swordman/weapon/beamsword/101040493.equ",
				"quality_seed":                "999999998",
				"reinforce":                   "7",
				"value_57":                    "2",
			},
		},
	}
	cases := []struct {
		name           string
		dropForgeKey   string
		mode1Override  [2]string
		legacyOverride [2]string
		want           [2]byte
	}{
		{name: "durable_both_aliases", want: [2]byte{4, 2}},
		{name: "durable_byte_57_only", dropForgeKey: "value_57", want: [2]byte{4, 2}},
		{name: "durable_value_57_only", dropForgeKey: "byte_57", want: [2]byte{4, 2}},
		{name: "mode1_explicit_zero", mode1Override: [2]string{"0", "0"}, want: [2]byte{0, 0}},
		{name: "mode1_explicit_nonzero", mode1Override: [2]string{"6", "5"}, want: [2]byte{6, 5}},
		{name: "state_alias_explicit_zero", legacyOverride: [2]string{"0", "0"}, want: [2]byte{0, 0}},
		{name: "state_alias_explicit_nonzero", legacyOverride: [2]string{"6", "5"}, want: [2]byte{6, 5}},
		{name: "primary_zero_beats_nonzero_alias", mode1Override: [2]string{"0", "0"}, legacyOverride: [2]string{"6", "5"}, want: [2]byte{0, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repos := testRepositoryGroup()
			record := dnfrepo.EquipmentRecord{
				CharacterID: "119",
				Entries:     make(map[string]dnfrepo.EquipmentEntry),
				UpdatedAt:   time.Date(2026, 10, 14, 0, 57, 29, 0, time.UTC),
			}
			for i, fixture := range fixtures {
				raw, err := hex.DecodeString(fixture.rawHex)
				if err != nil {
					t.Fatalf("decode P38 slot %d: %v", fixture.slot, err)
				}
				if len(raw) != 0x77 || raw[0x57] != 0 {
					t.Fatalf("slot %d fixture must have a 0x77 raw row with raw57=0", fixture.slot)
				}
				binary.LittleEndian.PutUint16(raw[:2], uint16(fixture.slot))
				extra := make(map[string]string, len(fixture.extra)+2)
				for key, value := range fixture.extra {
					extra[key] = value
				}
				delete(extra, tc.dropForgeKey)
				if value := tc.mode1Override[i]; value != "" {
					extra["mode1_state_120"] = value
				}
				if value := tc.legacyOverride[i]; value != "" {
					extra["state_120"] = value
				}
				record.Entries[strconv.Itoa(int(fixture.slot))] = dnfrepo.EquipmentEntry{
					SlotIndex: fixture.slot,
					ItemID:    101040493,
					RawEntry:  raw,
					Extra:     extra,
				}
			}
			if err := repos.Equipment.Save(context.Background(), record); err != nil {
				t.Fatalf("save P38 equipment: %v", err)
			}
			service := Service{
				repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
				characterStats:     testCharacterStatTable(t),
			}
			character := dnfrepo.CharacterRecord{
				CharacterID: "119",
				AccountID:   "dnf:10070",
				Name:        "v9",
				Job:         "11",
				Level:       90,
				Stats:       defaultCreatedCharacterStats(0),
			}
			character.Stats[dnfrepo.CharacterGrowTypeStatKey] = 4
			body := service.buildCurrentSelectedUserInfoMode1Body(context.Background(), nil, nil, character, true, 119)

			// Fixed P38 wire contract: two ordinary weapon create rows of 56 bytes.
			// Do not derive the expected size or state position from production rows.
			const rowSize = 56
			const forgeOffset = 33
			if len(body) != currentMode1BaseWireSize+2*rowSize {
				t.Fatalf("mode1 length = %d, want %d", len(body), currentMode1BaseWireSize+2*rowSize)
			}
			if got := body[currentMode1CreateCountOffset]; got != 2 {
				t.Fatalf("mode1 create count = %d, want both P38 instances", got)
			}
			for i, fixture := range fixtures {
				start := currentMode1CreateRowsOffset + i*rowSize
				row := body[start : start+rowSize]
				if got := row[0]; got != byte(fixture.slot) {
					t.Errorf("row %d slot = %d, want %d", i, got, fixture.slot)
				}
				if got := binary.LittleEndian.Uint32(row[1:5]); got != 101040493 {
					t.Errorf("slot %d item = %d, want 101040493", fixture.slot, got)
				}
				if got := binary.LittleEndian.Uint32(row[5:9]); got != fixture.quality {
					t.Errorf("slot %d quality instance = %d, want %d", fixture.slot, got, fixture.quality)
				}
				if got := row[9]; got != fixture.reinforce {
					t.Errorf("slot %d reinforce = %d, want %d", fixture.slot, got, fixture.reinforce)
				}
				if got := binary.LittleEndian.Uint16(row[10:12]); got != 48 {
					t.Errorf("slot %d durability = %d, want 48", fixture.slot, got)
				}
				if row[21] != fixture.ampType || binary.LittleEndian.Uint16(row[22:24]) != fixture.ampValue {
					t.Errorf("slot %d amplification bytes = %x, want type %d/value %d", fixture.slot, row[21:24], fixture.ampType, fixture.ampValue)
				}
				if row[24] != 0 || row[29] != 0 || row[32] != 0 {
					t.Fatalf("slot %d unexpected variable containers: %x", fixture.slot, row[24:33])
				}
				if got := row[forgeOffset]; got != tc.want[i] {
					t.Errorf("slot %d quality %d serialized state120 = %d, want %d (durable forge %s)", fixture.slot, fixture.quality, got, tc.want[i], fixture.extra["byte_57"])
				}
				if !bytes.Equal(row[34:43], make([]byte, 9)) {
					t.Errorf("slot %d sibling states160..170 changed: %x", fixture.slot, row[34:43])
				}
				// Reuse the existing mode1 reader, after checking lengths so a bad
				// serializer reports a failure instead of panicking in that helper.
				pos := 43
				for _, label := range []string{"C", "D"} {
					if got := binary.LittleEndian.Uint32(row[pos : pos+4]); got != 0 {
						t.Fatalf("slot %d raw %s length = %d, want 0", fixture.slot, label, got)
					}
					_, pos = readMode1RawBlockForTest(row, pos)
				}
				if row[pos] != 0 {
					t.Errorf("slot %d state183 = %d, want 0", fixture.slot, row[pos])
				}
				pos++
				if got := binary.LittleEndian.Uint32(row[pos : pos+4]); got != 0 {
					t.Fatalf("slot %d raw E length = %d, want 0", fixture.slot, got)
				}
				_, pos = readMode1RawBlockForTest(row, pos)
				if pos != rowSize {
					t.Fatalf("slot %d row ends at %d, want %d", fixture.slot, pos, rowSize)
				}
			}
			tail := currentMode1CreateRowsOffset + 2*rowSize
			if binary.LittleEndian.Uint32(body[tail:tail+4]) != 0 || binary.LittleEndian.Uint16(body[tail+4:tail+6]) != 0 || body[tail+6] != 0 {
				t.Errorf("create-only final state/key/update count changed: %x", body[tail:tail+7])
			}
			if got := body[tail+15]; got != 0xff {
				t.Errorf("resource group = %#x, want 0xff", got)
			}

			// Compare the complete production message with a second serialization
			// using explicit zero overrides; only the two state120 bytes may differ.
			for _, entry := range record.Entries {
				entry.Extra["mode1_state_120"] = "0"
			}
			if err := repos.Equipment.Save(context.Background(), record); err != nil {
				t.Fatalf("save explicit-zero control: %v", err)
			}
			control := service.buildCurrentSelectedUserInfoMode1Body(context.Background(), nil, nil, character, true, 119)
			masked := append([]byte(nil), body...)
			for i := range fixtures {
				masked[currentMode1CreateRowsOffset+i*rowSize+forgeOffset] = 0
			}
			if !bytes.Equal(masked, control) {
				t.Errorf("mode1 bytes outside state120 differ from explicit-zero control\nmasked: %x\ncontrol: %x", masked, control)
			}
		})
	}
}
