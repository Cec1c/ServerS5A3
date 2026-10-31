package dnfbridge

import (
	"net"
	"testing"
)

func TestPartyUDPRelayRemoteStaleChannelLossKeepsCurrentState(t *testing.T) {
	const currentSessionID uint64 = 2
	const roomID uint32 = 700001

	currentConn, peerConn := net.Pipe()
	t.Cleanup(func() {
		_ = currentConn.Close()
		_ = peerConn.Close()
	})

	remote := &currentPartyUDPRelayRemote{
		sessionID: currentSessionID,
		connected: true,
		conn:      currentConn,
		matrices: map[uint32]*partyUDPRelayRemoteMatrix{
			roomID: {
				generation: 4,
				ports: map[partyUDPRelayRemoteLegKey]uint16{
					{owner: 10, peer: 11}: 10250,
				},
			},
		},
		roomMembers: map[uint32][]uint16{
			roomID: {10, 11},
		},
		renewals: map[uint32][]uint16{
			roomID: {10, 11},
		},
		resyncPending: map[uint32]struct{}{
			roomID: {},
		},
	}

	remote.handleChannelLoss(1)

	if !remote.mu.TryLock() {
		t.Fatal("handleChannelLoss returned a stale session with the state mutex locked")
	}
	defer remote.mu.Unlock()

	if !remote.connected {
		t.Fatal("stale channel loss cleared the current connection state")
	}
	if remote.conn != currentConn {
		t.Fatal("stale channel loss replaced the current connection")
	}
	if got := len(remote.roomMembers); got != 1 {
		t.Fatalf("room count after stale channel loss = %d, want 1", got)
	}
	if got := remote.roomMembers[roomID]; len(got) != 2 || got[0] != 10 || got[1] != 11 {
		t.Fatalf("room members after stale channel loss = %v, want [10 11]", got)
	}
	matrix, ok := remote.matrices[roomID]
	if !ok {
		t.Fatal("stale channel loss removed the current room matrix")
	}
	if matrix.generation != 4 || matrix.ports[partyUDPRelayRemoteLegKey{owner: 10, peer: 11}] != 10250 {
		t.Fatal("stale channel loss changed the current room matrix")
	}
	if got := remote.renewals[roomID]; len(got) != 2 || got[0] != 10 || got[1] != 11 {
		t.Fatalf("renewals after stale channel loss = %v, want [10 11]", got)
	}
	if _, ok := remote.resyncPending[roomID]; !ok {
		t.Fatal("stale channel loss cleared pending resync state")
	}
}
