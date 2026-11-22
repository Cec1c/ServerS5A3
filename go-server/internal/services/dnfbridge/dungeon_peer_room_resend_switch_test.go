package dnfbridge

import "testing"

func TestParseDungeonPeerOp37ResendMode(t *testing.T) {
	for _, off := range []string{"", "off", "0", "false", "unknown"} {
		if got := parseDungeonPeerOp37ResendMode(off); got != dungeonPeerOp37ResendOff {
			t.Fatalf("mode(%q)=%v want off", off, got)
		}
	}
	if got := parseDungeonPeerOp37ResendMode("run"); got != dungeonPeerOp37ResendRun {
		t.Fatalf("mode(run)=%v want run", got)
	}
	for _, room := range []string{"room", "on", "1", "true", "legacy"} {
		if got := parseDungeonPeerOp37ResendMode(room); got != dungeonPeerOp37ResendRoom {
			t.Fatalf("mode(%q)=%v want room", room, got)
		}
	}
}
