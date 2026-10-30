package dnfbridge

import (
	"bytes"
	"net"
	"testing"
	"time"
)

// TestEmbeddedRelayUnrelatedObservedEndpointMustNotReceivePartyPayload pins
// the boundary of the observed-endpoint fallback: an endpoint outside the
// opposite member's authenticated binding address must not receive that
// member's party datagram.
//
// This is an isolated loopback diagnostic. All sockets are created only when
// the test runs; no probe listener is started, so observed-endpoint delivery
// uses the opposite directed leg's socket as its fallback source.
func TestEmbeddedRelayUnrelatedObservedEndpointMustNotReceivePartyPayload(t *testing.T) {
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	t.Cleanup(func() { _ = clientA.Close() })

	clientOther := testCurrentPartyUDPClient(t, "127.0.0.2")
	t.Cleanup(func() { _ = clientOther.Close() })

	relay, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
		enabled:                true,
		advertiseIP:            "127.0.0.1",
		portStart:              testCurrentPartyUDPRelayPortStart(t),
		portCount:              12,
		probeListen:            false,
		seedForward:            true,
		observedEndpointFanout: true,
	})
	if err != nil {
		t.Fatalf("new UDP relay: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })

	if !relay.Sync(77, []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}},
	}) {
		t.Fatal("relay did not create a complete two-member room")
	}

	relay.mu.Lock()
	legAB := relay.legs[currentPartyUDPRelayLegKey{partyID: 77, ownerID: 101, peerID: 202}]
	legBA := relay.legs[currentPartyUDPRelayLegKey{partyID: 77, ownerID: 202, peerID: 101}]
	relay.mu.Unlock()
	if legAB == nil || legBA == nil {
		t.Fatalf("relay legs missing: AB=%p BA=%p", legAB, legBA)
	}

	sender, ok := clientA.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("client A address type = %T, want *net.UDPAddr", clientA.LocalAddr())
	}
	legAB.mu.Lock()
	legAB.endpointKnown = true
	legAB.learnedEndpoint = &net.UDPAddr{
		IP:   append(net.IP(nil), sender.IP...),
		Port: sender.Port,
	}
	legAB.mu.Unlock()
	if learned := legBA.learnedEndpointSnapshot(); learned != nil {
		t.Fatalf("BA must start unlearned, got %v", learned)
	}

	other, ok := clientOther.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("unrelated client address type = %T, want *net.UDPAddr", clientOther.LocalAddr())
	}
	relay.noteObservedProbeEndpoint(other, 0)

	payload := []byte("party77-payload")
	delivered, kinds, sourcePort := relay.fanOutUnlearnedDatagram(legAB, legBA, payload, false)
	t.Logf("fanOut delivered=%t kinds=%q source_port=%d", delivered, kinds, sourcePort)

	if err := clientOther.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("set unrelated receiver deadline: %v", err)
	}
	buffer := make([]byte, currentPartyUDPRelayMaxDatagram)
	read, err := clientOther.Read(buffer)
	if err != nil {
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			return
		}
		t.Fatalf("read unrelated loopback endpoint: %v", err)
	}
	got := append([]byte(nil), buffer[:read]...)
	if bytes.Equal(got, payload) {
		t.Fatalf("unrelated observed endpoint %s received party payload %q (fanOut=%t kinds=%q source_port=%d)", other, got, delivered, kinds, sourcePort)
	}
	t.Fatalf("unrelated observed endpoint %s received unexpected datagram %q", other, got)
}
