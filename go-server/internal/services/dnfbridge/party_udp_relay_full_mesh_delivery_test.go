package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"net"
	"testing"
	"time"
)

const (
	partyUDPRelayFullMeshPartyID      uint16 = 77
	partyUDPRelayFullMeshLearnTimeout        = 2 * time.Second
	partyUDPRelayFullMeshReadTimeout         = 2 * time.Second
	partyUDPRelayFullMeshDrainQuiet          = 200 * time.Millisecond
	partyUDPRelayFullMeshReadQuiet           = 250 * time.Millisecond
)

type partyUDPRelayFullMeshClient struct {
	characterID uint16
	ordinal     uint16
	conn        *net.UDPConn
}

type partyUDPRelayFullMeshDirection struct {
	ownerID uint16
	peerID  uint16
}

type partyUDPRelayFullMeshDelivery struct {
	payload []byte
	source  *net.UDPAddr
}

type partyUDPRelayFullMeshEnvelope struct {
	phase       byte
	senderID    uint16
	recipientID uint16
	sequence    uint32
}

type partyUDPRelayFullMeshExpectation struct {
	payload     []byte
	source      *net.UDPAddr
	senderID    uint16
	recipientID uint16
	sequence    uint32
}

func TestPartyUDPRelayFullMeshDelivery(t *testing.T) {
	for _, members := range []int{2, 3, 4} {
		members := members
		t.Run(fmt.Sprintf("%d_members", members), func(t *testing.T) {
			relay, clients, bindings := partyUDPRelayFullMeshFixture(t, members)

			// The three-member case exercises the third member joining, leaving,
			// and rejoining through the same complete-topology Sync interface
			// used by production roster publication.
			if members == 3 {
				if !relay.Sync(partyUDPRelayFullMeshPartyID, bindings[:2]) {
					t.Fatal("initial two-member topology was not published")
				}
				if !relay.Sync(partyUDPRelayFullMeshPartyID, bindings) {
					t.Fatal("third member could not join")
				}
			} else if !relay.Sync(partyUDPRelayFullMeshPartyID, bindings) {
				t.Fatalf("%d-member topology was not published", members)
			}

			partyUDPRelayFullMeshExercise(t, relay, clients, 1)

			if members != 3 {
				return
			}

			if !relay.Sync(partyUDPRelayFullMeshPartyID, bindings[:2]) {
				t.Fatal("third member could not leave")
			}
			waitRelayCondition(t, time.Second, "third member legs removed", func() bool {
				return !partyUDPRelayFullMeshEndpointPresent(relay, clients[0].characterID, clients[2].characterID)
			})
			partyUDPRelayFullMeshDrain(t, clients)

			if !relay.Sync(partyUDPRelayFullMeshPartyID, bindings) {
				t.Fatal("third member could not rejoin")
			}
			// Do not assert that recreated directed legs use different ports. Ports
			// may be reused, and a late previous-phase datagram may consequently be
			// delivered from a source port that is also a current reverse leg.
			partyUDPRelayFullMeshExercise(t, relay, clients, 2)
		})
	}
}

func partyUDPRelayFullMeshFixture(t *testing.T, members int) (*currentPartyUDPRelay, []partyUDPRelayFullMeshClient, []currentPartyUDPRelayBinding) {
	t.Helper()
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", r137FreeRange(t), 64, nil)
	if err != nil {
		t.Fatalf("new full-mesh UDP relay: %v", err)
	}
	clients := make([]partyUDPRelayFullMeshClient, 0, members)
	t.Cleanup(func() {
		if err := relay.Close(); err != nil {
			t.Errorf("close full-mesh UDP relay: %v", err)
		}
		for _, client := range clients {
			_ = client.conn.Close()
		}
	})

	ids := make([]uint16, members)
	for index := 0; index < members; index++ {
		ids[index] = uint16(101 + index)
		clients = append(clients, partyUDPRelayFullMeshClient{
			characterID: ids[index],
			ordinal:     uint16(index + 1),
			conn:        testCurrentPartyUDPClient(t, "127.0.0.1"),
		})
	}
	return relay, clients, r137Bindings(ids...)
}

func partyUDPRelayFullMeshExercise(t *testing.T, relay *currentPartyUDPRelay, clients []partyUDPRelayFullMeshClient, phase byte) {
	t.Helper()
	endpoints := partyUDPRelayFullMeshLearnAll(t, relay, clients)
	expectedByRecipient := make(map[uint16]map[string]partyUDPRelayFullMeshExpectation, len(clients))
	for _, recipient := range clients {
		expectedByRecipient[recipient.characterID] = make(map[string]partyUDPRelayFullMeshExpectation, len(clients)-1)
	}

	for _, sender := range clients {
		for _, recipient := range clients {
			if sender.characterID == recipient.characterID {
				continue
			}
			sequence := uint32(phase)<<24 | uint32(sender.ordinal)<<16 | uint32(recipient.ordinal)<<8 | 1
			payload := partyUDPRelayFullMeshPayload(phase, sender.characterID, recipient.characterID, sequence)
			reverseSource := endpoints[partyUDPRelayFullMeshDirection{ownerID: recipient.characterID, peerID: sender.characterID}]
			expectedByRecipient[recipient.characterID][string(payload)] = partyUDPRelayFullMeshExpectation{
				payload:     append([]byte(nil), payload...),
				source:      reverseSource,
				senderID:    sender.characterID,
				recipientID: recipient.characterID,
				sequence:    sequence,
			}
			if _, err := sender.conn.WriteToUDP(payload, endpoints[partyUDPRelayFullMeshDirection{ownerID: sender.characterID, peerID: recipient.characterID}]); err != nil {
				t.Fatalf("phase %d send %d->%d: %v", phase, sender.characterID, recipient.characterID, err)
			}
		}
	}

	for _, recipient := range clients {
		expected := expectedByRecipient[recipient.characterID]
		deliveries := partyUDPRelayFullMeshCollect(t, recipient, phase, expected)
		partyUDPRelayFullMeshAssertDeliveries(t, recipient, phase, expected, deliveries)
	}
}

func partyUDPRelayFullMeshLearnAll(t *testing.T, relay *currentPartyUDPRelay, clients []partyUDPRelayFullMeshClient) map[partyUDPRelayFullMeshDirection]*net.UDPAddr {
	t.Helper()
	endpoints := make(map[partyUDPRelayFullMeshDirection]*net.UDPAddr, len(clients)*(len(clients)-1))
	for _, owner := range clients {
		for _, peer := range clients {
			if owner.characterID == peer.characterID {
				continue
			}
			address, port, ok := relay.Endpoint(partyUDPRelayFullMeshPartyID, owner.characterID, peer.characterID)
			if !ok {
				t.Fatalf("missing directed endpoint %d->%d", owner.characterID, peer.characterID)
			}
			endpoints[partyUDPRelayFullMeshDirection{ownerID: owner.characterID, peerID: peer.characterID}] = &net.UDPAddr{
				IP:   net.IPv4(address[0], address[1], address[2], address[3]),
				Port: int(port),
			}
		}
	}

	// One byte is enough for endpoint learning. Some of these probes can be
	// forwarded while later directed legs are still being learned, so wait for
	// every leg to be known before draining all fixture sockets.
	for _, owner := range clients {
		for _, peer := range clients {
			if owner.characterID == peer.characterID {
				continue
			}
			if _, err := owner.conn.WriteToUDP([]byte{byte(owner.ordinal)}, endpoints[partyUDPRelayFullMeshDirection{ownerID: owner.characterID, peerID: peer.characterID}]); err != nil {
				t.Fatalf("learn directed endpoint %d->%d: %v", owner.characterID, peer.characterID, err)
			}
		}
	}
	waitRelayCondition(t, partyUDPRelayFullMeshLearnTimeout, "all directed legs learned", func() bool {
		for _, owner := range clients {
			local := owner.conn.LocalAddr().(*net.UDPAddr)
			for _, peer := range clients {
				if owner.characterID == peer.characterID {
					continue
				}
				learned := relayLegFor(t, relay, owner.characterID, peer.characterID).learnedEndpointSnapshot()
				if learned == nil || learned.Port != local.Port || !learned.IP.Equal(local.IP) {
					return false
				}
			}
		}
		return true
	})
	partyUDPRelayFullMeshDrain(t, clients)
	return endpoints
}

func partyUDPRelayFullMeshDrain(t *testing.T, clients []partyUDPRelayFullMeshClient) {
	t.Helper()
	results := make(chan error, len(clients))
	for _, client := range clients {
		client := client
		go func() {
			if err := client.conn.SetReadDeadline(time.Now().Add(partyUDPRelayFullMeshDrainQuiet)); err != nil {
				results <- fmt.Errorf("client %d set drain deadline: %w", client.characterID, err)
				return
			}
			buffer := make([]byte, currentPartyUDPRelayMaxDatagram)
			for {
				_, _, err := client.conn.ReadFromUDP(buffer)
				if err == nil {
					continue
				}
				if networkError, ok := err.(net.Error); ok && networkError.Timeout() {
					results <- nil
					return
				}
				results <- fmt.Errorf("client %d drain: %w", client.characterID, err)
				return
			}
		}()
	}
	for range clients {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func partyUDPRelayFullMeshCollect(t *testing.T, client partyUDPRelayFullMeshClient, phase byte, expected map[string]partyUDPRelayFullMeshExpectation) []partyUDPRelayFullMeshDelivery {
	t.Helper()
	deadline := time.Now().Add(partyUDPRelayFullMeshReadTimeout)
	quietUntil := time.Time{}
	found := make(map[string]bool, len(expected))
	buffer := make([]byte, currentPartyUDPRelayMaxDatagram)
	deliveries := make([]partyUDPRelayFullMeshDelivery, 0, len(expected))
	for {
		now := time.Now()
		if !now.Before(deadline) {
			t.Fatalf("phase %d client %d received %d/%d required payloads before timeout", phase, client.characterID, len(found), len(expected))
		}
		readUntil := deadline
		if len(found) == len(expected) {
			if quietUntil.IsZero() {
				quietUntil = now.Add(partyUDPRelayFullMeshReadQuiet)
			}
			if quietUntil.Before(readUntil) {
				readUntil = quietUntil
			}
		}
		if err := client.conn.SetReadDeadline(readUntil); err != nil {
			t.Fatalf("phase %d client %d set receive deadline: %v", phase, client.characterID, err)
		}
		read, source, err := client.conn.ReadFromUDP(buffer)
		if err != nil {
			if networkError, ok := err.(net.Error); ok && networkError.Timeout() {
				if len(found) == len(expected) && !time.Now().Before(quietUntil) {
					return deliveries
				}
				continue
			}
			t.Fatalf("phase %d client %d receive: %v", phase, client.characterID, err)
		}
		payload := append([]byte(nil), buffer[:read]...)
		deliveries = append(deliveries, partyUDPRelayFullMeshDelivery{payload: payload, source: source})
		quietUntil = time.Now().Add(partyUDPRelayFullMeshReadQuiet)
		if envelope, ok := partyUDPRelayFullMeshDecode(payload); ok && envelope.phase == phase {
			if _, wanted := expected[string(payload)]; wanted {
				found[string(payload)] = true
			}
		}
	}
}

func partyUDPRelayFullMeshAssertDeliveries(
	t *testing.T,
	recipient partyUDPRelayFullMeshClient,
	phase byte,
	expected map[string]partyUDPRelayFullMeshExpectation,
	deliveries []partyUDPRelayFullMeshDelivery,
) {
	t.Helper()
	counts := make(map[string]int, len(expected))
	currentPhase := 0
	oldPhase := 0
	for _, delivery := range deliveries {
		envelope, ok := partyUDPRelayFullMeshDecode(delivery.payload)
		if !ok {
			t.Fatalf("phase %d client %d received malformed payload %x from %v", phase, recipient.characterID, delivery.payload, delivery.source)
		}
		if envelope.phase != phase {
			oldPhase++
			continue
		}
		currentPhase++
		if envelope.senderID == envelope.recipientID {
			t.Fatalf("phase %d client %d received self-forwarded payload sender=%d recipient=%d sequence=%d source=%v", phase, recipient.characterID, envelope.senderID, envelope.recipientID, envelope.sequence, delivery.source)
		}
		if envelope.recipientID != recipient.characterID {
			t.Fatalf("phase %d client %d received wrong-role payload sender=%d recipient=%d sequence=%d source=%v", phase, recipient.characterID, envelope.senderID, envelope.recipientID, envelope.sequence, delivery.source)
		}
		want, wanted := expected[string(delivery.payload)]
		if !wanted {
			t.Fatalf("phase %d client %d received unexpected current-phase payload sender=%d recipient=%d sequence=%d source=%v", phase, recipient.characterID, envelope.senderID, envelope.recipientID, envelope.sequence, delivery.source)
		}
		counts[string(delivery.payload)]++
		if delivery.source == nil || !partyUDPRelayFullMeshSameAddr(delivery.source, want.source) {
			t.Fatalf("phase %d client %d sender=%d source=%v want reverse leg %v", phase, recipient.characterID, envelope.senderID, delivery.source, want.source)
		}
	}
	if currentPhase != len(expected) {
		t.Fatalf("phase %d client %d received %d current-phase datagrams, want exactly %d", phase, recipient.characterID, currentPhase, len(expected))
	}
	for key, want := range expected {
		if got := counts[key]; got != 1 {
			t.Fatalf("phase %d client %d sender=%d recipient=%d sequence=%d copies=%d, want exactly one", phase, recipient.characterID, want.senderID, want.recipientID, want.sequence, got)
		}
	}
	if oldPhase > 0 {
		// A previous membership generation can be observed after rejoining when
		// a released directed port is reused. Its payload is phase-tagged so it
		// cannot be mistaken for a current delivery; no port-reuse assumption is
		// made here.
		t.Logf("phase %d client %d ignored %d late previous-phase datagrams", phase, recipient.characterID, oldPhase)
	}
}

func partyUDPRelayFullMeshPayload(phase byte, senderID, recipientID uint16, sequence uint32) []byte {
	payload := make([]byte, 13)
	copy(payload[:4], []byte{'F', 'M', 'U', '1'})
	payload[4] = phase
	binary.BigEndian.PutUint16(payload[5:7], senderID)
	binary.BigEndian.PutUint16(payload[7:9], recipientID)
	binary.BigEndian.PutUint32(payload[9:13], sequence)
	return payload
}

func partyUDPRelayFullMeshDecode(payload []byte) (partyUDPRelayFullMeshEnvelope, bool) {
	if len(payload) != 13 || string(payload[:4]) != "FMU1" {
		return partyUDPRelayFullMeshEnvelope{}, false
	}
	return partyUDPRelayFullMeshEnvelope{
		phase:       payload[4],
		senderID:    binary.BigEndian.Uint16(payload[5:7]),
		recipientID: binary.BigEndian.Uint16(payload[7:9]),
		sequence:    binary.BigEndian.Uint32(payload[9:13]),
	}, true
}

func partyUDPRelayFullMeshEndpointPresent(relay *currentPartyUDPRelay, ownerID, peerID uint16) bool {
	_, _, ok := relay.Endpoint(partyUDPRelayFullMeshPartyID, ownerID, peerID)
	return ok
}

func partyUDPRelayFullMeshSameAddr(got, want *net.UDPAddr) bool {
	return got != nil && want != nil && got.Port == want.Port && got.IP.Equal(want.IP)
}
