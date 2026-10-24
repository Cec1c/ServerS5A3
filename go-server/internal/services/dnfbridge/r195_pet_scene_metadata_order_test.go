package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// R177 established one current-client lookup contract for slot 26: while
// handling the list-3/op14 notification the client resolves the pet serial in
// the op105 table. A miss leaves the level/EXP/satiety controls at their PVF
// "null" defaults, and a later op105 does not replay that notification.
//
// These tests pin the contract at the two scene lifecycles that rebuild the
// selected actor with a full mode1. They reuse the existing real-PVF pet
// fixture and make no claim about a new client behavior or the GUI root cause.
func TestR195PetSceneMetadataOrderDungeonEntryAfterOldGeneration(t *testing.T) {
	service, session, conn := r195NewPetSceneMetadataFixture(t)
	session.preDungeonContextPlayerStateSent = true

	// The preceding scene generation already published op105. Clearing the
	// transport buffer models the next actor generation while leaving the
	// idempotence flag set, which is the state a dungeon entry actually sees.
	if err := service.sendSelectedCreatureStateTable(session, "r195_previous_scene_generation"); err != nil {
		t.Fatal(err)
	}
	if !session.selectedCreatureStateTableSent {
		t.Fatal("fixture did not publish the previous-generation op105")
	}
	conn.write.Reset()

	const source = "r195_dungeon_entry_mode1"
	if err := service.sendCurrentPostStartMapPlayerPlacement(session, nil, worldmap.DungeonRoomScene{}, source); err != nil {
		t.Fatal(err)
	}
	scan := r195ScanPetScenePackets(t, conn.write.Bytes())
	if scan.mode1 < 0 {
		t.Fatal("dungeon entry did not emit a full mode1 actor generation")
	}
	slot, ok := r195FirstSlot26After(scan, scan.mode1)
	if !ok {
		t.Fatalf("dungeon entry mode1 at packet %d emitted no slot26 op14 refresh", scan.mode1)
	}
	assertEquippedCreatureItemRefreshPacket(t, slot.packet, 400990168, 2, 0, 0)

	table, entry, ok := r195FirstCreatureTableAfter(scan, scan.mode1, 2)
	if !ok {
		t.Fatalf("R177 lookup contract violated: dungeon entry mode1 at packet %d rebuilt the actor, but no op105 carrying creature serial 2 was resent; slot26 op14 was sent at packet %d against the previous generation's table", scan.mode1, slot.index)
	}
	if table.index > slot.index {
		t.Fatalf("R177 lookup contract violated: slot26 op14 at packet %d precedes its op105 metadata at packet %d; the client notification handler cannot replay a later table", slot.index, table.index)
	}
	if entry.satiety != 72 || entry.exp != 123 || entry.level != 7 {
		t.Fatalf("slot26 op14 at packet %d resolved creature 2 to satiety=%d exp=%d level=%d, want 72/123/7", slot.index, entry.satiety, entry.exp, entry.level)
	}
}

// Verify the town post-transition path itself. Unlike dungeon entry, its
// mode1 helper already emits the slot26 op14 refresh, so this test first
// checks that the refresh exists and then checks the R177 metadata order for
// that same notification.
func TestR195PetSceneMetadataOrderTownReturnMode1Slot26(t *testing.T) {
	service, session, conn := r195NewPetSceneMetadataFixture(t)

	if err := service.sendSelectedCreatureStateTable(session, "r195_previous_scene_generation"); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()

	const source = "r195_town_return_mode1"
	service.armCurrentTownPostTransitionPlayerState(session, source)
	if err := service.sendCurrentTownPostTransitionPlayerState(session, source); err != nil {
		t.Fatal(err)
	}
	scan := r195ScanPetScenePackets(t, conn.write.Bytes())
	if scan.mode1 < 0 {
		t.Fatal("town post-transition did not emit a full mode1 actor generation")
	}
	var afterMode1 []r195PetScenePacket
	for _, slot := range scan.slot26 {
		if slot.index > scan.mode1 {
			afterMode1 = append(afterMode1, slot)
		}
	}
	if len(afterMode1) == 0 {
		t.Fatalf("town post-transition mode1 at packet %d emitted no slot26 op14 refresh for the equipped pet", scan.mode1)
	}
	if len(afterMode1) != 1 {
		t.Fatalf("town post-transition mode1 at packet %d emitted %d slot26 op14 refreshes, want exactly one", scan.mode1, len(afterMode1))
	}
	slot := afterMode1[0]
	assertEquippedCreatureItemRefreshPacket(t, slot.packet, 400990168, 2, 0, 0)

	table, entry, ok := r195FirstCreatureTableAfter(scan, scan.mode1, 2)
	if !ok {
		t.Fatalf("town post-transition mode1 at packet %d emitted slot26 op14 at packet %d but no op105 carrying creature serial 2", scan.mode1, slot.index)
	}
	if table.index > slot.index {
		t.Fatalf("R177 lookup contract violated in the town path: slot26 op14 at packet %d precedes its op105 resend at packet %d; the refresh is present but its notification is handled before the table", slot.index, table.index)
	}
	if entry.satiety != 72 || entry.exp != 123 || entry.level != 7 {
		t.Fatalf("slot26 op14 at packet %d resolved creature 2 to satiety=%d exp=%d level=%d, want 72/123/7", slot.index, entry.satiety, entry.exp, entry.level)
	}
}

func TestR195PetSceneMetadataOrderTownSlot26FailureResumesAtItemCursor(t *testing.T) {
	service, session, conn := r195NewPetSceneMetadataFixture(t)
	if err := service.sendSelectedCreatureStateTable(session, "r195_previous_scene_generation"); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()

	wantErr := errors.New("injected town slot26 op14 write failure")
	failing := &r195FailFirstSlot26Conn{err: wantErr}
	session.conn = failing
	const source = "r195_town_slot26_failure"
	service.armCurrentTownPostTransitionPlayerState(session, source)
	if err := service.sendCurrentTownPostTransitionPlayerState(session, source); !errors.Is(err, wantErr) {
		t.Fatalf("slot26 write failure=%v want=%v", err, wantErr)
	}
	if !failing.failed {
		t.Fatal("fixture did not reach the first slot26 op14 write")
	}
	if got := r195TownPacketSignatures(t, failing.write.Bytes()); !r195SameStrings(
		got,
		[]string{"mode0", "mode1", "op105"},
	) {
		t.Fatalf("failed town prefix=%v want=[mode0 mode1 op105]", got)
	}
	if session.townPostTransition.stage != currentTownPostTransitionCreatureTableSent {
		t.Fatalf("slot26 failure advanced stage=%d want=%d", session.townPostTransition.stage, currentTownPostTransitionCreatureTableSent)
	}
	if !session.selectedCreatureStateTableSent {
		t.Fatal("successful op105 was not committed before the slot26 write failure")
	}
	first := r195ScanPetScenePackets(t, failing.write.Bytes())
	if len(first.slot26) != 0 || len(first.op105) != 1 {
		t.Fatalf("failed prefix slot26=%d op105=%d, want slot26=0 op105=1", len(first.slot26), len(first.op105))
	}
	table := first.op105[0]
	entry, ok := table.entries[2]
	if !ok {
		t.Fatal("failed prefix op105 omitted creature serial 2")
	}
	if entry.satiety != 72 || entry.exp != 123 || entry.level != 7 {
		t.Fatalf("failed prefix op105 values=satiety:%d exp:%d level:%d want=72/123/7", entry.satiety, entry.exp, entry.level)
	}
	if countPacketsWithID(t, failing.write.Bytes(), currentCreatureGrowthMsgID) != 0 {
		t.Fatal("failed prefix emitted op102 before the slot26 retry")
	}
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("fixture repositories unavailable")
	}
	before, found, err := repositories.Pet.Load(context.Background(), "9")
	if err != nil || !found {
		t.Fatalf("load pet before retry found=%t err=%v", found, err)
	}

	retry := &bufferConn{}
	session.conn = retry
	if err := service.sendCurrentTownPostTransitionPlayerState(session, source+"_retry"); err != nil {
		t.Fatalf("town slot26 retry: %v", err)
	}
	raw := retry.write.Bytes()
	slot, rest := splitLongHengGameServerUpperPacket(t, raw)
	if slot.Header.Classification != 0 || slot.Header.MsgID != 14 ||
		len(slot.Body) <= 5 || slot.Body[0] != 3 ||
		binary.LittleEndian.Uint16(slot.Body[3:5]) != 26 {
		t.Fatalf("retry first packet header=%+v body=%x, want slot26 op14", slot.Header, slot.Body)
	}
	assertEquippedCreatureItemRefreshPacket(t, slot, 400990168, 2, 0, 0)
	growth, rest := splitLongHengGameServerUpperPacket(t, rest)
	if growth.Header.Classification != 0 || growth.Header.MsgID != currentCreatureGrowthMsgID ||
		!bytes.Equal(growth.Body, []byte{7, 0, 123, 0, 0, 0}) {
		t.Fatalf("retry growth header=%+v body=%x want level7/exp123/mode0", growth.Header, growth.Body)
	}
	if got := r195TownPacketSignatures(t, rest); !r195SameStrings(
		got,
		[]string{"op37", "op30", "op19", "op120"},
	) {
		t.Fatalf("retry suffix=%v want=[op37 op30 op19 op120]", got)
	}
	retryScan := r195ScanPetScenePackets(t, raw)
	if retryScan.mode1 >= 0 || len(retryScan.op105) != 0 || len(retryScan.slot26) != 1 {
		t.Fatalf(
			"retry replayed an actor/table: mode1=%d op105=%d slot26=%d",
			retryScan.mode1,
			len(retryScan.op105),
			len(retryScan.slot26),
		)
	}
	if session.townPostTransition.stage != currentTownPostTransitionComplete {
		t.Fatalf("slot26 retry stage=%d want=%d", session.townPostTransition.stage, currentTownPostTransitionComplete)
	}
	after, found, err := repositories.Pet.Load(context.Background(), "9")
	if err != nil || !found {
		t.Fatalf("load pet after retry found=%t err=%v", found, err)
	}
	beforeEntry := before.Entries["2"]
	afterEntry := after.Entries["2"]
	if before.EquippedKey != after.EquippedKey ||
		beforeEntry.Level != afterEntry.Level ||
		beforeEntry.Exp != afterEntry.Exp ||
		beforeEntry.Satiety != afterEntry.Satiety ||
		beforeEntry.SatietyMicros != afterEntry.SatietyMicros {
		t.Fatalf(
			"slot26 retry reset pet state before={equipped:%s level:%d exp:%d satiety:%d micros:%d} after={equipped:%s level:%d exp:%d satiety:%d micros:%d}",
			before.EquippedKey,
			beforeEntry.Level,
			beforeEntry.Exp,
			beforeEntry.Satiety,
			beforeEntry.SatietyMicros,
			after.EquippedKey,
			afterEntry.Level,
			afterEntry.Exp,
			afterEntry.Satiety,
			afterEntry.SatietyMicros,
		)
	}
}

func TestR195PetSceneMetadataOrderTownNoPetAdvancesPastItemCursor(t *testing.T) {
	service, session, conn := r195NewPetSceneMetadataFixture(t)
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("fixture repositories unavailable")
	}
	ctx := context.Background()
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "9",
		Entries:     map[string]dnfrepo.EquipmentEntry{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Pet.Save(ctx, dnfrepo.PetRecord{
		CharacterID: "9",
		Entries:     map[string]dnfrepo.PetEntry{},
	}); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()

	const source = "r195_town_no_pet"
	service.armCurrentTownPostTransitionPlayerState(session, source)
	if err := service.sendCurrentTownPostTransitionPlayerState(session, source); err != nil {
		t.Fatal(err)
	}
	raw := conn.write.Bytes()
	if got := r195TownPacketSignatures(t, raw); !r195SameStrings(
		got,
		[]string{"mode0", "mode1", "op105", "op37", "op30", "op19", "op120"},
	) {
		t.Fatalf("no-pet town order=%v want=[mode0 mode1 op105 op37 op30 op19 op120]", got)
	}
	scan := r195ScanPetScenePackets(t, raw)
	if scan.mode1 < 0 || len(scan.slot26) != 0 || len(scan.op105) != 1 {
		t.Fatalf("no-pet scan mode1=%d slot26=%d op105=%d", scan.mode1, len(scan.slot26), len(scan.op105))
	}
	if entries := scan.op105[0].entries; len(entries) != 0 {
		t.Fatalf("no-pet op105 entries=%v want empty", entries)
	}
	if countPacketsWithID(t, raw, currentCreatureGrowthMsgID) != 0 {
		t.Fatal("no-pet town transition emitted op102")
	}
	if session.townPostTransition.stage != currentTownPostTransitionComplete {
		t.Fatalf("no-pet town stage=%d want=%d", session.townPostTransition.stage, currentTownPostTransitionComplete)
	}
}

func TestR195PetSceneMetadataOrderProjectionWriteFailureRetries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		msgID  uint16
		prefix []string
	}{
		{name: "metadata", msgID: currentCreatureStateTableMsgID},
		{name: "slot26", msgID: uint16(dnfenum.CmdPacketWalkoutPartyMember), prefix: []string{"op105"}},
		{name: "growth", msgID: currentCreatureGrowthMsgID, prefix: []string{"op105", "slot26"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, session, _ := r195NewPetSceneMetadataFixture(t)
			wantErr := errors.New("injected pet projection write failure")
			conn := &failNthDungeonWriteConn{err: wantErr}
			conn.failWhen = func(data []byte) bool {
				if tc.msgID == uint16(dnfenum.CmdPacketWalkoutPartyMember) {
					return r195TownPetSlot26Write(data)
				}
				packet, rest := splitLongHengGameServerUpperPacket(t, data)
				if packet.Header.Classification != 0 || packet.Header.MsgID != tc.msgID || len(rest) != 0 {
					return false
				}
				return true
			}
			session.conn = conn
			if err := service.sendSelectedCreatureInitialProjection(session, "r195_projection_failure"); !errors.Is(err, wantErr) {
				t.Fatalf("projection error=%v want=%v", err, wantErr)
			}
			if len(conn.failed) == 0 {
				t.Fatal("fixture did not reach the selected pet write")
			}
			if session.selectedCreatureStateTableSent {
				t.Fatal("failed projection retained the table-sent flag and would suppress retry metadata")
			}
			if got := r195TownPacketSignatures(t, conn.write.Bytes()); !r195SameStrings(got, tc.prefix) {
				t.Fatalf("failed projection prefix=%v want=%v", got, tc.prefix)
			}

			conn.failWhen = nil
			conn.write.Reset()
			if err := service.sendSelectedCreatureInitialProjection(session, "r195_projection_retry"); err != nil {
				t.Fatal(err)
			}
			if got := r195TownPacketSignatures(t, conn.write.Bytes()); !r195SameStrings(got, []string{"op105", "slot26", "op102"}) {
				t.Fatalf("retry projection order=%v want=[op105 slot26 op102]", got)
			}
			if !session.selectedCreatureStateTableSent {
				t.Fatal("successful projection did not commit its metadata flag")
			}
			scan := r195ScanPetScenePackets(t, conn.write.Bytes())
			if len(scan.op105) != 1 || len(scan.slot26) != 1 {
				t.Fatalf("retry metadata=%d slot26=%d want=1/1", len(scan.op105), len(scan.slot26))
			}
			entry, ok := scan.op105[0].entries[2]
			if !ok || entry.satiety != 72 || entry.exp != 123 || entry.level != 7 {
				t.Fatalf("retry pet metadata=%+v present=%t want serial2 satiety72 exp123 level7", entry, ok)
			}
			assertEquippedCreatureItemRefreshPacket(t, scan.slot26[0].packet, 400990168, 2, 0, 0)
		})
	}
}

type r195FailFirstSlot26Conn struct {
	bufferConn
	err     error
	attempt int
	failed  bool
}

func (connection *r195FailFirstSlot26Conn) Write(data []byte) (int, error) {
	connection.attempt++
	if !connection.failed && r195TownPetSlot26Write(data) {
		connection.failed = true
		return 0, connection.err
	}
	return connection.bufferConn.Write(data)
}

const (
	r195TownPetRefreshSlot   uint16 = 26
	r195TownPetRefreshItemID uint32 = 400990168
	r195TownPetRefreshSerial uint32 = 2
)

func r195TownPetSlot26Body(body []byte) bool {
	// Fixed16 body: list3, count1, then one 0x77-byte entry. The entry starts
	// at body+3, so slot is +3..5, item ID is +5..9 and its instance is +9..13.
	return len(body) == 3+currentItemListEntryWireSize &&
		body[0] == currentSocketListEquipment &&
		binary.LittleEndian.Uint16(body[1:3]) == 1 &&
		binary.LittleEndian.Uint16(body[3:5]) == r195TownPetRefreshSlot &&
		binary.LittleEndian.Uint32(body[5:9]) == r195TownPetRefreshItemID &&
		binary.LittleEndian.Uint32(body[9:13]) == r195TownPetRefreshSerial
}

func r195TownPetSlot26Write(data []byte) bool {
	const headerSize = dnfproto.GameServerUpperHeaderSize16
	// The town fixture uses the 16-byte current header. Its sequence at 11..13
	// can be non-zero, so the auto splitter must not treat this packet as the
	// 13-byte legacy header and prepend the three zero header-tail bytes to it.
	if len(data) != headerSize+3+currentItemListEntryWireSize ||
		data[0] != 0 ||
		binary.LittleEndian.Uint16(data[1:3]) != uint16(dnfenum.CmdPacketWalkoutPartyMember) ||
		binary.LittleEndian.Uint32(data[3:7]) != uint32(len(data)) ||
		data[13] != 0 || data[14] != 0 || data[15] != 0 {
		return false
	}
	return r195TownPetSlot26Body(data[headerSize:])
}

func r195SameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func r195TownPacketSignatures(t *testing.T, data []byte) []string {
	t.Helper()
	signatures := make([]string, 0, 8)
	for len(data) != 0 {
		packet, rest := splitLongHengGameServerUpperPacket(t, data)
		switch packet.Header.MsgID {
		case uint16(dnfenum.CmdPacketSetUDPIPPort), 1371:
			if len(packet.Body) == 0 {
				t.Fatalf("town op2 has empty body")
			}
			switch packet.Body[0] {
			case 0:
				signatures = append(signatures, "mode0")
			case 1:
				signatures = append(signatures, "mode1")
			default:
				t.Fatalf("town op2 mode=%d body=%x", packet.Body[0], packet.Body)
			}
		case currentCreatureStateTableMsgID:
			signatures = append(signatures, "op105")
		case uint16(dnfenum.CmdPacketWalkoutPartyMember):
			if !r195TownPetSlot26Body(packet.Body) {
				t.Fatalf("unexpected town op14 body=%x", packet.Body)
			}
			signatures = append(signatures, "slot26")
		case currentCreatureGrowthMsgID:
			signatures = append(signatures, "op102")
		case uint16(dnfenum.CmdPacketFinishLoading):
			signatures = append(signatures, "op37")
		case currentIncreaseStatusResultMsgID:
			signatures = append(signatures, "op30")
		case currentSkillInfoMsgID:
			signatures = append(signatures, "op19")
		case uint16(dnfenum.CmdPacketRequestBlacklist):
			signatures = append(signatures, "op120")
		default:
			t.Fatalf("unexpected town packet header=%+v body=%x", packet.Header, packet.Body)
		}
		data = rest
	}
	return signatures
}

func r195NewPetSceneMetadataFixture(t *testing.T) (*Service, *gameSession, *bufferConn) {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_SINGLE_ANTON_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current369d PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "9",
		AccountID:   "account-1",
		Name:        "Actor9",
		Job:         "14",
		Level:       90,
		Stats:       map[string]int64{"fatigue": 156},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "9",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	equipment := dnfrepo.EquipmentRecord{
		CharacterID: "9",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"26": {
				SlotIndex: 26,
				ItemID:    400990168,
				RawEntry:  testCurrentEquippedCreatureEnchantRaw(26, 400990168, 2, 0, 0),
			},
		},
	}
	if err := repositories.Equipment.Save(ctx, equipment); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Pet.Save(ctx, dnfrepo.PetRecord{
		CharacterID: "9",
		EquippedKey: "2",
		Entries: map[string]dnfrepo.PetEntry{
			"2": {
				PetKey:      "2",
				CreatureKey: 2,
				ItemID:      400990168,
				Level:       7,
				Exp:         123,
				Satiety:     72,
				NameRaw:     []byte("PetB"),
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	service := &Service{
		initialEquipmentArchive: archive,
		options: options{
			accountID:            "account-1",
			gameUpperHeader:      gameUpperHeaderServer16,
			gameUpperBodyCodec:   gameUpperBodyCodecPlain,
			currentClientVersion: "official-24.9",
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{
		conn:                conn,
		selectedCharacterID: 9,
		accountID:           "account-1",
		channel:             channelcatalog.Channel{ID: 23, Type: 23},
	}
	return service, session, conn
}

type r195PetScenePacket struct {
	index  int
	packet dnfproto.ChannelPacket
}

type r195PetStateEntry struct {
	satiety byte
	mode    byte
	exp     uint32
	level   byte
}

type r195PetStateTable struct {
	index   int
	entries map[uint32]r195PetStateEntry
}

type r195PetSceneScan struct {
	mode1  int
	slot26 []r195PetScenePacket
	op105  []r195PetStateTable
}

func r195ScanPetScenePackets(t *testing.T, raw []byte) r195PetSceneScan {
	t.Helper()
	scan := r195PetSceneScan{mode1: -1}
	for index := 0; len(raw) > 0; index++ {
		packet, rest := splitLongHengGameServerUpperPacket(t, raw)
		raw = rest
		if (packet.Header.MsgID == 2 || packet.Header.MsgID == 1371) &&
			len(packet.Body) > 0 && packet.Body[0] == 1 {
			if scan.mode1 < 0 {
				scan.mode1 = index
			}
			continue
		}
		if packet.Header.MsgID == currentCreatureStateTableMsgID {
			scan.op105 = append(scan.op105, r195PetStateTable{
				index:   index,
				entries: r195ParseCreatureStateBody(t, packet.Body),
			})
			continue
		}
		if packet.Header.MsgID == 14 && r195TownPetSlot26Body(packet.Body) {
			scan.slot26 = append(scan.slot26, r195PetScenePacket{index: index, packet: packet})
		}
	}
	return scan
}

func r195FirstSlot26After(scan r195PetSceneScan, after int) (r195PetScenePacket, bool) {
	for _, slot := range scan.slot26 {
		if slot.index > after {
			return slot, true
		}
	}
	return r195PetScenePacket{}, false
}

func r195FirstCreatureTableAfter(
	scan r195PetSceneScan,
	after int,
	serial uint32,
) (r195PetStateTable, r195PetStateEntry, bool) {
	for _, table := range scan.op105 {
		if table.index <= after {
			continue
		}
		if entry, ok := table.entries[serial]; ok {
			return table, entry, true
		}
	}
	return r195PetStateTable{}, r195PetStateEntry{}, false
}

// r195ParseCreatureStateBody follows the same u8-count layout asserted by
// pet_login_tooltip_r177_test.go: key, satiety, mode, EXP, optional mode-1
// bytes, level, DSTR name, and tail flag per entry.
func r195ParseCreatureStateBody(t *testing.T, body []byte) map[uint32]r195PetStateEntry {
	t.Helper()
	if len(body) == 0 {
		t.Fatal("op105 body is empty")
	}
	count := int(body[0])
	rest := body[1:]
	entries := make(map[uint32]r195PetStateEntry, count)
	for index := 0; index < count; index++ {
		if len(rest) < 16 {
			t.Fatalf("op105 entry %d truncated: %x", index, rest)
		}
		key := binary.LittleEndian.Uint32(rest[0:4])
		satiety := rest[4]
		mode := rest[5]
		if mode > 1 {
			t.Fatalf("op105 entry %d has unsupported mode %d", index, mode)
		}
		exp := binary.LittleEndian.Uint32(rest[6:10])
		position := 10
		if mode == 1 {
			position += 2
		}
		if len(rest) < position+5 {
			t.Fatalf("op105 entry %d level/name header truncated: %x", index, rest)
		}
		level := rest[position]
		nameLength := int(int32(binary.LittleEndian.Uint32(rest[position+1 : position+5])))
		position += 5
		if nameLength < 0 || len(rest) < position+nameLength+1 {
			t.Fatalf("op105 entry %d name length %d truncated: %x", index, nameLength, rest)
		}
		position += nameLength + 1
		entries[key] = r195PetStateEntry{
			satiety: satiety,
			mode:    mode,
			exp:     exp,
			level:   level,
		}
		rest = rest[position:]
	}
	if len(rest) != 0 {
		t.Fatalf("op105 has %d trailing bytes after %d entries", len(rest), count)
	}
	return entries
}
