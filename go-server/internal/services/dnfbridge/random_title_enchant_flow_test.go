package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"os"
	"strconv"
	"testing"
)

func TestRealPVFLaborTitleBeadCommitsThenRehydratesWornRow(t *testing.T) {
	f := newRealCeraPackageFixture(t, 490700960, 2, 84)
	ctx := context.Background()
	title, err := f.catalog.ResolveItem(400330094)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := buildCurrentDungeonPickupStack(10, title, 1)
	if err != nil {
		t.Fatal(err)
	}
	inv := f.inventory(t)
	inv.Slots["0:10"] = stack
	if err := f.repositories.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	f.service.options.pvfPath = os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	f.service.options.gameUpperHeader = gameUpperHeaderServer16
	f.service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 19}
	body := []byte{0, 84, 0, 0, 10, 0, 0, 0, 0, 0}
	handled, err := f.service.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketEnchantByBead), body)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	inv = f.inventory(t)
	result := inv.Slots["0:10"]
	config := result.Extra["random_title_enchant_config_id"]
	rowID := result.Extra["random_title_enchant_row_id"]
	if inv.Slots["0:84"].Count != 1 || config != "10001" || rowID == "" {
		t.Fatalf("bead=%+v title=%+v", inv.Slots["0:84"], result)
	}
	table, err := parseCurrentRandomTitleEnchantCatalog(f.catalog.source)
	if err != nil {
		t.Fatal(err)
	}
	selected, _ := strconv.ParseUint(rowID, 10, 16)
	positive := false
	for _, row := range table.Configs[10001].Rows {
		if row.ID == uint16(selected) && row.Weight > 0 {
			positive = true
		}
	}
	if !positive {
		t.Fatalf("selected disabled or absent row %s", rowID)
	}
	row := currentItemListEntryFromStack(0, 10, result)
	if binary.LittleEndian.Uint16(row.data[0x61:0x63]) != 10001 || binary.LittleEndian.Uint16(row.data[0x63:0x65]) != uint16(selected) {
		t.Fatalf("wire title=%x", row.data)
	}
	// Persisted item becomes the worn title. Recreate the service to exclude
	// session state and require the row updater to read durable equipment.
	if err := f.repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{"13": {SlotIndex: 13, ItemID: result.ItemID, Extra: result.Extra, RawEntry: result.RawEntry}}}); err != nil {
		t.Fatal(err)
	}
	fresh := &Service{options: f.service.options, pvfItemCatalog: f.catalog, repositoryProvider: func() (dnfrepo.Group, bool) { return f.repositories, true }}
	cold := &bufferConn{}
	coldSession := &gameSession{conn: cold, selectedCharacterID: 19}
	reader := &csharpLegacyUserInfoReader{ctx: ctx, service: fresh, session: coldSession, characterID: "19"}
	reader.currentMode1EquipmentObjectRows()

	if err := fresh.sendSelectedWornTitleEnchantRowRefresh(coldSession, "test_actor_recreated"); err != nil {
		t.Fatal(err)
	}
	packet, tail := splitLongHengGameServerUpperPacket(t, cold.write.Bytes())
	if len(tail) != 0 || packet.Header.MsgID != 14 || packet.Header.Classification != 0 || len(packet.Body) != 122 || packet.Body[0] != 3 || binary.LittleEndian.Uint16(packet.Body[3:5]) != 13 {
		t.Fatalf("rehydrate packet=%+v tail=%x", packet, tail)
	}
	if binary.LittleEndian.Uint16(packet.Body[3+0x61:3+0x63]) != 10001 || binary.LittleEndian.Uint16(packet.Body[3+0x63:3+0x65]) != uint16(selected) {
		t.Fatalf("rehydrate title=%x", packet.Body)
	}
}

type titleEnchantFailConn struct{ *bufferConn }

func (c *titleEnchantFailConn) Write(b []byte) (int, error) {
	return 0, errors.New("title replay injected failure")
}

func TestRandomTitleEnchantReplayUsesOneOwnedProjectionWithoutRepositoryReads(t *testing.T) {
	s := &Service{options: options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (dnfrepo.Group, bool) { panic("replay must reuse the mode1 snapshot") }}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 19}
	if err := s.sendSelectedWornTitleEnchantRowRefresh(session, "no_title"); err != nil {
		t.Fatal(err)
	}
	row := currentItemListEntry{}
	row.patchCore(13, 400330094, 1)
	binary.LittleEndian.PutUint16(row.data[0x61:0x63], 10001)
	binary.LittleEndian.PutUint16(row.data[0x63:0x65], 37)
	reader := &csharpLegacyUserInfoReader{session: session, characterID: "19"}
	reader.resetCurrentRandomTitleEnchantRow()
	reader.noteCurrentRandomTitleEnchantRow(row)
	session.selectedCharacterID = 20
	if err := s.sendSelectedWornTitleEnchantRowRefresh(session, "changed_owner"); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatal("another selected character received a title row")
	}
	session.selectedCharacterID = 19
	session.conn = &titleEnchantFailConn{&bufferConn{}}
	if err := s.sendSelectedWornTitleEnchantRowRefresh(session, "failed_write"); err == nil || session.pendingTitleEnchantRow == nil {
		t.Fatalf("failed write err=%v pending=%v", err, session.pendingTitleEnchantRow)
	}
	session.conn = conn
	if err := s.sendSelectedWornTitleEnchantRowRefresh(session, "retry"); err != nil {
		t.Fatal(err)
	}
	size := conn.write.Len()
	if size == 0 || session.pendingTitleEnchantRow != nil {
		t.Fatal("successful replay was not consumed")
	}
	if err := s.sendSelectedWornTitleEnchantRowRefresh(session, "duplicate"); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != size {
		t.Fatal("duplicate replay emitted another packet")
	}
	reader.noteCurrentRandomTitleEnchantRow(row)
	reader.resetCurrentRandomTitleEnchantRow()
	if session.pendingTitleEnchantRow != nil {
		t.Fatal("a new projection retained the old title")
	}
}
