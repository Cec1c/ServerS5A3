package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The starter items used below are the real PVF `[create equipment list]`
// entries shared by the job character files, with the durability read from the
// runtime Script.pvf:
//
//	[weapon] 108000525 -> equipment/character/priest/weapon/cross/108000525.equ  [durability] 45
//	[coat]   10400     -> equipment/character/common/jacket/leather/vest_crleather.equ  [durability] 33
//	[pants]  12400     -> equipment/character/common/pants/leather/pant_crleather.equ   [durability] 27
//
// A live dnf_equipment_entries row for such a seed stores the 43-byte legacy
// summary built by buildInitialEquipmentRawEntry plus the Extra mirror written
// by equip/owner.go stackFromEntry (`raw_entry_hex` = the same 43 bytes) once
// the client has moved the item. The client renders wire +0x0A as the equipment
// reinforcement level and reads only its low five bits, so the misplaced
// durability byte used to reach the player as +13 (45), +27 (27) and +1 (33).
type currentStarterEquipmentReinforceCaseRow struct {
	name         string
	wornSlot     int16
	actorSlot    int16
	itemID       int64
	durability   uint16
	equipmentTyp string
	reportedAs   byte
}

func currentStarterReinforceCase(name string, wornSlot, actorSlot int16, itemID int64, durability uint16, equipmentTyp string, reportedAs byte) currentStarterEquipmentReinforceCaseRow {
	return currentStarterEquipmentReinforceCaseRow{
		name:         name,
		wornSlot:     wornSlot,
		actorSlot:    actorSlot,
		itemID:       itemID,
		durability:   durability,
		equipmentTyp: equipmentTyp,
		reportedAs:   reportedAs,
	}
}

var currentStarterEquipmentReinforceCases = []currentStarterEquipmentReinforceCaseRow{
	currentStarterReinforceCase("priest starter cross", 11, 12, 108000525, 45, "17", 13),
	currentStarterReinforceCase("starter pants", 15, 16, 12400, 27, "14", 27),
	currentStarterReinforceCase("starter coat", 13, 14, 10400, 33, "15", 1),
}

// currentStarterEquipmentExtra mirrors the dnf_equipment_entry_extra rows of a
// seeded starter item that the client has already moved: the PVF creation
// metadata plus the raw mirror stackFromEntry writes on every unequip.
func currentStarterEquipmentExtra(t *testing.T, row currentStarterEquipmentReinforceCaseRow, raw []byte) map[string]string {
	t.Helper()
	return map[string]string{
		"source":                      "pvf_create_equipment_list",
		"current_exe_create_value":    strconv.FormatUint(uint64(initialEquipmentCreateValue), 10),
		"durability":                  strconv.FormatUint(uint64(row.durability), 10),
		"max_durability":              strconv.FormatUint(uint64(row.durability), 10),
		"repair_gold":                 "0",
		"equipment_type":              row.equipmentTyp,
		"current_exe_equipment_type":  strconv.Itoa(int(row.actorSlot)),
		"equipped_slot":               strconv.Itoa(int(row.actorSlot)),
		"current_exe_inventory_count": "1",
		"current_exe_runtime_move":    "1",
		"raw_entry_hex":               hex.EncodeToString(raw),
	}
}

func buildCurrentStarterEquipmentEntry(t *testing.T, row currentStarterEquipmentReinforceCaseRow) dnfrepo.EquipmentEntry {
	t.Helper()
	raw := buildInitialEquipmentRawEntry(row.wornSlot, row.itemID, row.durability)
	if len(raw) != 43 {
		t.Fatalf("%s legacy summary size = %d, want 43", row.name, len(raw))
	}
	// The legacy summary itself is correct: reinforcement +9 is zero and the
	// durability word sits at +10..+12, matching MakeEquipListCodec
	// ParseDisplayFields (Reinforce raw[9], Durability raw[10..12]).
	if raw[9] != 0 {
		t.Fatalf("%s legacy summary reinforcement = %d, want 0", row.name, raw[9])
	}
	if got := binary.LittleEndian.Uint16(raw[10:12]); got != row.durability {
		t.Fatalf("%s legacy summary durability = %d, want %d", row.name, got, row.durability)
	}
	return dnfrepo.EquipmentEntry{
		SlotIndex: row.actorSlot,
		ItemID:    row.itemID,
		RawEntry:  raw,
		Extra:     currentStarterEquipmentExtra(t, row, raw),
	}
}

// TestCurrentEquipmentProjectionKeepsLegacyStarterSummaryAtZeroReinforcement is
// the regression test for "a new character's tutorial starter equipment ships
// with a very high reinforcement level".
//
// currentItemListEntryFromRaw used to zero-pad the 43-byte legacy summary into
// the 0x77 wire row, which moved every legacy field one byte forward and left
// the summary's durability low byte on wire +0x0A. Because setByte skips zero
// writes, the reinforcement byte kept that durability value and the client
// displayed it as +13 (45 & 0x1F), +27 (27 & 0x1F) and +1 (33 & 0x1F).
func TestCurrentEquipmentProjectionKeepsLegacyStarterSummaryAtZeroReinforcement(t *testing.T) {
	for _, row := range currentStarterEquipmentReinforceCases {
		row := row
		t.Run(row.name, func(t *testing.T) {
			// Pin the evidence: the low five bits of the durability byte are
			// exactly the level the player reported seeing before the fix.
			if got := byte(row.durability) & 0x1f; got != row.reportedAs {
				t.Fatalf("%s durability %d low five bits = %d, want reported +%d", row.name, row.durability, got, row.reportedAs)
			}

			entry := buildCurrentStarterEquipmentEntry(t, row)
			projected, ok := currentItemListEntryFromEquipment(entry)
			if !ok {
				t.Fatalf("%s starter row was not projected", row.name)
			}
			if got := projected.data[0x0A]; got != 0 {
				t.Fatalf("%s wire+0x0A reinforcement = %d, want 0", row.name, got)
			}
			if got := binary.LittleEndian.Uint16(projected.data[0x0B:0x0D]); got != row.durability {
				t.Fatalf("%s wire+0x0B durability = %d, want %d", row.name, got, row.durability)
			}
			if got := binary.LittleEndian.Uint16(projected.data[0x00:0x02]); got != uint16(row.actorSlot) {
				t.Fatalf("%s wire slot = %d, want actor slot %d", row.name, got, row.actorSlot)
			}
			if got := binary.LittleEndian.Uint32(projected.data[0x02:0x06]); got != uint32(row.itemID) {
				t.Fatalf("%s wire item id = %d, want %d", row.name, got, row.itemID)
			}
			if got := binary.LittleEndian.Uint32(projected.data[0x06:0x0A]); got != uint32(initialEquipmentCreateValue) {
				t.Fatalf("%s wire instance = %d, want %d", row.name, got, initialEquipmentCreateValue)
			}
			for offset := 0x0D; offset < len(projected.data); offset++ {
				if projected.data[offset] != 0 {
					t.Fatalf("%s leaked a legacy summary byte into wire+0x%02X = 0x%02X", row.name, offset, projected.data[offset])
				}
			}

			update := currentEquipmentUpdateEntryFromEquipment(entry)
			if got := update.data[0x0A]; got != 0 {
				t.Fatalf("%s equipment update reinforcement = %d, want 0", row.name, got)
			}
			if got := binary.LittleEndian.Uint16(update.data[0x0B:0x0D]); got != row.durability {
				t.Fatalf("%s equipment update durability = %d, want %d", row.name, got, row.durability)
			}
		})
	}
}

// TestCurrentEquipmentProjectionKeepsFreshStarterSeedAtZeroReinforcement covers
// the pre-move shape produced by initialEquipmentRecord, which carries no Extra
// raw mirror yet.
func TestCurrentEquipmentProjectionKeepsFreshStarterSeedAtZeroReinforcement(t *testing.T) {
	raw := buildInitialEquipmentRawEntry(11, 108000525, 45)
	entry := dnfrepo.EquipmentEntry{
		SlotIndex: 11,
		ItemID:    108000525,
		RawEntry:  raw,
		Extra: map[string]string{
			"source":                   "pvf_create_equipment_list",
			"current_exe_create_value": strconv.FormatUint(uint64(initialEquipmentCreateValue), 10),
			"durability":               "45",
			"max_durability":           "45",
			"repair_gold":              "0",
			"equipment_type":           "17",
		},
	}
	projected, ok := currentItemListEntryFromEquipment(entry)
	if !ok {
		t.Fatal("fresh starter row was not projected")
	}
	if got := projected.data[0x0A]; got != 0 {
		t.Fatalf("fresh starter wire+0x0A = %d, want 0", got)
	}
	if got := binary.LittleEndian.Uint16(projected.data[0x0B:0x0D]); got != 45 {
		t.Fatalf("fresh starter wire+0x0B durability = %d, want 45", got)
	}
	if got := binary.LittleEndian.Uint16(projected.data[0x00:0x02]); got != 12 {
		t.Fatalf("fresh starter wire slot = %d, want legacy worn slot 11 mapped to actor slot 12", got)
	}
}

// TestCurrentInventoryRowKeepsLegacyStarterSummaryAtZeroReinforcement covers the
// bag row the client receives after a starter item is unequipped, which goes
// through currentItemListEntryFromStack instead of the equipment projection.
func TestCurrentInventoryRowKeepsLegacyStarterSummaryAtZeroReinforcement(t *testing.T) {
	for _, row := range currentStarterEquipmentReinforceCases {
		row := row
		t.Run(row.name, func(t *testing.T) {
			entry := buildCurrentStarterEquipmentEntry(t, row)
			stack := dnfrepo.ItemStack{
				ItemID:   entry.ItemID,
				Count:    1,
				RawEntry: append([]byte(nil), entry.RawEntry...),
				Extra:    entry.Extra,
			}
			stack.Extra["item_kind"] = "equipment"

			projected := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, 5, stack)
			if got := projected.data[0x0A]; got != 0 {
				t.Fatalf("%s bag row wire+0x0A = %d, want 0", row.name, got)
			}
			if got := binary.LittleEndian.Uint16(projected.data[0x0B:0x0D]); got != row.durability {
				t.Fatalf("%s bag row wire+0x0B durability = %d, want %d", row.name, got, row.durability)
			}
		})
	}
}

// TestCurrentLegacyInventoryRowKeepsLegacyStarterSummaryAtZeroReinforcement
// covers the imported legacy_character_items projection, which shares the same
// raw-row base through currentItemListEntryFromRawExtra.
func TestCurrentLegacyInventoryRowKeepsLegacyStarterSummaryAtZeroReinforcement(t *testing.T) {
	for _, row := range currentStarterEquipmentReinforceCases {
		row := row
		t.Run(row.name, func(t *testing.T) {
			entry := buildCurrentStarterEquipmentEntry(t, row)
			legacy := dnfrepo.LegacyInventoryItem{
				ListType:       dnfrepo.MainInventoryListType,
				SlotIndex:      5,
				ItemTemplateID: row.itemID,
				StackCount:     1,
				InstanceValue:  1,
				Durability:     int64(row.durability),
				Extra:          entry.Extra,
			}
			projected := currentItemListEntryFromLegacyItem(legacy)
			if got := projected.data[0x0A]; got != 0 {
				t.Fatalf("%s legacy row wire+0x0A = %d, want 0", row.name, got)
			}
			if got := binary.LittleEndian.Uint16(projected.data[0x0B:0x0D]); got != row.durability {
				t.Fatalf("%s legacy row wire+0x0B durability = %d, want %d", row.name, got, row.durability)
			}
		})
	}
}

// TestCurrentItemListEntryKeepsGenuineWireRowPackedExtByte guards the exact
// length rule: a real 0x77 row is still copied byte for byte.
func TestCurrentItemListEntryKeepsGenuineWireRowPackedExtByte(t *testing.T) {
	const itemID = uint32(100390003)
	wire := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(wire[0:2], 25)
	binary.LittleEndian.PutUint32(wire[2:6], itemID)
	binary.LittleEndian.PutUint32(wire[6:10], 345678901)
	wire[0x0A] = 12
	binary.LittleEndian.PutUint16(wire[0x0B:0x0D], 45)

	entry := currentItemListEntryFromRaw(nil, map[string]string{"raw_entry_hex": hex.EncodeToString(wire)})
	if entry.data[0x0A] != 12 || binary.LittleEndian.Uint16(entry.data[0x0B:0x0D]) != 45 {
		t.Fatalf("genuine 0x77 extra row = %x", entry.data[:0x16])
	}

	equipped := dnfrepo.EquipmentEntry{
		SlotIndex: 25,
		ItemID:    int64(itemID),
		RawEntry:  append([]byte(nil), wire...),
		Extra: map[string]string{
			"current_exe_runtime_move":   "1",
			"current_exe_equipment_type": "25",
		},
	}
	projected, ok := currentItemListEntryFromEquipment(equipped)
	if !ok {
		t.Fatal("genuine wire row was not projected")
	}
	if projected.data[0x0A] != 12 || binary.LittleEndian.Uint16(projected.data[0x0B:0x0D]) != 45 {
		t.Fatalf("genuine wire equipment row = %x", projected.data[:0x16])
	}
}

// TestCurrentItemListEntryRejectsShortExtraRowAsWireBase pins the fix itself: a
// stored row shorter than the wire size is never zero-padded into the wire
// layout, while a complete 0x77 row kept in Extra is still honoured.
func TestCurrentItemListEntryRejectsShortExtraRowAsWireBase(t *testing.T) {
	legacy := buildInitialEquipmentRawEntry(11, 101010912, 45)
	if got := len(legacy); got != 43 {
		t.Fatalf("legacy summary size = %d, want 43", got)
	}
	shortExtra := map[string]string{"raw_entry_hex": hex.EncodeToString(legacy)}
	if entry := currentItemListEntryFromRaw(legacy, shortExtra); entry.data[0x0A] != 0 {
		t.Fatalf("padded short row wire+0x0A = %d, want 0", entry.data[0x0A])
	}
	if entry := currentItemListEntryFromRaw(legacy, nil); entry.data != [currentItemListEntryWireSize]byte{} {
		t.Fatalf("short row without Extra mirror produced %x", entry.data[:0x16])
	}

	wire := make([]byte, currentItemListEntryWireSize)
	wire[0x0A] = 9
	entry := currentItemListEntryFromRaw(legacy, map[string]string{"current_raw_entry_77": hex.EncodeToString(wire)})
	if entry.data[0x0A] != 9 {
		t.Fatalf("complete 0x77 Extra row wire+0x0A = %d, want 9", entry.data[0x0A])
	}
}
