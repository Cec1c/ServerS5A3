package dnfbridge

import "testing"

func TestParseDungeonPeerActorRecoveryEnabled(t *testing.T) {
	for _, off := range []string{"", "off", "0", "false", "no", "invalid"} {
		if parseDungeonPeerActorRecoveryEnabled(off) {
			t.Fatalf("expected %q to disable recovery chain", off)
		}
	}
	for _, on := range []string{"on", "ON", "1", "true", "yes", "legacy"} {
		if !parseDungeonPeerActorRecoveryEnabled(on) {
			t.Fatalf("expected %q to enable recovery chain", on)
		}
	}
}
