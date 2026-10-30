package dnfbridge

import (
	"bytes"
	"net"
	"testing"
	"time"
)

const relayKnownSameIPReadWait = 300 * time.Millisecond

type relayKnownSameIPFixture struct {
	relay         *currentPartyUDPRelay
	clientOther   *net.UDPConn
	clientUnowned *net.UDPConn
	other         *net.UDPAddr
	unowned       *net.UDPAddr
	leg77AB       *currentPartyUDPRelayLeg
	leg77BA       *currentPartyUDPRelayLeg
}

// newRelayKnownSameIPFixture creates two independent two-member parties on
// the same relay. All authenticated bindings use 127.0.0.1, so the observed
// fallback addresses below differ by port rather than family or subnet.
func newRelayKnownSameIPFixture(t *testing.T) *relayKnownSameIPFixture {
	t.Helper()

	sourceConn := testCurrentPartyUDPClient(t, "127.0.0.1")
	t.Cleanup(func() { _ = sourceConn.Close() })
	clientOther := testCurrentPartyUDPClient(t, "127.0.0.1")
	t.Cleanup(func() { _ = clientOther.Close() })
	clientUnowned := testCurrentPartyUDPClient(t, "127.0.0.1")
	t.Cleanup(func() { _ = clientUnowned.Close() })

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
		t.Fatal("relay did not create party 77")
	}
	if !relay.Sync(88, []currentPartyUDPRelayBinding{
		{characterID: 303, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: 404, generation: 1, address: [4]byte{127, 0, 0, 1}},
	}) {
		t.Fatal("relay did not create party 88")
	}

	relay.mu.Lock()
	leg77AB := relay.legs[currentPartyUDPRelayLegKey{partyID: 77, ownerID: 101, peerID: 202}]
	leg77BA := relay.legs[currentPartyUDPRelayLegKey{partyID: 77, ownerID: 202, peerID: 101}]
	otherLeg := relay.legs[currentPartyUDPRelayLegKey{partyID: 88, ownerID: 404, peerID: 303}]
	relay.mu.Unlock()
	if leg77AB == nil || leg77BA == nil || otherLeg == nil {
		t.Fatalf("relay legs missing: 77AB=%p 77BA=%p 88-BA=%p", leg77AB, leg77BA, otherLeg)
	}

	source, ok := sourceConn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("source address type = %T, want *net.UDPAddr", sourceConn.LocalAddr())
	}
	other, ok := clientOther.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("other-party address type = %T, want *net.UDPAddr", clientOther.LocalAddr())
	}
	unowned, ok := clientUnowned.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("unowned address type = %T, want *net.UDPAddr", clientUnowned.LocalAddr())
	}

	leg77AB.mu.Lock()
	leg77AB.endpointKnown = true
	leg77AB.learnedEndpoint = cloneRelayKnownSameIPEndpoint(source)
	leg77AB.mu.Unlock()

	// A learned endpoint on this leg means member 404's real UDP endpoint is
	// already known to the relay through party 88.
	otherLeg.mu.Lock()
	otherLeg.endpointKnown = true
	otherLeg.learnedEndpoint = cloneRelayKnownSameIPEndpoint(other)
	otherLeg.mu.Unlock()

	if learned := leg77BA.learnedEndpointSnapshot(); learned != nil {
		t.Fatalf("party 77 target leg must start unlearned, got %v", learned)
	}

	return &relayKnownSameIPFixture{
		relay:         relay,
		clientOther:   clientOther,
		clientUnowned: clientUnowned,
		other:         cloneRelayKnownSameIPEndpoint(other),
		unowned:       cloneRelayKnownSameIPEndpoint(unowned),
		leg77AB:       leg77AB,
		leg77BA:       leg77BA,
	}
}

func cloneRelayKnownSameIPEndpoint(address *net.UDPAddr) *net.UDPAddr {
	if address == nil {
		return nil
	}
	return &net.UDPAddr{
		IP:   append(net.IP(nil), address.IP...),
		Port: address.Port,
		Zone: address.Zone,
	}
}

func readRelayKnownSameIPDatagram(t *testing.T, conn *net.UDPConn) ([]byte, bool) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(relayKnownSameIPReadWait)); err != nil {
		t.Fatalf("set receiver deadline: %v", err)
	}
	buffer := make([]byte, currentPartyUDPRelayMaxDatagram)
	read, err := conn.Read(buffer)
	if err != nil {
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			return nil, false
		}
		t.Fatalf("read loopback endpoint: %v", err)
	}
	return append([]byte(nil), buffer[:read]...), true
}

// TestEmbeddedRelayKnownSameIPEndpointOwnedByOtherPartyMustNotReceiveParty77Fanout
// pins that observed fallback cannot send a party 77 datagram to an endpoint
// already learned as a real endpoint of party 88, even when both use the same
// loopback IPv4 address. This is expected to fail against the current tree.
func TestEmbeddedRelayKnownSameIPEndpointOwnedByOtherPartyMustNotReceiveParty77Fanout(t *testing.T) {
	fixture := newRelayKnownSameIPFixture(t)
	fixture.relay.noteObservedProbeEndpoint(fixture.other, 0)

	payload := []byte("party77-owned-endpoint-probe")
	delivered, kinds, sourcePort := fixture.relay.fanOutUnlearnedDatagram(
		fixture.leg77AB,
		fixture.leg77BA,
		payload,
		false,
	)
	t.Logf("fanOut delivered=%t kinds=%q source_port=%d", delivered, kinds, sourcePort)

	got, received := readRelayKnownSameIPDatagram(t, fixture.clientOther)
	if received {
		t.Fatalf("party 88 endpoint %s received party 77 payload %q (fanOut=%t kinds=%q source_port=%d)", fixture.other, got, delivered, kinds, sourcePort)
	}
}

// TestEmbeddedRelayUnownedSameIPObservedEndpointRemainsEligibleForParty77Fanout
// preserves the current beacon-compatible positive path: an observed endpoint
// on the target's authenticated IPv4 remains deliverable while party 77's
// opposite leg is unlearned and no other party owns that endpoint.
func TestEmbeddedRelayUnownedSameIPObservedEndpointRemainsEligibleForParty77Fanout(t *testing.T) {
	fixture := newRelayKnownSameIPFixture(t)
	fixture.relay.noteObservedProbeEndpoint(fixture.unowned, 0)

	payload := []byte("party77-unowned-endpoint-positive")
	delivered, kinds, sourcePort := fixture.relay.fanOutUnlearnedDatagram(
		fixture.leg77AB,
		fixture.leg77BA,
		payload,
		false,
	)
	t.Logf("fanOut delivered=%t kinds=%q source_port=%d", delivered, kinds, sourcePort)
	if !delivered {
		t.Fatalf("unowned observed endpoint remained deliverable expectation failed: kinds=%q source_port=%d", kinds, sourcePort)
	}

	got, received := readRelayKnownSameIPDatagram(t, fixture.clientUnowned)
	if !received {
		t.Fatalf("unowned observed endpoint %s received no party 77 payload (kinds=%q source_port=%d)", fixture.unowned, kinds, sourcePort)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("unowned observed endpoint %s received %q, want %q", fixture.unowned, got, payload)
	}
}
