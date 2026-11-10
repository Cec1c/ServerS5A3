package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// These fixtures deliberately choose rows whose current create wire size is the
// fixed 56-byte base. That lets the test decode the actual mode1/mode3 bodies
// instead of asking the row-selection code under test which rows it expected.
const peerOtherAttributesCreateRowBytes = 56

type peerOtherAttributesWireRow struct {
	slot   byte
	itemID uint32
	wire   []byte
}

func TestPeerOtherAttributesAmplificationCreateGate(t *testing.T) {
	// state112 == 0xffffffff is the native "no usable mode1 state" sentinel.
	// Unlike the ordinary zero state, it cannot establish a create row by itself;
	// the independently serialized amplification type/value must do that.
	const nativeNoState = uint32(0xffffffff)
	tests := []struct {
		name       string
		bindFlag   byte
		marker16   uint16
		state112   uint32
		wantCreate bool
		wantOld    bool
	}{
		{name: "amplification only with native sentinel", bindFlag: 3, marker16: 7, state112: nativeNoState, wantCreate: true, wantOld: false},
		{name: "marker16 only with bindFlag zero", marker16: 7, state112: nativeNoState, wantCreate: true, wantOld: false},
		{name: "bindFlag only with marker16 zero", bindFlag: 3, state112: nativeNoState, wantCreate: true, wantOld: false},
		{name: "ordinary zero state", state112: 0, wantCreate: true, wantOld: true},
		{name: "native sentinel without amplification", state112: nativeNoState, wantCreate: false, wantOld: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			record := dnfrepo.EquipmentRecord{
				CharacterID: "19",
				Entries: map[string]dnfrepo.EquipmentEntry{
					"14": {
						SlotIndex: 14,
						ItemID:    140014,
						RawEntry: peerOtherAttributesRaw(140014, peerOtherAttributesRawState{
							durability:   55,
							amplifyType:  tc.bindFlag,
							amplifyValue: tc.marker16,
							state112:     tc.state112,
						}),
						Extra: map[string]string{"current_exe_equipment_type": "14"},
					},
				},
			}

			rows := peerOtherAttributesObjectRows(t, record)
			if len(rows) != 1 {
				t.Fatalf("projected rows = %d, want 1: %+v", len(rows), rows)
			}
			if rows[0].state112 != tc.state112 || rows[0].bindFlag != tc.bindFlag || rows[0].marker16 != tc.marker16 {
				t.Fatalf("fixture row state112/bindFlag/marker16 = %08x/%d/%d, want %08x/%d/%d",
					rows[0].state112, rows[0].bindFlag, rows[0].marker16, tc.state112, tc.bindFlag, tc.marker16)
			}
			if got := currentMode1EquipmentCreateRowHasVerifiedState(rows[0]); got != tc.wantCreate {
				t.Fatalf("production create gate = %t, want %t", got, tc.wantCreate)
			}
			if got := peerOtherAttributesLegacyCreateGate(rows[0]); got != tc.wantOld {
				t.Fatalf("pre-fix gate = %t, want %t (red-proof expectation)", got, tc.wantOld)
			}

			mode1, mode3 := peerOtherAttributesBodies(t, record, nil)
			mode1Rows := peerOtherAttributesMode1CreateRows(t, mode1)
			mode3Rows := peerOtherAttributesMode3CreateRows(t, mode3)
			if !tc.wantCreate {
				if len(mode1Rows) != 0 || len(mode3Rows) != 0 {
					t.Fatalf("non-create state mode1=%+v mode3=%+v, want no create rows", mode1Rows, mode3Rows)
				}
				if got := peerOtherAttributesMode1UpdateCount(t, mode1); got != 1 {
					t.Fatalf("mode1 deferred update count = %d, want exactly the sentinel control", got)
				}
				return
			}
			if len(mode1Rows) != 1 || len(mode3Rows) != 1 {
				t.Fatalf("create rows mode1=%+v mode3=%+v, want one in each body", mode1Rows, mode3Rows)
			}
			if mode1Rows[0].slot != 14 || mode1Rows[0].itemID != 140014 ||
				mode3Rows[0].slot != mode1Rows[0].slot || mode3Rows[0].itemID != mode1Rows[0].itemID {
				t.Fatalf("create identity mode1=%+v mode3=%+v", mode1Rows[0], mode3Rows[0])
			}
			if !bytes.Equal(mode1Rows[0].wire, mode3Rows[0].wire) {
				t.Fatalf("mode1/mode3 create rows differ:\n mode1=%x\n mode3=%x", mode1Rows[0].wire, mode3Rows[0].wire)
			}
			row := mode1Rows[0].wire
			if row[21] != tc.bindFlag || binary.LittleEndian.Uint16(row[22:24]) != tc.marker16 {
				t.Fatalf("amplification wire = %x, want type=%d value=%d", row[21:24], tc.bindFlag, tc.marker16)
			}
			if got := peerOtherAttributesMode1UpdateCount(t, mode1); got != 0 {
				t.Fatalf("mode1 unexpected deferred update count = %d", got)
			}
		})
	}
}

func TestPeerOtherAttributesLegacyArtifactsResolveFromRepository(t *testing.T) {
	t.Run("markerless with normal slot", func(t *testing.T) {
		record := dnfrepo.EquipmentRecord{
			CharacterID: "19",
			Entries: map[string]dnfrepo.EquipmentEntry{
				"14": peerOtherAttributesNormalEntry(14, 140014),
				"27": peerOtherAttributesArtifactEntry(27, 270027, nil),
				"28": peerOtherAttributesArtifactEntry(28, 280028, nil),
				"29": peerOtherAttributesArtifactEntry(29, 290029, nil),
			},
		}
		wrongIdentity := dnfrepo.EquipmentRecord{
			CharacterID: "20",
			Entries: map[string]dnfrepo.EquipmentEntry{
				"29": peerOtherAttributesArtifactEntry(29, 990029, nil),
			},
		}
		mode1, mode3 := peerOtherAttributesBodies(t, record, &wrongIdentity)
		peerOtherAttributesAssertRows(t, mode1, mode3, []peerOtherAttributesWireRow{
			{slot: 15, itemID: 140014},
			{slot: 27, itemID: 270027},
			{slot: 28, itemID: 280028},
			{slot: 29, itemID: 290029},
		})
		for _, rows := range [][]peerOtherAttributesWireRow{
			peerOtherAttributesMode1CreateRows(t, mode1),
			peerOtherAttributesMode3CreateRows(t, mode3),
		} {
			for _, row := range rows {
				if row.itemID == 990029 {
					t.Fatalf("equipment for character 20 leaked into character 19 body: %+v", row)
				}
			}
		}
	})

	t.Run("explicit markers", func(t *testing.T) {
		record := dnfrepo.EquipmentRecord{
			CharacterID: "19",
			Entries: map[string]dnfrepo.EquipmentEntry{
				"27": peerOtherAttributesArtifactEntry(27, 270027, map[string]string{"current_exe_equipment_type": "27"}),
				"28": peerOtherAttributesArtifactEntry(28, 280028, map[string]string{"current_exe_equipment_type": "28"}),
				"29": peerOtherAttributesArtifactEntry(29, 290029, map[string]string{"current_exe_equipment_type": "29"}),
			},
		}
		mode1, mode3 := peerOtherAttributesBodies(t, record, nil)
		peerOtherAttributesAssertRows(t, mode1, mode3, []peerOtherAttributesWireRow{
			{slot: 27, itemID: 270027},
			{slot: 28, itemID: 280028},
			{slot: 29, itemID: 290029},
		})
	})

	t.Run("explicit wrong identity and normal slot coexist", func(t *testing.T) {
		record := dnfrepo.EquipmentRecord{
			CharacterID: "19",
			Entries: map[string]dnfrepo.EquipmentEntry{
				"14": peerOtherAttributesNormalEntry(14, 140014),
				"27": peerOtherAttributesArtifactEntry(27, 270027, map[string]string{
					"current_exe_equipment_type": "14",
					"equipment_slot":             "26",
					"equipped_slot":              "26",
				}),
			},
		}
		mode1, mode3 := peerOtherAttributesBodies(t, record, nil)
		// The explicit marker keeps its established precedence. The normal worn
		// slot still uses its legacy 14 -> actor 15 mapping in the same body.
		peerOtherAttributesAssertRows(t, mode1, mode3, []peerOtherAttributesWireRow{
			{slot: 15, itemID: 140014},
			{slot: 14, itemID: 270027},
		})
	})
}

func peerOtherAttributesObjectRows(t *testing.T, record dnfrepo.EquipmentRecord) []currentMode1EquipmentObjectRow {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Equipment.Save(context.Background(), record); err != nil {
		t.Fatalf("save equipment for %s: %v", record.CharacterID, err)
	}
	reader := csharpLegacyUserInfoReader{
		ctx:         context.Background(),
		characterID: record.CharacterID,
		service: &Service{
			repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		},
	}
	return reader.currentMode1EquipmentObjectRows()
}

// peerOtherAttributesLegacyCreateGate is a byte-for-byte behavioral copy of the
// pre-fix predicate. It makes the red proof independent of the production gate
// under test without restoring or modifying another source file.
func peerOtherAttributesLegacyCreateGate(row currentMode1EquipmentObjectRow) bool {
	if _, ok := currentMode1EquipmentActorSlot(row); !ok || !row.durabilityKnown || row.itemID == 0 {
		return false
	}
	if _, ok := currentMode1EquipmentRuntimeType(row); !ok {
		return false
	}
	if row.indexedState1C61F40.currentItemMismatch {
		return false
	}
	if row.currentItemStateDerived && !row.readsRawBlocks && !row.readsDurabilityOverrideU32 &&
		row.linkedItemID == 0 && row.auxValue == 0 && row.auxByte == 0 && row.bindFlag == 0 && row.marker16 == 0 &&
		len(row.vector1D6E020) == 0 && row.state112 == 0 && len(row.vector225CCA0) == 0 &&
		row.state120 == 0 && row.state160 == 0 && row.state128 == 0 && row.state140 == 0 &&
		row.state144 == 0 && row.resourceByte == 0 && row.state168 == 0 && row.state169 == 0 &&
		row.state170 == 0 && len(row.lateRawC) == 0 && len(row.lateRawD) == 0 &&
		row.state183 == 0 && len(row.lateRawE) == 0 {
		return true
	}
	if (row.readsRawBlocks && (len(row.rawA) > 0 || len(row.rawB) > 0)) ||
		(row.linkedItemID != 0 && row.slot == 9 && len(row.linkedRaw) > 0) ||
		len(row.vector1D6E020) > 0 || len(row.vector225CCA0) > 0 ||
		len(row.lateRawC) > 0 || len(row.lateRawD) > 0 || len(row.lateRawE) > 0 {
		return true
	}
	return row.linkedItemID != 0 || row.auxValue != 0 || row.auxByte != 0 ||
		row.state112 != 0xffffffff ||
		row.state120 != 0 || row.state160 != 0 || row.state128 != 0 ||
		row.state140 != 0 || row.state144 != 0 || row.resourceByte != 0 ||
		row.state168 != 0 || row.state169 != 0 || row.state170 != 0 || row.state183 != 0
}

type peerOtherAttributesRawState struct {
	ext          byte
	durability   uint16
	auxValue     uint32
	auxByte      byte
	amplifyType  byte
	amplifyValue uint16
	state112     uint32
	state120     byte
}

func peerOtherAttributesRaw(itemID uint32, state peerOtherAttributesRawState) []byte {
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint32(raw[0x02:0x06], itemID)
	raw[0x0a] = state.ext
	binary.LittleEndian.PutUint16(raw[0x0b:0x0d], state.durability)
	binary.LittleEndian.PutUint32(raw[0x0e:0x12], state.auxValue)
	raw[0x12] = state.auxByte
	raw[0x13] = state.amplifyType
	binary.LittleEndian.PutUint16(raw[0x14:0x16], state.amplifyValue)
	binary.LittleEndian.PutUint32(raw[0x38:0x3c], state.state112)
	raw[0x57] = state.state120
	return raw
}

func peerOtherAttributesNormalEntry(slot int16, itemID int64) dnfrepo.EquipmentEntry {
	return dnfrepo.EquipmentEntry{
		SlotIndex: slot,
		ItemID:    itemID,
		RawEntry:  peerOtherAttributesRaw(uint32(itemID), peerOtherAttributesRawState{durability: 44}),
	}
}

func peerOtherAttributesArtifactEntry(slot int16, itemID int64, extra map[string]string) dnfrepo.EquipmentEntry {
	return dnfrepo.EquipmentEntry{
		SlotIndex: slot,
		ItemID:    itemID,
		RawEntry: peerOtherAttributesRaw(uint32(itemID), peerOtherAttributesRawState{
			ext:        2,
			durability: uint16(50 + slot),
			auxValue:   uint32(700 + slot),
			auxByte:    1,
			state120:   4,
		}),
		Extra: extra,
	}
}

func peerOtherAttributesBodies(t *testing.T, record dnfrepo.EquipmentRecord, wrongIdentity *dnfrepo.EquipmentRecord) ([]byte, []byte) {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Equipment.Save(context.Background(), record); err != nil {
		t.Fatalf("save equipment for %s: %v", record.CharacterID, err)
	}
	if wrongIdentity != nil {
		if err := repositories.Equipment.Save(context.Background(), *wrongIdentity); err != nil {
			t.Fatalf("save equipment for %s: %v", wrongIdentity.CharacterID, err)
		}
	}
	service := &Service{
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		characterStats:     testCharacterStatTable(t),
	}
	character := dnfrepo.CharacterRecord{
		CharacterID: record.CharacterID,
		AccountID:   "dnf:1",
		Name:        "hero",
		Job:         "11",
		Level:       90,
		Stats:       defaultCreatedCharacterStats(0),
	}
	mode1 := service.buildCurrentSelectedUserInfoMode1Body(context.Background(), nil, nil, character, true, 19)
	mode3 := service.buildCurrentSelectedUserInfoMode3Body(context.Background(), nil, nil, character, true, 19)
	return mode1, mode3
}

func peerOtherAttributesMode1CreateRows(t *testing.T, body []byte) []peerOtherAttributesWireRow {
	t.Helper()
	if len(body) <= currentMode1CreateCountOffset {
		t.Fatalf("mode1 body is truncated before create count: %x", body)
	}
	count := int(body[currentMode1CreateCountOffset])
	position := currentMode1CreateRowsOffset
	rows := make([]peerOtherAttributesWireRow, 0, count)
	for index := 0; index < count; index++ {
		end := position + peerOtherAttributesCreateRowBytes
		if end > len(body) {
			t.Fatalf("mode1 create row %d is truncated at %d/%d: %x", index, position, len(body), body)
		}
		wire := append([]byte(nil), body[position:end]...)
		rows = append(rows, peerOtherAttributesWireRow{
			slot:   wire[0],
			itemID: binary.LittleEndian.Uint32(wire[1:5]),
			wire:   wire,
		})
		position = end
	}
	return rows
}

func peerOtherAttributesMode1UpdateCount(t *testing.T, body []byte) int {
	t.Helper()
	if len(body) <= currentMode1CreateCountOffset {
		t.Fatalf("mode1 body is truncated before create count: %x", body)
	}
	createCount := int(body[currentMode1CreateCountOffset])
	updateCountOffset := currentMode1CreateRowsOffset + createCount*peerOtherAttributesCreateRowBytes + 4 + 2
	if updateCountOffset >= len(body) {
		t.Fatalf("mode1 update count is outside %d-byte body", len(body))
	}
	return int(body[updateCountOffset])
}

func peerOtherAttributesMode3CreateRows(t *testing.T, body []byte) []peerOtherAttributesWireRow {
	t.Helper()
	if len(body) < 0x17 {
		t.Fatalf("mode3 body is truncated before stat length: %x", body)
	}
	statLength := int(binary.LittleEndian.Uint32(body[0x13:0x17]))
	countOffset := 0x17 + statLength + 1
	if countOffset >= len(body) {
		t.Fatalf("mode3 create count is outside %d-byte body", len(body))
	}
	count := int(body[countOffset])
	position := countOffset + 1
	rows := make([]peerOtherAttributesWireRow, 0, count)
	for index := 0; index < count; index++ {
		end := position + peerOtherAttributesCreateRowBytes
		if end > len(body) {
			t.Fatalf("mode3 create row %d is truncated at %d/%d: %x", index, position, len(body), body)
		}
		wire := append([]byte(nil), body[position:end]...)
		rows = append(rows, peerOtherAttributesWireRow{
			slot:   wire[0],
			itemID: binary.LittleEndian.Uint32(wire[1:5]),
			wire:   wire,
		})
		position = end
	}
	return rows
}

func peerOtherAttributesAssertRows(t *testing.T, mode1, mode3 []byte, want []peerOtherAttributesWireRow) {
	t.Helper()
	mode1Rows := peerOtherAttributesMode1CreateRows(t, mode1)
	mode3Rows := peerOtherAttributesMode3CreateRows(t, mode3)
	if len(mode1Rows) != len(want) || len(mode3Rows) != len(want) {
		t.Fatalf("create row counts mode1=%d mode3=%d want=%d\n mode1=%+v\n mode3=%+v",
			len(mode1Rows), len(mode3Rows), len(want), mode1Rows, mode3Rows)
	}
	for index := range want {
		if mode1Rows[index].slot != want[index].slot || mode1Rows[index].itemID != want[index].itemID {
			t.Fatalf("mode1 row %d = %+v, want slot=%d item=%d", index, mode1Rows[index], want[index].slot, want[index].itemID)
		}
		if mode3Rows[index].slot != want[index].slot || mode3Rows[index].itemID != want[index].itemID {
			t.Fatalf("mode3 row %d = %+v, want slot=%d item=%d", index, mode3Rows[index], want[index].slot, want[index].itemID)
		}
		if !bytes.Equal(mode1Rows[index].wire, mode3Rows[index].wire) {
			t.Fatalf("row %d differs between mode1/mode3:\n mode1=%x\n mode3=%x", index, mode1Rows[index].wire, mode3Rows[index].wire)
		}
	}
}
