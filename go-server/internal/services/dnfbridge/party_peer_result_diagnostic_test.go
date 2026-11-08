package dnfbridge

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

func TestCurrentLegacyPeerResultDiagnosticPreservesDispatch(t *testing.T) {
	s, p, _, _, _ := changeHostFixture(t, 10, 10)
	s.options.currentClientVersion = "official-24.9"
	var buf bytes.Buffer
	s.logger = slog.New(slog.NewTextHandler(&buf, nil))
	writeFrames(t, p)
	before := runtimePartyStateSnapshot(p)
	body := []byte{0xab, 0x00, 0xff}
	raw := buildLegacyGamePacketForBridgeTest(1, 97, 1, body)
	if err := s.handleLegacyGamePacket(p, raw); err != nil {
		t.Fatal(err)
	}
	if frames := writeFrames(t, p); len(frames) != 0 {
		t.Fatalf("unexpected reply: %d frames", len(frames))
	}
	if !reflect.DeepEqual(before, runtimePartyStateSnapshot(p)) {
		t.Fatal("membership changed")
	}
	if !bytes.Equal(body, []byte{0xab, 0, 0xff}) {
		t.Fatal("body mutated")
	}
	for _, want := range []string{"game-party-legacy-peer-result-observed", "char_id=10", "party_id=", "body_len=3", "body_prefix_hex=ab00ff", "body_truncated=false", "unhandled latest game command"} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("missing %q: %s", want, buf.String())
		}
	}
}

func TestCurrentLegacyPeerResultDiagnosticBoundsAndScope(t *testing.T) {
	s, p, _, _, _ := changeHostFixture(t, 10, 10)
	s.options.currentClientVersion = "official-24.9"
	var buf bytes.Buffer
	s.logger = slog.New(slog.NewTextHandler(&buf, nil))
	for _, pair := range [][2]uint16{{0, 97}, {1, 96}, {1, 98}} {
		s.observeCurrentLegacyPeerResult(p, byte(pair[0]), pair[1], nil)
	}
	s.observeCurrentLegacyPeerResult(nil, 1, 97, nil)
	old := p.selectedCharacterID
	p.selectedCharacterID = 0
	s.observeCurrentLegacyPeerResult(p, 1, 97, nil)
	p.selectedCharacterID = old
	s.options.currentClientVersion = "legacy"
	s.observeCurrentLegacyPeerResult(p, 1, 97, nil)
	s.options.currentClientVersion = "official-24.9"
	if buf.Len() != 0 || p.peerResultDiagnosticCount != 0 {
		t.Fatal("unrelated scope observed")
	}
	for i := 0; i < 80; i++ {
		s.observeCurrentLegacyPeerResult(p, 1, 97, bytes.Repeat([]byte{0xab}, 256))
	}
	if p.peerResultDiagnosticCount != 64 || strings.Count(buf.String(), "game-party-legacy-peer-result-observed") != 64 {
		t.Fatal("connection observation cap not enforced")
	}
	if !strings.Contains(buf.String(), "body_len=256 body_prefix_hex="+strings.Repeat("ab", 16)+" body_truncated=true") || strings.Contains(buf.String(), strings.Repeat("ab", 17)) {
		t.Fatal("body prefix not bounded")
	}
}
