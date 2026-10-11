// character_delete_test.go 覆盖选角界面删除角色（class1/op6）的开关、解析、回包与落库。
//
// 现场 bug（_dist/rename-delete-diagnosis.md §3）：op6 解析偏移错 1 字节 + 失败路径不回包
// ⇒ 客户端「立即删除」后假死。本文件钉死修复后的可观测行为：
//   - 现场 17 字节体 02 00 0b000000 "test78_copy" 必须解析为 slot=2/name=test78_copy；
//   - 默认档必须回**恰好一个** game-upper/classification=1/msg_id=6 包，字节 = 厂商 ACK；
//   - DNFBRIDGE_CHARACTER_DELETE_ACK=legacy 必须回到今天的行为：零字节、零落库。
package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// gameDeleteCharacterLiveBody 是 2026-09-28 现场抓到的 op6 请求体（17 字节）：
// 02 00 | 0b000000 | "test78_copy"（packet_log.txt:61050 / dnf90.out:9441）。
func gameDeleteCharacterLiveBody() []byte {
	return append([]byte{0x02, 0x00, 0x0b, 0x00, 0x00, 0x00}, []byte("test78_copy")...)
}

// gameDeleteCharacterBody 按真实 wire 形态造一个删除请求体。
func gameDeleteCharacterBody(slot uint16, name string) []byte {
	body := []byte{byte(slot & 0xFF), byte((slot >> 8) & 0xFF), 0, 0, 0, 0}
	body[2] = byte(len(name) & 0xFF)
	body[3] = byte((len(name) >> 8) & 0xFF)
	return append(body, []byte(name)...)
}

// gameDeleteCharacterRoster 造一个与现场同形的账号（dnf:10001，slot 0..3）。
func gameDeleteCharacterRoster() *fakeCharacterStore {
	store := &fakeCharacterStore{records: make(map[string]dnfrepo.CharacterRecord), nextID: 17}
	for _, record := range []dnfrepo.CharacterRecord{
		{CharacterID: "1", AccountID: "dnf:10001", Slot: 0, Name: "test1", Job: "0", Level: 1},
		{CharacterID: "3", AccountID: "dnf:10001", Slot: 1, Name: "test2", Job: "0", Level: 1},
		{CharacterID: "15", AccountID: "dnf:10001", Slot: 2, Name: "test78_copy", Job: "0", Level: 1},
		{CharacterID: "16", AccountID: "dnf:10001", Slot: 3, Name: "test88", Job: "0", Level: 1},
	} {
		store.records[record.CharacterID] = record
	}
	return store
}

func gameDeleteCharacterGroup(store dnfrepo.CharacterRepository) dnfrepo.Group {
	group := testRepositoryGroup()
	if store != nil {
		group.Character = store
		if unit, ok := group.CharacterAssets.(*fakeCharacterAssetUnitOfWork); ok {
			unit.character = store
		}
	}
	return group
}

func newGameDeleteCharacterService(group dnfrepo.Group, withRepositories bool) (*Service, *gameSession, *bufferConn) {
	service := &Service{options: options{accountPrefix: "dnf:"}}
	if withRepositories {
		service.repositoryProvider = func() (dnfrepo.Group, bool) { return group, true }
	}
	conn := &bufferConn{}
	session := &gameSession{
		conn:      conn,
		channel:   channelcatalog.Channel{ID: 252, Type: 40, Name: "ch.252", Port: 10252},
		accountID: "dnf:10001",
	}
	return service, session, conn
}

// gameDeleteCharacterReply 取走"恰好一个"上行走包并返回它；多包/少包都直接失败。
func gameDeleteCharacterReply(t *testing.T, conn *bufferConn) dnfproto.ChannelPacket {
	t.Helper()
	if len(conn.write.Bytes()) == 0 {
		t.Fatal("no reply written: client would freeze waiting for the op6 ACK")
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if len(rest) != 0 {
		t.Fatalf("unexpected trailing bytes after op6 ack: %x", rest)
	}
	if packet.Header.Classification != dnfproto.DefaultChannelClassification ||
		packet.Header.MsgID != uint16(dnfenum.CmdPacketDeleteCharacter) {
		t.Fatalf("op6 ack header = %+v, want classification=%d msg_id=6",
			packet.Header, dnfproto.DefaultChannelClassification)
	}
	return packet
}

// TestCharacterDeleteAckSwitchDefaultsToFixed 钉死开关语义：
// 只有字面量 legacy 回到旧行为，空/未知值一律修复档。
func TestCharacterDeleteAckSwitchDefaultsToFixed(t *testing.T) {
	cases := map[string]bool{
		"":            false,
		"   ":         false,
		"fix":         false,
		"FIX":         false,
		"fixed":       false,
		"1":           false,
		"on":          false,
		"legacyy":     false,
		"legacy_fix":  false,
		"legacy":      true,
		"legacy ":     true,
		"  LEGACY  ":  true,
		"LeGaCy":      true,
		"0":           false,
		"unknown-any": false,
	}
	for value, wantLegacy := range cases {
		if got := characterDeleteAckLegacyFromEnv(value); got != wantLegacy {
			t.Fatalf("characterDeleteAckLegacyFromEnv(%q) = %v, want %v", value, got, wantLegacy)
		}
	}
	t.Setenv(characterDeleteAckEnv, "")
	if characterDeleteAckLegacy() {
		t.Fatalf("unset %s must default to the fixed path", characterDeleteAckEnv)
	}
	t.Setenv(characterDeleteAckEnv, characterDeleteAckLegacyValue)
	if !characterDeleteAckLegacy() {
		t.Fatalf("%s=%s must select the legacy path", characterDeleteAckEnv, characterDeleteAckLegacyValue)
	}
}

// TestHandleGameDeleteCharacterSendsVendorSuccessAckAndSoftDeletes 是本次修复的主用例：
// 现场体 → 恰好一个 game-upper ACK（厂商成功形态）+ 与 GM 面板同口径的软删。
func TestHandleGameDeleteCharacterSendsVendorSuccessAckAndSoftDeletes(t *testing.T) {
	t.Setenv(characterDeleteAckEnv, "")
	store := gameDeleteCharacterRoster()
	group := gameDeleteCharacterGroup(store)
	service, session, conn := newGameDeleteCharacterService(group, true)

	body := gameDeleteCharacterLiveBody()
	if len(body) != 17 {
		t.Fatalf("live fixture = %d bytes, want 17", len(body))
	}
	if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketDeleteCharacter), body); err != nil {
		t.Fatalf("handle op6: %v", err)
	}

	packet := gameDeleteCharacterReply(t, conn)
	// 厂商 CharacterSelectHandler.cs:990-1003：[0x01][flag=0x00][slot:u16 LE]。
	if want := []byte{0x01, 0x00, 0x02, 0x00}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("op6 success ack body = %x, want %x", packet.Body, want)
	}

	// 落库：与 GM 软删同口径（delete_flag=1 + 负墓碑槽）。
	record, ok := store.records["15"]
	if !ok {
		t.Fatal("character 15 row vanished: soft delete must keep the row")
	}
	if flag := fakeCharacterDeleteFlag(record); flag != characterDeleteDeletedFlag {
		t.Fatalf("delete_flag = %d, want %d", flag, characterDeleteDeletedFlag)
	}
	if record.Slot != -15 {
		t.Fatalf("tombstone slot = %d, want -15", record.Slot)
	}
	if record.Name != "test78_copy" || record.AccountID != "dnf:10001" {
		t.Fatalf("soft delete corrupted the row: %+v", record)
	}
	// 读面（与选角/角色列表同一方法）必须真的看不到它了。
	if visible, err := service.gameCharacterStillVisible(context.Background(), group, "dnf:10001", "15"); err != nil || visible {
		t.Fatalf("character 15 still visible after delete: visible=%v err=%v", visible, err)
	}
	// 同一账号其它角色不受影响。
	for _, id := range []string{"1", "3", "16"} {
		if flag := fakeCharacterDeleteFlag(store.records[id]); flag != 0 {
			t.Fatalf("character %s was affected by the delete: delete_flag=%d", id, flag)
		}
	}
	// 厂商 :1005-1006：删除后不补发角色列表 ⇒ 上面"恰好一个包"的断言就是这条口径。
}

// TestHandleGameDeleteCharacterBadBodyAcks0x02 坏请求（短包 / nameLen 非法）⇒ 单字节 {0x02}。
func TestHandleGameDeleteCharacterBadBodyAcks0x02(t *testing.T) {
	t.Setenv(characterDeleteAckEnv, "")
	bodies := map[string][]byte{
		"short body":          {0x02, 0x00, 0x0b, 0x00, 0x00},
		"zero name length":    {0x02, 0x00, 0x00, 0x00, 0x00, 0x00},
		"name length over 30": append([]byte{0x02, 0x00, 0x1f, 0x00, 0x00, 0x00}, make([]byte, 31)...),
		"name past body end":  {0x02, 0x00, 0x09, 0x00, 0x00, 0x00, 'a', 'b', 'c'},
	}
	for name, body := range bodies {
		store := gameDeleteCharacterRoster()
		service, session, conn := newGameDeleteCharacterService(gameDeleteCharacterGroup(store), true)
		if err := service.handleGameDeleteCharacter(session, body); err != nil {
			t.Fatalf("%s: handle: %v", name, err)
		}
		packet := gameDeleteCharacterReply(t, conn)
		if want := []byte{characterDeleteAckBadRequest}; !bytes.Equal(packet.Body, want) {
			t.Fatalf("%s: ack = %x, want %x", name, packet.Body, want)
		}
		if flag := fakeCharacterDeleteFlag(store.records["15"]); flag != 0 {
			t.Fatalf("%s: bad request must not write, delete_flag=%d", name, flag)
		}
	}
}

// TestHandleGameDeleteCharacterSlotOutOfRangeAcks0x02 空角色表 ⇒ 越界 {0x02}（厂商 :951-956）。
func TestHandleGameDeleteCharacterSlotOutOfRangeAcks0x02(t *testing.T) {
	t.Setenv(characterDeleteAckEnv, "")
	store := &fakeCharacterStore{records: map[string]dnfrepo.CharacterRecord{}, nextID: 1}
	service, session, conn := newGameDeleteCharacterService(gameDeleteCharacterGroup(store), true)

	if err := service.handleGameDeleteCharacter(session, gameDeleteCharacterLiveBody()); err != nil {
		t.Fatalf("handle: %v", err)
	}
	packet := gameDeleteCharacterReply(t, conn)
	if want := []byte{characterDeleteAckBadRequest}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("ack = %x, want %x", packet.Body, want)
	}
	if len(store.records) != 0 {
		t.Fatalf("out-of-range delete wrote rows: %+v", store.records)
	}
}

// TestHandleGameDeleteCharacterNameMismatchAcks0x15 名字与槽位不符 ⇒ {0x15}，且绝不误删。
func TestHandleGameDeleteCharacterNameMismatchAcks0x15(t *testing.T) {
	t.Setenv(characterDeleteAckEnv, "")
	store := gameDeleteCharacterRoster()
	service, session, conn := newGameDeleteCharacterService(gameDeleteCharacterGroup(store), true)

	if err := service.handleGameDeleteCharacter(session, gameDeleteCharacterBody(2, "test88")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	packet := gameDeleteCharacterReply(t, conn)
	if want := []byte{characterDeleteAckNameMismatch}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("ack = %x, want %x", packet.Body, want)
	}
	if flag := fakeCharacterDeleteFlag(store.records["15"]); flag != 0 {
		t.Fatalf("name mismatch must not delete, delete_flag=%d", flag)
	}
	if flag := fakeCharacterDeleteFlag(store.records["16"]); flag != 0 {
		t.Fatalf("name mismatch must not delete the named character either, delete_flag=%d", flag)
	}
}

// TestHandleGameDeleteCharacterRepositoryMissingAcks0x28 仓储没接上 ⇒ {0x28}（不是静默）。
func TestHandleGameDeleteCharacterRepositoryMissingAcks0x28(t *testing.T) {
	t.Setenv(characterDeleteAckEnv, "")
	service, session, conn := newGameDeleteCharacterService(dnfrepo.Group{}, false)

	if err := service.handleGameDeleteCharacter(session, gameDeleteCharacterLiveBody()); err != nil {
		t.Fatalf("handle: %v", err)
	}
	packet := gameDeleteCharacterReply(t, conn)
	if want := []byte{characterDeleteAckBlocked}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("ack = %x, want %x", packet.Body, want)
	}
}

// TestHandleGameDeleteCharacterWriteFailureAcks0x28 写库失败 ⇒ {0x28}，不能谎报成功。
func TestHandleGameDeleteCharacterWriteFailureAcks0x28(t *testing.T) {
	t.Setenv(characterDeleteAckEnv, "")
	store := &failingSaveCharacterStore{
		fakeCharacterStore: gameDeleteCharacterRoster(),
		saveErr:            errors.New("injected save failure"),
	}
	service, session, conn := newGameDeleteCharacterService(gameDeleteCharacterGroup(store), true)

	if err := service.handleGameDeleteCharacter(session, gameDeleteCharacterLiveBody()); err != nil {
		t.Fatalf("handle: %v", err)
	}
	packet := gameDeleteCharacterReply(t, conn)
	if want := []byte{characterDeleteAckBlocked}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("ack = %x, want %x", packet.Body, want)
	}
}

// TestHandleGameDeleteCharacterVerifyFailureAcks0x28 写成功但读面仍可见（复核失败）⇒ {0x28}。
// 与 GM 软删同一口径（api_character_delete.go:271-282）：读不回来才算删掉。
func TestHandleGameDeleteCharacterVerifyFailureAcks0x28(t *testing.T) {
	t.Setenv(characterDeleteAckEnv, "")
	store := &stubbornVisibleCharacterStore{
		fakeCharacterStore: gameDeleteCharacterRoster(),
	}
	// 快照 = 软删前的完整可见角色表：首次寻址正常，写完复核仍能"看到"它。
	for _, record := range store.records {
		store.snapshot = append(store.snapshot, record)
	}
	service, session, conn := newGameDeleteCharacterService(gameDeleteCharacterGroup(store), true)

	if err := service.handleGameDeleteCharacter(session, gameDeleteCharacterLiveBody()); err != nil {
		t.Fatalf("handle: %v", err)
	}
	packet := gameDeleteCharacterReply(t, conn)
	if want := []byte{characterDeleteAckBlocked}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("ack = %x, want %x", packet.Body, want)
	}
}

// TestHandleGameDeleteCharacterLegacySwitchIsSilentAndWritesNothing 回退档：
// 与今天逐字节一致 —— 走原 aligned fallback、零回包、零落库。
func TestHandleGameDeleteCharacterLegacySwitchIsSilentAndWritesNothing(t *testing.T) {
	t.Setenv(characterDeleteAckEnv, characterDeleteAckLegacyValue)
	store := gameDeleteCharacterRoster()
	group := gameDeleteCharacterGroup(store)
	service, session, conn := newGameDeleteCharacterService(group, true)

	if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketDeleteCharacter), gameDeleteCharacterLiveBody()); err != nil {
		t.Fatalf("legacy handle: %v", err)
	}
	if got := conn.write.Bytes(); len(got) != 0 {
		t.Fatalf("legacy mode wrote %d bytes (%x), want today's silent drop", len(got), got)
	}
	record := store.records["15"]
	if flag := fakeCharacterDeleteFlag(record); flag != 0 {
		t.Fatalf("legacy mode must not write: delete_flag=%d", flag)
	}
	if record.Slot != 2 {
		t.Fatalf("legacy mode moved the slot to %d, want 2", record.Slot)
	}
	if visible, err := service.gameCharacterStillVisible(context.Background(), group, "dnf:10001", "15"); err != nil || !visible {
		t.Fatalf("legacy mode must leave the character visible: visible=%v err=%v", visible, err)
	}
}

// TestHandleGameDeleteCharacterAckShapeFollowsVendor 固定成功 ACK 的字节形态，
// 特别是厂商明令禁止的 [00][charId] 形态不得出现。
func TestHandleGameDeleteCharacterAckShapeFollowsVendor(t *testing.T) {
	cases := map[uint16][]byte{
		0:     {0x01, 0x00, 0x00, 0x00},
		2:     {0x01, 0x00, 0x02, 0x00},
		0x123: {0x01, 0x00, 0x23, 0x01},
	}
	for slot, want := range cases {
		got := characterDeleteSuccessAckBody(slot)
		if !bytes.Equal(got, want) {
			t.Fatalf("slot %d ack = %x, want %x", slot, got, want)
		}
		if got[0] == 0x00 {
			t.Fatalf("slot %d ack starts with 0x00: vendor says the client treats it as Error and crashes", slot)
		}
	}
}

// failingSaveCharacterStore 只覆盖 Save：注入写库失败（SaveCharacterFields 会退化到 Save）。
type failingSaveCharacterStore struct {
	*fakeCharacterStore
	saveErr error
}

func (s *failingSaveCharacterStore) Save(ctx context.Context, record dnfrepo.CharacterRecord) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	return s.fakeCharacterStore.Save(ctx, record)
}

// stubbornVisibleCharacterStore 模拟"写成功但读面仍可见"：ListByAccount 恒返回删除前快照。
type stubbornVisibleCharacterStore struct {
	*fakeCharacterStore
	snapshot []dnfrepo.CharacterRecord
}

func (s *stubbornVisibleCharacterStore) ListByAccount(context.Context, string, int) ([]dnfrepo.CharacterRecord, error) {
	out := make([]dnfrepo.CharacterRecord, len(s.snapshot))
	copy(out, s.snapshot)
	return out, nil
}
