package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// Live framed bodies (HK packet_log 2026-09-24/25). The op197 account blob is
// 178 bytes on the current client; the reference server truncates its own
// 150-byte copy, so the round trip is asserted against the client's own length.
func testGameOptionBlob(length int, fill byte) []byte {
	blob := make([]byte, length)
	for index := range blob {
		blob[index] = fill
	}
	return blob
}

func testLengthPrefixedBlob(blob []byte) []byte {
	body := make([]byte, 4+len(blob))
	binary.LittleEndian.PutUint32(body[0:4], uint32(len(blob)))
	copy(body[4:], blob)
	return body
}

func testQuickchatBody(bank byte, blob []byte) []byte {
	body := make([]byte, 5+len(blob))
	body[0] = bank
	binary.LittleEndian.PutUint32(body[1:5], uint32(len(blob)))
	copy(body[5:], blob)
	return body
}

func testGameOptionFixture(t *testing.T) (*Service, *gameSession, *bufferConn, dnfrepo.Group) {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Job:         "0",
		Level:       90,
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	service, session, connection := newGameplayDomainBridgeFixture(repos)
	// The client only pushes its option template during the entry bootstrap;
	// mark this fixture as "player is in the world" so the entry-window guard
	// does not defer the save (see gameOptionSaveWindowOpen).
	session.initialTownRouteStage = currentInitialTownRoutePlayerStateSent
	session.initialTownRouteCharacterID = session.selectedCharacterID
	return service, session, connection, repos
}

func drainUpperPackets(t *testing.T, connection *bufferConn) []struct {
	msgID uint16
	body  []byte
} {
	t.Helper()
	raw := append([]byte(nil), connection.write.Bytes()...)
	connection.write.Reset()
	out := make([]struct {
		msgID uint16
		body  []byte
	}, 0, 4)
	for len(raw) > 0 {
		packet, rest := splitGameServerUpperPacket(t, raw)
		out = append(out, struct {
			msgID uint16
			body  []byte
		}{msgID: packet.Header.MsgID, body: packet.Body})
		raw = rest
	}
	return out
}

// Reproduces the operator report "游戏设置每次登陆游戏重置": the client pushes
// op197 on every entry and expects the account blob back as class0/0x00AD.
func TestGameOptionBridgePersistsOp197AndReplaysClass0Notice(t *testing.T) {
	service, session, connection, repos := testGameOptionFixture(t)
	ctx := context.Background()
	blob := testGameOptionBlob(178, 0x11)
	blob[0] = 0xB2

	handled, err := service.handleGameOptionCommand(session, currentSaveGameOption1Opcode, testLengthPrefixedBlob(blob))
	if err != nil || !handled {
		t.Fatalf("handleGameOptionCommand handled=%t err=%v", handled, err)
	}
	if len(connection.write.Bytes()) != 0 {
		t.Fatalf("op197 answered with %d bytes; the current client sends no request id", connection.write.Len())
	}
	record, found, err := repos.Settings.Load(ctx, accountGameOptionScope("account-1"))
	if err != nil || !found {
		t.Fatalf("settings found=%t err=%v", found, err)
	}
	if record.Values["main_game_option_hex"] != hex.EncodeToString(blob) {
		t.Fatalf("stored blob mismatch len=%d", len(record.Values["main_game_option_hex"])/2)
	}
	if record.Values["source"] != "current_exe_op197" {
		t.Fatalf("source = %q", record.Values["source"])
	}

	// 2 quickchat banks make the 0x00AD body's three length prefixes observable.
	if _, err := service.handleGameOptionCommand(session, currentSaveQuickchatOpcode, testQuickchatBody(0, []byte("bank-zero"))); err != nil {
		t.Fatal(err)
	}
	if _, err := service.handleGameOptionCommand(session, currentSaveQuickchatOpcode, testQuickchatBody(1, []byte("bank-one"))); err != nil {
		t.Fatal(err)
	}
	connection.write.Reset()
	if err := service.sendAccountGameOptionNotices(session, "test_entry"); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, connection)
	if len(packets) != 1 {
		t.Fatalf("notices = %d, want only 0x00AD (no saved hotkey slots)", len(packets))
	}
	if packets[0].msgID != currentAccountGameOptionNoticeMsgID {
		t.Fatalf("msg_id = %#x, want %#x", packets[0].msgID, currentAccountGameOptionNoticeMsgID)
	}
	want := buildAccountGameOptionNoticeBody(blob, []byte("bank-zero"), []byte("bank-one"))
	if string(packets[0].body) != string(want) {
		t.Fatalf("0x00AD body = %x want %x", packets[0].body, want)
	}
}

// The reference contract is "no saved settings => send nothing, the client keeps
// its local defaults", so an account that never opened the option window must
// keep byte-identical traffic.
func TestGameOptionBridgeSkipsNoticeWithoutSavedSettings(t *testing.T) {
	service, session, connection, _ := testGameOptionFixture(t)
	if err := service.sendAccountGameOptionNotices(session, "test_entry"); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("unsaved account produced %d bytes", connection.write.Len())
	}
}

func TestGameOptionNoticeDisabledByEnv(t *testing.T) {
	service, session, connection, repos := testGameOptionFixture(t)
	ctx := context.Background()
	if err := service.saveAccountGameOptionValues(session, "account-1", map[string]string{
		"main_game_option_hex": hex.EncodeToString(testGameOptionBlob(8, 0x22)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repos.Settings.Load(ctx, accountGameOptionScope("account-1")); err != nil || !found {
		t.Fatalf("seed found=%t err=%v", found, err)
	}
	t.Setenv(currentGameOptionNoticeEnv, "0")
	if err := service.sendAccountGameOptionNotices(session, "test_entry"); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("disabled replay still produced %d bytes", connection.write.Len())
	}
}

// op198 carries the keyboard/hotkey blob. Only its first slot pair is
// account-scoped (86JP AccountSettings.ExtractAccountScopedHotkeySlots); the
// full blob belongs to the character.
func TestGameOptionBridgeOp198StoresAccountScopedPrefixAndCharacterBlob(t *testing.T) {
	service, session, _, repos := testGameOptionFixture(t)
	ctx := context.Background()
	blob := []byte{0x4A, 0xF7, 'I', '0', '1', '\t', '3', '\n'}
	if _, err := service.handleGameOptionCommand(session, currentSaveGameOption2Opcode, testLengthPrefixedBlob(blob)); err != nil {
		t.Fatal(err)
	}
	account, found, err := repos.Settings.Load(ctx, accountGameOptionScope("account-1"))
	if err != nil || !found {
		t.Fatalf("account settings found=%t err=%v", found, err)
	}
	if account.Values["hotkey_slots_hex"] != hex.EncodeToString(blob[:2]) {
		t.Fatalf("account scoped slots = %q", account.Values["hotkey_slots_hex"])
	}
	character, found, err := repos.Settings.Load(ctx, characterHotkeyConfigScope("19"))
	if err != nil || !found {
		t.Fatalf("character hotkey config found=%t err=%v", found, err)
	}
	if character.Values["hotkey_config_hex"] != hex.EncodeToString(blob) {
		t.Fatalf("character hotkey config = %q", character.Values["hotkey_config_hex"])
	}
}

// The hotkey notice must also be replayable from the default layout seeded at
// creation, otherwise a character switch inherits the previous key table.
func TestGameOptionBridgeReplaysHotkeyNoticeFromSeededCharacterLayout(t *testing.T) {
	// 2026-09-27：0x01C7（455）热键公告默认被"厂商从不发送"名单挡掉（现场实测它会
	// 让客户端弹出一块公告/任务书面板并停住）。本用例验证的是**该功能自身的回放
	// 逻辑**，因此显式关掉生产阻断名单后再断言。
	t.Setenv(vendorAbsentSendBlockEnvKey, "none")
	t.Setenv(currentHotkeySeededLayoutEnv, "on")
	resetVendorAbsentBlockCache()
	defer resetVendorAbsentBlockCache()
	service, session, connection, repos := testGameOptionFixture(t)
	ctx := context.Background()
	slots := []byte{0x02, 0x00, 0x00, 0x00, 0x03, 0x00}
	if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{
		Scope: newCharacterHotkeySettingsScope("19"),
		Values: map[string]string{
			"key_type":  "1",
			"slots_hex": hex.EncodeToString(slots),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.saveAccountGameOptionValues(session, "account-1", map[string]string{
		"main_game_option_hex": hex.EncodeToString(testGameOptionBlob(6, 0x33)),
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.sendAccountGameOptionNotices(session, "test_entry"); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, connection)
	if len(packets) != 2 {
		t.Fatalf("notices = %d, want 0x00AD + 0x01C7", len(packets))
	}
	hotkey := packets[1]
	if hotkey.msgID != currentAccountHotkeyNoticeMsgID {
		t.Fatalf("hotkey msg_id = %#x", hotkey.msgID)
	}
	want := buildAccountHotkeyNoticeBody(1, slots)
	if string(hotkey.body) != string(want) {
		t.Fatalf("0x01C7 body = %x want %x", hotkey.body, want)
	}
}

func TestGameOptionBridgeRejectsMalformedBlobWithoutWriting(t *testing.T) {
	service, session, _, repos := testGameOptionFixture(t)
	ctx := context.Background()
	// Declared length exceeds the frame: the reference handler returns early.
	body := testLengthPrefixedBlob(testGameOptionBlob(4, 0x44))
	binary.LittleEndian.PutUint32(body[0:4], 64)
	if handled, err := service.handleGameOptionCommand(session, currentSaveGameOption1Opcode, body); err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if _, found, err := repos.Settings.Load(ctx, accountGameOptionScope("account-1")); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("malformed op197 still created a settings row")
	}
	// Same for op368's bank byte.
	if _, err := service.handleGameOptionCommand(session, currentSaveQuickchatOpcode, testQuickchatBody(2, []byte("x"))); err != nil {
		t.Fatal(err)
	}
	if record, found, err := repos.Settings.Load(ctx, accountGameOptionScope("account-1")); err != nil {
		t.Fatal(err)
	} else if found && strings.Contains(record.Values["quickchat_bank0_hex"]+record.Values["quickchat_bank1_hex"], "78") {
		t.Fatalf("out-of-range bank persisted: %+v", record.Values)
	}
}

func TestGameOptionBridgeOp447StoresPerCharacterOption(t *testing.T) {
	service, session, _, repos := testGameOptionFixture(t)
	ctx := context.Background()
	blob := testGameOptionBlob(512, 0xFF)
	blob[0], blob[1] = 0x01, 0x00
	if _, err := service.handleGameOptionCommand(session, currentSaveCharacterOptionOpcode, testLengthPrefixedBlob(blob)); err != nil {
		t.Fatal(err)
	}
	record, found, err := repos.Settings.Load(ctx, characterOptionScope("19"))
	if err != nil || !found {
		t.Fatalf("character option found=%t err=%v", found, err)
	}
	if record.Values["character_option_hex"] != hex.EncodeToString(blob) {
		t.Fatalf("character option blob mismatch len=%d", len(record.Values["character_option_hex"])/2)
	}
}

func TestGameOptionBridgeIgnoresUnrelatedOpcodes(t *testing.T) {
	service, session, _, _ := testGameOptionFixture(t)
	for _, typ := range []uint16{1, 4, 19, 28, 29, 331, 491} {
		handled, err := service.handleGameOptionCommand(session, typ, nil)
		if err != nil || handled {
			t.Fatalf("type=%d handled=%t err=%v", typ, handled, err)
		}
	}
}

// The client's entry-time op197 is its factory template, not a player save.
// Persisting it would erase the account's saved settings on the next login, so
// the entry window defers it and says so in the log.
func TestGameOptionBridgeDefersEntryTemplateSave(t *testing.T) {
	service, session, connection, repos := testGameOptionFixture(t)
	ctx := context.Background()
	session.initialTownRouteStage = currentInitialTownRouteIdle
	session.townSceneReadyCharacterID = 0
	blob := testGameOptionBlob(178, 0x55)
	if handled, err := service.handleGameOptionCommand(session, currentSaveGameOption1Opcode, testLengthPrefixedBlob(blob)); err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if _, found, err := repos.Settings.Load(ctx, accountGameOptionScope("account-1")); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("entry template was persisted despite the entry-window guard")
	}
	if connection.write.Len() != 0 {
		t.Fatalf("deferred save answered with %d bytes", connection.write.Len())
	}

	// op447 shares the guard (live: header_seq=29 during entry).
	option := testGameOptionBlob(512, 0xFF)
	if _, err := service.handleGameOptionCommand(session, currentSaveCharacterOptionOpcode, testLengthPrefixedBlob(option)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repos.Settings.Load(ctx, characterOptionScope("19")); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("entry-time op447 was persisted despite the entry-window guard")
	}

	// The guard is switchable for live A/B.
	t.Setenv(currentGameOptionEntryGuardEnv, "0")
	if _, err := service.handleGameOptionCommand(session, currentSaveGameOption1Opcode, testLengthPrefixedBlob(blob)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repos.Settings.Load(ctx, accountGameOptionScope("account-1")); err != nil || !found {
		t.Fatalf("guard-off save found=%t err=%v", found, err)
	}
}
