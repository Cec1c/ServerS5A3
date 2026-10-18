package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	itemSpaceSyncTestAccountID   = "dnf:66"
	itemSpaceSyncTestCharacterID = uint16(66)
)

type itemSpaceSyncAssetSnapshot struct {
	account     dnfrepo.AccountRecord
	accountOK   bool
	character   dnfrepo.CharacterRecord
	charOK      bool
	inventory   dnfrepo.InventoryRecord
	inventoryOK bool
	equipment   dnfrepo.EquipmentRecord
	equipOK     bool
}

func newItemSpaceSyncTest(t *testing.T) (*Service, *gameSession, *bufferConn, *dnfrepo.Group) {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID:            itemSpaceSyncTestAccountID,
		State:                "active",
		HonorExp:             12345,
		RepresentAccountName: "barrier-account",
		Metadata:             map[string]string{"keep": "yes", "fixture": "nonempty"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "66",
		AccountID:   itemSpaceSyncTestAccountID,
		Name:        "barrier-owner",
		Level:       90,
		Stats:       map[string]int64{"gold": 987654321, "fatigue": 156},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "66",
		Slots: map[string]dnfrepo.ItemStack{
			"0:65": {
				ItemID: 10164868,
				Count:  7,
				Extra:  map[string]string{"pvf_path": "stackable/10164001/10164868.stk", "stackable_type": "[material]"},
			},
			"0:66": {ItemID: 3033, Count: 11, Bind: true},
		},
		Warehouse: map[string]dnfrepo.ItemStack{
			"2:0": {ItemID: 100000123, Count: 3},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "66",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"15": {
				SlotIndex: 15,
				ItemID:    100120001,
				Bind:      true,
				RawEntry:  []byte{15, 0, 1, 2, 3, 4, 5, 6},
				Extra:     map[string]string{"pvf_path": "equipment/weapon/100120001.equ", "equipment_type": "[weapon]"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "item-space-sync-test",
		accountID:           itemSpaceSyncTestAccountID,
		accountFromToken:    true,
		loginPacketSeen:     true,
		selectedCharacterID: itemSpaceSyncTestCharacterID,
		characterGeneration: 7,
	}
	group := &repositories
	service := &Service{
		options: options{
			currentClientVersion: "official-24.9",
			gameUpperHeader:      gameUpperHeaderChannel13,
			gameUpperBodyCodec:   gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return *group, true },
		gameSessions:       map[uint16]*gameSession{itemSpaceSyncTestCharacterID: session},
	}
	return service, session, connection, group
}

func itemSpaceSyncAssets(t *testing.T, repositories dnfrepo.Group) itemSpaceSyncAssetSnapshot {
	t.Helper()
	ctx := context.Background()
	account, accountOK, err := repositories.Account.Load(ctx, itemSpaceSyncTestAccountID)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	character, charOK, err := repositories.Character.Load(ctx, "66")
	if err != nil {
		t.Fatalf("load character: %v", err)
	}
	inventory, inventoryOK, err := repositories.Inventory.Load(ctx, "66")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	equipment, equipOK, err := repositories.Equipment.Load(ctx, "66")
	if err != nil {
		t.Fatalf("load equipment: %v", err)
	}
	return itemSpaceSyncAssetSnapshot{
		account: account, accountOK: accountOK,
		character: character, charOK: charOK,
		inventory: inventory, inventoryOK: inventoryOK,
		equipment: equipment, equipOK: equipOK,
	}
}

func seededItemSpaceSyncAssets(t *testing.T, repositories dnfrepo.Group) itemSpaceSyncAssetSnapshot {
	t.Helper()
	snapshot := itemSpaceSyncAssets(t, repositories)
	if !snapshot.accountOK || snapshot.account.RepresentAccountName == "" || len(snapshot.account.Metadata) == 0 {
		t.Fatalf("account fixture is not seeded: %+v", snapshot.account)
	}
	if !snapshot.charOK || snapshot.character.Stats["gold"] <= 0 {
		t.Fatalf("character fixture is not seeded: %+v", snapshot.character)
	}
	if !snapshot.inventoryOK || len(snapshot.inventory.Slots) == 0 || len(snapshot.inventory.Warehouse) == 0 {
		t.Fatalf("inventory fixture is not seeded: %+v", snapshot.inventory)
	}
	if !snapshot.equipOK || len(snapshot.equipment.Entries) == 0 {
		t.Fatalf("equipment fixture is not seeded: %+v", snapshot.equipment)
	}
	return snapshot
}

func expectedItemSpaceSyncPacket(t *testing.T, sequence uint16) []byte {
	t.Helper()
	packet, err := dnfproto.BuildGameServerUpperPacketWithHeaderSize(
		currentItemSpaceSyncOpcode,
		[]byte{1},
		sequence,
		dnfproto.DefaultChannelClassification,
		dnfproto.GameServerUpperHeaderSize,
	)
	if err != nil {
		t.Fatal(err)
	}
	return packet
}

func itemSpaceSyncLegacyFrame(cmd byte, typ, sequence uint16, body []byte) []byte {
	frame := make([]byte, dnfproto.LegacyGameHeaderSize+len(body))
	frame[0] = cmd
	binary.LittleEndian.PutUint16(frame[1:3], typ)
	binary.LittleEndian.PutUint32(frame[3:7], uint32(len(frame)))
	binary.LittleEndian.PutUint16(frame[11:13], sequence)
	copy(frame[dnfproto.LegacyGameHeaderSize:], body)
	return frame
}

func assertItemSpaceSyncExactReply(t *testing.T, connection *bufferConn, sequence uint16) {
	t.Helper()
	written := connection.write.Bytes()
	want := expectedItemSpaceSyncPacket(t, sequence)
	if !bytes.Equal(written, want) {
		t.Fatalf("production dispatch wire=%x want=%x", written, want)
	}
}

func TestItemSpaceSyncProductionHandleGameCommandLiteralFF(t *testing.T) {
	service, session, connection, repositories := newItemSpaceSyncTest(t)
	before := seededItemSpaceSyncAssets(t, *repositories)
	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		currentItemSpaceSyncOpcode,
		[]byte{0xFF},
	); err != nil {
		t.Fatalf("handleGameCommand: %v", err)
	}
	assertItemSpaceSyncExactReply(t, connection, 0)
	if after := itemSpaceSyncAssets(t, *repositories); !reflect.DeepEqual(before, after) {
		t.Fatalf("handleGameCommand changed player data\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestItemSpaceSyncProductionHandleLegacyGamePacketLiteralFF(t *testing.T) {
	service, session, connection, repositories := newItemSpaceSyncTest(t)
	before := seededItemSpaceSyncAssets(t, *repositories)
	frame := itemSpaceSyncLegacyFrame(byte(dnfenum.GameCmdCommand), currentItemSpaceSyncOpcode, 23, []byte{0xFF})
	if err := service.handleLegacyGamePacket(session, frame); err != nil {
		t.Fatalf("handleLegacyGamePacket: %v", err)
	}
	assertItemSpaceSyncExactReply(t, connection, 0)
	if after := itemSpaceSyncAssets(t, *repositories); !reflect.DeepEqual(before, after) {
		t.Fatalf("handleLegacyGamePacket changed player data\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestItemSpaceSyncProductionDispatchDoesNotReplyOutsideOfficialCommand(t *testing.T) {
	for _, test := range []struct {
		name   string
		cmd    byte
		client string
	}{
		{name: "wrong classification", cmd: byte(dnfenum.GameCmdNotice), client: "official-24.9"},
		{name: "historical client", cmd: byte(dnfenum.GameCmdCommand), client: "legacy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, session, connection, _ := newItemSpaceSyncTest(t)
			service.options.currentClientVersion = test.client
			frame := itemSpaceSyncLegacyFrame(test.cmd, currentItemSpaceSyncOpcode, 24, []byte{0xFF})
			if err := service.handleLegacyGamePacket(session, frame); err != nil {
				t.Fatalf("handleLegacyGamePacket: %v", err)
			}
			if got := connection.write.Bytes(); len(got) != 0 {
				t.Fatalf("guarded production dispatch emitted op868: %x", got)
			}
		})
	}
}

func TestItemSpaceSyncLiteralFFEmitsExactSuccessWireAfterPriorOutput(t *testing.T) {
	service, session, connection, repositories := newItemSpaceSyncTest(t)
	session.upperSeq = 7
	prefix := []byte{0xde, 0xad, 0xbe, 0xef}
	if _, err := connection.write.Write(prefix); err != nil {
		t.Fatal(err)
	}
	before := seededItemSpaceSyncAssets(t, *repositories)
	if err := service.handleCurrentItemSpaceSync(session, []byte{0xFF}); err != nil {
		t.Fatalf("handle literal FF: %v", err)
	}
	written := connection.write.Bytes()
	if !bytes.Equal(written[:len(prefix)], prefix) {
		t.Fatalf("prior session output was reordered: %x", written)
	}
	want := expectedItemSpaceSyncPacket(t, 7)
	if got := written[len(prefix):]; !bytes.Equal(got, want) {
		t.Fatalf("exact success wire=%x want=%x", got, want)
	}
	if session.upperSeq != 8 {
		t.Fatalf("upper sequence=%d want 8", session.upperSeq)
	}
	if after := itemSpaceSyncAssets(t, *repositories); !reflect.DeepEqual(before, after) {
		t.Fatalf("success changed player data\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestItemSpaceSyncRepeatedRequestsAreIdempotentAndDoNotMutateAssets(t *testing.T) {
	service, session, connection, repositories := newItemSpaceSyncTest(t)
	before := seededItemSpaceSyncAssets(t, *repositories)
	for attempt := 0; attempt < 2; attempt++ {
		if err := service.handleCurrentItemSpaceSync(session, []byte{0xFF}); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	remaining := connection.write.Bytes()
	for sequence := uint16(0); sequence < 2; sequence++ {
		packet, rest := splitGameServerUpperPacket(t, remaining)
		if packet.Header.MsgID != currentItemSpaceSyncOpcode ||
			packet.Header.Classification != dnfproto.DefaultChannelClassification ||
			!bytes.Equal(packet.Body, []byte{1}) {
			t.Fatalf("attempt %d success packet=%+v", sequence, packet)
		}
		want := expectedItemSpaceSyncPacket(t, sequence)
		packetWireLen := len(remaining) - len(rest)
		if got := remaining[:packetWireLen]; !bytes.Equal(got, want) {
			t.Fatalf("attempt %d wire=%x want=%x", sequence, got, want)
		}
		remaining = rest
	}
	if len(remaining) != 0 {
		t.Fatalf("repeated request replayed containers or trailing bytes: %x", remaining)
	}
	if after := itemSpaceSyncAssets(t, *repositories); !reflect.DeepEqual(before, after) {
		t.Fatalf("repeated request changed player data\nbefore=%+v\nafter=%+v", before, after)
	}
}

type itemSpaceSyncMissingAccount struct{ dnfrepo.AccountRepository }

func (itemSpaceSyncMissingAccount) Load(context.Context, string) (dnfrepo.AccountRecord, bool, error) {
	return dnfrepo.AccountRecord{}, false, nil
}

type itemSpaceSyncMissingCharacter struct{ dnfrepo.CharacterRepository }

func (itemSpaceSyncMissingCharacter) Load(context.Context, string) (dnfrepo.CharacterRecord, bool, error) {
	return dnfrepo.CharacterRecord{}, false, nil
}

type itemSpaceSyncFailAccountLoad struct{ dnfrepo.AccountRepository }

func (itemSpaceSyncFailAccountLoad) Load(context.Context, string) (dnfrepo.AccountRecord, bool, error) {
	return dnfrepo.AccountRecord{}, false, errors.New("injected item-space account load failure")
}

type itemSpaceSyncFailCharacterLoad struct{ dnfrepo.CharacterRepository }

func (itemSpaceSyncFailCharacterLoad) Load(context.Context, string) (dnfrepo.CharacterRecord, bool, error) {
	return dnfrepo.CharacterRecord{}, false, errors.New("injected item-space character load failure")
}

type itemSpaceSyncForeignCharacter struct{ dnfrepo.CharacterRepository }

func (r itemSpaceSyncForeignCharacter) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	record, found, err := r.CharacterRepository.Load(ctx, id)
	record.AccountID = "dnf:foreign"
	return record, found, err
}

type itemSpaceSyncRebindCharacter struct {
	dnfrepo.CharacterRepository
	onLoad func()
}

func (r itemSpaceSyncRebindCharacter) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	if r.onLoad != nil {
		r.onLoad()
	}
	return r.CharacterRepository.Load(ctx, id)
}

func TestItemSpaceSyncRejectsEverythingWithoutVerifiedOwnership(t *testing.T) {
	for _, test := range []struct {
		name   string
		body   []byte
		change func(*Service, *gameSession, *dnfrepo.Group)
	}{
		{name: "nil body", body: nil},
		{name: "empty body", body: []byte{}},
		{name: "zero byte", body: []byte{0x00}},
		{name: "one byte", body: []byte{0x01}},
		{name: "FF plus extra", body: []byte{0xFF, 0x00}},
		{name: "two FF bytes", body: []byte{0xFF, 0xFF}},
		{name: "legacy client", body: []byte{0xFF}, change: func(s *Service, _ *gameSession, _ *dnfrepo.Group) {
			s.options.currentClientVersion = "legacy"
		}},
		{name: "login packet unseen", body: []byte{0xFF}, change: func(_ *Service, session *gameSession, _ *dnfrepo.Group) {
			session.loginPacketSeen = false
		}},
		{name: "token unauthenticated", body: []byte{0xFF}, change: func(_ *Service, session *gameSession, _ *dnfrepo.Group) {
			session.accountFromToken = false
		}},
		{name: "empty account", body: []byte{0xFF}, change: func(_ *Service, session *gameSession, _ *dnfrepo.Group) {
			session.accountID = " "
		}},
		{name: "unselected", body: []byte{0xFF}, change: func(s *Service, session *gameSession, _ *dnfrepo.Group) {
			session.selectedCharacterID = 0
			delete(s.gameSessions, itemSpaceSyncTestCharacterID)
		}},
		{name: "unbound selection", body: []byte{0xFF}, change: func(s *Service, _ *gameSession, _ *dnfrepo.Group) {
			delete(s.gameSessions, itemSpaceSyncTestCharacterID)
		}},
		{name: "foreign character", body: []byte{0xFF}, change: func(_ *Service, _ *gameSession, group *dnfrepo.Group) {
			group.Character = itemSpaceSyncForeignCharacter{group.Character}
		}},
		{name: "missing character", body: []byte{0xFF}, change: func(_ *Service, _ *gameSession, group *dnfrepo.Group) {
			group.Character = itemSpaceSyncMissingCharacter{group.Character}
		}},
		{name: "missing account", body: []byte{0xFF}, change: func(_ *Service, _ *gameSession, group *dnfrepo.Group) {
			group.Account = itemSpaceSyncMissingAccount{group.Account}
		}},
		{name: "account read failure", body: []byte{0xFF}, change: func(_ *Service, _ *gameSession, group *dnfrepo.Group) {
			group.Account = itemSpaceSyncFailAccountLoad{group.Account}
		}},
		{name: "character read failure", body: []byte{0xFF}, change: func(_ *Service, _ *gameSession, group *dnfrepo.Group) {
			group.Character = itemSpaceSyncFailCharacterLoad{group.Character}
		}},
		{name: "repository unavailable", body: []byte{0xFF}, change: func(s *Service, _ *gameSession, _ *dnfrepo.Group) {
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return dnfrepo.Group{}, false }
		}},
		{name: "binding changed during reads", body: []byte{0xFF}, change: func(s *Service, session *gameSession, group *dnfrepo.Group) {
			replacement := &gameSession{
				conn: &bufferConn{}, connID: "replacement", accountID: itemSpaceSyncTestAccountID,
				accountFromToken: true, loginPacketSeen: true,
				selectedCharacterID: itemSpaceSyncTestCharacterID, characterGeneration: 8,
			}
			group.Character = itemSpaceSyncRebindCharacter{
				CharacterRepository: group.Character,
				onLoad:              func() { s.gameSessions[itemSpaceSyncTestCharacterID] = replacement },
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, session, connection, group := newItemSpaceSyncTest(t)
			if test.change != nil {
				test.change(service, session, group)
			}
			if err := service.handleCurrentItemSpaceSync(session, test.body); err != nil {
				t.Fatalf("controlled rejection returned error: %v", err)
			}
			if got := connection.write.Bytes(); len(got) != 0 {
				t.Fatalf("rejected request emitted success: %x", got)
			}
		})
	}
}

type itemSpaceSyncFailOnceConn struct {
	bufferConn
	err    error
	failed bool
}

func (connection *itemSpaceSyncFailOnceConn) Write(data []byte) (int, error) {
	if !connection.failed {
		connection.failed = true
		return 0, connection.err
	}
	return connection.bufferConn.Write(data)
}

func TestItemSpaceSyncWriteFailureIsRetryableAndNeverMutatesAssets(t *testing.T) {
	service, session, _, repositories := newItemSpaceSyncTest(t)
	writeErr := errors.New("injected item-space ACK write failure")
	connection := &itemSpaceSyncFailOnceConn{err: writeErr}
	session.conn = connection
	before := seededItemSpaceSyncAssets(t, *repositories)
	if err := service.handleCurrentItemSpaceSync(session, []byte{0xFF}); !errors.Is(err, writeErr) {
		t.Fatalf("write failure=%v want=%v", err, writeErr)
	}
	if got := connection.write.Bytes(); len(got) != 0 {
		t.Fatalf("failed write left a success packet: %x", got)
	}
	if err := service.handleCurrentItemSpaceSync(session, []byte{0xFF}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	want := expectedItemSpaceSyncPacket(t, 1)
	if got := connection.write.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("retry wire=%x want=%x", got, want)
	}
	if after := itemSpaceSyncAssets(t, *repositories); !reflect.DeepEqual(before, after) {
		t.Fatalf("write retry changed player data\nbefore=%+v\nafter=%+v", before, after)
	}
}
