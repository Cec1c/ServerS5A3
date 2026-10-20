package dnfbridge

import (
	"context"
	"encoding/binary"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"path/filepath"
	"testing"
)

func TestRealPVFCurrentUnequipSendsAckThenCommittedBagRow(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires published PVF for the real bridge placement dependency")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newLoginWearBridgeFixture(t)
	fixture.service.initialEquipmentArchive = archive
	logger, logPath := newPeerEndpointTestLogger(t)
	defer logger.Close()
	fixture.service.packetLog = logger
	fixture.service.options.currentClientVersion = "official-24.9"
	// Exercise the committed path reached after the existing quiet-window
	// reader dispatches the saved real player request.
	fixture.session.op19QuietResolving = true
	record, _, err := fixture.group.Equipment.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	entry := record.Entries["12"]
	delete(record.Entries, "12")
	entry.SlotIndex = 17
	binary.LittleEndian.PutUint16(entry.RawEntry, 17)
	record.Entries["17"] = entry
	if err := fixture.group.Equipment.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 28)
	binary.LittleEndian.PutUint16(body[1:], 10)
	binary.LittleEndian.PutUint32(body[7:], 1)
	body[11] = 3
	binary.LittleEndian.PutUint16(body[12:], 17)
	binary.LittleEndian.PutUint32(body[22:], 0xffffffff)
	body[26] = 1 // Actual player drag; do not treat this as a wear claim.
	handled, err := fixture.service.handleAlignedGameCommand(fixture.session, 1, 19, body)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	stream := fixture.buffer.write.Bytes()
	if len(stream) == 0 {
		t.Fatalf("no response: %s", readPeerEndpointTestLog(t, logger, logPath))
	}
	first, stream := splitLongHengGameServerUpperPacket(t, stream)
	if first.Header.MsgID != 19 || first.Header.Classification != 1 || len(first.Body) != 11 || first.Body[0] != 1 {
		t.Fatalf("first packet=%+v body=%x", first.Header, first.Body)
	}
	row, _ := splitLongHengGameServerUpperPacket(t, stream)
	if row.Header.MsgID != 14 || row.Header.Classification != 0 || len(row.Body) != 3+currentItemListEntryWireSize || row.Body[0] != 0 || binary.LittleEndian.Uint16(row.Body[1:]) != 1 {
		t.Fatalf("bag packet=%+v len=%d body=%x", row.Header, len(row.Body), row.Body)
	}
	if binary.LittleEndian.Uint16(row.Body[3:]) != 10 || binary.LittleEndian.Uint32(row.Body[5:]) != uint32(loginWearBridgeWeaponItemID) {
		t.Fatalf("bag row=%x", row.Body)
	}
	inv, _, err := fixture.group.Inventory.Load(context.Background(), "19")
	if err != nil || inv.Slots["0:10"].ItemID != int64(loginWearBridgeWeaponItemID) {
		t.Fatalf("inventory=%+v err=%v", inv, err)
	}
	if dir := os.Getenv("DNF90_UNEQUIP_NATIVE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "unequip-bag-row.bin"), row.Body, 0644); err != nil {
			t.Fatal(err)
		}
	}
}
