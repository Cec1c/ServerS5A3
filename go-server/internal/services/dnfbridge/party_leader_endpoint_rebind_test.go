package dnfbridge

import (
	"bytes"
	"net"
	"testing"
	"time"
)

// Transport-only coverage: a legal source-port change must replace the learned
// endpoint without changing the advertised route. These are synthetic bytes,
// not native movement packets or an in-game reproduction.
func TestLeaderUDPRelayRelearnsSameIPSourcePort(t *testing.T) {
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 12, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	const partyID uint16 = 77
	if !relay.Sync(partyID, []currentPartyUDPRelayBinding{
		{characterID: inviteAcceptLeaderCharacterID, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: inviteAcceptFollowerCharacterID, generation: 1, address: [4]byte{127, 0, 0, 1}},
	}) {
		t.Fatal("could not create synthetic relay room")
	}
	routes := leaderUDPDraftCaptureEndpoints(t, relay, partyID)
	ab := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(routes.abPort)}
	ba := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(routes.baPort)}
	oldA := testCurrentPartyUDPClient(t, "127.0.0.1")
	newA := testCurrentPartyUDPClient(t, "127.0.0.1")
	peerB := testCurrentPartyUDPClient(t, "127.0.0.1")
	for _, client := range []*net.UDPConn{oldA, newA, peerB} {
		t.Cleanup(func() { _ = client.Close() })
	}
	if oldA.LocalAddr().String() == newA.LocalAddr().String() {
		t.Fatal("source ports must differ")
	}
	leaderUDPDraftRoundtrip(t, oldA, peerB, ab, ba, "before-rebind")

	// Delivery to B proves the read loop has learned the new source before the
	// reverse packet is sent. No direct assignment to relay state is involved.
	probe := []byte("synthetic-new-source")
	leaderUDPDraftExchange(t, peerB, func() error {
		_, err := newA.WriteToUDP(probe, ab)
		return err
	}, probe, ba.Port, "new source to peer")
	reverse := []byte("synthetic-reverse-after-rebind")
	leaderUDPDraftExchange(t, newA, func() error {
		_, err := peerB.WriteToUDP(reverse, ba)
		return err
	}, reverse, ab.Port, "peer to new source")
	leaderUDPDraftRequireEndpoints(t, relay, partyID, routes, "after source-port replacement")

	// Earlier handshake retries may remain queued at the old socket. Ignore
	// those, but reject any post-rebind payload throughout this bounded window.
	if err := oldA.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, currentPartyUDPRelayMaxDatagram)
	for {
		n, _, err := oldA.ReadFromUDP(buffer)
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(buffer[:n], reverse) {
			t.Fatal("post-rebind payload was also delivered to the old source port")
		}
	}
}
