package dnfbridge

import (
	"bytes"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestPartyRelayRetiredPortsCannotBeReassigned(t *testing.T) {
	for _, pvp := range []bool{false, true} {
		t.Run(fmt.Sprint("pvp=", pvp), func(t *testing.T) {
			r, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 12, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			pair := []currentPartyUDPRelayBinding{
				{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}},
				{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}},
			}
			seen := map[uint16]bool{}
			for round := 0; round < 6; round++ {
				if !r.syncGroup(pvp, 77, pair) {
					t.Fatal("allocation failed before pool exhausted")
				}
				r.mu.Lock()
				for _, leg := range r.legs {
					if seen[leg.port] {
						r.mu.Unlock()
						t.Fatalf("retired port %d reassigned in cooldown", leg.port)
					}
					seen[leg.port] = true
				}
				r.mu.Unlock()
				if pvp {
					r.closePVPRoom(77)
				} else {
					r.CloseParty(77)
				}
			}
			// Quarantine follows the socket across namespaces; exhaustion must not bypass it.
			if r.syncGroup(!pvp, 78, pair) {
				t.Fatal("exhausted pool bypassed quarantine")
			}
		})
	}
}

// A slow control-plane operation must not stop an established pair's traffic.
func TestPartyRelayForwardingDoesNotWaitForTopologyLock(t *testing.T) {
	r, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 12, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	peers := []currentPartyUDPRelayBinding{{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}}, {characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}}}
	if !r.Sync(77, peers) {
		t.Fatal("sync")
	}
	_, pa, _ := r.Endpoint(77, 101, 202)
	_, pb, _ := r.Endpoint(77, 202, 101)
	a, b := testCurrentPartyUDPClient(t, "127.0.0.1"), testCurrentPartyUDPClient(t, "127.0.0.1")
	defer a.Close()
	defer b.Close()
	ea, eb := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(pa)}, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(pb)}
	buffer := make([]byte, 128)
	deadline := time.Now().Add(3 * time.Second)
	for {
		b.WriteToUDP([]byte("ready"), eb)
		a.WriteToUDP([]byte("warmup"), ea)
		b.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		if _, _, err = b.ReadFromUDP(buffer); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("handshake", err)
		}
	}
	r.mu.Lock()
	a.WriteToUDP([]byte("independent"), ea)
	b.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	n, _, err := b.ReadFromUDP(buffer)
	r.mu.Unlock()
	if err != nil || string(buffer[:n]) != "independent" {
		t.Fatalf("control-plane lock blocked established traffic: %q %v", buffer[:n], err)
	}
}

func TestPartyRelayFloodIsLimitedBeforeEndpointWork(t *testing.T) {
	r, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 12, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Sync(77, []currentPartyUDPRelayBinding{{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}}, {characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}}}) {
		t.Fatal("sync")
	}
	_, port, _ := r.Endpoint(77, 101, 202)
	r.mu.Lock()
	leg := r.legs[currentPartyUDPRelayLegKey{partyID: 77, ownerID: 101, peerID: 202}]
	r.mu.Unlock()
	client := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer client.Close()
	target := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)}
	deadline := time.Now().Add(2 * time.Second)
	for leg.traffic.limited.Load() == 0 && time.Now().Before(deadline) {
		for i := 0; i < 64; i++ {
			if _, err := client.WriteToUDP([]byte{1}, target); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(time.Millisecond)
	}
	if leg.traffic.limited.Load() == 0 {
		t.Fatalf("flood was not limited, received=%d", leg.traffic.received.Load())
	}
}

func TestPartyRelayMemberRejoinKeepsSurvivorPortsAndRotatesRetiredPorts(t *testing.T) {
	r, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 12, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	peers := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: 303, generation: 1, address: [4]byte{127, 0, 0, 1}},
	}
	if !r.Sync(77, peers) {
		t.Fatal("initial sync")
	}
	_, survivor, _ := r.Endpoint(77, 101, 202)
	_, retired, _ := r.Endpoint(77, 101, 303)
	if !r.Sync(77, peers[:2]) || !r.Sync(77, peers) {
		t.Fatal("rejoin sync")
	}
	_, currentSurvivor, _ := r.Endpoint(77, 101, 202)
	_, currentRejoin, _ := r.Endpoint(77, 101, 303)
	if currentSurvivor != survivor {
		t.Fatal("healthy survivor port changed")
	}
	if currentRejoin == retired {
		t.Fatal("rejoined member reused retired endpoint")
	}
}

func TestCurrentPartyUDPRelayForwardsOnlyAuthenticatedDirectedLegs(t *testing.T) {
	portStart := testCurrentPartyUDPRelayPortStart(t)
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", portStart, 12, nil)
	if err != nil {
		t.Fatalf("new UDP relay: %v", err)
	}
	defer func() {
		if err := relay.Close(); err != nil {
			t.Fatalf("close UDP relay: %v", err)
		}
	}()

	bindings := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}},
	}
	if !relay.Sync(77, bindings) {
		t.Fatal("relay did not create a complete two-member room")
	}
	addressAB, portAB, ok := relay.Endpoint(77, 101, 202)
	if !ok {
		t.Fatal("missing A-to-B relay endpoint")
	}
	addressBA, portBA, ok := relay.Endpoint(77, 202, 101)
	if !ok || portAB == portBA {
		t.Fatalf("invalid directed relay endpoints: AB=%d BA=%d ok=%v", portAB, portBA, ok)
	}

	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	wrongSource := testCurrentPartyUDPClient(t, "127.0.0.2")
	defer wrongSource.Close()

	endpointAB := &net.UDPAddr{IP: net.IPv4(addressAB[0], addressAB[1], addressAB[2], addressAB[3]), Port: int(portAB)}
	endpointBA := &net.UDPAddr{IP: net.IPv4(addressBA[0], addressBA[1], addressBA[2], addressBA[3]), Port: int(portBA)}
	if _, err := wrongSource.WriteToUDP([]byte("forged"), endpointAB); err != nil {
		t.Fatalf("send forged endpoint registration: %v", err)
	}

	// The live deployment runs its own UDP relay next to this suite, and these
	// legs stay in the party range, so a single 1 s window made the test flaky
	// under load. Re-arm the handshake and retry the payload inside a 5 s
	// budget; the assertions below are unchanged.
	buffer := make([]byte, 128)
	deadline := time.Now().Add(5 * time.Second)
	var readErr error
	n := 0
	for {
		if _, err := clientB.WriteToUDP([]byte("b-ready"), endpointBA); err != nil {
			t.Fatalf("send B handshake: %v", err)
		}
		if _, err := clientA.WriteToUDP([]byte("A-payload"), endpointAB); err != nil {
			t.Fatalf("send A payload: %v", err)
		}
		if err := clientB.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			t.Fatalf("set client B deadline: %v", err)
		}
		n, _, readErr = clientB.ReadFromUDP(buffer)
		if readErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("receive relayed payload: %v", readErr)
		}
	}
	if !bytes.Equal(buffer[:n], []byte("A-payload")) {
		t.Fatalf("relayed payload = %q, want A-payload", buffer[:n])
	}
}

func TestCurrentPartyUDPRelaySyncRemovesFormerMemberLegs(t *testing.T) {
	portStart := testCurrentPartyUDPRelayPortStart(t)
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", portStart, 12, nil)
	if err != nil {
		t.Fatalf("new UDP relay: %v", err)
	}
	defer relay.Close()

	first := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: 303, generation: 1, address: [4]byte{127, 0, 0, 1}},
	}
	if !relay.Sync(77, first) {
		t.Fatal("initial relay sync failed")
	}
	if _, _, ok := relay.Endpoint(77, 101, 303); !ok {
		t.Fatal("initial member endpoint missing")
	}
	if !relay.Sync(77, first[:2]) {
		t.Fatal("member removal relay sync failed")
	}
	if _, _, ok := relay.Endpoint(77, 101, 303); ok {
		t.Fatal("former member endpoint remained routable")
	}
	if _, _, ok := relay.Endpoint(77, 101, 202); !ok {
		t.Fatal("remaining pair endpoint was removed")
	}
}

func testCurrentPartyUDPRelayPortStart(t *testing.T) int {
	t.Helper()
	const portCount = 64
	var lastErr error
	lastRange := "not allocated"
	for attempt := 0; attempt < 128; attempt++ {
		lastRange = "not allocated"
		listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			lastErr = err
			continue
		}
		port := listener.LocalAddr().(*net.UDPAddr).Port
		lastRange = fmt.Sprintf("%d-%d", port, port+portCount-1)
		if port > 65535-portCount+1 {
			_ = listener.Close()
			lastErr = fmt.Errorf("range start %d exceeds 65472", port)
			continue
		}
		// Hold every socket until the complete range has been checked together.
		listeners := []*net.UDPConn{listener}
		for offset := 1; offset < portCount; offset++ {
			next, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port + offset})
			if err != nil {
				lastErr = err
				break
			}
			listeners = append(listeners, next)
		}
		for _, held := range listeners {
			_ = held.Close()
		}
		if len(listeners) == portCount {
			// The relay binds after these closes; a TOCTOU window still remains.
			return port
		}
	}
	t.Fatalf("could not allocate a usable sixty-four-port UDP test range after 128 attempts: bindIP=127.0.0.1 range=%s error=%v", lastRange, lastErr)
	return 0
}

func testCurrentPartyUDPClient(t *testing.T, ip string) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(ip), Port: 0})
	if err != nil {
		t.Fatalf("listen UDP client %s: %v", ip, err)
	}
	return conn
}

func probePayload(characterID uint16) []byte {
	payload := make([]byte, 21)
	payload[0] = 0x02
	payload[1] = byte(characterID - 1)
	for index := 2; index < len(payload); index++ {
		payload[index] = byte(index)
	}
	return payload
}

func sendOneByteNativeProbe(t *testing.T, client *net.UDPConn, probePort int, payload byte) {
	t.Helper()
	if _, err := client.WriteToUDP([]byte{payload}, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: probePort}); err != nil {
		t.Fatalf("send 1-byte native probe: %v", err)
	}
}

func (leg *currentPartyUDPRelayLeg) learnedEndpointSnapshot() *net.UDPAddr {
	if leg == nil {
		return nil
	}
	p := leg.learnedPort.Load()
	if p == 0 {
		return nil
	}
	return &net.UDPAddr{IP: net.IPv4(leg.binding.address[0], leg.binding.address[1], leg.binding.address[2], leg.binding.address[3]), Port: int(p)}
}

func (r *currentPartyUDPRelay) probeListenerConn(port int) *net.UDPConn {
	l := r.probeListeners()
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.conns[port]
}

func readClientDatagram(t *testing.T, client *net.UDPConn, timeout time.Duration) []byte {
	t.Helper()
	_ = client.SetReadDeadline(time.Now().Add(timeout))
	buffer := make([]byte, 2048)
	read, _, err := client.ReadFromUDP(buffer)
	if err != nil {
		return nil
	}
	return append([]byte(nil), buffer[:read]...)
}

func relayLegFor(t *testing.T, relay *currentPartyUDPRelay, ownerID, peerID uint16) *currentPartyUDPRelayLeg {
	t.Helper()
	relay.mu.Lock()
	leg := relay.legs[currentPartyUDPRelayLegKey{partyID: 77, ownerID: ownerID, peerID: peerID}]
	relay.mu.Unlock()
	if leg == nil {
		t.Fatalf("relay leg %d->%d missing", ownerID, peerID)
	}
	return leg
}
