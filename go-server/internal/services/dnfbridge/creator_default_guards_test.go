package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const creatorDefaultNoticeMsgID = 454

func creatorDefaultGuardRealPVF(t *testing.T) string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to a normal Script.pvf for Creator default guards")
	}
	return path
}

func newCreatorDefaultGuardFixture(t *testing.T, job, pvfPath string) (*Service, *gameSession, *bufferConn, dnfrepo.Group) {
	t.Helper()
	t.Setenv(currentGameOptionNoticeEnv, "1")
	service, session, connection, repos := testGameOptionFixture(t)
	service.options.currentClientVersion = "official-24.9"
	service.options.pvfPath = pvfPath
	record, found, err := repos.Character.Load(context.Background(), "19")
	if err != nil {
		t.Fatalf("load character: %v", err)
	}
	if !found {
		t.Fatal("fixture character 19 is missing")
	}
	record.Job = job
	if err := repos.Character.Save(context.Background(), record); err != nil {
		t.Fatalf("save character job %s: %v", job, err)
	}
	return service, session, connection, repos
}

// seedCreatorDefaultGuardAccount keeps the request on the saved-account path so
// a guard case exercises the Creator fallback itself instead of returning early
// for a completely absent account settings row.
func seedCreatorDefaultGuardAccount(t *testing.T, repos dnfrepo.Group) dnfrepo.SettingsRecord {
	t.Helper()
	record := dnfrepo.SettingsRecord{
		Scope:  accountGameOptionScope("account-1"),
		Values: map[string]string{"source": "creator-default-guards"},
	}
	if err := repos.Settings.Save(context.Background(), record); err != nil {
		t.Fatalf("seed account settings: %v", err)
	}
	return dnfrepo.CloneSettings(record)
}

func assertCreatorDefaultGuardNo454(t *testing.T, service *Service, session *gameSession, connection *bufferConn, source string) {
	t.Helper()
	connection.write.Reset()
	if err := service.sendAccountGameOptionNotices(session, source); err != nil {
		t.Fatalf("%s: send notices: %v", source, err)
	}
	for _, packet := range drainUpperPackets(t, connection) {
		if packet.msgID == creatorDefaultNoticeMsgID {
			t.Fatalf("%s: Creator default 454 was sent, body=%x", source, packet.body)
		}
	}
}

func assertCreatorDefaultTextNotice(t *testing.T, packet struct {
	msgID uint16
	body  []byte
}) {
	t.Helper()
	if packet.msgID != creatorDefaultNoticeMsgID {
		t.Fatalf("msg_id = %d, want %d", packet.msgID, creatorDefaultNoticeMsgID)
	}
	if len(packet.body) < 6 {
		t.Fatalf("454 body too short: %x", packet.body)
	}
	if packet.body[0] != 1 {
		t.Fatalf("454 key type = %d, want 1", packet.body[0])
	}
	if declared := int(binary.LittleEndian.Uint32(packet.body[1:5])); declared != len(packet.body)-5 {
		t.Fatalf("454 text length = %d, body has %d text bytes", declared, len(packet.body)-5)
	}
	text := packet.body[5:]
	if !bytes.Contains(text, []byte("I01\t87\n")) {
		t.Fatalf("454 body is not the PVF Creator key text: %q", text)
	}
}

func assertCreatorDefaultGuardPVFControl(t *testing.T, service *Service, session *gameSession, connection *bufferConn, source string) {
	t.Helper()
	connection.write.Reset()
	if err := service.sendAccountGameOptionNotices(session, source); err != nil {
		t.Fatalf("%s: send notices: %v", source, err)
	}
	packets := drainUpperPackets(t, connection)
	if len(packets) != 1 {
		t.Fatalf("%s: fresh Creator control notices = %+v, want one 454", source, packets)
	}
	assertCreatorDefaultTextNotice(t, packets[0])
}

func TestCreatorDefaultGuardsFreshCreatorGetsType1Text454(t *testing.T) {
	path := creatorDefaultGuardRealPVF(t)
	service, session, connection, repos := newCreatorDefaultGuardFixture(t, "10", path)
	assertCreatorDefaultGuardPVFControl(t, service, session, connection, "fresh_creator_entry")

	if _, found, err := repos.Settings.Load(context.Background(), characterHotkeyConfigScope("19")); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("Creator default text was persisted as character hotkey configuration")
	}
	if _, found, err := repos.Settings.Load(context.Background(), accountGameOptionScope("account-1")); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("Creator default text was persisted as account game-option settings")
	}
}

func TestCreatorDefaultGuardsNonCreatorJobsStaySilent(t *testing.T) {
	path := creatorDefaultGuardRealPVF(t)
	service, session, connection, repos := newCreatorDefaultGuardFixture(t, "10", path)
	beforeAccount := seedCreatorDefaultGuardAccount(t, repos)
	ctx := context.Background()
	assertCreatorDefaultGuardPVFControl(t, service, session, connection, "creator_guard_job_control")

	for _, job := range []string{"0", "12"} {
		t.Run("job-"+job, func(t *testing.T) {
			record, found, err := repos.Character.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("load character: found=%t err=%v", found, err)
			}
			record.Job = job
			if err := repos.Character.Save(ctx, record); err != nil {
				t.Fatal(err)
			}
			assertCreatorDefaultGuardNo454(t, service, session, connection, "creator_guard_job_"+job)
			if _, found, err := repos.Settings.Load(ctx, characterHotkeyConfigScope("19")); err != nil {
				t.Fatal(err)
			} else if found {
				t.Fatalf("job %s persisted a Creator default hotkey configuration", job)
			}
		})
	}

	afterAccount, found, err := repos.Settings.Load(ctx, accountGameOptionScope("account-1"))
	if err != nil || !found {
		t.Fatalf("account settings found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(afterAccount.Values, beforeAccount.Values) {
		t.Fatalf("non-Creator jobs rewrote account settings: before=%+v after=%+v", beforeAccount.Values, afterAccount.Values)
	}
}

func TestCreatorDefaultGuardsMissingOrUnownedCharacterStaySilent(t *testing.T) {
	path := creatorDefaultGuardRealPVF(t)
	service, session, connection, repos := newCreatorDefaultGuardFixture(t, "10", path)
	beforeAccount := seedCreatorDefaultGuardAccount(t, repos)
	ctx := context.Background()
	assertCreatorDefaultGuardPVFControl(t, service, session, connection, "creator_guard_character_control")

	t.Run("no-selected-character", func(t *testing.T) {
		previous := session.selectedCharacterID
		session.selectedCharacterID = 0
		defer func() { session.selectedCharacterID = previous }()
		assertCreatorDefaultGuardNo454(t, service, session, connection, "creator_guard_no_selected")
		if _, found, err := repos.Settings.Load(ctx, characterHotkeyConfigScope("0")); err != nil {
			t.Fatal(err)
		} else if found {
			t.Fatal("no-selected-character path persisted a Creator default hotkey configuration")
		}
	})

	t.Run("missing-character", func(t *testing.T) {
		previous := session.selectedCharacterID
		session.selectedCharacterID = 999
		defer func() { session.selectedCharacterID = previous }()
		assertCreatorDefaultGuardNo454(t, service, session, connection, "creator_guard_missing_character")
		if _, found, err := repos.Settings.Load(ctx, characterHotkeyConfigScope("999")); err != nil {
			t.Fatal(err)
		} else if found {
			t.Fatal("missing-character path persisted a Creator default hotkey configuration")
		}
	})

	t.Run("unowned-character", func(t *testing.T) {
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
			CharacterID: "31",
			AccountID:   "other-account",
			Job:         "10",
			Level:       90,
		}); err != nil {
			t.Fatal(err)
		}
		previous := session.selectedCharacterID
		session.selectedCharacterID = 31
		defer func() { session.selectedCharacterID = previous }()
		assertCreatorDefaultGuardNo454(t, service, session, connection, "creator_guard_unowned_character")
		if _, found, err := repos.Settings.Load(ctx, characterHotkeyConfigScope("31")); err != nil {
			t.Fatal(err)
		} else if found {
			t.Fatal("unowned-character path persisted a Creator default hotkey configuration")
		}
	})

	afterAccount, found, err := repos.Settings.Load(ctx, accountGameOptionScope("account-1"))
	if err != nil || !found {
		t.Fatalf("account settings found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(afterAccount.Values, beforeAccount.Values) {
		t.Fatalf("missing/unowned character rewrote account settings: before=%+v after=%+v", beforeAccount.Values, afterAccount.Values)
	}
}

func TestCreatorDefaultGuardsExistingInvalidConfigNeverFallsBack(t *testing.T) {
	path := creatorDefaultGuardRealPVF(t)
	service, session, connection, repos := newCreatorDefaultGuardFixture(t, "10", path)
	beforeAccount := seedCreatorDefaultGuardAccount(t, repos)
	ctx := context.Background()

	// Prove this PVF can actually produce the default first; the negative cases
	// below must suppress it because of their existing invalid row.
	assertCreatorDefaultGuardPVFControl(t, service, session, connection, "creator_guard_invalid_config_control")

	cases := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "bad-hex", value: "not-a-hex-configuration"},
		{name: "native-capacity", value: strings.Repeat("00", 0x578)},
		{name: "above-native-capacity", value: strings.Repeat("11", 0x600)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{
				Scope:  characterHotkeyConfigScope("19"),
				Values: map[string]string{"hotkey_config_hex": testCase.value},
			}); err != nil {
				t.Fatal(err)
			}
			assertCreatorDefaultGuardNo454(t, service, session, connection, "creator_guard_invalid_config_"+testCase.name)

			record, found, err := repos.Settings.Load(ctx, characterHotkeyConfigScope("19"))
			if err != nil || !found {
				t.Fatalf("existing hotkey configuration found=%t err=%v", found, err)
			}
			if record.Values["hotkey_config_hex"] != testCase.value {
				t.Fatalf("existing invalid configuration was overwritten: got len=%d want len=%d",
					len(record.Values["hotkey_config_hex"])/2, len(testCase.value)/2)
			}
			account, found, err := repos.Settings.Load(ctx, accountGameOptionScope("account-1"))
			if err != nil || !found {
				t.Fatalf("account settings found=%t err=%v", found, err)
			}
			if !reflect.DeepEqual(account.Values, beforeAccount.Values) {
				t.Fatalf("invalid existing configuration rewrote account settings: before=%+v after=%+v", beforeAccount.Values, account.Values)
			}
		})
	}
}

func TestCreatorDefaultGuardsMissingPVFStaysSilent(t *testing.T) {
	missingPVF := filepath.Join(t.TempDir(), "missing-Script.pvf")
	if _, err := os.Stat(missingPVF); !os.IsNotExist(err) {
		t.Fatalf("missing PVF fixture unexpectedly exists: %v", err)
	}
	service, session, connection, repos := newCreatorDefaultGuardFixture(t, "10", missingPVF)
	beforeAccount := seedCreatorDefaultGuardAccount(t, repos)
	assertCreatorDefaultGuardNo454(t, service, session, connection, "creator_guard_missing_pvf")

	if _, found, err := repos.Settings.Load(context.Background(), characterHotkeyConfigScope("19")); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("missing-PVF path persisted a Creator default hotkey configuration")
	}
	afterAccount, found, err := repos.Settings.Load(context.Background(), accountGameOptionScope("account-1"))
	if err != nil || !found {
		t.Fatalf("account settings found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(afterAccount.Values, beforeAccount.Values) {
		t.Fatalf("missing-PVF path rewrote account settings: before=%+v after=%+v", beforeAccount.Values, afterAccount.Values)
	}
}
