package dnfbridge

import (
	"bytes"
	"net"
	"testing"
	"time"
)

// r195SilentPeerConn keeps the existing bufferConn write capture while giving
// the relay fixture a real TCP RemoteAddr. syncRuntimePartyUDPRelay refuses to
// create bindings without an observed IPv4 endpoint.
type r195SilentPeerConn struct {
	bufferConn
	remote net.TCPAddr
}

func (c *r195SilentPeerConn) RemoteAddr() net.Addr { return &c.remote }

type r195SilentPeerFailConn struct {
	r195SilentPeerConn
	err error
}

func (c *r195SilentPeerFailConn) Write([]byte) (int, error) { return 0, c.err }

func r195SilentPeerFrames(t *testing.T, session *gameSession) []upperFrame {
	t.Helper()
	var buf *bytes.Buffer
	switch conn := session.conn.(type) {
	case *bufferConn:
		buf = &conn.write
	case *r195SilentPeerConn:
		buf = &conn.write
	case *r195SilentPeerFailConn:
		buf = &conn.write
	default:
		t.Fatalf("r195 silent peer fixture: unsupported conn %T", session.conn)
	}
	frames := splitUpperFrames(t, buf.Bytes())
	buf.Reset()
	return frames
}

func r195SilentPeerFixture(t *testing.T, mode partyPeerEndpointsAfterRosterMode) (*Service, *gameSession, *gameSession, *currentPartyUDPRelay, uint16) {
	t.Helper()
	service, leader, member, _, _ := changeHostFixture(t, 44, 0)
	leader.conn = &r195SilentPeerConn{remote: net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40101}}
	member.conn = &r195SilentPeerConn{remote: net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: 40102}}
	service.options.partyFormationOrder = partyFormationOrderVendorWire
	service.options.partyPeerEndpointsAfterRoster = mode
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 12, nil)
	if err != nil {
		t.Fatalf("create embedded relay: %v", err)
	}
	service.partyUDPRelay = relay
	t.Cleanup(func() { _ = relay.Close() })
	state := runtimePartyStateSnapshot(member)
	if state.PartyID <= 0 || len(runtimePartyMembers(state)) != 2 {
		t.Fatalf("runtime party fixture state=%+v members=%d, want 2", state, len(runtimePartyMembers(state)))
	}
	_ = r195SilentPeerFrames(t, leader)
	_ = r195SilentPeerFrames(t, member)
	return service, leader, member, relay, uint16(state.PartyID)
}

// Silent-peer TCP kicks were retired. Learning a one-way UDP endpoint must
// not inject formation frames into either connected game session.
func TestR195SilentUDPDoesNotInjectTCPFormation(t *testing.T) {
	service, leader, member, relay, partyID := r195SilentPeerFixture(t, partyPeerEndpointsAfterRosterRepeat)
	if !service.syncRuntimePartyUDPRelay(runtimePartyStateSnapshot(member)) {
		t.Fatal("relay sync failed")
	}
	ab, _ := testCurrentPartyUDPRelayPair(t, relay, partyID, leader.selectedCharacterID, member.selectedCharacterID)
	client := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer client.Close()
	_, err := client.WriteToUDP([]byte("native-handshake"), &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(ab.port)})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for ab.traffic.unlearned.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if ab.traffic.unlearned.Load() != 1 {
		t.Fatal("UDP payload not processed")
	}
	if len(r195SilentPeerFrames(t, member)) != 0 || len(r195SilentPeerFrames(t, leader)) != 0 {
		t.Fatal("silent UDP peer injected TCP formation")
	}
}
