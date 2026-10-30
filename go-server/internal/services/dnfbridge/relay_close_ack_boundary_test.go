package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestPartyRemoteCloseAckBoundary covers the ordering barrier established by
// CLOSE and released by CLOSE_OK. It deliberately completes the replacement
// request through completeSync so the test does not wait on the wire timeout.
func TestPartyRemoteCloseAckBoundary(t *testing.T) {
	r, conn := remoteRetirementFixture()

	r.closeRoom(23, "close_ack_boundary")
	frameCountAfterClose := conn.write.Len()
	reader := bytes.NewReader(append([]byte(nil), conn.write.Bytes()...))
	opcode, payload, err := readPartyRelayFrame(reader)
	if err != nil || opcode != partyRelayOpClose || !bytes.Equal(payload, buildPartyRelayClosePayload(23)) {
		t.Fatalf("first close did not send exactly one CLOSE frame: opcode=%d err=%v body=%x", opcode, err, payload)
	}
	if reader.Len() != 0 || frameCountAfterClose == 0 {
		t.Fatalf("first close emitted unexpected trailing frames: remaining=%d total=%d", reader.Len(), frameCountAfterClose)
	}

	if r.syncRoom(23, []uint16{99, 100}) {
		t.Fatal("syncRoom succeeded while the room was awaiting CLOSE_OK")
	}
	if got := conn.write.Len(); got != frameCountAfterClose {
		t.Fatalf("syncRoom wrote a frame while CLOSE was pending: before=%d after=%d", frameCountAfterClose, got)
	}

	r.closeRoom(23, "duplicate_close")
	if got := conn.write.Len(); got != frameCountAfterClose {
		t.Fatalf("duplicate CloseRoom emitted another CLOSE before CLOSE_OK: before=%d after=%d", frameCountAfterClose, got)
	}
	if got := r.closeSentCt.Load(); got != 1 {
		t.Fatalf("CloseRoom sent %d CLOSE frames while awaiting CLOSE_OK; want 1", got)
	}

	closeOK := make([]byte, 4)
	binary.BigEndian.PutUint32(closeOK, 23)
	r.dispatch(partyRelayOpCloseOK, closeOK)
	r.mu.Lock()
	_, stillClosing := r.closingRooms[23]
	r.mu.Unlock()
	if stillClosing {
		t.Fatal("valid CLOSE_OK did not clear the room closing barrier")
	}

	pending := &partyUDPRelayRemotePending{
		members: []uint16{99, 100},
		done:    make(chan partyUDPRelayRemoteSyncResult, 1),
	}
	r.mu.Lock()
	r.pending[23] = pending
	r.mu.Unlock()
	ports := map[partyUDPRelayRemoteLegKey]uint16{
		{owner: 99, peer: 100}: 31001,
		{owner: 100, peer: 99}: 31002,
	}
	r.completeSync(23, ports, partyUDPRelayRemoteSyncResult{ok: true})

	select {
	case result := <-pending.done:
		if !result.ok {
			t.Fatalf("current pending request completed with failure: %+v", result)
		}
	default:
		t.Fatal("current pending request was not completed")
	}

	r.mu.Lock()
	matrix := r.matrices[23]
	members := append([]uint16(nil), r.roomMembers[23]...)
	_, pendingRetained := r.pending[23]
	r.mu.Unlock()
	if matrix == nil {
		t.Fatal("current SYNC_OK did not publish a port matrix")
	}
	if len(members) != 2 || members[0] != 99 || members[1] != 100 {
		t.Fatalf("published matrix has unexpected members: %v", members)
	}
	if len(matrix.ports) != 2 || matrix.ports[partyUDPRelayRemoteLegKey{owner: 99, peer: 100}] != 31001 || matrix.ports[partyUDPRelayRemoteLegKey{owner: 100, peer: 99}] != 31002 {
		t.Fatalf("published matrix has unexpected ports: %v", matrix.ports)
	}
	if pendingRetained {
		t.Fatal("completed current request remained in the pending table")
	}
	if got := conn.write.Len(); got != frameCountAfterClose {
		t.Fatalf("local CLOSE_OK completion unexpectedly wrote another frame: before=%d after=%d", frameCountAfterClose, got)
	}
}
