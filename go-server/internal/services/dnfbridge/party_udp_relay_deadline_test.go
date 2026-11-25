package dnfbridge

import (
	"net"
	"net/netip"
	"testing"
	"time"
)

func relayDeadlineTestPair(t testing.TB) (*currentPartyUDPRelay, *currentPartyUDPRelayLeg, *currentPartyUDPRelayLeg) {
	t.Helper()
	listen := func() *net.UDPConn {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	receiver := listen()
	from := new(currentPartyUDPRelayLeg)
	to := &currentPartyUDPRelayLeg{conn: listen(), binding: currentPartyUDPRelayBinding{address: [4]byte{127, 0, 0, 1}}}
	to.learnedPort.Store(uint32(receiver.LocalAddr().(*net.UDPAddr).Port))
	from.opposite.Store(to)
	return new(currentPartyUDPRelay), from, to
}

func TestPartyRelayForwardDeadlineReuseAndFailure(t *testing.T) {
	relay, from, to := relayDeadlineTestPair(t)
	// Keep the synthetic packet clock independent of test runner scheduling.
	now := time.Now().Add(time.Minute)
	var installed int64
	for i := 0; i < 100; i++ {
		relay.forwardDatagram(from, to, []byte{1}, now.Add(time.Duration(i)*time.Microsecond))
		deadline := to.writeDeadline.Load()
		if i == 0 {
			installed = deadline
		}
		if deadline == 0 || deadline != installed {
			t.Fatalf("packet %d changed deadline %d to %d", i, installed, deadline)
		}
	}
	if from.traffic.forwarded.Load() != 100 {
		t.Fatal("failed to forward burst")
	}
	now = now.Add(currentPartyRelayWriteTimeout * 3 / 4)
	relay.forwardDatagram(from, to, []byte{1}, now)
	if to.writeDeadline.Load() <= installed {
		t.Fatal("deadline not renewed near expiry")
	}
	// Expire the underlying socket while the cached window is still current.
	// This forces a real netpoll timeout, independent of OS send buffer sizes.
	if err := to.conn.SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	relay.forwardDatagram(from, to, []byte{1}, now)
	if from.traffic.failed.Load() != 1 || to.writeDeadline.Load() != 0 {
		t.Fatal("write failure did not invalidate deadline")
	}
	relay.forwardDatagram(from, to, []byte{1}, now)
	if to.writeDeadline.Load() == 0 || from.traffic.forwarded.Load() != 102 {
		t.Fatal("first write after failure did not refresh")
	}
	_ = to.conn.Close()
	relay.forwardDatagram(from, to, []byte{1}, now.Add(currentPartyRelayWriteTimeout))
	if to.traffic.failed.Load() != 1 || to.writeDeadline.Load() != 0 {
		t.Fatal("deadline setup failure not recorded")
	}
}

func BenchmarkPartyRelayForwardDatagram(b *testing.B) {
	for _, mode := range []string{"per_packet", "reuse"} {
		b.Run(mode, func(b *testing.B) {
			relay, from, to := relayDeadlineTestPair(b)
			payload := make([]byte, 32)
			b.ReportAllocs()
			for b.Loop() {
				now := time.Now() // timestamp already collected in readLoop in both cases
				if mode == "reuse" {
					relay.forwardDatagram(from, to, payload, now)
					continue
				}
				if from.retired.Load() || to.retired.Load() || from.opposite.Load() != to {
					continue
				}
				port := to.learnedPort.Load()
				if port == 0 {
					continue
				}
				if err := to.conn.SetWriteDeadline(time.Now().Add(currentPartyRelayWriteTimeout)); err != nil {
					b.Fatal(err)
				}
				if _, err := to.conn.WriteToUDPAddrPort(payload, netip.AddrPortFrom(netip.AddrFrom4(to.binding.address), uint16(port))); err != nil {
					b.Fatal(err)
				}
				from.traffic.forwarded.Add(1)
				from.traffic.bytes.Add(uint64(len(payload)))
				relay.forwardedTotal.Add(1)
			}
			if from.traffic.forwarded.Load() != uint64(b.N) {
				b.Fatal("forward count", from.traffic.forwarded.Load())
			}
		})
	}
}
