package dnfbridge

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// The native official-24.9 completion callback consumes class1/op121 status.
// Each accepted request must finish with body [1]. Repeats and self-selection
// finish the pending callback without replaying the committed party roster.
func TestOfficial249ChangeHostCompletionAfterPublishedRoster(t *testing.T) {
	service, former, next, formerGeneration, nextGeneration := changeHostFixture(t, 10, 10)
	service.options.currentClientVersion = "official-24.9"

	if err := service.handleOnlineChangePartyHost(former, []byte{1}); err != nil {
		t.Fatalf("first op121: %v", err)
	}
	firstFormer := changeHostCompletionDrain(t, former)
	firstNext := changeHostCompletionDrain(t, next)
	changeHostCompletionRequireFinalSuccess(t, firstFormer)
	if !changeHostCompletionHasRoster(firstFormer) || !changeHostCompletionHasRoster(firstNext) {
		t.Fatalf("successful transfer omitted a member roster: former=%v next=%v", changeHostCompletionIDs(firstFormer), changeHostCompletionIDs(firstNext))
	}
	changeHostCompletionRequireNoOp121(t, firstNext)
	initial, found := service.runtimePartyManagerForService().SnapshotByUser(1002, nextGeneration)
	if !found || initial.Leader != 1002 || len(initial.Members) != 2 || initial.Members[0].Slot != 0 || initial.Members[1].Slot != 1 {
		t.Fatalf("first transfer changed fixed slots or missed leader: found=%t snapshot=%+v", found, initial)
	}

	if err := service.handleOnlineChangePartyHost(former, []byte{1}); err != nil {
		t.Fatalf("duplicate op121: %v", err)
	}
	changeHostCompletionRequireOnlySuccess(t, changeHostCompletionDrain(t, former))
	if got := changeHostCompletionDrain(t, next); len(got) != 0 {
		t.Fatalf("duplicate replayed roster to new leader: %v", changeHostCompletionIDs(got))
	}
	if after, found := service.runtimePartyManagerForService().SnapshotByUser(1001, formerGeneration); !found || after.ID != initial.ID || after.Leader != 1002 {
		t.Fatalf("duplicate changed party generation/leader: found=%t snapshot=%+v", found, after)
	}

	if err := service.handleOnlineChangePartyHost(next, []byte{1}); err != nil {
		t.Fatalf("new leader selecting self: %v", err)
	}
	changeHostCompletionRequireOnlySuccess(t, changeHostCompletionDrain(t, next))
	if got := changeHostCompletionDrain(t, former); len(got) != 0 {
		t.Fatalf("self-selection replayed roster to peer: %v", changeHostCompletionIDs(got))
	}
	if after, found := service.runtimePartyManagerForService().SnapshotByUser(1002, nextGeneration); !found || after.ID != initial.ID || after.Leader != 1002 {
		t.Fatalf("self-selection changed party generation/leader: found=%t snapshot=%+v", found, after)
	}

	// The former leader's other slot is not the previously committed request.
	if err := service.handleOnlineChangePartyHost(former, []byte{0}); err != nil {
		t.Fatalf("non-leader op121 rejection: %v", err)
	}
	changeHostCompletionRequireOnlyFailure(t, changeHostCompletionDrain(t, former))
	if got := changeHostCompletionDrain(t, next); len(got) != 0 {
		t.Fatalf("non-leader request wrote to leader: %v", changeHostCompletionIDs(got))
	}
}

// Older profiles retain the existing silent success and one-based index
// compatibility. The separate one-based test covers body [2] for a two-person
// party; this control checks that the new ACK is confined to official-24.9.
func TestChangeHostCompletionLegacyProfileRemainsSilent(t *testing.T) {
	service, former, next, _, _ := changeHostFixture(t, 10, 10)
	if err := service.handleOnlineChangePartyHost(former, []byte{1}); err != nil {
		t.Fatal(err)
	}
	changeHostCompletionRequireNoOp121(t, changeHostCompletionDrain(t, former))
	changeHostCompletionRequireNoOp121(t, changeHostCompletionDrain(t, next))
	if err := service.handleOnlineChangePartyHost(former, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if got := changeHostCompletionDrain(t, former); len(got) != 0 {
		t.Fatalf("legacy duplicate was not silent: %v", changeHostCompletionIDs(got))
	}
	if err := service.handleOnlineChangePartyHost(next, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if got := changeHostCompletionDrain(t, next); len(got) != 0 {
		t.Fatalf("legacy self-selection was not silent: %v", changeHostCompletionIDs(got))
	}
}

// Leaving fixed slot 1 produces 0/2/3. Official op121 [1] must fail rather
// than selecting the sorted roster's second member (slot 2). The full four-way
// relay matrix also remains stable across a valid slot-3 transfer.
func TestOfficial249ChangeHostRejectsHoleAndOutOfRangeWithoutReroutingFourPeers(t *testing.T) {
	service, first, second, _, _ := changeHostFixture(t, 10, 10)
	service.options.currentClientVersion = "official-24.9"
	sessions := map[uint16]*gameSession{1001: first, 1002: second}
	manager := service.runtimePartyManagerForService()
	initial, found := manager.SnapshotByUser(1001, 0)
	if !found {
		t.Fatal("initial two-person party missing")
	}
	for _, id := range []uint16{1003, 1004} {
		peer := &gameSession{conn: &bufferConn{}, channel: first.channel, selectedCharacterID: id, townActorOwnerChannel: 10, connectionTownActorOwnerChannel: 10}
		service.bindGameSessionCharacter(peer, id)
		identity, bound := service.boundGameSessionCharacterSnapshot(peer)
		if !bound {
			t.Fatalf("char %d not bound", id)
		}
		joined := manager.Join(initial.ID, party.RuntimePartyMember{
			UserID: id, SessionGeneration: identity.generation,
			State: alignedcmd.PartyMemberState{UserID: id, UserState: 1, HPPercent: 100, MPPercent: 100},
		})
		if !joined.OK {
			t.Fatalf("char %d join: %+v", id, joined)
		}
		service.publishRuntimePartySnapshot(joined.Party)
		service.onlinePlayers.EnterArea(&onlinePlayerInfo{
			CharacterID: id, AccountID: fmt.Sprint(id), Name: "peer", Job: 1, Level: 50,
			TownID: 38, AreaID: 2, ChannelID: 10, Session: peer,
		})
		sessions[id] = peer
	}
	for id, session := range sessions {
		session.conn = &tcpAddrBufferConn{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(12000 + id)}}
	}
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 24, nil)
	if err != nil {
		t.Fatalf("create relay: %v", err)
	}
	t.Cleanup(func() {
		if err := relay.Close(); err != nil {
			t.Errorf("close relay: %v", err)
		}
	})
	service.mu.Lock()
	service.partyUDPRelay = relay
	service.mu.Unlock()
	complete, found := manager.SnapshotByID(initial.ID)
	if !found || len(complete.Members) != 4 {
		t.Fatalf("four-person manager roster missing: found=%t snapshot=%+v", found, complete)
	}
	state := service.publishRuntimePartySnapshot(complete)
	if !service.syncRuntimePartyUDPRelay(state) {
		t.Fatal("four-person relay sync failed")
	}
	ids := []uint16{1001, 1002, 1003, 1004}
	before := changeHostCompletionEndpoints(t, relay, initial.ID, ids)
	for _, session := range sessions {
		changeHostCompletionDrain(t, session)
	}

	if err := service.handleOnlineChangePartyHost(first, []byte{3}); err != nil {
		t.Fatalf("valid four-person slot-3 transfer: %v", err)
	}
	changeHostCompletionRequireFinalSuccess(t, changeHostCompletionDrain(t, first))
	for _, id := range []uint16{1002, 1003, 1004} {
		changeHostCompletionRequireNoOp121(t, changeHostCompletionDrain(t, sessions[id]))
	}
	changeHostCompletionRequireEndpoints(t, relay, initial.ID, ids, before)
	transferred, found := manager.SnapshotByUser(1004, 0)
	if !found || transferred.Leader != 1004 || transferred.ID != initial.ID || len(transferred.Members) != 4 {
		t.Fatalf("four-person transfer: found=%t snapshot=%+v", found, transferred)
	}

	if err := service.handleOnlineLeaveParty(second, uint16(dnfenum.CmdPacketLeaveParty)); err != nil {
		t.Fatalf("slot-1 op13 leave: %v", err)
	}
	for _, session := range sessions {
		changeHostCompletionDrain(t, session)
	}
	survivorIDs := []uint16{1001, 1003, 1004}
	survivors := changeHostCompletionEndpoints(t, relay, initial.ID, survivorIDs)
	for leg, endpoint := range survivors {
		if before[leg] != endpoint {
			t.Fatalf("survivor endpoint %+v changed after leave: %v -> %v", leg, before[leg], endpoint)
		}
	}
	for _, id := range survivorIDs {
		snapshot, ok := manager.SnapshotByUser(id, 0)
		if !ok || snapshot.Leader != 1004 || runtimePartySlotList(snapshot) != "0:1001,2:1003,3:1004" {
			t.Fatalf("slot-1 hole was compressed: char=%d found=%t snapshot=%+v", id, ok, snapshot)
		}
	}
	for _, missing := range []byte{1, 255} {
		if err := service.handleOnlineChangePartyHost(sessions[1004], []byte{missing}); err != nil {
			t.Fatalf("missing fixed slot %d: %v", missing, err)
		}
		changeHostCompletionRequireOnlyFailure(t, changeHostCompletionDrain(t, sessions[1004]))
		for _, id := range []uint16{1001, 1003} {
			if got := changeHostCompletionDrain(t, sessions[id]); len(got) != 0 {
				t.Fatalf("invalid slot %d repainted char %d: %v", missing, id, changeHostCompletionIDs(got))
			}
		}
		unchanged, ok := manager.SnapshotByUser(1004, 0)
		if !ok || unchanged.ID != initial.ID || unchanged.Leader != 1004 {
			t.Fatalf("invalid slot %d changed leader: found=%t snapshot=%+v", missing, ok, unchanged)
		}
		changeHostCompletionRequireEndpoints(t, relay, initial.ID, survivorIDs, survivors)
	}
	if err := service.handleOnlineChangePartyHost(sessions[1004], []byte{2}); err != nil {
		t.Fatalf("valid fixed slot 2 after slot-1 hole: %v", err)
	}
	changeHostCompletionRequireFinalSuccess(t, changeHostCompletionDrain(t, sessions[1004]))
	for _, id := range []uint16{1001, 1003} {
		changeHostCompletionRequireNoOp121(t, changeHostCompletionDrain(t, sessions[id]))
	}
	if sparse, ok := manager.SnapshotByUser(1003, 0); !ok || sparse.ID != initial.ID || sparse.Leader != 1003 || runtimePartySlotList(sparse) != "0:1001,2:1003,3:1004" {
		t.Fatalf("valid sparse-slot transfer changed party/slots: found=%t snapshot=%+v", ok, sparse)
	}
	changeHostCompletionRequireEndpoints(t, relay, initial.ID, survivorIDs, survivors)
}

// A committed transfer must finish publishing the remaining members and ACK
// its requester even when one receiver's socket, or a town observer, fails.
// A repeat must only complete its own pending request, without retrying the
// failed projection or repainting healthy clients.
func TestOfficial249ChangeHostCommittedTransferIsolatesPublicationFailures(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		failedMember uint16
		observer     bool
	}{
		{name: "first_member", failedMember: 1001},
		{name: "middle_member", failedMember: 1002},
		{name: "town_observer", observer: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			service, first, second, _, _ := changeHostFixture(t, 10, 10)
			service.options.currentClientVersion = "official-24.9"
			sessions := map[uint16]*gameSession{1001: first, 1002: second}
			manager := service.runtimePartyManagerForService()
			initial, found := manager.SnapshotByUser(1001, 0)
			if !found {
				t.Fatal("initial runtime party missing")
			}
			for _, id := range []uint16{1003, 1004} {
				peer := &gameSession{conn: &bufferConn{}, channel: first.channel, selectedCharacterID: id, townActorOwnerChannel: 10, connectionTownActorOwnerChannel: 10}
				service.bindGameSessionCharacter(peer, id)
				identity, bound := service.boundGameSessionCharacterSnapshot(peer)
				if !bound {
					t.Fatalf("char %d not bound", id)
				}
				joined := manager.Join(initial.ID, party.RuntimePartyMember{
					UserID: id, SessionGeneration: identity.generation,
					State: alignedcmd.PartyMemberState{UserID: id, UserState: 1, HPPercent: 100, MPPercent: 100},
				})
				if !joined.OK {
					t.Fatalf("char %d join: %+v", id, joined)
				}
				service.publishRuntimePartySnapshot(joined.Party)
				service.onlinePlayers.EnterArea(&onlinePlayerInfo{
					CharacterID: id, AccountID: fmt.Sprint(id), Name: "peer", Job: 1, Level: 50,
					TownID: 38, AreaID: 2, ChannelID: 10, Session: peer,
				})
				sessions[id] = peer
			}
			caller, selectedSlot, leaderID := first, byte(3), uint16(1004)
			if scenario.failedMember == 1001 {
				// Keep the requester healthy while failing the first roster member.
				if err := service.handleOnlineChangePartyHost(first, []byte{2}); err != nil {
					t.Fatalf("prepare first-member failure: %v", err)
				}
				caller = sessions[1003]
			}
			for _, session := range sessions {
				changeHostCompletionDrain(t, session)
			}

			failed := &changeHostCompletionFailConn{err: errors.New("injected change-host projection write failure")}
			if scenario.observer {
				observer := &gameSession{conn: failed, channel: first.channel, selectedCharacterID: 900, townActorOwnerChannel: 10, connectionTownActorOwnerChannel: 10}
				service.bindGameSessionCharacter(observer, 900)
				service.onlinePlayers.EnterArea(&onlinePlayerInfo{
					CharacterID: 900, AccountID: "observer", Name: "observer", Job: 1, Level: 50,
					TownID: 38, AreaID: 2, ChannelID: 10, Session: observer,
				})
			} else {
				sessions[scenario.failedMember].conn = failed
			}
			if err := service.handleOnlineChangePartyHost(caller, []byte{selectedSlot}); err != nil {
				t.Fatalf("committed transfer returned peer publication error: %v", err)
			}
			if failed.writes == 0 {
				t.Fatal("injected failing connection was not written")
			}
			committed, found := manager.SnapshotByUser(leaderID, 0)
			if !found || committed.ID != initial.ID || committed.Leader != leaderID || runtimePartySlotList(committed) != "0:1001,1:1002,2:1003,3:1004" {
				t.Fatalf("failed publication changed committed roster: found=%t snapshot=%+v", found, committed)
			}
			for id, session := range sessions {
				if id == scenario.failedMember {
					continue
				}
				packets := changeHostCompletionDrain(t, session)
				changeHostCompletionRequireRealtime(t, packets, committed.StateFor(id))
				if session == caller {
					changeHostCompletionRequireFinalSuccess(t, packets)
				} else {
					changeHostCompletionRequireNoOp121(t, packets)
				}
			}
			writesBeforeRepeat := failed.writes
			if err := service.handleOnlineChangePartyHost(caller, []byte{selectedSlot}); err != nil {
				t.Fatalf("repeat after failed projection: %v", err)
			}
			changeHostCompletionRequireOnlySuccess(t, changeHostCompletionDrain(t, caller))
			for id, session := range sessions {
				if session == caller || id == scenario.failedMember {
					continue
				}
				if packets := changeHostCompletionDrain(t, session); len(packets) != 0 {
					t.Fatalf("repeat repainted healthy char %d: %v", id, changeHostCompletionIDs(packets))
				}
			}
			if failed.writes != writesBeforeRepeat {
				t.Fatalf("repeat retried failed projection: writes=%d -> %d", writesBeforeRepeat, failed.writes)
			}
		})
	}
}

func TestOfficial249ChangeHostCommittedTransferReturnsOwnACKError(t *testing.T) {
	service, former, next, _, nextGeneration := changeHostFixture(t, 10, 10)
	service.options.currentClientVersion = "official-24.9"
	ackErr := errors.New("injected change-host ACK write failure")
	conn := &changeHostCompletionACKFailConn{t: t, err: ackErr, fail: true}
	former.conn = conn
	if err := service.handleOnlineChangePartyHost(former, []byte{1}); !errors.Is(err, ackErr) {
		t.Fatalf("committed transfer returned %v, want own ACK error", err)
	}
	if conn.ackAttempts != 1 {
		t.Fatalf("ACK attempts=%d, want one", conn.ackAttempts)
	}
	first := changeHostCompletionDrain(t, former)
	if !changeHostCompletionHasRoster(first) {
		t.Fatalf("requester missed roster before failed ACK: %v", changeHostCompletionIDs(first))
	}
	changeHostCompletionRequireNoOp121(t, first)
	if peer := changeHostCompletionDrain(t, next); !changeHostCompletionHasRoster(peer) {
		t.Fatalf("new leader missed committed roster: %v", changeHostCompletionIDs(peer))
	}
	if snapshot, found := service.runtimePartyManagerForService().SnapshotByUser(1002, nextGeneration); !found || snapshot.Leader != 1002 {
		t.Fatalf("ACK write error undid committed transfer: found=%t snapshot=%+v", found, snapshot)
	}
	conn.fail = false
	if err := service.handleOnlineChangePartyHost(former, []byte{1}); err != nil {
		t.Fatalf("repeat after own ACK error: %v", err)
	}
	changeHostCompletionRequireOnlySuccess(t, changeHostCompletionDrain(t, former))
	if peer := changeHostCompletionDrain(t, next); len(peer) != 0 {
		t.Fatalf("repeat after ACK error repainted leader: %v", changeHostCompletionIDs(peer))
	}
}

type changeHostCompletionFailConn struct {
	bufferConn
	writes int
	err    error
}

type changeHostCompletionACKFailConn struct {
	bufferConn
	t           *testing.T
	err         error
	fail        bool
	ackAttempts int
}

func (conn *changeHostCompletionACKFailConn) Write(data []byte) (int, error) {
	packet, _ := splitGameServerUpperPacket(conn.t, data)
	if packet.Header.MsgID == uint16(dnfenum.CmdPacketChangeHost) {
		conn.ackAttempts++
		if conn.fail {
			return 0, conn.err
		}
	}
	return conn.bufferConn.Write(data)
}

func (conn *changeHostCompletionFailConn) Write(_ []byte) (int, error) {
	conn.writes++
	return 0, conn.err
}

func changeHostCompletionRequireRealtime(t *testing.T, packets []dnfproto.ChannelPacket, state alignedcmd.PartyState) {
	t.Helper()
	want := runtimePartyRealtimeInfoBody(state)
	for _, packet := range packets {
		if packet.Header.MsgID == 153 && bytes.Equal(packet.Body, want) {
			return
		}
	}
	t.Fatalf("healthy member %d missed committed realtime roster: packets=%v", state.UserID, changeHostCompletionIDs(packets))
}

func changeHostCompletionDrain(t *testing.T, session *gameSession) []dnfproto.ChannelPacket {
	t.Helper()
	var data []byte
	switch conn := session.conn.(type) {
	case *bufferConn:
		data = append([]byte(nil), conn.write.Bytes()...)
		conn.write.Reset()
	case *tcpAddrBufferConn:
		data = append([]byte(nil), conn.write.Bytes()...)
		conn.write.Reset()
	case *changeHostCompletionACKFailConn:
		data = append([]byte(nil), conn.write.Bytes()...)
		conn.write.Reset()
	default:
		t.Fatalf("unsupported completion test connection %T", session.conn)
	}
	var packets []dnfproto.ChannelPacket
	for len(data) > 0 {
		packet, rest := splitGameServerUpperPacket(t, data)
		packets = append(packets, packet)
		data = rest
	}
	return packets
}

func changeHostCompletionIDs(packets []dnfproto.ChannelPacket) []uint16 {
	ids := make([]uint16, 0, len(packets))
	for _, packet := range packets {
		ids = append(ids, packet.Header.MsgID)
	}
	return ids
}

func changeHostCompletionHasRoster(packets []dnfproto.ChannelPacket) bool {
	for _, packet := range packets {
		if packet.Header.MsgID == 9 || packet.Header.MsgID == 153 {
			return true
		}
	}
	return false
}

func changeHostCompletionRequireFinalSuccess(t *testing.T, packets []dnfproto.ChannelPacket) {
	t.Helper()
	if len(packets) < 2 {
		t.Fatalf("transfer lacks roster followed by ACK: %v", changeHostCompletionIDs(packets))
	}
	last := packets[len(packets)-1]
	if last.Header.Classification != dnfproto.DefaultChannelClassification || last.Header.MsgID != uint16(dnfenum.CmdPacketChangeHost) || !bytes.Equal(last.Body, []byte{1}) {
		t.Fatalf("final transfer packet class=%d id=%d body=%x, want class1/op121 [1]", last.Header.Classification, last.Header.MsgID, last.Body)
	}
	for _, packet := range packets[:len(packets)-1] {
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketChangeHost) {
			t.Fatalf("op121 ACK preceded roster: %v", changeHostCompletionIDs(packets))
		}
	}
}

func changeHostCompletionRequireOnlySuccess(t *testing.T, packets []dnfproto.ChannelPacket) {
	t.Helper()
	if len(packets) != 1 {
		t.Fatalf("repeat/self frames=%v, want only op121 ACK", changeHostCompletionIDs(packets))
	}
	packet := packets[0]
	if packet.Header.Classification != dnfproto.DefaultChannelClassification || packet.Header.MsgID != uint16(dnfenum.CmdPacketChangeHost) || !bytes.Equal(packet.Body, []byte{1}) {
		t.Fatalf("repeat/self ACK class=%d id=%d body=%x", packet.Header.Classification, packet.Header.MsgID, packet.Body)
	}
}

func changeHostCompletionRequireOnlyFailure(t *testing.T, packets []dnfproto.ChannelPacket) {
	t.Helper()
	if len(packets) != 1 {
		t.Fatalf("rejected op121 frames=%v, want one failure", changeHostCompletionIDs(packets))
	}
	packet := packets[0]
	if packet.Header.Classification != dnfproto.DefaultChannelClassification || packet.Header.MsgID != uint16(dnfenum.CmdPacketChangeHost) || !bytes.Equal(packet.Body, []byte{0, partyCommandFailureGeneric}) {
		t.Fatalf("rejected op121 class=%d id=%d body=%x", packet.Header.Classification, packet.Header.MsgID, packet.Body)
	}
}

func changeHostCompletionRequireNoOp121(t *testing.T, packets []dnfproto.ChannelPacket) {
	t.Helper()
	for _, packet := range packets {
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketChangeHost) {
			t.Fatalf("peer received requester's op121 completion: %v", changeHostCompletionIDs(packets))
		}
	}
}

type changeHostCompletionLeg struct{ owner, peer uint16 }
type changeHostCompletionEndpoint struct {
	address [4]byte
	port    uint16
}

func changeHostCompletionEndpoints(t *testing.T, relay *currentPartyUDPRelay, partyID uint16, ids []uint16) map[changeHostCompletionLeg]changeHostCompletionEndpoint {
	t.Helper()
	result := make(map[changeHostCompletionLeg]changeHostCompletionEndpoint, len(ids)*(len(ids)-1))
	ports := make(map[uint16]bool)
	for _, owner := range ids {
		for _, peer := range ids {
			if owner == peer {
				continue
			}
			address, port, ok := relay.Endpoint(partyID, owner, peer)
			if !ok || address != [4]byte{127, 0, 0, 1} || port == 0 || ports[port] {
				t.Fatalf("invalid/duplicate directed endpoint %d->%d: address=%v port=%d ok=%t", owner, peer, address, port, ok)
			}
			ports[port] = true
			result[changeHostCompletionLeg{owner, peer}] = changeHostCompletionEndpoint{address, port}
		}
	}
	if len(result) != len(ids)*(len(ids)-1) {
		t.Fatalf("directed endpoints=%d want=%d", len(result), len(ids)*(len(ids)-1))
	}
	return result
}

func changeHostCompletionRequireEndpoints(t *testing.T, relay *currentPartyUDPRelay, partyID uint16, ids []uint16, want map[changeHostCompletionLeg]changeHostCompletionEndpoint) {
	t.Helper()
	got := changeHostCompletionEndpoints(t, relay, partyID, ids)
	for leg, endpoint := range want {
		if got[leg] != endpoint {
			t.Fatalf("directed endpoint %+v changed: %v -> %v", leg, endpoint, got[leg])
		}
	}
}
