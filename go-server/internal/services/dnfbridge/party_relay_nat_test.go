package dnfbridge

import (
	"bytes"
	"net"
	"testing"
	"time"
)

func TestPartyRelayFourMembersWithTwelveIndependentNATMappings(t *testing.T) {
	r, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 64, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Sync(77, r137Bindings(101, 102, 103, 104)) {
		t.Fatal("sync")
	}
	clients := make(map[currentPartyUDPRelayLegKey]*net.UDPConn)
	for key, leg := range r.legs {
		c := testCurrentPartyUDPClient(t, "127.0.0.1")
		defer c.Close()
		clients[key] = c
		if _, err = c.WriteToUDP([]byte{1}, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(leg.port)}); err != nil {
			t.Fatal(err)
		}
	}
	waitRelayCondition(t, time.Second, "all twelve mappings", func() bool {
		for key, c := range clients {
			if r.legs[key].learnedPort.Load() != uint32(c.LocalAddr().(*net.UDPAddr).Port) {
				return false
			}
		}
		return true
	})
	var buffer [128]byte
	for _, c := range clients {
		c.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		for {
			if _, _, err = c.ReadFromUDP(buffer[:]); err != nil {
				break
			}
		}
	}
	for key, c := range clients {
		leg := r.legs[key]
		opposite := leg.opposite.Load()
		target := clients[opposite.key]
		payload := []byte{byte(key.ownerID), byte(key.peerID), 0x51, 0x72}
		c.WriteToUDP(payload, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(leg.port)})
		target.SetReadDeadline(time.Now().Add(time.Second))
		n, source, e := target.ReadFromUDP(buffer[:])
		if e != nil || !bytes.Equal(buffer[:n], payload) || source.Port != int(opposite.port) {
			t.Fatalf("direction %v: payload=%x source=%v err=%v", key, buffer[:n], source, e)
		}
	}
	// Allocation is measured after setup and learning; every measured call
	// performs the actual UDP write through the production forwarding path.
	for _, leg := range r.legs {
		if allocations := testing.AllocsPerRun(100, func() { r.forwardDatagram(leg, leg.opposite.Load(), buffer[:4], time.Now()) }); allocations != 0 {
			t.Fatalf("forward allocations=%v", allocations)
		}
		break
	}
}

func TestPartyRelayNativeProbeDoesNotAuthorizeUnrelatedEndpoint(t *testing.T) {
	start := r137FreeRange(t)
	r := r137NewRelay(t, start, []int{start}, true, 0)
	a := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer a.Close()
	stranger := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer stranger.Close()
	sendOneByteNativeProbe(t, stranger, start, 1)
	if !r.Sync(77, r137Bindings(101, 102)) {
		t.Fatal("sync")
	}
	leg := relayLegFor(t, r, 101, 102)
	a.WriteToUDP([]byte("party-payload"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(leg.port)})
	stranger.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var buffer [128]byte
	if n, _, err := stranger.ReadFromUDP(buffer[:]); err == nil {
		t.Fatalf("unrelated endpoint received %q", buffer[:n])
	}
}
