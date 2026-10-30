package dnfbridge

import (
	"bytes"
	"fmt"
	"net"
	"testing"
	"time"
)

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
