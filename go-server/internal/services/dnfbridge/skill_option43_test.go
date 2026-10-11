package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strconv"
	"testing"
)

// Controlled frames based on EXE word indexes. This is neither a player capture
// nor a GUI test and does not equate skill_commands/Enabled/Cooldowns with flags.
func TestProtocol43CharacterSkillOptionWordsRoundTripAcrossCharacters(t *testing.T) {
	t.Setenv(currentCharacterOptionNoticeEnv, "on")
	t.Setenv("DNFBRIDGE_CHARACTER_OPTION_PROJECTION", "off")
	s, session, conn, repos := testGameOptionFixture(t)
	s.options.currentClientVersion = "official-24.9"
	frame := func(a, b uint16) []byte {
		body := testLengthPrefixedBlob(bytes.Repeat([]byte{255}, 512))
		for index, value := range map[int]uint16{2: a, 22: b, 128: a, 192: b, 126: 0x55, 127: 0x66, 43: 1} {
			binary.LittleEndian.PutUint16(body[4+index*2:6+index*2], value)
		}
		return body
	}
	a, b := frame(9, 25), frame(100, 511)
	for _, tc := range []struct {
		id   uint16
		body []byte
	}{{19, a}, {20, b}} {
		session.selectedCharacterID = tc.id
		session.initialTownRouteCharacterID = tc.id
		session.initialTownRouteStage = currentInitialTownRoutePlayerStateSent
		handled, err := s.handleGameOptionCommand(session, 447, tc.body)
		if err != nil || !handled {
			t.Fatalf("save %d handled=%v err=%v", tc.id, handled, err)
		}
	}
	for _, tc := range []struct {
		id   uint16
		body []byte
	}{{19, a}, {20, b}, {19, a}} {
		cold := &gameSession{conn: conn, selectedCharacterID: tc.id}
		if err := s.sendCharacterOptionNotice(cold, "controlled_protocol43_cold_entry"); err != nil {
			t.Fatal(err)
		}
		packets := drainUpperPackets(t, conn)
		if len(packets) != 1 || packets[0].msgID != 390 || !bytes.Equal(packets[0].body, tc.body) {
			t.Fatalf("character %d lost or crossed option words", tc.id)
		}
	}
	if _, found, err := repos.Settings.Load(context.Background(), characterOptionScope("21")); err != nil || found {
		t.Fatalf("unrelated character found=%v err=%v", found, err)
	}
	cold := &gameSession{conn: conn, selectedCharacterID: 21}
	if err := s.sendCharacterOptionNotice(cold, "controlled_unsaved"); err != nil {
		t.Fatal(err)
	}
	if packets := drainUpperPackets(t, conn); len(packets) != 0 {
		t.Fatal("unsaved character inherited previous flags")
	}
}

func TestProtocol43Native512OptionRejectsOtherLengthsWithoutOverwriting(t *testing.T) {
	for _, n := range []int{1, 511, 513} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			s, session, _, repos := testGameOptionFixture(t)
			s.options.currentClientVersion = "official-24.9"
			good := testLengthPrefixedBlob(bytes.Repeat([]byte{255}, 512))
			if err := s.handleSaveCharacterOption(session, good); err != nil {
				t.Fatal(err)
			}
			before, found, err := repos.Settings.Load(context.Background(), characterOptionScope("19"))
			if err != nil || !found {
				t.Fatal("missing initial valid option")
			}
			if err := s.handleSaveCharacterOption(session, testLengthPrefixedBlob(bytes.Repeat([]byte{0x6d}, n))); err != nil {
				t.Fatal(err)
			}
			after, found, err := repos.Settings.Load(context.Background(), characterOptionScope("19"))
			if err != nil || !found {
				t.Fatal("lost saved option")
			}
			if after.Values["character_option_body_hex"] != before.Values["character_option_body_hex"] {
				t.Fatalf("non-native len=%d overwrote the valid 512-byte snapshot; EXE reader rejects it", n)
			}
		})
	}
}
