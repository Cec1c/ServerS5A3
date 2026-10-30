package dnfbridge

import "testing"

func TestCurrentPartyUDPRelayRepeatedSyncKeepsDirectedLegs(t *testing.T) {
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 12, nil)
	if err != nil {
		t.Fatalf("new UDP relay: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })

	bindings := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}, advertisedPort: 31001},
		{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}, advertisedPort: 31002},
	}
	if !relay.Sync(77, bindings) {
		t.Fatal("initial relay sync failed")
	}
	abBefore, baBefore := testCurrentPartyUDPRelayPair(t, relay, 77, 101, 202)
	_, abPortBefore, _ := relay.Endpoint(77, 101, 202)
	_, baPortBefore, _ := relay.Endpoint(77, 202, 101)

	if !relay.Sync(77, bindings) {
		t.Fatal("repeated relay sync failed")
	}
	abAfter, baAfter := testCurrentPartyUDPRelayPair(t, relay, 77, 101, 202)
	if abAfter != abBefore || baAfter != baBefore {
		t.Fatal("identical bindings replaced a directed leg")
	}
	_, abPortAfter, _ := relay.Endpoint(77, 101, 202)
	_, baPortAfter, _ := relay.Endpoint(77, 202, 101)
	if abPortAfter != abPortBefore || baPortAfter != baPortBefore {
		t.Fatalf("endpoint changed: A->B %d -> %d, B->A %d -> %d", abPortBefore, abPortAfter, baPortBefore, baPortAfter)
	}
}

func TestCurrentPartyUDPRelayPeerGenerationReplacesBothLegs(t *testing.T) {
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 12, nil)
	if err != nil {
		t.Fatalf("new UDP relay: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })

	a := currentPartyUDPRelayBinding{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}, advertisedPort: 31001}
	b1 := currentPartyUDPRelayBinding{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}, advertisedPort: 31002}
	b2 := b1
	b2.generation = 2
	if !relay.Sync(77, []currentPartyUDPRelayBinding{a, b1}) {
		t.Fatal("initial relay sync failed")
	}
	oldAB, oldBA := testCurrentPartyUDPRelayPair(t, relay, 77, a.characterID, b1.characterID)
	_, oldABPort, _ := relay.Endpoint(77, a.characterID, b1.characterID)
	_, oldBAPort, _ := relay.Endpoint(77, b1.characterID, a.characterID)

	// The default constructor keeps replay disabled, so seed only the old A leg's
	// pending bytes to audit whether Sync copies this state into its replacement.
	oldAB.mu.Lock()
	oldAB.pending.bytes = 37
	oldAB.mu.Unlock()

	if !relay.Sync(77, []currentPartyUDPRelayBinding{a, b2}) {
		t.Fatal("generation-change relay sync failed")
	}
	newAB, newBA := testCurrentPartyUDPRelayPair(t, relay, 77, a.characterID, b2.characterID)
	if newAB == oldAB || newBA == oldBA {
		t.Fatal("generation change did not replace both directed legs")
	}
	_, newABPort, _ := relay.Endpoint(77, a.characterID, b2.characterID)
	_, newBAPort, _ := relay.Endpoint(77, b2.characterID, a.characterID)
	if newABPort == oldABPort || newBAPort == oldBAPort {
		t.Fatalf("generation change kept an endpoint: A->B %d -> %d, B->A %d -> %d", oldABPort, newABPort, oldBAPort, newBAPort)
	}

	newAB.mu.Lock()
	abBinding, abPeer, abPendingBytes := newAB.binding, newAB.peerBinding, newAB.pending.bytes
	newAB.mu.Unlock()
	newBA.mu.Lock()
	baBinding, baPeer, baPendingBytes := newBA.binding, newBA.peerBinding, newBA.pending.bytes
	newBA.mu.Unlock()
	if abBinding != a || abPeer != b2 || baBinding != b2 || baPeer != a {
		t.Fatalf("new leg bindings are stale: AB=(%+v,%+v) BA=(%+v,%+v)", abBinding, abPeer, baBinding, baPeer)
	}
	if abPendingBytes != 0 || baPendingBytes != 0 {
		t.Fatalf("old pending inherited by replacement legs: AB=%d BA=%d", abPendingBytes, baPendingBytes)
	}
}

func testCurrentPartyUDPRelayPair(t *testing.T, relay *currentPartyUDPRelay, partyID, a, b uint16) (*currentPartyUDPRelayLeg, *currentPartyUDPRelayLeg) {
	t.Helper()
	relay.mu.Lock()
	ab := relay.legs[currentPartyUDPRelayLegKey{partyID: partyID, ownerID: a, peerID: b}]
	ba := relay.legs[currentPartyUDPRelayLegKey{partyID: partyID, ownerID: b, peerID: a}]
	relay.mu.Unlock()
	if ab == nil || ba == nil {
		t.Fatalf("missing directed legs for party %d: AB=%v BA=%v", partyID, ab != nil, ba != nil)
	}
	return ab, ba
}
