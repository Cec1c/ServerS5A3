package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func r169ResetDungeonPeerProjection(session *gameSession) {
	session.dungeonPartyRunMu.Lock()
	session.dungeonPeerProjected = make(map[uint16]boundGameSessionCharacter, 4)
	session.dungeonPartyRunMu.Unlock()
}

func r169HasDungeonPeerProjection(session *gameSession, peer uint16) bool {
	session.dungeonPartyRunMu.Lock()
	defer session.dungeonPartyRunMu.Unlock()
	_, found := session.dungeonPeerProjected[peer]
	return found
}

func r169SendExistingEncounterRequest(t *testing.T, service *Service, sender, target *gameSession) {
	t.Helper()
	body := make([]byte, 13)
	binary.LittleEndian.PutUint16(body, target.selectedCharacterID)
	r134N1Send(t, service, sender, dnfenum.CmdPacketRequestPeer, body)
}

func r169FiendWarRequiresActorProjection(t *testing.T, peer *gameSession, actorID uint16) {
	t.Helper()
	frames := splitUpperFrames(t, peer.conn.(*partyDungeonSyncConn).frames())
	mode0, mode1, op9 := r160UpperPeerActorCounts(frames, actorID)
	if mode0 == 0 || mode1 == 0 || op9 == 0 {
		t.Fatalf("receiver=%d actor=%d missing mode0/mode1/op9: %d/%d/%d",
			peer.selectedCharacterID, actorID, mode0, mode1, op9)
	}
}

func r169FiendWarHasNoPeerActorModel(t *testing.T, peer *gameSession, actorID uint16) {
	t.Helper()
	frames := splitUpperFrames(t, peer.conn.(*partyDungeonSyncConn).frames())
	mode0, mode1, _ := r160UpperPeerActorCounts(frames, actorID)
	if mode0 != 0 || mode1 != 0 {
		t.Fatalf("receiver=%d actor=%d unexpectedly projected model: %d/%d",
			peer.selectedCharacterID, actorID, mode0, mode1)
	}
}

func r169ForbidHandshakePackets(t *testing.T, peer *gameSession) {
	t.Helper()
	for _, packet := range splitAllGameServerUpperPackets(t, peer.conn.(*partyDungeonSyncConn).frames()) {
		if packet.Header.Classification == 0 && (packet.Header.MsgID == 7 || packet.Header.MsgID == 8 || packet.Header.MsgID == 11) {
			t.Fatalf("recovery replayed class0/op%d to receiver %d", packet.Header.MsgID, peer.selectedCharacterID)
		}
	}
}

// Repeating an already admitted and fully ready encounter must restore both
// actor directions through the normal idempotent projection, after op153/op9.
func TestR169FiendWarExistingEncounterRestoresBothPeerActorsIdempotently(t *testing.T) {
	f, p := r159ActiveEncounter(t)
	s := f.service
	beforeBattle := cloneRuntimeRaidBattle(f.state.Battle)
	beforeAssets := r159Assets(t, f, p)
	beforeParty := s.runtimePartyStateForSession(p[0])
	runs := []*runtimeDungeonState{p[0].dungeon.runtime, p[1].dungeon.runtime}
	keys := []dungeonPartyRunKey{p[0].dungeonPartyRun, p[1].dungeonPartyRun}
	for _, peer := range p {
		r169ResetDungeonPeerProjection(peer)
		r134N1Reset(peer)
	}

	r169SendExistingEncounterRequest(t, s, p[0], p[1])
	for i, peer := range p {
		r169FiendWarRequiresActorProjection(t, peer, p[1-i].selectedCharacterID)
		r169ForbidHandshakePackets(t, peer)
	}
	if !r169HasDungeonPeerProjection(p[0], p[1].selectedCharacterID) ||
		!r169HasDungeonPeerProjection(p[1], p[0].selectedCharacterID) {
		t.Fatal("a successful actor projection did not claim both peer bindings")
	}
	if !reflect.DeepEqual(beforeBattle, cloneRuntimeRaidBattle(f.state.Battle)) ||
		!reflect.DeepEqual(beforeParty, s.runtimePartyStateForSession(p[0])) ||
		!bytes.Equal(beforeAssets, r159Assets(t, f, p)) ||
		p[0].dungeon.runtime != runs[0] || p[1].dungeon.runtime != runs[1] ||
		p[0].dungeonPartyRun != keys[0] || p[1].dungeonPartyRun != keys[1] {
		t.Fatal("actor recovery changed membership, run/scene or persisted assets")
	}

	for _, peer := range p {
		r134N1Reset(peer)
	}
	r169SendExistingEncounterRequest(t, s, p[0], p[1])
	for i, peer := range p {
		r169FiendWarHasNoPeerActorModel(t, peer, p[1-i].selectedCharacterID)
		r169ForbidHandshakePackets(t, peer)
	}
}

func TestR169FiendWarExistingEncounterDifferentRunDoesNotProjectPeers(t *testing.T) {
	f, p := r159ActiveEncounter(t)
	s := f.service
	p[1].dungeonPartyRunMu.Lock()
	originalKey := p[1].dungeonPartyRun
	p[1].dungeonPartyRun.Seed++
	p[1].dungeonPartyRunMu.Unlock()
	t.Cleanup(func() {
		p[1].dungeonPartyRunMu.Lock()
		p[1].dungeonPartyRun = originalKey
		p[1].dungeonPartyRunMu.Unlock()
	})
	for _, peer := range p {
		r169ResetDungeonPeerProjection(peer)
		r134N1Reset(peer)
	}
	beforeBattle := cloneRuntimeRaidBattle(f.state.Battle)
	beforeParty := s.runtimePartyStateForSession(p[0])

	r169SendExistingEncounterRequest(t, s, p[0], p[1])
	for i, peer := range p {
		r169FiendWarHasNoPeerActorModel(t, peer, p[1-i].selectedCharacterID)
		r169ForbidHandshakePackets(t, peer)
	}
	if r169HasDungeonPeerProjection(p[0], p[1].selectedCharacterID) ||
		r169HasDungeonPeerProjection(p[1], p[0].selectedCharacterID) {
		t.Fatal("a different run acquired an actor projection claim")
	}
	if !reflect.DeepEqual(beforeBattle, cloneRuntimeRaidBattle(f.state.Battle)) ||
		!reflect.DeepEqual(beforeParty, s.runtimePartyStateForSession(p[0])) {
		t.Fatal("different-run rejection mutated membership or rewards")
	}
}

type r169FailPeerMode1WriteConn struct {
	net.Conn
	failed bool
}

func (connection *r169FailPeerMode1WriteConn) Write(data []byte) (int, error) {
	packet, err := dnfproto.ParseChannelPacket(data)
	if err == nil && packet.Header.Classification == 0 && packet.Header.MsgID == uint16(dnfenum.CmdPacketSetUDPIPPort) &&
		len(packet.Body) > 0 && packet.Body[0] == 1 && !connection.failed {
		connection.failed = true
		return 0, errors.New("injected peer actor mode1 write failure")
	}
	return connection.Conn.Write(data)
}

func TestR169FiendWarExistingEncounterPeerActorWriteFailureCanResume(t *testing.T) {
	f, p := r159ActiveEncounter(t)
	s := f.service
	beforeBattle := cloneRuntimeRaidBattle(f.state.Battle)
	beforeParty := s.runtimePartyStateForSession(p[0])
	originalConn := p[0].conn
	fault := &r169FailPeerMode1WriteConn{Conn: originalConn}
	for _, peer := range p {
		r169ResetDungeonPeerProjection(peer)
		r134N1Reset(peer)
	}
	p[0].conn = fault
	t.Cleanup(func() { p[0].conn = originalConn })
	body := make([]byte, 13)
	binary.LittleEndian.PutUint16(body, p[1].selectedCharacterID)
	if err := s.handleGameUpper(p[0], buildCurrentDungeonClientUpperForPhaseTest(t, 10, body)); err != nil {
		t.Fatalf("actor write failure escaped the already acknowledged recovery: %v", err)
	}
	if !fault.failed {
		t.Fatal("injected actor mode1 failure was not exercised")
	}
	if r169HasDungeonPeerProjection(p[0], p[1].selectedCharacterID) ||
		r169HasDungeonPeerProjection(p[1], p[0].selectedCharacterID) {
		t.Fatal("failed actor direction retained a projection claim")
	}
	if !reflect.DeepEqual(beforeBattle, cloneRuntimeRaidBattle(f.state.Battle)) ||
		!reflect.DeepEqual(beforeParty, s.runtimePartyStateForSession(p[0])) {
		t.Fatal("actor write failure changed membership or rewards")
	}

	p[0].conn = originalConn
	for _, peer := range p {
		r134N1Reset(peer)
	}
	r169SendExistingEncounterRequest(t, s, p[0], p[1])
	for i, peer := range p {
		r169FiendWarRequiresActorProjection(t, peer, p[1-i].selectedCharacterID)
		r169ForbidHandshakePackets(t, peer)
	}

	for _, peer := range p {
		r134N1Reset(peer)
	}
	r169SendExistingEncounterRequest(t, s, p[0], p[1])
	for i, peer := range p {
		r169FiendWarHasNoPeerActorModel(t, peer, p[1-i].selectedCharacterID)
	}
}
