package dnfbridge

import (
	"net"
	"testing"
	"time"
)

// Read-only experiment: the live R133 defaults put PROBE_PORTS at PORT_START.
// Both legitimate clients continue their ordinary one-byte native keepalive.
// There is no outsider, forged role payload, or guessed directed destination.
func TestR136LiveProbePortOverlap(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "127.0.0.2"} {
		for _, keepalive := range []byte{0, 1} {
			name := ip + map[byte]string{0: "/keepalive00", 1: "/keepalive01"}[keepalive]
			t.Run(name, func(t *testing.T) {
				f, _ := r137ProbeFixture(t)
				f.service.partyUDPRelay.Close()
				start := r137FreeRange(t)
				if start > 65472 {
					start -= 64
				}
				r, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
					enabled: true, advertiseIP: "127.0.0.1", portStart: start, portCount: 64,
					probeReplay: true, probeListen: true, seedForward: true, observedEndpointFanout: true,
					probePorts: []int{start},
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { r.Close() })
				if len(r.probeListenerPorts()) != 1 {
					t.Fatal("probe fixture did not bind")
				}
				f.service.partyUDPRelay = r
				f.service.options.currentClientVersion = "official-24.9"
				f.follower.conn.(*r137ProbeTestConn).remote.IP = net.ParseIP(ip)
				a, b := testCurrentPartyUDPClient(t, "127.0.0.1"), testCurrentPartyUDPClient(t, ip)
				defer a.Close()
				defer b.Close()
				probe := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: start}
				r136Write(t, a, probe, []byte{keepalive})
				r136Write(t, b, probe, []byte{keepalive})
				waitRelayCondition(t, time.Second, "pre-party native probes", func() bool { return r.observedProbeEndpointCount() == 2 })
				r136Register(t, f.service, f.leader, uint16(a.LocalAddr().(*net.UDPAddr).Port), 10)
				r136Register(t, f.service, f.follower, uint16(b.LocalAddr().(*net.UDPAddr).Port), 11)
				r136Invite(t, f.service, f.leader, f.follower)
				ab := r136WireEndpoint(t, f.service, f.leader, 100)
				ba := r136WireEndpoint(t, f.service, f.follower, 99)
				if ab.Port == start || ba.Port == start || len(r.probeListenerPorts()) != 1 {
					t.Fatal("probe dedicated port was assigned to a leg")
				}
				r136Learn(t, r, b, ba, 100, 99)
				r136Learn(t, r, a, ab, 99, 100)
				// Drain bootstrap fanout/replay copies before uniquely tagged application data.
				for _, c := range []*net.UDPConn{a, b} {
					for n := 0; n < 16; n++ {
						if readClientDatagram(t, c, 20*time.Millisecond) == nil {
							break
						}
					}
				}
				since := time.Now()
				t.Cleanup(func() {
					if t.Failed() {
						t.Logf("probe observations=%+v since=%s ports=%v", r.observedProbeEndpointEntries(nil), since, r.probeListenerPorts())
					}
				})
				r136Write(t, a, probe, []byte{keepalive})
				r136Write(t, b, probe, []byte{keepalive})
				waitRelayCondition(t, time.Second, "both ordinary keepalives stay on probe listener", func() bool {
					refreshed := 0
					for _, e := range r.observedProbeEndpointEntries(nil) {
						if !e.seenAt.Before(since) && e.hitPort == start && (int(e.port) == a.LocalAddr().(*net.UDPAddr).Port || int(e.port) == b.LocalAddr().(*net.UDPAddr).Port) {
							refreshed++
						}
					}
					return refreshed == 2
				})
				if ep := r136Leg(t, r, 99, 100).learnedEndpointSnapshot(); ep.String() != a.LocalAddr().String() {
					t.Fatal("A endpoint overwritten by native keepalive")
				}
				if ep := r136Leg(t, r, 100, 99).learnedEndpointSnapshot(); ep.String() != b.LocalAddr().String() {
					t.Fatal("B endpoint overwritten by native keepalive")
				}
				for n := 0; n < 3; n++ {
					r136Write(t, a, ab, []byte("A-after-keepalive"))
					r136Read(t, b, []byte("A-after-keepalive"), ba)
					r136Write(t, b, ba, []byte("B-after-keepalive"))
					r136Read(t, a, []byte("B-after-keepalive"), ab)
				}

			})
		}
	}
}
