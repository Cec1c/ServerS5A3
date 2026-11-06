package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func Test90CNHotkeySaved673ByteConfigurationReplaysVerbatim(t *testing.T) {
	t.Setenv(currentGameOptionNoticeEnv, "1")
	s, session, conn, repos := testGameOptionFixture(t)
	s.options.currentClientVersion = "90cn"
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Job: "0", Level: 1}); err != nil {
		t.Fatal(err)
	}
	session.selectedCharacterID, session.characterGeneration = 19, 1
	config := bytes.Repeat([]byte{0x49}, 673)
	request := make([]byte, 4+len(config))
	binary.LittleEndian.PutUint32(request, uint32(len(config)))
	copy(request[4:], config)
	if err := s.handleSaveGameOption2(session, request); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	if err := s.sendSelectedCharacterHotkeyNotice(session, "test_relogin"); err != nil {
		t.Fatal(err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes())
	if len(packets) != 1 || packets[0].Header.MsgID != 454 || !bytes.Equal(packets[0].Body, buildAccountHotkeyNoticeBody(0, config)) {
		t.Fatal("full saved keyboard configuration not replayed verbatim to native NOTI454")
	}
}

func Test90CNHotkeyMissingOrWrongOwnerDoesNotReplayAccountPrefix(t *testing.T) {
	t.Setenv(currentGameOptionNoticeEnv, "1")
	s, session, conn, repos := testGameOptionFixture(t)
	s.options.currentClientVersion = "90cn"
	session.selectedCharacterID, session.characterGeneration = 19, 1
	for _, owner := range []string{"account-1", "other-account"} {
		if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "19", AccountID: owner, Job: "0", Level: 1}); err != nil {
			t.Fatal(err)
		}
		conn.write.Reset()
		if err := s.sendAccountHotkeyNotice(session, "test_missing_or_wrong_owner", "account-1", map[string]string{"hotkey_slots_hex": "4930"}); err != nil {
			t.Fatal(err)
		}
		if conn.write.Len() != 0 {
			t.Fatal("missing configuration or wrong owner replaced native keys with an account prefix")
		}
	}
}

func Test90CNSelectedHotkeyReplayPreservesSavedTextAcrossCharacterGenerations(t *testing.T) {
	t.Setenv(currentGameOptionNoticeEnv, "1")
	service, session, conn, repos := testGameOptionFixture(t)
	service.options.currentClientVersion = "90cn"
	ctx := context.Background()
	for id, job := range map[string]string{"19": "10", "20": "0"} {
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: "account-1", Job: job, Level: 1}); err != nil {
			t.Fatal(err)
		}
		text := []byte("I01\t90\nI02\t88\n")
		if id == "20" {
			text = []byte("I01\t38\nI02\t37\n")
		}
		if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{Scope: characterHotkeyConfigScope(id), Values: map[string]string{"hotkey_config_hex": hex.EncodeToString(text)}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		id         uint16
		generation uint64
		count      int
		keyType    byte
		text       string
	}{
		{19, 1, 1, 1, "I01\t90\nI02\t88\n"},
		{19, 1, 0, 1, ""},
		{20, 2, 1, 0, "I01\t38\nI02\t37\n"},
		{19, 3, 1, 1, "I01\t90\nI02\t88\n"},
	} {
		session.selectedCharacterID, session.characterGeneration = tc.id, tc.generation
		conn.write.Reset()
		if err := service.sendSelectedCharacterHotkeyNotice(session, "test_reselection"); err != nil {
			t.Fatal(err)
		}
		packets := splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes())
		if len(packets) != tc.count {
			t.Fatalf("id%d gen%d packets=%d want%d", tc.id, tc.generation, len(packets), tc.count)
		}
		if tc.count != 0 && (packets[0].Header.MsgID != 454 || !bytes.Equal(packets[0].Body, buildAccountHotkeyNoticeBody(tc.keyType, []byte(tc.text)))) {
			t.Fatalf("id%d keys changed: %+v", tc.id, packets)
		}
	}
}

func Test90CNNormalRoleContaminatedCreatorWASDNotReplayed(t *testing.T) {
	t.Setenv(currentGameOptionNoticeEnv, "1")
	s, session, conn, repos := testGameOptionFixture(t)
	s.options.currentClientVersion = "90cn"
	ctx := context.Background()

	// Normal Slayer character
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "account-1", Job: "0", Level: 1}); err != nil {
		t.Fatal(err)
	}
	// Creator character
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "2", AccountID: "account-1", Job: "10", Level: 1}); err != nil {
		t.Fatal(err)
	}

	wasdConfig := []byte("I01\t77\nI02\t55\nI03\t73\nI04\t58\nI05\t64\nI06\t65\n")
	for _, id := range []string{"1", "2"} {
		if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{
			Scope:  characterHotkeyConfigScope(id),
			Values: map[string]string{"hotkey_config_hex": hex.EncodeToString(wasdConfig)},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Normal Slayer with WASD should NOT replay 454 (drop contaminated Creator layout)
	session.selectedCharacterID, session.characterGeneration = 1, 1
	conn.write.Reset()
	if err := s.sendSelectedCharacterHotkeyNotice(session, "test_normal_contaminated"); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatal("normal character replayed contaminated Creator WASD hotkeys to 90cn client")
	}

	// 2. Creator with WASD should replay 454 with keyType=1
	session.selectedCharacterID, session.characterGeneration = 2, 2
	conn.write.Reset()
	if err := s.sendSelectedCharacterHotkeyNotice(session, "test_creator_valid"); err != nil {
		t.Fatal(err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes())
	if len(packets) != 1 || packets[0].Header.MsgID != 454 || !bytes.Equal(packets[0].Body, buildAccountHotkeyNoticeBody(1, wasdConfig)) {
		t.Fatalf("creator did not receive valid hotkeys: %+v", packets)
	}
}

func Test90CNReselectionSameCharacterDoesNotReplay454(t *testing.T) {
	t.Setenv(currentGameOptionNoticeEnv, "1")
	s, session, conn, repos := testGameOptionFixture(t)
	s.options.currentClientVersion = "90cn"
	ctx := context.Background()

	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "account-1", Job: "0", Level: 1}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "2", AccountID: "account-1", Job: "0", Level: 1}); err != nil {
		t.Fatal(err)
	}

	validKeys1 := []byte("I01\t2\nI02\t0\nI03\t3\nI04\t1\nI06\t65\n")
	validKeys2 := []byte("I01\t2\nI02\t0\nI03\t3\nI04\t1\nI06\t66\n")
	for id, text := range map[string][]byte{"1": validKeys1, "2": validKeys2} {
		if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{
			Scope:  characterHotkeyConfigScope(id),
			Values: map[string]string{"hotkey_config_hex": hex.EncodeToString(text)},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Initial select char 1
	session.selectedCharacterID, session.characterGeneration = 1, 1
	conn.write.Reset()
	if err := s.sendSelectedCharacterHotkeyNotice(session, "test_initial"); err != nil {
		t.Fatal(err)
	}
	if len(splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes())) != 1 {
		t.Fatal("initial selection did not send 454")
	}

	// Re-select same character after op7 (generation advances to 2)
	session.characterGeneration = 2
	conn.write.Reset()
	if err := s.sendSelectedCharacterHotkeyNotice(session, "test_reselect_same"); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatal("reselecting same character sent redundant 454 and wiped in-memory player hotkeys")
	}

	// Switch to different character (char 2) -> sends char 2 hotkeys
	session.selectedCharacterID, session.characterGeneration = 2, 3
	conn.write.Reset()
	if err := s.sendSelectedCharacterHotkeyNotice(session, "test_switch_char2"); err != nil {
		t.Fatal(err)
	}
	p2 := splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes())
	if len(p2) != 1 || !bytes.Equal(p2[0].Body, buildAccountHotkeyNoticeBody(0, validKeys2)) {
		t.Fatal("switching character did not restore target character hotkeys")
	}

	// Switch back to char 1 -> sends char 1 hotkeys
	session.selectedCharacterID, session.characterGeneration = 1, 4
	conn.write.Reset()
	if err := s.sendSelectedCharacterHotkeyNotice(session, "test_switch_back_char1"); err != nil {
		t.Fatal(err)
	}
	p1 := splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes())
	if len(p1) != 1 || !bytes.Equal(p1[0].Body, buildAccountHotkeyNoticeBody(0, validKeys1)) {
		t.Fatal("switching back to character 1 did not restore character 1 hotkeys")
	}
}

func Test90CNSaveGameOption2PreservesPlayerWASDOnNormalJob(t *testing.T) {
	t.Setenv(currentGameOptionNoticeEnv, "1")
	s, session, _, repos := testGameOptionFixture(t)
	s.options.currentClientVersion = "90cn"
	ctx := context.Background()

	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "account-1", Job: "0", Level: 1}); err != nil {
		t.Fatal(err)
	}

	wasdConfig := []byte("I01\t77\nI02\t55\nI03\t73\nI04\t58\n")
	requestWASD := make([]byte, 4+len(wasdConfig))
	binary.LittleEndian.PutUint32(requestWASD, uint32(len(wasdConfig)))
	copy(requestWASD[4:], wasdConfig)

	session.selectedCharacterID = 1
	if err := s.handleSaveGameOption2(session, requestWASD); err != nil {
		t.Fatal(err)
	}

	// Explicit op198 saves are authoritative even if their movement keys
	// match Creator defaults; a four-line prefix is not provenance.
	saved, found, err := repos.Settings.Load(ctx, characterHotkeyConfigScope("1"))
	if err != nil {
		t.Fatal(err)
	}
	if !found || saved.Values["hotkey_config_hex"] != hex.EncodeToString(wasdConfig) || saved.Values["source"] != "current_exe_op198" {
		t.Fatal("player-authored WASD configuration was not preserved")
	}

	// Verify valid custom config saves successfully
	validConfig := []byte("I01\t2\nI02\t0\nI03\t3\nI04\t1\nI06\t55\n")
	requestValid := make([]byte, 4+len(validConfig))
	binary.LittleEndian.PutUint32(requestValid, uint32(len(validConfig)))
	copy(requestValid[4:], validConfig)

	if err := s.handleSaveGameOption2(session, requestValid); err != nil {
		t.Fatal(err)
	}
	rec, found, err := repos.Settings.Load(ctx, characterHotkeyConfigScope("1"))
	if err != nil || !found {
		t.Fatalf("valid hotkey config was not saved: found=%v err=%v", found, err)
	}
	if rec.Values["hotkey_config_hex"] != hex.EncodeToString(validConfig) {
		t.Fatalf("saved hex mismatch: got %v want %v", rec.Values["hotkey_config_hex"], hex.EncodeToString(validConfig))
	}
}

func Test90CNSaveGameOption2FallbackTownSceneReadyCharacterID(t *testing.T) {
	t.Setenv(currentGameOptionNoticeEnv, "1")
	s, session, _, repos := testGameOptionFixture(t)
	s.options.currentClientVersion = "90cn"
	ctx := context.Background()

	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "account-1", Job: "0", Level: 1}); err != nil {
		t.Fatal(err)
	}

	validConfig := []byte("I01\t2\nI02\t0\nI03\t3\nI04\t1\n")
	requestValid := make([]byte, 4+len(validConfig))
	binary.LittleEndian.PutUint32(requestValid, uint32(len(validConfig)))
	copy(requestValid[4:], validConfig)

	// selectedCharacterID is 0 (as during op7 return select), but townSceneReadyCharacterID is 1
	session.selectedCharacterID = 0
	session.townSceneReadyCharacterID = 1

	if err := s.handleSaveGameOption2(session, requestValid); err != nil {
		t.Fatal(err)
	}
	rec, found, err := repos.Settings.Load(ctx, characterHotkeyConfigScope("1"))
	if err != nil || !found {
		t.Fatalf("valid hotkey config was not saved with fallback: found=%v err=%v", found, err)
	}
	if rec.Values["hotkey_config_hex"] != hex.EncodeToString(validConfig) {
		t.Fatalf("saved hex mismatch: got %v want %v", rec.Values["hotkey_config_hex"], hex.EncodeToString(validConfig))
	}
}
