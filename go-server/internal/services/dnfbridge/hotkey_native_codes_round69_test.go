package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const round69NativeInputEnv = "DNFBRIDGE_ROUND69_NATIVE_INPUTS"

// round69DecodeDefaultText strictly decodes the current 454 text layout. The
// values are expected to be engine keycodes, not raw PVF Windows virtual keys.
func round69DecodeDefaultText(t *testing.T, text []byte) map[string]int {
	t.Helper()
	rows := make(map[string]int)
	for lineNumber, line := range strings.Split(strings.TrimSuffix(string(text), "\n"), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "\t")
		if !ok || key == "" || value == "" {
			t.Fatalf("line %d is not key<TAB>value: %q", lineNumber+1, line)
		}
		parsed, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("line %d has invalid decimal value %q: %v", lineNumber+1, value, err)
		}
		if _, duplicate := rows[key]; duplicate {
			t.Fatalf("line %d repeats hotkey %s", lineNumber+1, key)
		}
		rows[key] = parsed
	}
	return rows
}

func round69RequireHotkeyCodes(t *testing.T, rows map[string]int, want map[string]int) {
	t.Helper()
	for key, value := range want {
		if got, ok := rows[key]; !ok || got != value {
			t.Fatalf("hotkey %s = %d (found=%t), want engine code %d; all=%v", key, got, ok, value, rows)
		}
	}
}

func round69SingleHotkey454(t *testing.T, packets []struct {
	msgID uint16
	body  []byte
}, wantType byte) []byte {
	t.Helper()
	var body []byte
	count := 0
	for _, packet := range packets {
		if packet.msgID != 454 {
			continue
		}
		count++
		body = packet.body
	}
	if count != 1 {
		t.Fatalf("class0/454 count = %d, want exactly one; packets=%+v", count, packets)
	}
	if len(body) < 5 {
		t.Fatalf("454 body too short: %x", body)
	}
	if body[0] != wantType {
		t.Fatalf("454 key_type = %d, want %d; body=%x", body[0], wantType, body)
	}
	declared := int(binary.LittleEndian.Uint32(body[1:5]))
	if declared != len(body)-5 {
		t.Fatalf("454 declared text length=%d, actual=%d", declared, len(body)-5)
	}
	return body
}

func round69ExportNativeInput(t *testing.T, name string, body []byte) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv(round69NativeInputEnv))
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create native-input directory %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write native input %s: %v", path, err)
	}
	t.Logf("wrote %s (%d bytes)", path, len(body))
}

func TestRound69RealPVFDefaultHotkeyEngineCodes(t *testing.T) {
	pvfPath := creatorDefaultGuardRealPVF(t)
	tests := []struct {
		name       string
		job        string
		wantType   byte
		wantCodes  map[string]int
		outputName string
	}{
		{
			name:     "creator",
			job:      "10",
			wantType: 1,
			wantCodes: map[string]int{
				"I01": 77,
				"I02": 55,
				"I03": 73,
				"I04": 58,
				"I06": 13,
				"I49": 26,
			},
			outputName: "creator-default-454-body.bin",
		},
		{
			name:     "normal",
			job:      "0",
			wantType: 0,
			wantCodes: map[string]int{
				"I01": 2,
				"I02": 0,
				"I03": 3,
				"I04": 1,
				"I49": 26,
			},
			outputName: "normal-default-454-body.bin",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			service, session, connection, repos := testGameOptionFixture(t)
			service.options.currentClientVersion = "official-24.9"
			service.options.pvfPath = pvfPath
			ctx := context.Background()
			character, found, err := repos.Character.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("load character: found=%t err=%v", found, err)
			}
			character.Job = testCase.job
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatalf("save character job %s: %v", testCase.job, err)
			}

			if err := service.sendAccountGameOptionNotices(session, "round69_real_pvf_"+testCase.name); err != nil {
				t.Fatalf("send default hotkey notice: %v", err)
			}
			body := round69SingleHotkey454(t, drainUpperPackets(t, connection), testCase.wantType)
			rows := round69DecodeDefaultText(t, body[5:])
			round69RequireHotkeyCodes(t, rows, testCase.wantCodes)
			round69ExportNativeInput(t, testCase.outputName, body)

			if _, found, err := repos.Settings.Load(ctx, characterHotkeyConfigScope("19")); err != nil {
				t.Fatalf("load hotkey config: %v", err)
			} else if found {
				t.Fatal("PVF default was persisted as a character hotkey configuration")
			}
		})
	}
}

func TestRound69SameSessionCreatorThenUnsavedNormalReplaysNormalDefault(t *testing.T) {
	pvfPath := creatorDefaultGuardRealPVF(t)
	service, session, connection, repos := testGameOptionFixture(t)
	service.options.currentClientVersion = "official-24.9"
	service.options.pvfPath = pvfPath
	ctx := context.Background()

	creator, found, err := repos.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load creator: found=%t err=%v", found, err)
	}
	creator.Job = "10"
	if err := repos.Character.Save(ctx, creator); err != nil {
		t.Fatalf("save creator: %v", err)
	}
	normal := dnfrepo.CharacterRecord{
		CharacterID: "20",
		AccountID:   "account-1",
		Job:         "0",
		Level:       90,
	}
	if err := repos.Character.Save(ctx, normal); err != nil {
		t.Fatalf("save normal character: %v", err)
	}

	if err := service.sendAccountGameOptionNotices(session, "round69_creator_first"); err != nil {
		t.Fatalf("send creator default: %v", err)
	}
	creatorBody := round69SingleHotkey454(t, drainUpperPackets(t, connection), 1)
	round69RequireHotkeyCodes(t, round69DecodeDefaultText(t, creatorBody[5:]), map[string]int{
		"I01": 77, "I02": 55, "I03": 73, "I04": 58, "I06": 13, "I49": 26,
	})

	service.bindGameSessionCharacter(session, 20)
	session.gameOptionNoticeSent = true
	connection.write.Reset()
	if err := service.sendSelectedCharacterHotkeyNotice(session, "round69_switch_to_unsaved_normal"); err != nil {
		t.Fatalf("send normal default after character switch: %v", err)
	}
	normalBody := round69SingleHotkey454(t, drainUpperPackets(t, connection), 0)
	round69RequireHotkeyCodes(t, round69DecodeDefaultText(t, normalBody[5:]), map[string]int{
		"I01": 2, "I02": 0, "I03": 3, "I04": 1, "I49": 26,
	})
	round69ExportNativeInput(t, "normal-default-after-creator-454-body.bin", normalBody)
}

func TestRound69SavedCustomHotkeysReplayVerbatimWithoutDefaultConversion(t *testing.T) {
	for _, job := range []string{"0", "10"} {
		t.Run("job_"+job, func(t *testing.T) {
			service, session, connection, repos := testGameOptionFixture(t)
			service.options.currentClientVersion = "official-24.9"
			ctx := context.Background()
			character, found, err := repos.Character.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("load character: found=%t err=%v", found, err)
			}
			character.Job = job
			if err := repos.Character.Save(ctx, character); err != nil {
				t.Fatalf("save character job %s: %v", job, err)
			}

			// These intentionally use the raw PVF virtual-key values. They must be
			// replayed byte-for-byte even though fresh defaults need conversion.
			custom := []byte("I01\t87\nI02\t65\nI03\t83\nI04\t68\nI06\t32\nI49\t27\n")
			if err := service.handleSaveGameOption2(session, testLengthPrefixedBlob(custom)); err != nil {
				t.Fatalf("save custom hotkeys: %v", err)
			}
			if got := drainUpperPackets(t, connection); len(got) != 0 {
				t.Fatalf("op198 unexpectedly sent packets: %+v", got)
			}
			saved, found, err := repos.Settings.Load(ctx, characterHotkeyConfigScope("19"))
			if err != nil || !found {
				t.Fatalf("load saved hotkeys: found=%t err=%v", found, err)
			}
			if saved.Values["hotkey_config_hex"] != hex.EncodeToString(custom) {
				t.Fatalf("stored custom hotkeys changed: got=%s want=%x", saved.Values["hotkey_config_hex"], custom)
			}

			connection.write.Reset()
			if err := service.sendAccountGameOptionNotices(session, "round69_saved_custom_replay"); err != nil {
				t.Fatalf("replay custom hotkeys: %v", err)
			}
			wantType := byte(0)
			if job == "10" {
				wantType = 1
			}
			body := round69SingleHotkey454(t, drainUpperPackets(t, connection), wantType)
			if want := buildAccountHotkeyNoticeBody(wantType, custom); !bytes.Equal(body, want) {
				t.Fatalf("saved custom 454 was transformed: got=%x want=%x", body, want)
			}
			after, found, err := repos.Settings.Load(ctx, characterHotkeyConfigScope("19"))
			if err != nil || !found {
				t.Fatalf("reload saved hotkeys: found=%t err=%v", found, err)
			}
			if after.Values["hotkey_config_hex"] != hex.EncodeToString(custom) {
				t.Fatalf("replay mutated saved hotkeys: got=%s want=%x", after.Values["hotkey_config_hex"], custom)
			}
		})
	}
}
