package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestPeerCreatureEnchantDurableCardInActorAndInspectBodies(t *testing.T) {
	t.Setenv(peerSkillPageEnv, "off")
	ctx := context.Background()
	repos := testRepositoryGroup()
	const (
		id             = "19"
		petItemID      = uint32(400990167)
		petSerial      = uint32(37)
		petLevel       = byte(7)
		enchantCardID  = uint32(10008665) // real +40 INT creature card
		enchantUpgrade = byte(0)
	)
	worn := dnfrepo.EquipmentRecord{CharacterID: id, Entries: map[string]dnfrepo.EquipmentEntry{
		"26": {
			SlotIndex: 26,
			ItemID:    int64(petItemID),
			RawEntry:  testCurrentEquippedCreatureEnchantRaw(26, petItemID, petSerial, enchantCardID, enchantUpgrade),
			Extra: map[string]string{
				"current_exe_equipment_type": "26",
				"equipment_slot":             "26",
				"creature_serial_or_handle":  "37",
			},
		},
	}}
	if err := repos.Equipment.Save(ctx, worn); err != nil {
		t.Fatal(err)
	}
	pets := dnfrepo.PetRecord{CharacterID: id, EquippedKey: "37", Entries: map[string]dnfrepo.PetEntry{
		"37": {
			PetKey:      "37",
			CreatureKey: 37,
			ItemID:      int64(petItemID),
			Level:       int64(petLevel),
			Extra: map[string]string{
				"pet_enchant_card_item_id": "10008665",
				"enchant_upgrade_count":    "0",
			},
		},
	}}
	if err := repos.Pet.Save(ctx, pets); err != nil {
		t.Fatal(err)
	}
	s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	r := csharpLegacyUserInfoReader{ctx: ctx, characterID: id, service: s, session: &gameSession{selectedCharacterID: 19}}
	equipmentRows := r.currentMode1EquipmentObjectRows()
	rows := currentMode1EquipmentCreateRows(equipmentRows)
	if len(rows) != 1 {
		t.Fatalf("peer equipment rows=%d", len(rows))
	}
	row := rows[0]
	if row.slot != 26 || row.itemID != petItemID || row.instance != petSerial ||
		row.auxValue != enchantCardID || row.auxByte != enchantUpgrade {
		t.Fatalf("peer card/upgrade/serial/slot=%d/%d/%d/%d", row.auxValue, row.auxByte, row.instance, row.slot)
	}
	var expectedWriter packetWriter
	writeCurrentMode1EquipmentCreateRow(&expectedWriter, row)
	expectedRowWire := expectedWriter.bytes()
	if len(expectedRowWire) != currentMode1EquipmentCreateRowWireSizeFor(row) {
		t.Fatalf("peer expected row len=%d want=%d", len(expectedRowWire), currentMode1EquipmentCreateRowWireSizeFor(row))
	}
	character := dnfrepo.CharacterRecord{CharacterID: id, Level: 90, Job: "14"}
	for _, mode := range []byte{1, 3} {
		var body []byte
		var actualRows []peerCreatureRowWireForTest
		var raw8Offset int
		modeName := "mode1"
		if mode == 1 {
			body = r.buildCurrentUserInfoMode1(character, true, 19, 0)
			actualRows, raw8Offset = peerCreatureMode1CreateRowsForTest(t, body, equipmentRows)
		} else {
			modeName = "mode3"
			body = r.buildCurrentUserInfoMode3(character, true, 19, 0)
			actualRows, raw8Offset = peerCreatureMode3CreateRowsForTest(t, body, equipmentRows)
		}
		if len(actualRows) != 1 {
			t.Fatalf("%s peer equipment rows=%d want=1", modeName, len(actualRows))
		}
		actualRow := actualRows[0]
		if !bytes.Equal(actualRow.wire, expectedRowWire) {
			t.Fatalf("%s row=%x want complete row=%x", modeName, actualRow.wire, expectedRowWire)
		}
		if binary.LittleEndian.Uint32(actualRow.wire[1:5]) != petItemID ||
			binary.LittleEndian.Uint32(actualRow.wire[5:9]) != petSerial ||
			binary.LittleEndian.Uint32(actualRow.wire[16:20]) != enchantCardID ||
			actualRow.wire[20] != enchantUpgrade {
			t.Fatalf("%s card/item/serial row=%x", modeName, actualRow.wire)
		}
		peerCreatureAssertZeroNameTagRaw8ForTest(t, modeName, body, raw8Offset)
		peerCreatureAssertCreatureLevelForTest(t, modeName, body, raw8Offset, petLevel)
		peerCreatureExportProjectionForTest(t, os.Getenv("DNF90_PEER_CREATURE_EXPORT"), modeName, body, actualRow.wire)
	}
	after, _, err := repos.Equipment.Load(ctx, id)
	if err != nil || !reflect.DeepEqual(worn, after) {
		t.Fatal("read-only peer projection changed equipment", err)
	}
	afterPets, _, err := repos.Pet.Load(ctx, id)
	if err != nil || !reflect.DeepEqual(pets, afterPets) {
		t.Fatal("read-only peer projection changed pet", err)
	}
}

func TestPeerCreatureEnchantAndSameCharacterNameTag30KeepIndependentRows(t *testing.T) {
	t.Setenv(peerSkillPageEnv, "off")
	ctx := context.Background()
	repos := testRepositoryGroup()
	const (
		id             = "19"
		petItemID      = uint32(400990167)
		petSerial      = uint32(37)
		petLevel       = byte(7)
		enchantCardID  = uint32(10008665)
		enchantUpgrade = byte(0)
		nameTagItemID  = uint32(9003)
		nameTagExpire  = uint32(1700000123)
	)
	worn := dnfrepo.EquipmentRecord{CharacterID: id, Entries: map[string]dnfrepo.EquipmentEntry{
		"26": {
			SlotIndex: 26,
			ItemID:    int64(petItemID),
			RawEntry:  testCurrentEquippedCreatureEnchantRaw(26, petItemID, petSerial, enchantCardID, enchantUpgrade),
			Extra: map[string]string{
				"current_exe_equipment_type": "26",
				"equipment_slot":             "26",
				"creature_serial_or_handle":  "37",
			},
		},
		"30": {
			SlotIndex: 30,
			ItemID:    int64(nameTagItemID),
			RawEntry:  testCurrentEquippedCreatureRaw(30, nameTagItemID, 0),
			Extra: map[string]string{
				"current_exe_equipment_type": "30",
				"equipment_slot":             "30",
				"expire_time":                "1700000123",
			},
		},
	}}
	if err := repos.Equipment.Save(ctx, worn); err != nil {
		t.Fatal(err)
	}
	pets := dnfrepo.PetRecord{CharacterID: id, EquippedKey: "37", Entries: map[string]dnfrepo.PetEntry{
		"37": {
			PetKey:      "37",
			CreatureKey: 37,
			ItemID:      int64(petItemID),
			Level:       int64(petLevel),
			Extra: map[string]string{
				"pet_enchant_card_item_id": "10008665",
				"enchant_upgrade_count":    "0",
			},
		},
	}}
	if err := repos.Pet.Save(ctx, pets); err != nil {
		t.Fatal(err)
	}
	s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	r := csharpLegacyUserInfoReader{ctx: ctx, characterID: id, service: s, session: &gameSession{selectedCharacterID: 19}}
	equipmentRows := r.currentMode1EquipmentObjectRows()
	rows := currentMode1EquipmentCreateRows(equipmentRows)
	var creatureRow currentMode1EquipmentObjectRow
	for _, candidate := range rows {
		if candidate.slot == 26 {
			creatureRow = candidate
			break
		}
	}
	if creatureRow.slot != 26 {
		t.Fatalf("same-character slot26 row missing: %+v", rows)
	}
	var expectedWriter packetWriter
	writeCurrentMode1EquipmentCreateRow(&expectedWriter, creatureRow)
	expectedCreatureWire := expectedWriter.bytes()
	character := dnfrepo.CharacterRecord{CharacterID: id, Level: 90, Job: "14"}
	for _, mode := range []byte{1, 3} {
		var body []byte
		var actualRows []peerCreatureRowWireForTest
		var raw8Offset int
		modeName := "mode1"
		if mode == 1 {
			body = r.buildCurrentUserInfoMode1(character, true, 19, 0)
			actualRows, raw8Offset = peerCreatureMode1CreateRowsForTest(t, body, equipmentRows)
		} else {
			modeName = "mode3"
			body = r.buildCurrentUserInfoMode3(character, true, 19, 0)
			actualRows, raw8Offset = peerCreatureMode3CreateRowsForTest(t, body, equipmentRows)
		}
		creatureWire := peerCreatureFindCreatedRowForTest(t, actualRows, 26)
		if !bytes.Equal(creatureWire, expectedCreatureWire) {
			t.Fatalf("%s slot26 row=%x want complete row=%x", modeName, creatureWire, expectedCreatureWire)
		}
		if got := binary.LittleEndian.Uint32(creatureWire[16:20]); got != enchantCardID {
			t.Fatalf("%s slot26 retained card=%d want=%d", modeName, got, enchantCardID)
		}
		if creatureWire[20] != enchantUpgrade {
			t.Fatalf("%s slot26 retained upgrade=%d want=%d", modeName, creatureWire[20], enchantUpgrade)
		}
		peerCreatureAssertNameTagRaw8ForTest(t, modeName, body, raw8Offset, nameTagItemID, nameTagExpire)
		peerCreatureAssertCreatureLevelForTest(t, modeName, body, raw8Offset, petLevel)
	}
}

func TestPeerCreatureEnchantRecovers40INTFromMatchingPetMetadata(t *testing.T) {
	t.Setenv(peerSkillPageEnv, "off")
	ctx := context.Background()
	repos := testRepositoryGroup()
	worn := dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{
		"26": {
			SlotIndex: 26,
			ItemID:    400990167,
			RawEntry:  testCurrentEquippedCreatureEnchantRaw(26, 400990167, 37, 0, 0),
			Extra: map[string]string{
				"equipment_slot":            "26",
				"creature_serial_or_handle": "37",
			},
		},
	}}
	pets := dnfrepo.PetRecord{CharacterID: "19", EquippedKey: "37", Entries: map[string]dnfrepo.PetEntry{
		"37": {
			PetKey: "37", CreatureKey: 37, ItemID: 400990167, Level: 7,
			Extra: map[string]string{"pet_enchant_card_item_id": "10008665", "enchant_upgrade_count": "0"},
		},
	}}
	if err := repos.Equipment.Save(ctx, worn); err != nil {
		t.Fatal(err)
	}
	if err := repos.Pet.Save(ctx, pets); err != nil {
		t.Fatal(err)
	}
	s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	r := csharpLegacyUserInfoReader{ctx: ctx, characterID: "19", service: s}
	equipmentRows := r.currentMode1EquipmentObjectRows()
	character := dnfrepo.CharacterRecord{CharacterID: "19", Level: 90, Job: "14"}
	for _, mode := range []byte{1, 3} {
		var body []byte
		var rows []peerCreatureRowWireForTest
		var raw8Offset int
		modeName := "mode1"
		if mode == 1 {
			body = r.buildCurrentUserInfoMode1(character, true, 19, 0)
			rows, raw8Offset = peerCreatureMode1CreateRowsForTest(t, body, equipmentRows)
		} else {
			modeName = "mode3"
			body = r.buildCurrentUserInfoMode3(character, true, 19, 0)
			rows, raw8Offset = peerCreatureMode3CreateRowsForTest(t, body, equipmentRows)
		}
		wire := peerCreatureFindCreatedRowForTest(t, rows, 26)
		if binary.LittleEndian.Uint32(wire[16:20]) != 10008665 || wire[20] != 0 ||
			binary.LittleEndian.Uint32(wire[1:5]) != 400990167 || binary.LittleEndian.Uint32(wire[5:9]) != 37 {
			t.Fatalf("%s recovered pet card/item/serial row=%x", modeName, wire)
		}
		peerCreatureAssertZeroNameTagRaw8ForTest(t, modeName, body, raw8Offset)
		peerCreatureAssertCreatureLevelForTest(t, modeName, body, raw8Offset, 7)
	}
	afterEquipment, _, equipmentErr := repos.Equipment.Load(ctx, "19")
	afterPets, _, petErr := repos.Pet.Load(ctx, "19")
	if equipmentErr != nil || petErr != nil || !reflect.DeepEqual(worn, afterEquipment) || !reflect.DeepEqual(pets, afterPets) {
		t.Fatal("peer durable-card fallback changed persisted assets", equipmentErr, petErr)
	}
}

func TestPeerCreatureEnchantNeverBorrowsDifferentPetOrOverridesWornCard(t *testing.T) {
	for _, kind := range []string{"other_serial", "other_template", "duplicate", "other_owner", "worn_card"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			repos := testRepositoryGroup()
			entry := dnfrepo.EquipmentEntry{SlotIndex: 26, ItemID: 400990167, RawEntry: testCurrentEquippedCreatureEnchantRaw(26, 400990167, 37, 0, 0), Extra: map[string]string{"equipment_slot": "26", "creature_serial_or_handle": "37"}}
			pet := dnfrepo.PetEntry{PetKey: "37", CreatureKey: 37, ItemID: 400990167, Extra: map[string]string{"pet_enchant_card_item_id": "10008713", "enchant_upgrade_count": "2"}}
			want := uint32(0)
			owner := "19"
			switch kind {
			case "other_serial":
				pet.CreatureKey, pet.PetKey = 38, "38"
			case "other_template":
				pet.ItemID++
			case "other_owner":
				owner = "20"
			case "worn_card":
				entry.Extra["pet_enchant_card_item_id"] = "10008705"
				want = 10008705
			}
			pets := dnfrepo.PetRecord{CharacterID: owner, Entries: map[string]dnfrepo.PetEntry{"37": pet}}
			if kind == "duplicate" {
				pets.Entries["dup"] = pet
			}
			if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{"26": entry}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Pet.Save(ctx, pets); err != nil {
				t.Fatal(err)
			}
			s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			r := csharpLegacyUserInfoReader{ctx: ctx, characterID: "19", service: s}
			rows := currentMode1EquipmentCreateRows(r.currentMode1EquipmentObjectRows())
			if len(rows) != 1 || rows[0].auxValue != want {
				t.Fatalf("peer card=%+v want %d", rows, want)
			}
		})
	}
}

type peerCreatureRowWireForTest struct {
	row  currentMode1EquipmentObjectRow
	wire []byte
}

func peerCreatureMode1CreateRowsForTest(
	t *testing.T,
	body []byte,
	equipmentRows []currentMode1EquipmentObjectRow,
) ([]peerCreatureRowWireForTest, int) {
	t.Helper()
	if len(body) <= currentMode1CreateCountOffset {
		t.Fatalf("mode1 body truncated at create count: %x", body)
	}
	createRows := currentMode1EquipmentCreateRows(equipmentRows)
	updateRows := currentMode1EquipmentDeferredUpdateRows(equipmentRows)
	if got := int(body[currentMode1CreateCountOffset]); got != len(createRows) {
		t.Fatalf("mode1 create count=%d want=%d body=%x", got, len(createRows), body)
	}
	position := currentMode1CreateRowsOffset
	rows := make([]peerCreatureRowWireForTest, 0, len(createRows))
	for _, row := range createRows {
		size := currentMode1EquipmentCreateRowWireSizeFor(row)
		if size <= 0 || position+size > len(body) {
			t.Fatalf("mode1 row slot=%d size=%d truncated at %d/%d body=%x", row.slot, size, position, len(body), body)
		}
		rows = append(rows, peerCreatureRowWireForTest{
			row:  row,
			wire: append([]byte(nil), body[position:position+size]...),
		})
		position += size
	}
	if position+4 > len(body) {
		t.Fatalf("mode1 final equipment state truncated at %d/%d body=%x", position, len(body), body)
	}
	position += 4
	if position+3 > len(body) {
		t.Fatalf("mode1 update header truncated at %d/%d body=%x", position, len(body), body)
	}
	if got := int(body[position+2]); got != len(updateRows) {
		t.Fatalf("mode1 update count=%d want=%d body=%x", got, len(updateRows), body)
	}
	position += 3
	for _, row := range updateRows {
		size := 4 + len(row.update.data)
		if row.readsRawBlocks {
			size += 8
		}
		if position+size > len(body) {
			t.Fatalf("mode1 update slot=%d size=%d truncated at %d/%d body=%x", row.slot, size, position, len(body), body)
		}
		position += size
	}
	return rows, position
}

func peerCreatureMode3CreateRowsForTest(
	t *testing.T,
	body []byte,
	equipmentRows []currentMode1EquipmentObjectRow,
) ([]peerCreatureRowWireForTest, int) {
	t.Helper()
	if len(body) < 0x17 {
		t.Fatalf("mode3 body truncated before stat length: %x", body)
	}
	statLength := int(binary.LittleEndian.Uint32(body[0x13:0x17]))
	tailOffset := 0x17 + statLength
	if tailOffset+2 > len(body) {
		t.Fatalf("mode3 equipment header truncated at %d/%d body=%x", tailOffset, len(body), body)
	}
	createRows := currentMode1EquipmentCreateRows(equipmentRows)
	if got := int(body[tailOffset+1]); got != len(createRows) {
		t.Fatalf("mode3 create count=%d want=%d body=%x", got, len(createRows), body)
	}
	position := tailOffset + 2
	rows := make([]peerCreatureRowWireForTest, 0, len(createRows))
	for _, row := range createRows {
		size := currentMode1EquipmentCreateRowWireSizeFor(row)
		if size <= 0 || position+size > len(body) {
			t.Fatalf("mode3 row slot=%d size=%d truncated at %d/%d body=%x", row.slot, size, position, len(body), body)
		}
		rows = append(rows, peerCreatureRowWireForTest{
			row:  row,
			wire: append([]byte(nil), body[position:position+size]...),
		})
		position += size
	}
	if position+4 > len(body) {
		t.Fatalf("mode3 final equipment state truncated at %d/%d body=%x", position, len(body), body)
	}
	return rows, position + 4
}

func peerCreatureFindCreatedRowForTest(t *testing.T, rows []peerCreatureRowWireForTest, slot byte) []byte {
	t.Helper()
	var found []byte
	for _, row := range rows {
		if row.row.slot != slot {
			continue
		}
		if found != nil {
			t.Fatalf("duplicate slot%d create rows", slot)
		}
		found = row.wire
	}
	if found == nil {
		t.Fatalf("slot%d create row missing: %+v", slot, rows)
	}
	return found
}

func peerCreatureAssertZeroNameTagRaw8ForTest(t *testing.T, mode string, body []byte, offset int) {
	t.Helper()
	peerCreatureAssertNameTagRaw8ForTest(t, mode, body, offset, 0, 0)
}

func peerCreatureAssertNameTagRaw8ForTest(t *testing.T, mode string, body []byte, offset int, itemID, expire uint32) {
	t.Helper()
	if offset < 0 || offset+8 > len(body) {
		t.Fatalf("%s name-tag raw8 truncated at %d/%d body=%x", mode, offset, len(body), body)
	}
	if got := binary.LittleEndian.Uint32(body[offset : offset+4]); got != itemID {
		t.Fatalf("%s name-tag item=%d want=%d body=%x", mode, got, itemID, body)
	}
	if got := binary.LittleEndian.Uint32(body[offset+4 : offset+8]); got != expire {
		t.Fatalf("%s name-tag expire=%d want=%d body=%x", mode, got, expire, body)
	}
}

func peerCreatureAssertCreatureLevelForTest(t *testing.T, mode string, body []byte, raw8Offset int, level byte) {
	t.Helper()
	const emptySkillPageBlockSize = 3
	levelOffset := raw8Offset + 8 + emptySkillPageBlockSize
	if levelOffset < 0 || levelOffset >= len(body) {
		t.Fatalf("%s creature level truncated at %d/%d body=%x", mode, levelOffset, len(body), body)
	}
	if got := body[levelOffset]; got != level {
		t.Fatalf("%s creature level=%d want=%d body=%x", mode, got, level, body)
	}
}

func peerCreatureExportProjectionForTest(t *testing.T, dir, mode string, body, row []byte) {
	t.Helper()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		mode + ".bin":      body,
		mode + "_body.bin": body,
		mode + "_row.bin":  row,
	}
	if mode == "mode1" {
		// Keep the original root-level names so a row.bin captured with this
		// test can be returned alongside the complete mode1 body.
		files["body.bin"] = body
		files["row.bin"] = row
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
