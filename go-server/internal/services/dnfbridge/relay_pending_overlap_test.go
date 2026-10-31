package dnfbridge

import "testing"

// TestPartyRemoteForegroundOverlapKeepsOriginalRequest covers the three
// protocol states that have an outstanding room-23 request. The remote
// protocol has no request ID, so a foreground syncRoom call must not add or
// replace a waiter while any of those states is active.
func TestPartyRemoteForegroundOverlapKeepsOriginalRequest(t *testing.T) {
	members := []uint16{99, 100}
	ports := map[partyUDPRelayRemoteLegKey]uint16{
		{owner: 99, peer: 100}: 31001,
		{owner: 100, peer: 99}: 31002,
	}

	t.Run("pending", func(t *testing.T) {
		r, conn := remoteRetirementFixture()
		conn.write.Reset()
		r.mu.Lock()
		delete(r.matrices, 23)
		original := &partyUDPRelayRemotePending{
			members: append([]uint16(nil), members...),
			done:    make(chan partyUDPRelayRemoteSyncResult, 1),
		}
		r.pending[23] = original
		r.mu.Unlock()

		frameCount := conn.write.Len()
		if r.syncRoom(23, members) {
			t.Fatal("foreground syncRoom succeeded over an existing pending request")
		}
		if got := conn.write.Len(); got != frameCount {
			t.Fatalf("foreground syncRoom wrote while pending: before=%d after=%d", frameCount, got)
		}
		r.mu.Lock()
		pending := r.pending[23]
		r.mu.Unlock()
		if pending != original {
			t.Fatal("foreground syncRoom replaced the existing pending request")
		}

		r.completeSync(23, ports, partyUDPRelayRemoteSyncResult{ok: true})
		select {
		case result := <-original.done:
			if !result.ok {
				t.Fatalf("original pending request completed with failure: %+v", result)
			}
		default:
			t.Fatal("original pending request was not completed")
		}
		r.mu.Lock()
		_, pendingRetained := r.pending[23]
		matrix := r.matrices[23]
		r.mu.Unlock()
		if pendingRetained {
			t.Fatal("completed original pending request remained registered")
		}
		if matrix == nil || len(matrix.ports) != 2 ||
			matrix.ports[partyUDPRelayRemoteLegKey{owner: 99, peer: 100}] != 31001 ||
			matrix.ports[partyUDPRelayRemoteLegKey{owner: 100, peer: 99}] != 31002 {
			t.Fatalf("original pending request did not publish its matrix: %+v", matrix)
		}
		if got := conn.write.Len(); got != frameCount {
			t.Fatalf("completing the original pending request unexpectedly wrote a frame: before=%d after=%d", frameCount, got)
		}
	})

	t.Run("renewal", func(t *testing.T) {
		r, conn := remoteRetirementFixture()
		conn.write.Reset()
		r.mu.Lock()
		delete(r.matrices, 23)
		r.renewals[23] = append([]uint16(nil), members...)
		r.mu.Unlock()

		frameCount := conn.write.Len()
		if r.syncRoom(23, members) {
			t.Fatal("foreground syncRoom succeeded over an outstanding renewal")
		}
		if got := conn.write.Len(); got != frameCount {
			t.Fatalf("foreground syncRoom wrote while renewal was outstanding: before=%d after=%d", frameCount, got)
		}
		r.mu.Lock()
		renewal, renewing := r.renewals[23]
		pending := r.pending[23]
		r.mu.Unlock()
		if !renewing || !equalPartyRelayRemoteMembers(renewal, members) {
			t.Fatalf("foreground syncRoom changed the outstanding renewal: %v", renewal)
		}
		if pending != nil {
			t.Fatal("foreground syncRoom created a pending request over an outstanding renewal")
		}

		r.completeSync(23, ports, partyUDPRelayRemoteSyncResult{ok: true})
		r.mu.Lock()
		_, renewalRetained := r.renewals[23]
		matrix := r.matrices[23]
		r.mu.Unlock()
		if renewalRetained {
			t.Fatal("original renewal remained registered after completion")
		}
		if matrix == nil || len(matrix.ports) != 2 ||
			matrix.ports[partyUDPRelayRemoteLegKey{owner: 99, peer: 100}] != 31001 ||
			matrix.ports[partyUDPRelayRemoteLegKey{owner: 100, peer: 99}] != 31002 {
			t.Fatalf("original renewal did not publish its matrix: %+v", matrix)
		}
		if got := conn.write.Len(); got != frameCount {
			t.Fatalf("completing the original renewal unexpectedly wrote a frame: before=%d after=%d", frameCount, got)
		}
	})

	t.Run("resync", func(t *testing.T) {
		r, conn := remoteRetirementFixture()
		conn.write.Reset()
		r.mu.Lock()
		delete(r.matrices, 23)
		r.resyncPending[23] = struct{}{}
		r.mu.Unlock()

		frameCount := conn.write.Len()
		if r.syncRoom(23, members) {
			t.Fatal("foreground syncRoom succeeded over an outstanding resync")
		}
		if got := conn.write.Len(); got != frameCount {
			t.Fatalf("foreground syncRoom wrote while resync was outstanding: before=%d after=%d", frameCount, got)
		}
		r.mu.Lock()
		_, resyncing := r.resyncPending[23]
		pending := r.pending[23]
		r.mu.Unlock()
		if !resyncing {
			t.Fatal("foreground syncRoom cleared the outstanding resync")
		}
		if pending != nil {
			t.Fatal("foreground syncRoom created a pending request over an outstanding resync")
		}

		r.completeSync(23, ports, partyUDPRelayRemoteSyncResult{ok: true})
		r.mu.Lock()
		_, resyncRetained := r.resyncPending[23]
		matrix := r.matrices[23]
		r.mu.Unlock()
		if resyncRetained {
			t.Fatal("original resync remained registered after completion")
		}
		if matrix == nil || len(matrix.ports) != 2 ||
			matrix.ports[partyUDPRelayRemoteLegKey{owner: 99, peer: 100}] != 31001 ||
			matrix.ports[partyUDPRelayRemoteLegKey{owner: 100, peer: 99}] != 31002 {
			t.Fatalf("original resync did not publish its matrix: %+v", matrix)
		}
		if got := conn.write.Len(); got != frameCount {
			t.Fatalf("completing the original resync unexpectedly wrote a frame: before=%d after=%d", frameCount, got)
		}
	})
}
