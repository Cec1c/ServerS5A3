package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

func TestLukeChannelExitRetiresLateRaidRoster(t *testing.T) {
	s, p, other, c, otherConn, state := prepareApprovedCurrentRaid(t)
	p.channel = channelcatalog.Channel{ID: 28, Type: 32, Group: "luke_raid", Port: 10028}
	other.channel = p.channel
	c.write.Reset()
	otherConn.write.Reset()
	if err := s.handleCurrentChannelExit(p, []byte{1}, "luke-return-regression"); err != nil {
		t.Fatal(err)
	}
	ack := append([]byte(nil), c.write.Bytes()...)
	if len(ack) == 0 {
		t.Fatal("missing EXIT ACK")
	}
	// A member refresh captured before teardown can arrive from another player
	// while this socket waits for the client's close. Use the real broadcaster.
	if err := s.broadcastRuntimeRaidRoster(state); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c.write.Bytes(), ack) {
		t.Fatalf("late raid roster followed EXIT: ACK=%d final=%d", len(ack), c.write.Len())
	}
	if otherConn.write.Len() == 0 {
		t.Fatal("healthy raid recipient lost its roster")
	}
}
