package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
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

func TestR195SilentPeerKickVendorWireRepeatFramesAndDirectedPort(t *testing.T) {
	service, leader, member, relay, partyID := r195SilentPeerFixture(t, partyPeerEndpointsAfterRosterRepeat)
	logger, logPath := newPeerEndpointTestLogger(t)
	service.packetLog = logger

	if !service.kickSilentPartyMember(partyID, member.selectedCharacterID) {
		t.Fatal("silent kick returned false")
	}
	frames := r195SilentPeerFrames(t, member)
	ids := frameMessageIDs(frames)
	wantIDs := []uint16{0x000b, 0x0009, 0x0099, 0x000b}
	if len(ids) != len(wantIDs) {
		t.Fatalf("silent kick frames=%v, want %v", ids, wantIDs)
	}
	for i := range wantIDs {
		if ids[i] != wantIDs[i] {
			t.Fatalf("silent kick frames=%v, want %v", ids, wantIDs)
		}
	}

	endpoints := peerEndpointFrameBodies(frames)
	if len(endpoints) != 2 {
		t.Fatalf("op11 frames=%d, want exactly 2", len(endpoints))
	}
	if !bytes.Equal(endpoints[0], endpoints[1]) {
		t.Fatalf("repeat op11 differs from bootstrap: first=% X second=% X", endpoints[0], endpoints[1])
	}
	if len(endpoints[0]) != 1+currentPartyPeerEndpointSize || endpoints[0][0] != 1 {
		t.Fatalf("op11 body len=%d peer_count=%d, want 23/1", len(endpoints[0]), endpoints[0][0])
	}

	gotPort := binary.BigEndian.Uint16(endpoints[0][11:13])
	_, wantPort, ok := relay.Endpoint(partyID, member.selectedCharacterID, leader.selectedCharacterID)
	if !ok {
		t.Fatal("relay has no directed member->leader leg")
	}
	if gotPort != wantPort {
		t.Fatalf("op11 directed port=%d, want %d", gotPort, wantPort)
	}
	_, reversePort, reverseOK := relay.Endpoint(partyID, leader.selectedCharacterID, member.selectedCharacterID)
	if reverseOK && gotPort == reversePort {
		t.Fatalf("op11 used reverse leg port %d; want member->leader port %d", gotPort, wantPort)
	}

	for _, frame := range r195SilentPeerFrames(t, leader) {
		if frame.msgID == 0x000b {
			t.Fatal("silent kick fanned an op11 to the leader; recovery must be receiver-only")
		}
	}

	logged := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"ordering=op11_then_op9_then_op153_then_op11_after_roster",
		"formation_order=vendor_wire",
		"after_roster=repeat",
		"source=udp_relay_reverse_leg_has_never_sent_a_datagram",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("silent kick log missing %q: %s", want, logged)
		}
	}
}

func TestR195SilentPeerKickRepeatOffKeepsSingleEndpointFrame(t *testing.T) {
	service, leader, member, _, partyID := r195SilentPeerFixture(t, partyPeerEndpointsAfterRosterOff)
	logger, logPath := newPeerEndpointTestLogger(t)
	service.packetLog = logger

	if !service.kickSilentPartyMember(partyID, member.selectedCharacterID) {
		t.Fatal("silent kick returned false")
	}
	ids := frameMessageIDs(r195SilentPeerFrames(t, member))
	wantIDs := []uint16{0x000b, 0x0009, 0x0099}
	if len(ids) != len(wantIDs) {
		t.Fatalf("silent kick repeat=off frames=%v, want %v", ids, wantIDs)
	}
	for i := range wantIDs {
		if ids[i] != wantIDs[i] {
			t.Fatalf("silent kick repeat=off frames=%v, want %v", ids, wantIDs)
		}
	}
	for _, frame := range r195SilentPeerFrames(t, leader) {
		if frame.msgID == 0x000b {
			t.Fatal("repeat=off kick fanned an op11 to the leader")
		}
	}
	logged := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"ordering=op11_then_op9_then_op153_single_op11_before_roster",
		"formation_order=vendor_wire",
		"after_roster=off",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("silent kick repeat=off log missing %q: %s", want, logged)
		}
	}
}

func TestR195SilentPeerKickWriteFailureReturnsFalseWithoutFanout(t *testing.T) {
	service, leader, member, _, partyID := r195SilentPeerFixture(t, partyPeerEndpointsAfterRosterRepeat)
	member.conn = &r195SilentPeerFailConn{
		r195SilentPeerConn: r195SilentPeerConn{remote: net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: 40102}},
		err:                errors.New("r195 injected silent kick write failure"),
	}
	_ = r195SilentPeerFrames(t, leader)
	_ = r195SilentPeerFrames(t, member)

	if service.kickSilentPartyMember(partyID, member.selectedCharacterID) {
		t.Fatal("silent kick returned true after write failure")
	}
	if frames := r195SilentPeerFrames(t, member); len(frames) != 0 {
		t.Fatalf("failed silent kick wrote %d frames to the silent member", len(frames))
	}
	if frames := r195SilentPeerFrames(t, leader); len(frames) != 0 {
		t.Fatalf("failed silent kick fanned %d frames to the leader", len(frames))
	}
}

func TestR195SilentPeerKickMissingPartyOrOfflineMemberReturnsFalse(t *testing.T) {
	service, leader, member, _, partyID := r195SilentPeerFixture(t, partyPeerEndpointsAfterRosterRepeat)
	_ = r195SilentPeerFrames(t, leader)
	_ = r195SilentPeerFrames(t, member)

	if service.kickSilentPartyMember(partyID+1, member.selectedCharacterID) {
		t.Fatal("silent kick returned true for a missing party")
	}
	if service.kickSilentPartyMember(partyID, uint16(65000)) {
		t.Fatal("silent kick returned true for an offline member")
	}
	if frames := r195SilentPeerFrames(t, member); len(frames) != 0 {
		t.Fatalf("boundary-negative silent kick wrote %d frames", len(frames))
	}
	if frames := r195SilentPeerFrames(t, leader); len(frames) != 0 {
		t.Fatalf("boundary-negative silent kick fanned %d frames", len(frames))
	}
}

func TestR195SilentPeerKickOrderingReflectsFormationAndRepeat(t *testing.T) {
	cases := []struct {
		name  string
		order partyFormationOrderMode
		mode  partyPeerEndpointsAfterRosterMode
		want  string
	}{
		{"vendor_wire_repeat", partyFormationOrderVendorWire, partyPeerEndpointsAfterRosterRepeat, "op11_then_op9_then_op153_then_op11_after_roster"},
		{"vendor_wire_off", partyFormationOrderVendorWire, partyPeerEndpointsAfterRosterOff, "op11_then_op9_then_op153_single_op11_before_roster"},
		{"vendor_repeat", partyFormationOrderVendor, partyPeerEndpointsAfterRosterRepeat, "op11_then_op153_then_op9_then_op11_after_roster"},
		{"legacy_off", partyFormationOrderLegacy, partyPeerEndpointsAfterRosterOff, "op153_then_op11_then_op9_single_op11_before_roster"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := silentPeerKickOrdering(tc.order, tc.mode); got != tc.want {
				t.Fatalf("silentPeerKickOrdering=%q want %q", got, tc.want)
			}
		})
	}
}
