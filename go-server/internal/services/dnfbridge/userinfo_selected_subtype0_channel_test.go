package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// renderSelectedSubtype0TailForTest renders the class0/op2 selected-character
// record tail with a fixed character row so the channel projection can be
// compared byte for byte.
func renderSelectedSubtype0TailForTest(
	t *testing.T,
	session *gameSession,
	tail dnfrepo.LegacyUserInfoRow,
	character dnfrepo.CharacterRecord,
) []byte {
	t.Helper()
	var writer packetWriter
	reader := csharpLegacyUserInfoReader{session: session}
	reader.writeCSharpUserInfoSubtype0Tail(&writer, tail, dnfrepo.LegacyUserInfoRow{}, character)
	body := writer.bytes()
	if len(body) == 0 {
		t.Fatal("selected-character subtype0 tail rendered empty")
	}
	return body
}

func countDifferingChannelTestBytes(left, right []byte) int {
	if len(left) != len(right) {
		return -1
	}
	count := 0
	for index := range left {
		if left[index] != right[index] {
			count++
		}
	}
	return count
}

// The served channel table marks the ancient-dungeon channel 121 and the
// death-tower channel 508 with the special kind 22 while every ordinary area
// channel uses kind 11. That kind belongs to the served directory, not to the
// per-character record: publishing it there gave the client a second place to
// read a special channel family for the character, and it is the only
// per-character downlink value that differed between the blocked session
// (dnf:10005 on channel 121) and the working sessions (dnf:10001 on channels
// 10/11) on 2026-09-24. The record must therefore be kind-independent.
func TestSelectedCharacterRecordIgnoresServedChannelKind(t *testing.T) {
	tail := dnfrepo.LegacyUserInfoRow{}
	character := dnfrepo.CharacterRecord{
		CharacterID: "5",
		AccountID:   "dnf:10005",
		Name:        "扫射之王浩成",
		Level:       90,
		Stats:       map[string]int64{"channel_id": 2},
	}

	ancientKind := renderSelectedSubtype0TailForTest(t, &gameSession{
		channel: channelcatalog.Channel{ID: 121, Type: 22},
	}, tail, character)
	zeroKind := renderSelectedSubtype0TailForTest(t, &gameSession{
		channel: channelcatalog.Channel{ID: 121, Type: 0},
	}, tail, character)
	areaKind := renderSelectedSubtype0TailForTest(t, &gameSession{
		channel: channelcatalog.Channel{ID: 121, Type: 11},
	}, tail, character)

	if !bytes.Equal(ancientKind, zeroKind) || !bytes.Equal(ancientKind, areaKind) {
		t.Fatalf(
			"served channel kind leaked into the character record: kind22=%x kind0=%x kind11=%x",
			ancientKind,
			zeroKind,
			areaKind,
		)
	}
}

// The display-mode slot and the channel-id slot keep following the live session
// channel (the pinned current-EXE contract of this record). Moving the session
// to another channel may therefore only touch those two u16 slots - never the
// channel-kind byte.
func TestSelectedCharacterRecordPublishesOnlyTheLiveChannelSlots(t *testing.T) {
	tail := dnfrepo.LegacyUserInfoRow{}
	character := dnfrepo.CharacterRecord{
		CharacterID: "3",
		AccountID:   "dnf:10001",
		Level:       90,
		Stats:       map[string]int64{"channel_id": 2},
	}

	ancient := renderSelectedSubtype0TailForTest(t, &gameSession{
		channel: channelcatalog.Channel{ID: 121, Type: 22},
	}, tail, character)
	area := renderSelectedSubtype0TailForTest(t, &gameSession{
		channel: channelcatalog.Channel{ID: 10, Type: 11},
	}, tail, character)
	far := renderSelectedSubtype0TailForTest(t, &gameSession{
		channel: channelcatalog.Channel{ID: 262, Type: 42},
	}, tail, character)
	noSession := renderSelectedSubtype0TailForTest(t, nil, tail, character)

	if bytes.Equal(ancient, area) {
		t.Fatal("live session channel is no longer projected into the character record")
	}
	if bytes.Equal(ancient, noSession) {
		t.Fatal("live session channel equals the persisted row id; the session projection was dropped")
	}
	// Two u16 slots carry the session channel, so at most four bytes may differ.
	// 121 (0x0079) vs 10 (0x000A) differs in the low byte of both slots.
	if diff := countDifferingChannelTestBytes(ancient, area); diff != 2 {
		t.Fatalf("channel 121 -> 10 changed %d record bytes, want the 2 low bytes of the two u16 channel slots", diff)
	}
	// 121 (0x0079) vs 262 (0x0106) differs in both bytes of both slots.
	if diff := countDifferingChannelTestBytes(ancient, far); diff != 4 {
		t.Fatalf("channel 121 -> 262 changed %d record bytes, want all 4 bytes of the two u16 channel slots", diff)
	}
	// Session 121 vs the persisted row (display mode 0, channel id 2): again the
	// two u16 slots only, one differing byte each.
	if diff := countDifferingChannelTestBytes(ancient, noSession); diff != 2 {
		t.Fatalf("session channel 121 vs persisted row changed %d record bytes, want the two u16 channel slots", diff)
	}
}

// The channel-kind slot is owned by the persisted character row even when a
// session exists: the session kind (22 for the ancient-dungeon channel 121)
// must never be republished, and the row value must be honoured verbatim.
func TestSelectedCharacterRecordKeepsPersistedChannelKindWhenSessionExists(t *testing.T) {
	character := dnfrepo.CharacterRecord{
		CharacterID: "2",
		AccountID:   "dnf:10005",
		Level:       40,
		Stats:       map[string]int64{"channel_id": 2},
	}
	session := &gameSession{channel: channelcatalog.Channel{ID: 121, Type: 22}}

	persistedKind := renderSelectedSubtype0TailForTest(t, session, dnfrepo.LegacyUserInfoRow{
		"channel_type": "9",
	}, character)
	defaultKind := renderSelectedSubtype0TailForTest(t, session, dnfrepo.LegacyUserInfoRow{}, character)

	if bytes.Equal(persistedKind, defaultKind) {
		t.Fatal("session channel kind overwrote the persisted channel-kind row value")
	}
	if diff := countDifferingChannelTestBytes(persistedKind, defaultKind); diff != 1 {
		t.Fatalf("channel-kind row value changed %d record bytes, want the single u8 kind slot", diff)
	}
}
