package dnfbridge

import (
	"net"
	"testing"
	"time"
)

// Resume a payload captured by an old reader after both peers rejoin. A stale
// reader keeps its own retired socket and cannot use the replacement route.
func TestEmbeddedRelayStaleForwardMustNotReachRejoinedPair(t *testing.T) {
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 12, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	bindings := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}},
	}
	if !relay.Sync(77, bindings) {
		t.Fatal("first sync")
	}
	oldAB, oldBA := testCurrentPartyUDPRelayPair(t, relay, 77, 101, 202)
	relay.CloseParty(77)
	bindings[0].generation++
	bindings[1].generation++
	if !relay.Sync(77, bindings) {
		t.Fatal("rejoin sync")
	}
	newAB, newBA := testCurrentPartyUDPRelayPair(t, relay, 77, 101, 202)
	if oldAB == newAB || oldBA == newBA || !oldAB.retired.Load() || !oldBA.retired.Load() {
		t.Fatal("old generation was not retired")
	}
	client := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer client.Close()
	port := uint32(client.LocalAddr().(*net.UDPAddr).Port)
	oldBA.learnedPort.Store(port)
	newBA.learnedPort.Store(port)
	relay.forwardDatagram(oldAB, oldBA, []byte("stale-old-route"), time.Now())
	relay.forwardDatagram(oldAB, newBA, []byte("stale-new-route"), time.Now())
	if err := client.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var buf [128]byte
	if n, _, e := client.ReadFromUDP(buf[:]); e == nil {
		t.Fatalf("stale payload escaped: %q", buf[:n])
	} else if timeout, ok := e.(net.Error); !ok || !timeout.Timeout() {
		t.Fatal(e)
	}
	relay.forwardDatagram(newAB, newBA, []byte("current"), time.Now())
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n, from, err := client.ReadFromUDP(buf[:])
	if err != nil || string(buf[:n]) != "current" || from.Port != int(newBA.port) {
		t.Fatalf("current route failed: %q %v %v", buf[:n], from, err)
	}
	if oldAB.traffic.forwarded.Load() != 0 || newAB.traffic.forwarded.Load() != 1 {
		t.Fatal("generation forwarding counters mismatch")
	}
}
