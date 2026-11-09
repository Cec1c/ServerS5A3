package dnfbridge

import (
	"net"
	"reflect"
	"testing"
	"time"
)

func TestAntonSecondPhaseKeepsLivePartyAndUDP(t *testing.T) {
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
	before, found := f.service.runtimePartyManagerForService().SnapshotByUser(99, 0)
	if !found {
		t.Fatal("live party missing")
	}
	endpoints := leaderUDPDraftCaptureEndpoints(t, relay, before.ID)
	a, b := testCurrentPartyUDPClient(t, "127.0.0.1"), testCurrentPartyUDPClient(t, "127.0.0.1")
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	ab := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(endpoints.abPort)}
	ba := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(endpoints.baPort)}
	leaderUDPDraftRoundtrip(t, a, b, ab, ba, "before-phase-two")
	// Historical raid seats need not match a freely formed ordinary party.
	// Beginning the next phase must not recreate the old grouping.
	state := runtimeRaidState{RaidKey: 99, LeaderID: 99, CurrentLayout: true, Started: true,
		Battle:  &runtimeRaidBattle{Mode: 1, Phase: 1},
		Members: []runtimeRaidMemberState{{CharID: 99, GroupIndex: 1, SlotOrder: 0}, {CharID: 100, GroupIndex: 2, SlotOrder: 0}}}
	lc.write.Reset()
	mc.write.Reset()
	if err := f.service.applyRuntimeRaidParties(state); err != nil {
		t.Fatal(err)
	}
	after, found := f.service.runtimePartyManagerForService().SnapshotByUser(99, 0)
	if !found || !reflect.DeepEqual(before, after) {
		t.Fatalf("second phase replaced the live party: before=%+v after=%+v", before, after)
	}
	if lc.write.Len() != 0 || mc.write.Len() != 0 {
		t.Fatal("second phase replayed ordinary-party formation/roster packets")
	}
	leaderUDPDraftRequireEndpoints(t, relay, before.ID, endpoints, "after-phase-two")
	leaderUDPDraftRoundtrip(t, a, b, ab, ba, "after-phase-two")
}

func TestRealPVFAntonSecondPhaseStartDoesNotReformParty(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	enterRaidStartWaitingFixture(t, f, f.state.Battle.InstanceID)
	flow := f.state.Battle.RewardFlow[0]
	for _, delay := range []time.Duration{0, 20 * time.Second, 40 * time.Second} {
		if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(delay), zeroAntonRoll); len(errs) != 0 {
			t.Fatal(errs)
		}
	}
	before, found := f.service.runtimePartyManagerForService().SnapshotByUser(19, 0)
	if !found {
		t.Fatal("live ordinary party missing")
	}
	f.state.Members[1].GroupIndex = 2
	f.state.Members[1].SlotOrder = 0
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 666, nil)
	ack, _ := splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if ack.Header.MsgID != 666 || len(ack.Body) != 1 || ack.Body[0] != 1 || f.state.Battle.Phase != 1 {
		t.Fatal("legitimate phase-two start did not advance")
	}
	after, found := f.service.runtimePartyManagerForService().SnapshotByUser(19, 0)
	if !found || !reflect.DeepEqual(before, after) {
		t.Fatalf("start handler recreated historical groups: before=%+v after=%+v", before, after)
	}
	for _, frame := range splitUpperFrames(t, f.conn.write.Bytes()) {
		if frame.msgID == 7 || frame.msgID == 11 {
			t.Fatalf("phase-two start replayed party formation opcode %d", frame.msgID)
		}
	}
}
