package dnfbridge

import (
	"encoding/binary"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"net"
	"testing"
	"time"
)

// Acquire an out-of-pool lease before probing 64 pool ports in disjoint 128-port
// blocks below the usual dynamic port range. Hold the lease until test cleanup
// so another copy cannot briefly bind this block while its owner is testing
// close/rebind. Probing the pool before the lease would itself cause contention.
// An unrelated process ignoring this lease can still contend;
// bind failures are fixture failures, never evidence of a product defect.
// No test sends traffic beyond loopback.
func r137FreeRange(t *testing.T) int {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		first := testCurrentPartyUDPClient(t, "127.0.0.1")
		start := 20000 + (first.LocalAddr().(*net.UDPAddr).Port%128)*128
		first.Close()
		lease, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: start + 64})
		if err != nil {
			continue
		}
		var sockets []*net.UDPConn
		for port := start; port < start+64; port++ {
			c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
			if err != nil {
				break
			}
			sockets = append(sockets, c)
		}
		if len(sockets) == 64 {
			t.Cleanup(func() { lease.Close() })
			for _, c := range sockets {
				c.Close()
			}
			return start
		}
		for _, c := range sockets {
			c.Close()
		}
		lease.Close()
	}
	t.Fatal("no complete 65-port fixture range available")
	return 0
}

func r137NewRelay(t *testing.T, start int, ports []int, on bool, via int) *currentPartyUDPRelay {
	t.Helper()
	r, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
		enabled: true, advertiseIP: "127.0.0.1", portStart: start, portCount: 64,
		probeListen: on, probePorts: ports, probeReplay: true, seedForward: true,
		observedEndpointFanout: true, viaPort: via,
		logWarn: func(message string, fields ...any) {
			if message == "party UDP relay native probe listener unavailable" {
				t.Logf("%s %v", message, fields)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func r137Bindings(ids ...uint16) []currentPartyUDPRelayBinding {
	var result []currentPartyUDPRelayBinding
	for _, id := range ids {
		result = append(result, currentPartyUDPRelayBinding{characterID: id, generation: 1, address: [4]byte{127, 0, 0, 1}})
	}
	return result
}

func r137AssertPool(t *testing.T, r *currentPartyUDPRelay, want int) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.portCount != 64 || len(r.legs) != want || len(r.usedPorts) != want {
		t.Fatalf("pool count=%d legs=%d used=%d want=%d", r.portCount, len(r.legs), len(r.usedPorts), want)
	}
	for _, leg := range r.legs {
		if int(leg.port) < r.portStart || int(leg.port) >= r.portStart+64 {
			t.Fatal("leg outside published pool")
		}
		if r.probeListener != nil {
			if _, reserved := r.probeListener.reservedPorts[int(leg.port)]; reserved {
				t.Fatal("dedicated probe allocated to leg")
			}
		}
	}
}

func TestR137ProbeReservationCapacityMatrix(t *testing.T) {
	for _, name := range []string{"one", "multiple", "outside", "off", "empty", "bind_failure", "socket_closed", "via_port", "kernel_assigned"} {
		t.Run(name, func(t *testing.T) {
			start := r137FreeRange(t)
			ports := []int{start}
			on := true
			via := 0
			want := 63
			live := 1
			var blocker *net.UDPConn
			switch name {
			case "multiple":
				ports = []int{start, start + 1, start + 2, start}
				want = 61
				live = 3
			case "outside":
				// A second leased block avoids the ephemeral-port release/rebind
				// race with other UDP test processes, while staying outside this pool.
				ports = []int{r137FreeRange(t)}
				want = 64
			case "off":
				on = false
				want = 64
				live = 0
			case "empty":
				ports = nil
				want = 64
				live = 0
			case "bind_failure":
				var err error
				blocker, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: start})
				if err != nil {
					t.Fatal(err)
				}
				live = 0
			case "socket_closed":
				live = 0
			case "via_port":
				via = start
			case "kernel_assigned":
				ports = []int{0}
			}
			r := r137NewRelay(t, start, ports, on, via)
			if blocker != nil {
				blocker.Close()
			}
			if name == "socket_closed" && !r.probeListeners().releasePort(start) {
				t.Fatal("probe close failed")
			}
			if name == "kernel_assigned" {
				actual := r.probeListenerPorts()
				if len(actual) != 1 {
					t.Fatal("kernel probe missing")
				}
				if actual[0] < start || actual[0] >= start+64 {
					want = 64
				}
			}
			if got := len(r.probeListenerPorts()); got != live {
				t.Fatalf("live probes=%d want=%d start=%d configured=%v", got, live, start, ports)
			}
			// Exercise the allocator to exact capacity, including the otherwise odd
			// final port. Sync tests below require complete directed topologies.
			r.mu.Lock()
			for n := 0; n < want; n++ {
				key := currentPartyUDPRelayLegKey{partyID: uint16(100 + n), ownerID: 101, peerID: 202}
				leg, err := r.newLegLocked(key, r137Bindings(101)[0])
				if err != nil {
					r.mu.Unlock()
					t.Fatalf("allocation %d/%d: %v", n, want, err)
				}
				r.legs[key] = leg
			}
			extra, err := r.newLegLocked(currentPartyUDPRelayLegKey{partyID: 999, ownerID: 101, peerID: 202}, r137Bindings(101)[0])
			if extra != nil {
				r.releaseLegLocked(extra)
			}
			r.mu.Unlock()
			if err == nil {
				t.Fatal("allocation exceeded dedicated capacity")
			}
			r137AssertPool(t, r, want)
			if got := len(r.probeListenerPorts()); got != live {
				t.Fatal("allocator closed a probe")
			}
		})
	}
}

func TestR137ProbeReservationLifecycle(t *testing.T) {
	start := r137FreeRange(t)
	r := r137NewRelay(t, start, []int{start}, true, 0)
	probe := r.probeListenerConn(start)
	if probe == nil {
		t.Fatal("probe not bound")
	}
	members := r137Bindings(101, 202, 303)
	for cycle := 0; cycle < 3; cycle++ {
		if !r.Sync(77, members[:2]) {
			t.Fatal("first party")
		}
		r137AssertPool(t, r, 2)
		old := relayLegFor(t, r, 101, 202)
		if !r.Sync(77, members) {
			t.Fatal("third member")
		}
		r137AssertPool(t, r, 6)
		if relayLegFor(t, r, 101, 202) != old {
			t.Fatal("unchanged pair recreated")
		}
		if !r.Sync(88, r137Bindings(404, 505)) {
			t.Fatal("second party")
		}
		r137AssertPool(t, r, 8)
		members[0].generation++
		if !r.Sync(77, members) {
			t.Fatal("new generation")
		}
		r137AssertPool(t, r, 8)
		fresh := relayLegFor(t, r, 101, 202)
		if fresh == old || fresh.binding.generation != members[0].generation || fresh.learnedEndpointSnapshot() != nil {
			t.Fatal("stale generation retained")
		}
		r.CloseParty(77)
		r137AssertPool(t, r, 2)
		r.CloseParty(88)
		r137AssertPool(t, r, 0)
		if r.probeListenerConn(start) != probe {
			t.Fatal("probe changed across party rebuild")
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if r.Sync(77, members[:2]) {
		t.Fatal("closed relay accepted sync")
	}
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: start})
	if err != nil {
		t.Fatal("relay close leaked probe socket", err)
	}
	socket.Close()
}

// 31 complete two-person topologies consume 62 of 63 directed ports. Neither
// a new two-person party nor an existing party's expansion may commit half.
func TestR137ProbeReservationAtomicExhaustion(t *testing.T) {
	start := r137FreeRange(t)
	r := r137NewRelay(t, start, []int{start}, true, 0)
	for i := uint16(0); i < 31; i++ {
		if !r.Sync(500+i, r137Bindings(1000+i*2, 1001+i*2)) {
			t.Fatal("initial capacity", i)
		}
	}
	r137AssertPool(t, r, 62)
	r.mu.Lock()
	old := make(map[currentPartyUDPRelayLegKey]*currentPartyUDPRelayLeg)
	for k, v := range r.legs {
		old[k] = v
	}
	r.mu.Unlock()
	for _, tc := range []struct {
		party   uint16
		members []currentPartyUDPRelayBinding
	}{
		{900, r137Bindings(2000, 2001)},
		{500, r137Bindings(1000, 1001, 3000)},
		{500, []currentPartyUDPRelayBinding{{characterID: 1000, generation: 2, address: [4]byte{127, 0, 0, 1}}, {characterID: 1001, generation: 2, address: [4]byte{127, 0, 0, 1}}}},
	} {
		if r.Sync(tc.party, tc.members) {
			t.Fatal("incomplete topology committed")
		}
		r137AssertPool(t, r, 62)
		r.mu.Lock()
		for k, v := range old {
			if r.legs[k] != v {
				r.mu.Unlock()
				t.Fatal("rollback replaced existing leg")
			}
		}
		r.mu.Unlock()
		if _, _, ok := r.Endpoint(900, 2000, 2001); ok {
			t.Fatal("partial new party advertised")
		}
		// Rollback closes its tentative final socket, not just its map entry.
		spare, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: start + 63})
		if err != nil {
			t.Fatal("tentative socket leaked", err)
		}
		spare.Close()
	}
	if len(r.probeListenerPorts()) != 1 {
		t.Fatal("exhaustion stole probe")
	}
	r.CloseParty(530)
	if !r.Sync(900, r137Bindings(2000, 2001)) {
		t.Fatal("pool did not recover after party close")
	}
	r137AssertPool(t, r, 62)
}

func TestR137ProbeReservationViaPortOverlap(t *testing.T) {
	start := r137FreeRange(t)
	r := r137NewRelay(t, start, []int{start}, true, start)
	ab, ba := syncTwoMemberRoom(t, r)
	if int(ab) != start || int(ba) != start {
		t.Fatal("shared advertisement changed")
	}
	r137AssertPool(t, r, 2)
	a, b := testCurrentPartyUDPClient(t, "127.0.0.1"), testCurrentPartyUDPClient(t, "127.0.0.1")
	defer a.Close()
	defer b.Close()
	sendOneByteNativeProbe(t, a, start, 0)
	sendOneByteNativeProbe(t, b, start, 1)
	waitRelayCondition(t, time.Second, "via-port peers observed", func() bool { return r.observedProbeEndpointCount() == 2 })
	dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: start}
	r136Write(t, a, dst, probePayload(101))
	r136Read(t, b, probePayload(101), dst)
	r136Write(t, b, dst, probePayload(202))
	r136Read(t, a, probePayload(202), dst)
}

func TestR137ProbeReservationHandlerExhaustionFallback(t *testing.T) {
	f, _ := r137ProbeFixture(t)
	f.service.partyUDPRelay.Close()
	start := r137FreeRange(t)
	r := r137NewRelay(t, start, []int{start}, true, 0)
	f.service.partyUDPRelay = r
	f.service.options.currentClientVersion = "official-24.9"
	for i := uint16(0); i < 31; i++ {
		if !r.Sync(500+i, r137Bindings(1000+i*2, 1001+i*2)) {
			t.Fatal("fill", i)
		}
	}
	r136Register(t, f.service, f.leader, 12001, 10)
	r136Register(t, f.service, f.follower, 12002, 11)
	r136Invite(t, f.service, f.leader, f.follower)
	r137AssertPool(t, r, 62)
	state := runtimePartyStateSnapshot(f.leader)
	if _, _, ok := r.Endpoint(uint16(state.PartyID), 99, 100); ok {
		t.Fatal("half topology published")
	}
	for _, tc := range []struct {
		session *gameSession
		peer    uint16
		ip      byte
		port    uint16
	}{
		{f.leader, 100, 2, 12002}, {f.follower, 99, 1, 12001},
	} {
		found := false
		for raw := tc.session.conn.(*r137ProbeTestConn).write.Bytes(); len(raw) > 0; {
			if len(raw) < dnfproto.GameServerUpperHeaderSize {
				t.Fatal("truncated frame")
			}
			total := int(binary.LittleEndian.Uint32(raw[3:7]))
			if total < dnfproto.GameServerUpperHeaderSize || total > len(raw) {
				t.Fatal("bad frame size")
			}
			packet, err := dnfproto.ParseChannelPacket(raw[:total])
			if err != nil {
				t.Fatal(err)
			}
			raw = raw[total:]
			if packet.Header.Classification != 0 || packet.Header.MsgID != 11 {
				continue
			}
			frame := upperFrame{msgID: packet.Header.MsgID, body: packet.Body}
			if len(frame.body) < 1 || frame.body[0] == 0 {
				continue
			}
			if len(frame.body) != 23 || frame.body[0] != 1 {
				t.Fatal("fallback changed peer-only row shape")
			}
			row := frame.body[1:]
			if binary.LittleEndian.Uint16(row[:2]) != tc.peer || !net.IP(row[2:6]).Equal(net.IPv4(127, 0, 0, tc.ip)) || binary.BigEndian.Uint16(row[10:12]) != tc.port {
				t.Fatal("exhaustion did not emit complete direct fallback")
			}
			found = true
		}
		if !found {
			t.Fatal("accepted party emitted no fallback op11")
		}
	}
	if len(r.probeListenerPorts()) != 1 {
		t.Fatal("handler fallback released probe")
	}
}
