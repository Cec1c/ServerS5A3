package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func TestCurrentRaidChannelReadyOnlySelectedSupportedRaidBootstrap(t *testing.T) {
	for _, kind := range []string{"anton", "luke", "ordinary", "unselected", "legacy", "nil"} {
		t.Run(kind, func(t *testing.T) {
			s, a, ac := prepareCurrentRaidTest(t)
			switch kind {
			case "luke":
				a.channel.Type = dnfenum.HiddenRaidType
			case "ordinary":
				a.channel.Type = 11
			case "unselected":
				a.selectedCharacterID = 0
			case "legacy":
				s.options.currentClientVersion = "legacy"
			case "nil":
				a = nil
			}
			if err := s.sendCurrentRaidChannelReady(a, "test_bootstrap"); err != nil {
				t.Fatal(err)
			}
			if kind != "anton" && kind != "luke" {
				if ac.write.Len() != 0 {
					t.Fatal("non-raid bootstrap reset raid cooldown")
				}
				return
			}
			packet, rest := splitGameServerUpperPacket(t, ac.write.Bytes())
			if packet.Header.MsgID != 1228 || packet.Header.Classification != 0 || !bytes.Equal(packet.Body, []byte{0}) || len(rest) != 0 {
				t.Fatal("ready notice disagrees with current native reader")
			}
		})
	}
}
