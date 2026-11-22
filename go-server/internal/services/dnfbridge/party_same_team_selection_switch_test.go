package dnfbridge

import "testing"

func TestParseSamePartyTownSelectionNoticeEnabled(t *testing.T) {
	for _, off := range []string{"", "off", "0", "false", "legacy", "unknown"} {
		if parseSamePartyTownSelectionNoticeEnabled(off) {
			t.Fatalf("expected %q to keep legacy off behavior", off)
		}
	}
	for _, on := range []string{"on", "ON", "1", "true", "yes"} {
		if !parseSamePartyTownSelectionNoticeEnabled(on) {
			t.Fatalf("expected %q to enable same-party town selection notice", on)
		}
	}
}
