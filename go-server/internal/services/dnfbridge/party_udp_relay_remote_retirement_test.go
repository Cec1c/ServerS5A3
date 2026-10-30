package dnfbridge

import (
	"bytes"
	"testing"
)

func remoteRetirementFixture() (*currentPartyUDPRelayRemote, *bufferConn) {
	conn := &bufferConn{}
	return &currentPartyUDPRelayRemote{
		conn: conn, connected: true,
		roomMembers:   map[uint32][]uint16{23: {99, 100}},
		matrices:      map[uint32]*partyUDPRelayRemoteMatrix{},
		pending:       map[uint32]*partyUDPRelayRemotePending{},
		renewals:      map[uint32][]uint16{},
		resyncPending: map[uint32]struct{}{},
	}, conn
}

func TestPartyRemoteRetirementRejectsQueuedRenewalAfterSameLobbyRejoin(t *testing.T) {
	for _, resync := range []bool{false, true} {
		t.Run(map[bool]string{false: "renew", true: "resync"}[resync], func(t *testing.T) {
			r, conn := remoteRetirementFixture()
			old := r.trackedRooms()[0]
			r.closeRoom(23, "test_retire")
			conn.write.Reset()
			r.mu.Lock()
			r.roomMembers[23] = []uint16{99, 100}
			r.mu.Unlock()
			r.sendTrackedRoom(old, resync)
			if conn.write.Len() != 0 || len(r.renewals) != 0 || len(r.resyncPending) != 0 {
				t.Fatal("old renewal revived a retired room generation")
			}
			r.dispatch(partyRelayOpCloseOK, []byte{0, 0, 0, 23})
			r.sendTrackedRoom(r.trackedRooms()[0], resync)
			opcode, payload, err := readPartyRelayFrame(bytes.NewReader(conn.write.Bytes()))
			if err != nil || opcode != partyRelayOpSync || !bytes.Equal(payload, buildPartyRelaySyncPayload(23, []uint16{99, 100})) {
				t.Fatalf("new room renewal missing: opcode=%d err=%v body=%x", opcode, err, payload)
			}
		})
	}
}

func TestPartyRemoteRetirementClosesPendingUnpublishedRoom(t *testing.T) {
	r, conn := remoteRetirementFixture()
	delete(r.roomMembers, 23)
	pending := &partyUDPRelayRemotePending{members: []uint16{99, 100}, done: make(chan partyUDPRelayRemoteSyncResult, 1)}
	r.pending[23] = pending
	r.closeRoom(23, "pending_retire")
	opcode, payload, err := readPartyRelayFrame(bytes.NewReader(conn.write.Bytes()))
	if err != nil || opcode != partyRelayOpClose || !bytes.Equal(payload, buildPartyRelayClosePayload(23)) {
		t.Fatalf("pending room did not send CLOSE: opcode=%d err=%v", opcode, err)
	}
	select {
	case result := <-pending.done:
		if result.ok {
			t.Fatal("retired pending reported success")
		}
	default:
		t.Fatal("pending caller was not released")
	}
	if _, exists := r.pending[23]; exists {
		t.Fatal("retired pending was retained")
	}
}
