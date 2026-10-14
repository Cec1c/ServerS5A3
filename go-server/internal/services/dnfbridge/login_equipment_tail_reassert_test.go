package dnfbridge

// Regression fixture from own role119 cold-login SQL, 2026-10-14.

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"testing"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	loginTailReassertP38MainRawHex = "18006DC10506C30AA514003000006DC105060000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
	loginTailReassertP38SubRawHex  = "0C006DC10506FEC99A3B003000006DC105060000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

	loginTailReassertP38ItemID        = 101040493
	loginTailReassertP38MainInstance  = 346360515
	loginTailReassertP38SubInstance   = 999999998
	loginTailReassertP38MainForge     = 4
	loginTailReassertP38SubForge      = 2
	loginTailReassertP38MainReinforce = 9
	loginTailReassertP38SubReinforce  = 7
)

func loginTailReassertP38RawRow(t *testing.T, rawHex string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(rawHex)
	if err != nil {
		t.Fatalf("decode p38 raw row: %v", err)
	}
	if len(raw) != currentItemListEntryWireSize {
		t.Fatalf("p38 raw row = %d bytes, want %d", len(raw), currentItemListEntryWireSize)
	}
	if raw[loginEquipmentTailBlockFirstByte] != 0 {
		t.Fatalf("p38 raw row already carries 0x%02X at +0x57 = %d; the fixture must keep the authoritative value in Extra only",
			loginEquipmentTailBlockFirstByte, raw[loginEquipmentTailBlockFirstByte])
	}
	return raw
}

func loginTailReassertP38Entries(t *testing.T) map[string]dnfrepo.EquipmentEntry {
	t.Helper()
	return map[string]dnfrepo.EquipmentEntry{
		"12": {
			SlotIndex: 12,
			ItemID:    loginTailReassertP38ItemID,
			RawEntry:  loginTailReassertP38RawRow(t, loginTailReassertP38MainRawHex),
			Extra: map[string]string{
				"byte_57":                     "4",
				"value_57":                    "4",
				"ext_data0":                   "9",
				"reinforce":                   "9",
				"quality_seed":                "346360515",
				"current_exe_equipment_type":  "24", // 旧槽（现场实测），有意保留
				"equipped_slot":               "24",
				"current_exe_runtime_move":    "1",
				"current_exe_inventory_count": "1",
				"item_kind":                   "equipment",
				"equipment_type":              "[weapon]",
				"durability":                  "48",
				"max_durability":              "48",
				"pvf_path":                    "equipment/character/swordman/weapon/beamsword/101040493.equ",
			},
		},
		"24": {
			SlotIndex: 24,
			ItemID:    loginTailReassertP38ItemID,
			RawEntry:  loginTailReassertP38RawRow(t, loginTailReassertP38SubRawHex),
			Extra: map[string]string{
				"byte_57":                     "2",
				"value_57":                    "2",
				"ext_data0":                   "7",
				"reinforce":                   "7",
				"quality_seed":                "999999998",
				"current_exe_equipment_type":  "12", // 旧槽（现场实测），有意保留
				"equipped_slot":               "12",
				"current_exe_runtime_move":    "1",
				"current_exe_inventory_count": "1",
				"item_kind":                   "equipment",
				"equipment_type":              "[weapon]",
				"durability":                  "48",
				"max_durability":              "48",
				"pvf_path":                    "equipment/character/swordman/weapon/beamsword/101040493.equ",
			},
		},
	}
}

func seedLoginTailReassertEquipment(t *testing.T, repositories dnfrepo.Group, entries map[string]dnfrepo.EquipmentEntry) {
	t.Helper()
	if err := repositories.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{
		CharacterID: "119",
		Entries:     entries,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLoginTailReassertCarriesForgeByteForColdLogin(t *testing.T) {
	repositories := testRepositoryGroup()
	seedLoginTailReassertEquipment(t, repositories, loginTailReassertP38Entries(t))

	connection := &bufferConn{}
	service := newLoginEquipmentTestService(repositories)
	session := newLoginEquipmentTestSession(connection, 119)
	service.reassertLoginEquipmentTailRows(session, "regression_cold_login_forge")

	packet, rest := splitCurrentGameServerUpperPacketAuto(t, connection.write.Bytes())
	if len(rest) != 0 {
		t.Fatalf("unexpected trailing packets: %x", rest)
	}
	if packet.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("msg_id=%d want=%d（逐槽 op14 载体）", packet.Header.MsgID, uint16(dnfenum.CmdPacketWalkoutPartyMember))
	}
	if packet.Header.Classification != 0 {
		t.Fatalf("classification=%d want=0", packet.Header.Classification)
	}
	body := packet.Body
	if len(body) != 3+2*currentItemListEntryWireSize {
		t.Fatalf("body len=%d want=%d body=%x", len(body), 3+2*currentItemListEntryWireSize, body)
	}
	if body[0] != currentSocketListEquipment {
		t.Fatalf("list_type=%d want=%d", body[0], currentSocketListEquipment)
	}
	if count := binary.LittleEndian.Uint16(body[1:3]); count != 2 {
		t.Fatalf("row count=%d want=2", count)
	}

	rows := decodeCurrentItemListTestRows(t, body[3:])
	byInstance := make(map[uint32][]byte, len(rows))
	for _, row := range rows {
		if itemID := binary.LittleEndian.Uint32(row[0x02:0x06]); itemID != loginTailReassertP38ItemID {
			t.Fatalf("row item_id=%d want=%d row=%x", itemID, loginTailReassertP38ItemID, row)
		}
		byInstance[binary.LittleEndian.Uint32(row[0x06:0x0A])] = row
	}

	main, ok := byInstance[loginTailReassertP38MainInstance]
	if !ok {
		t.Fatalf("main weapon (instance=%d) missing from tail reassert rows: %x", loginTailReassertP38MainInstance, body)
	}
	if got := main[0x57]; got != loginTailReassertP38MainForge {
		t.Fatalf("cold-login main forge byte(0x57)=%d want=%d row=%x", got, loginTailReassertP38MainForge, main)
	}
	if got := main[0x0A]; got != loginTailReassertP38MainReinforce {
		t.Fatalf("main reinforce(0x0A)=%d want=%d", got, loginTailReassertP38MainReinforce)
	}
	if got := binary.LittleEndian.Uint16(main[0x0B:0x0D]); got != 48 {
		t.Fatalf("main durability(0x0B)=%d want=48", got)
	}

	sub, ok := byInstance[loginTailReassertP38SubInstance]
	if !ok {
		t.Fatalf("sub weapon (instance=%d) missing from tail reassert rows: %x", loginTailReassertP38SubInstance, body)
	}
	if got := sub[0x57]; got != loginTailReassertP38SubForge {
		t.Fatalf("cold-login sub forge byte(0x57)=%d want=%d row=%x", got, loginTailReassertP38SubForge, sub)
	}
	if got := sub[0x0A]; got != loginTailReassertP38SubReinforce {
		t.Fatalf("sub reinforce(0x0A)=%d want=%d", got, loginTailReassertP38SubReinforce)
	}
}

func TestLoginTailReassertSkipsRowsWithoutTailBlock(t *testing.T) {
	repositories := testRepositoryGroup()
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint32(raw[0x06:0x0A], 1)
	seedLoginTailReassertEquipment(t, repositories, map[string]dnfrepo.EquipmentEntry{
		"12": {
			SlotIndex: 12,
			ItemID:    loginTailReassertP38ItemID,
			RawEntry:  raw,
			Extra: map[string]string{
				"current_exe_equipment_type": "12",
				"ext_data0":                  "0",
				"durability":                 "48",
				"max_durability":             "48",
				"item_kind":                  "equipment",
			},
		},
	})

	connection := &bufferConn{}
	service := newLoginEquipmentTestService(repositories)
	session := newLoginEquipmentTestSession(connection, 119)
	service.reassertLoginEquipmentTailRows(session, "regression_no_tail_block")

	if connection.write.Len() != 0 {
		t.Fatalf("a row without a tail block produced %d bytes: %x", connection.write.Len(), connection.write.Bytes())
	}
}

func TestLoginTailReassertRollbackLiteralWritesNothing(t *testing.T) {
	t.Setenv(loginEquipmentTailReassertEnv, loginEquipmentTailReassertOff)

	repositories := testRepositoryGroup()
	seedLoginTailReassertEquipment(t, repositories, loginTailReassertP38Entries(t))

	connection := &bufferConn{}
	service := newLoginEquipmentTestService(repositories)
	session := newLoginEquipmentTestSession(connection, 119)
	service.reassertLoginEquipmentTailRows(session, "regression_rollback")

	if connection.write.Len() != 0 {
		t.Fatalf("rollback literal still wrote %d bytes: %x", connection.write.Len(), connection.write.Bytes())
	}
	if loginEquipmentTailReassertEnabled() {
		t.Fatal("rollback literal must be the only way to disable the fix")
	}
}

func TestLoginTailReassertIsReadOnly(t *testing.T) {
	ctx := context.Background()
	repositories := testRepositoryGroup()
	seedLoginTailReassertEquipment(t, repositories, loginTailReassertP38Entries(t))
	before, found, err := repositories.Equipment.Load(ctx, "119")
	if err != nil || !found {
		t.Fatalf("load before: found=%t err=%v", found, err)
	}

	connection := &bufferConn{}
	service := newLoginEquipmentTestService(repositories)
	session := newLoginEquipmentTestSession(connection, 119)
	service.reassertLoginEquipmentTailRows(session, "regression_read_only")

	after, found, err := repositories.Equipment.Load(ctx, "119")
	if err != nil || !found {
		t.Fatalf("load after: found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("tail reassert mutated durable equipment: before=%+v after=%+v", before, after)
	}
}

func TestLoginTailReassertFilterIsByteDriven(t *testing.T) {
	if loginEquipmentTailBlockFirstByte != 0x57 || loginEquipmentTailBlockLastByte != 0x59 {
		t.Fatalf("tail block window = 0x%02X..0x%02X, want 0x57..0x59", loginEquipmentTailBlockFirstByte, loginEquipmentTailBlockLastByte)
	}
	var unlockOnly currentItemListEntry
	unlockOnly.data[0x58] = 3
	if !loginEquipmentRowTailBlockNonZero(unlockOnly) {
		t.Fatal("a row whose only tail byte is the unlock level (0x58) must still be reasserted")
	}
	var beyondTail currentItemListEntry
	beyondTail.data[0x5A] = 9
	if loginEquipmentRowTailBlockNonZero(beyondTail) {
		t.Fatal("0x5A is outside the tail block and must not qualify a row")
	}
	rows := loginEquipmentRowsNeedingTailReassert([]currentItemListEntry{unlockOnly, beyondTail})
	if len(rows) != 1 || rows[0].data[0x58] != 3 {
		t.Fatalf("filter kept %d rows, want exactly the 0x58 row", len(rows))
	}
}

func TestEquipmentProjectionReadsForgeFromBothExtraAliases(t *testing.T) {
	for _, key := range []string{"byte_57", "value_57"} {
		key := key
		t.Run(key, func(t *testing.T) {
			equipped := dnfrepo.EquipmentEntry{
				SlotIndex: 12,
				ItemID:    loginTailReassertP38ItemID,
				RawEntry:  make([]byte, currentItemListEntryWireSize),
				Extra: map[string]string{
					"current_exe_equipment_type": "12",
					key:                          "5",
				},
			}
			row, ok := currentItemListEntryFromEquipment(equipped)
			if !ok {
				t.Fatal("equipped row was not projected")
			}
			if row.data[0x57] != 5 {
				t.Fatalf("projection wrote 0x57=%d from Extra %s, want 5", row.data[0x57], key)
			}
			if !loginEquipmentRowTailBlockNonZero(row) {
				t.Fatalf("row projected from Extra %s does not qualify for tail reassert", key)
			}
		})
	}
}
