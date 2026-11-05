package dnfbridge

import (
	"net"
	"testing"
)

// The client is not emulated: loopback payloads assert transport continuity,
// while the real bridge projection asserts that no new formation was sent.
func TestRaidStartKeepsPreformedPartyUDPAndDoesNotRehandshake(t *testing.T) {
	f := newLeaderHandoffRoundtripFixture(t)
	lc := &tcpAddrBufferConn{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12001}}
	mc := &tcpAddrBufferConn{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12002}}
	f.leader.conn, f.follower.conn = lc, mc
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 12, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	f.service.partyUDPRelay = relay
	f.formPartyThroughClientAccept(t)
	formed := runtimePartyStateSnapshot(f.leader)
	id := uint16(formed.PartyID)
	endpoints := leaderUDPDraftCaptureEndpoints(t, relay, id)
	a, b := testCurrentPartyUDPClient(t, "127.0.0.1"), testCurrentPartyUDPClient(t, "127.0.0.1")
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	ab := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(endpoints.abPort)}
	ba := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(endpoints.baPort)}
	leaderUDPDraftRoundtrip(t, a, b, ab, ba, "before-raid-start")
	state := runtimeRaidState{RaidKey: 99, LeaderID: 99, CurrentLayout: true, Started: true,
		Members: []runtimeRaidMemberState{{CharID: 99, GroupIndex: 1, SlotOrder: 0}, {CharID: 100, GroupIndex: 1, SlotOrder: 1}}}
	lc.write.Reset()
	mc.write.Reset()
	for range 2 {
		if err := f.service.applyRuntimeRaidParties(state); err != nil {
			t.Fatal(err)
		}
		for _, session := range []*gameSession{f.leader, f.follower} {
			if got := runtimePartyStateSnapshot(session); got.PartyID != formed.PartyID {
				t.Fatalf("unchanged raid party replaced: %d -> %d", formed.PartyID, got.PartyID)
			}
		}
		leaderUDPDraftRequireEndpoints(t, relay, id, endpoints, "after-raid-start")
		leaderUDPDraftRoundtrip(t, a, b, ab, ba, "after-raid-start")
	}
	for _, data := range [][]byte{lc.write.Bytes(), mc.write.Bytes()} {
		for _, frame := range splitUpperFrames(t, data) {
			if frame.msgID == 11 {
				t.Fatal("unchanged raid party replayed op11 formation")
			}
		}
	}
	// A changed composition must still go through the normal replacement path.
	state.Members[1].GroupIndex = 2
	if err := f.service.applyRuntimeRaidParties(state); err != nil {
		t.Fatal(err)
	}
	left, right := runtimePartyStateSnapshot(f.leader), runtimePartyStateSnapshot(f.follower)
	if left.PartyID == right.PartyID || len(runtimePartyMembers(left)) != 1 || len(runtimePartyMembers(right)) != 1 {
		t.Fatal("changed raid groups were incorrectly preserved")
	}
}
