package dnfbridge

import "testing"

// A diagnostic-only constructed interleaving. It does not establish the
// deployed relay server's response ordering; that must be checked separately.
func TestPartyRemoteLateAckCannotCompleteReplacementRequest(t *testing.T) {
	r, _ := remoteRetirementFixture()
	old := &partyUDPRelayRemotePending{members: []uint16{99, 100}, done: make(chan partyUDPRelayRemoteSyncResult, 1)}
	r.pending[23] = old
	r.closeRoom(23, "diagnostic-old-room")
	next := &partyUDPRelayRemotePending{members: []uint16{99, 100}, done: make(chan partyUDPRelayRemoteSyncResult, 1)}
	r.pending[23] = next
	// This represents a SYNC_OK sent before the CLOSE and delivered after the
	// foreground caller has published another request for the same room ID.
	r.completeSync(23, nil, partyUDPRelayRemoteSyncResult{ok: true})
	select {
	case result := <-next.done:
		t.Fatalf("prior room reply completed replacement request: %+v", result)
	default:
	}
	if r.matrices[23] != nil {
		t.Fatal("prior room reply published a replacement matrix")
	}
}
