package dnfbridge

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"
)

// Hold an old read after receiving its datagram, then retire and rejoin the
// same pair. The old payload must not enter the replacement sockets.
func TestEmbeddedRelayOldReadMustNotReachRejoinedPair(t *testing.T) {
	const (
		partyID                   = uint16(77)
		oldPayload                = "embedded-relay-old-read-probe"
		fallbackLearnedLogMessage = "party UDP relay endpoint fallback learned real endpoint"
	)

	oldLoggerPaused := make(chan struct{})
	releaseOldLogger := make(chan struct{})
	var pauseOnce sync.Once
	var releaseOnce sync.Once
	releaseOldReadLoop := func() {
		releaseOnce.Do(func() {
			close(releaseOldLogger)
		})
	}
	logWarn := func(message string, _ ...any) {
		if message != fallbackLearnedLogMessage {
			return
		}
		pauseOnce.Do(func() {
			close(oldLoggerPaused)
		})
		<-releaseOldLogger
	}

	relay, err := newCurrentPartyUDPRelay(
		true,
		"127.0.0.1",
		testCurrentPartyUDPRelayPortStart(t),
		12,
		logWarn,
	)
	if err != nil {
		t.Fatalf("new loopback UDP relay: %v", err)
	}
	// Cleanups run in reverse order. Release a diagnostic pause before Close,
	// otherwise a failing assertion while readLoop is paused would deadlock.
	t.Cleanup(func() {
		_ = relay.Close()
	})
	t.Cleanup(releaseOldReadLoop)

	oldBindings := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}},
	}
	if !relay.Sync(partyID, oldBindings) {
		t.Fatal("initial two-member relay sync failed")
	}

	keyAB := currentPartyUDPRelayLegKey{
		partyID: partyID,
		ownerID: 101,
		peerID:  202,
	}
	keyBA := currentPartyUDPRelayLegKey{
		partyID: partyID,
		ownerID: 202,
		peerID:  101,
	}
	relay.mu.Lock()
	oldAB := relay.legs[keyAB]
	oldBA := relay.legs[keyBA]
	relay.mu.Unlock()
	if oldAB == nil || oldBA == nil {
		t.Fatal("initial directed legs missing")
	}
	if oldAB == oldBA {
		t.Fatal("initial directed legs unexpectedly share a pointer")
	}

	// Borrow the existing one-shot fallback-learned log as a deterministic
	// pause point immediately before readLoop reacquires r.mu.
	oldAB.mu.Lock()
	oldAB.endpointFallbackDatagrams = 1
	oldAB.mu.Unlock()

	addressAB, portAB, ok := relay.Endpoint(partyID, 101, 202)
	if !ok {
		t.Fatal("initial A-to-B relay endpoint missing")
	}
	if _, _, ok := relay.Endpoint(partyID, 202, 101); !ok {
		t.Fatal("initial B-to-A relay endpoint missing")
	}

	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()

	endpointAB := &net.UDPAddr{
		IP:   net.IPv4(addressAB[0], addressAB[1], addressAB[2], addressAB[3]),
		Port: int(portAB),
	}
	if _, err := clientA.WriteToUDP([]byte(oldPayload), endpointAB); err != nil {
		t.Fatalf("send old A payload: %v", err)
	}

	select {
	case <-oldLoggerPaused:
	case <-time.After(2 * time.Second):
		t.Fatal("old readLoop did not reach the fallback-learned log pause within 2s")
	}

	relay.CloseParty(partyID)
	newBindings := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 2, address: [4]byte{127, 0, 0, 1}},
		{characterID: 202, generation: 2, address: [4]byte{127, 0, 0, 1}},
	}
	if !relay.Sync(partyID, newBindings) {
		t.Fatal("generation-2 relay sync failed")
	}

	relay.mu.Lock()
	newAB := relay.legs[keyAB]
	newBA := relay.legs[keyBA]
	relay.mu.Unlock()
	if newAB == nil || newBA == nil {
		t.Fatal("generation-2 directed legs missing")
	}
	if newAB == oldAB || newBA == oldBA {
		t.Fatal("generation-2 relay reused an old leg pointer")
	}
	if newAB.binding.generation != 2 || newBA.binding.generation != 2 {
		t.Fatalf(
			"generation-2 relay legs have wrong bindings: AB=%d BA=%d",
			newAB.binding.generation,
			newBA.binding.generation,
		)
	}

	clientBAddress, ok := clientB.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("client B local address has unexpected type %T", clientB.LocalAddr())
	}
	newBA.mu.Lock()
	newBA.learnedEndpoint = cloneCurrentPartyUDPRelayAddr(clientBAddress)
	newBA.mu.Unlock()

	releaseOldReadLoop()

	if err := clientB.SetReadDeadline(time.Now().Add(750 * time.Millisecond)); err != nil {
		t.Fatalf("set client B read deadline: %v", err)
	}
	buffer := make([]byte, 128)
	n, source, err := clientB.ReadFromUDP(buffer)
	if err == nil {
		if bytes.Equal(buffer[:n], []byte(oldPayload)) {
			t.Fatalf(
				"stale old readLoop payload reached rejoined generation-2 pair: payload=%q source=%s",
				buffer[:n],
				source,
			)
		}
		t.Fatalf("client B unexpectedly received payload=%q source=%s", buffer[:n], source)
	}
	if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("receive on client B: %v", err)
	}
}
