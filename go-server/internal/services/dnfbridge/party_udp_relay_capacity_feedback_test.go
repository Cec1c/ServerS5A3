package dnfbridge

import (
	"fmt"
	"net"
	"runtime"
	"testing"
	"time"
)

const (
	partyUDPRelayCapacityFeedbackPoolSize   = 512
	partyUDPRelayCapacityFeedbackPartyCount = 42
	partyUDPRelayCapacityFeedbackMembers    = 4
	partyUDPRelayCapacityFeedbackUsedPorts  = partyUDPRelayCapacityFeedbackPartyCount *
		partyUDPRelayCapacityFeedbackMembers *
		(partyUDPRelayCapacityFeedbackMembers - 1)
	partyUDPRelayCapacityFeedbackFirstPartyID uint16 = 100
	partyUDPRelayCapacityFeedbackCandidateID  uint16 = 900
)

// TestPartyUDPRelayCapacityFeedback512 exercises the 512-port production size
// through real UDP sockets. One port in the pool remains dedicated to the
// native probe listener, so 42 four-member parties consume 504
// directed legs and the next 12-leg topology must fail atomically.
func TestPartyUDPRelayCapacityFeedback512(t *testing.T) {
	start := partyUDPRelayCapacityFeedbackFreeRange(t)
	relay := partyUDPRelayCapacityFeedbackNewRelay(t, start)
	var occupied map[currentPartyUDPRelayLegKey]partyUDPRelayCapacityFeedbackLeg

	t.Run("allocates_more_than_64_and_reserves_probe_port", func(t *testing.T) {
		for party := 0; party < partyUDPRelayCapacityFeedbackPartyCount; party++ {
			partyID := partyUDPRelayCapacityFeedbackFirstPartyID + uint16(party)
			bindings := partyUDPRelayCapacityFeedbackBindings(party)
			if !relay.Sync(partyID, bindings) {
				t.Fatalf("four-member party %d (%d-directed-leg topology) was not allocated", party, len(bindings)*(len(bindings)-1))
			}
		}

		occupied = partyUDPRelayCapacityFeedbackSnapshot(relay)
		if got := len(occupied); got != partyUDPRelayCapacityFeedbackUsedPorts {
			t.Fatalf("occupied directed legs = %d, want %d; pool must exceed 64 legs", got, partyUDPRelayCapacityFeedbackUsedPorts)
		}
		partyUDPRelayCapacityFeedbackAssertPool(t, relay, start, partyUDPRelayCapacityFeedbackUsedPorts)
		if ports := relay.probeListenerPorts(); len(ports) != 1 || ports[0] != start {
			t.Fatalf("live probe listener ports = %v, want reserved pool port %d", ports, start)
		}

		for party := 0; party < partyUDPRelayCapacityFeedbackPartyCount; party++ {
			partyID := partyUDPRelayCapacityFeedbackFirstPartyID + uint16(party)
			bindings := partyUDPRelayCapacityFeedbackBindings(party)
			ports := make(map[uint16]struct{}, len(bindings)*(len(bindings)-1))
			for _, owner := range bindings {
				for _, peer := range bindings {
					if owner.characterID == peer.characterID {
						continue
					}
					_, port, ok := relay.Endpoint(partyID, owner.characterID, peer.characterID)
					if !ok {
						t.Fatalf("party %d endpoint %d->%d missing", partyID, owner.characterID, peer.characterID)
					}
					if port == uint16(start) {
						t.Fatalf("party %d endpoint %d->%d consumed reserved probe port %d", partyID, owner.characterID, peer.characterID, start)
					}
					if _, duplicate := ports[port]; duplicate {
						t.Fatalf("party %d reused directed port %d", partyID, port)
					}
					ports[port] = struct{}{}
				}
			}
			if got := len(ports); got != 12 {
				t.Fatalf("party %d directed port count = %d, want n*(n-1)=12", partyID, got)
			}
		}
	})
	if t.Failed() {
		return
	}

	t.Run("exhaustion_preserves_topology_and_rolls_back_tentative_sockets", func(t *testing.T) {
		freeBefore := partyUDPRelayCapacityFeedbackFreePoolPorts(relay)
		if len(freeBefore) != partyUDPRelayCapacityFeedbackPoolSize-1-partyUDPRelayCapacityFeedbackUsedPorts {
			t.Fatalf("unreserved free pool ports = %d, want a 12-leg request to overrun the remaining capacity", len(freeBefore))
		}

		candidate := partyUDPRelayCapacityFeedbackCandidateBindings()
		if relay.Sync(partyUDPRelayCapacityFeedbackCandidateID, candidate) {
			t.Fatal("four-member candidate committed after the available directed ports were exhausted")
		}

		afterFailure := partyUDPRelayCapacityFeedbackSnapshot(relay)
		partyUDPRelayCapacityFeedbackAssertSameTopology(t, occupied, afterFailure)
		partyUDPRelayCapacityFeedbackAssertPool(t, relay, start, partyUDPRelayCapacityFeedbackUsedPorts)
		if got := partyUDPRelayCapacityFeedbackFreePoolPorts(relay); !partyUDPRelayCapacityFeedbackSamePorts(freeBefore, got) {
			t.Fatalf("free pool ports after rollback = %v, want original %v", got, freeBefore)
		}
		for _, owner := range candidate {
			for _, peer := range candidate {
				if owner.characterID == peer.characterID {
					continue
				}
				if _, _, ok := relay.Endpoint(partyUDPRelayCapacityFeedbackCandidateID, owner.characterID, peer.characterID); ok {
					t.Fatalf("partial candidate endpoint %d->%d remained published", owner.characterID, peer.characterID)
				}
			}
		}
		// The failed attempt opened every remaining unreserved pool socket before
		// failing to complete the twelve-leg topology. Binding each one again proves rollback
		// closed the tentative sockets rather than only removing map entries.
		for _, port := range freeBefore {
			r137AssertPortReleased(t, port)
		}
		if ports := relay.probeListenerPorts(); len(ports) != 1 || ports[0] != start {
			t.Fatalf("exhaustion disturbed probe listener: ports=%v want [%d]", ports, start)
		}
	})
	if t.Failed() {
		return
	}

	t.Run("closeparty_releases_capacity_and_candidate_retry_succeeds", func(t *testing.T) {
		closedPartyID := partyUDPRelayCapacityFeedbackFirstPartyID
		closedPorts := partyUDPRelayCapacityFeedbackPartyPorts(occupied, closedPartyID)
		if len(closedPorts) != 12 {
			t.Fatalf("party %d occupied %d ports, want 12", closedPartyID, len(closedPorts))
		}

		relay.CloseParty(closedPartyID)
		partyUDPRelayCapacityFeedbackAssertPool(t, relay, start, partyUDPRelayCapacityFeedbackUsedPorts-12)
		for _, port := range closedPorts {
			r137AssertPortReleased(t, port)
		}

		candidate := partyUDPRelayCapacityFeedbackCandidateBindings()
		waitRelayCondition(t, 3*time.Second, "candidate four-member topology to allocate after CloseParty", func() bool {
			return relay.Sync(partyUDPRelayCapacityFeedbackCandidateID, candidate)
		})

		recovered := partyUDPRelayCapacityFeedbackSnapshot(relay)
		if got := len(recovered); got != partyUDPRelayCapacityFeedbackUsedPorts {
			t.Fatalf("recovered directed legs = %d, want %d", got, partyUDPRelayCapacityFeedbackUsedPorts)
		}
		partyUDPRelayCapacityFeedbackAssertSameTopologyExceptParty(t, occupied, recovered, closedPartyID)
		partyUDPRelayCapacityFeedbackAssertPool(t, relay, start, partyUDPRelayCapacityFeedbackUsedPorts)

		candidatePorts := make(map[uint16]struct{}, 12)
		for _, owner := range candidate {
			for _, peer := range candidate {
				if owner.characterID == peer.characterID {
					continue
				}
				_, port, ok := relay.Endpoint(partyUDPRelayCapacityFeedbackCandidateID, owner.characterID, peer.characterID)
				if !ok {
					t.Fatalf("retried candidate endpoint %d->%d missing", owner.characterID, peer.characterID)
				}
				if port == uint16(start) {
					t.Fatalf("retried candidate consumed reserved probe port %d", start)
				}
				candidatePorts[port] = struct{}{}
			}
		}
		if len(candidatePorts) != 12 {
			t.Fatalf("retried candidate directed ports = %d, want 12", len(candidatePorts))
		}
		if _, _, ok := relay.Endpoint(closedPartyID, 1000, 1001); ok {
			t.Fatalf("closed party %d remained published after successful retry", closedPartyID)
		}
		if ports := relay.probeListenerPorts(); len(ports) != 1 || ports[0] != start {
			t.Fatalf("capacity recovery disturbed probe listener: ports=%v want [%d]", ports, start)
		}
	})
}

type partyUDPRelayCapacityFeedbackLeg struct {
	leg  *currentPartyUDPRelayLeg
	port uint16
}

func partyUDPRelayCapacityFeedbackNewRelay(t *testing.T, start int) *currentPartyUDPRelay {
	t.Helper()
	relay, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
		enabled:     true,
		advertiseIP: "127.0.0.1",
		portStart:   start,
		portCount:   partyUDPRelayCapacityFeedbackPoolSize,
		probeListen: true,
		probePorts:  []int{start},
		logWarn: func(message string, fields ...any) {
			if message == "party UDP relay native probe listener unavailable" {
				t.Logf("%s %v", message, fields)
			}
		},
	})
	if err != nil {
		t.Fatalf("new 512-port UDP relay: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	return relay
}

func partyUDPRelayCapacityFeedbackBindings(party int) []currentPartyUDPRelayBinding {
	bindings := make([]currentPartyUDPRelayBinding, 0, partyUDPRelayCapacityFeedbackMembers)
	firstID := uint16(1000 + party*partyUDPRelayCapacityFeedbackMembers)
	for member := 0; member < partyUDPRelayCapacityFeedbackMembers; member++ {
		bindings = append(bindings, currentPartyUDPRelayBinding{
			characterID: firstID + uint16(member),
			generation:  1,
			address:     [4]byte{127, 0, 0, 1},
		})
	}
	return bindings
}

func partyUDPRelayCapacityFeedbackCandidateBindings() []currentPartyUDPRelayBinding {
	bindings := make([]currentPartyUDPRelayBinding, 0, partyUDPRelayCapacityFeedbackMembers)
	for member := 0; member < partyUDPRelayCapacityFeedbackMembers; member++ {
		bindings = append(bindings, currentPartyUDPRelayBinding{
			characterID: uint16(9000 + member),
			generation:  1,
			address:     [4]byte{127, 0, 0, 1},
		})
	}
	return bindings
}

func partyUDPRelayCapacityFeedbackSnapshot(relay *currentPartyUDPRelay) map[currentPartyUDPRelayLegKey]partyUDPRelayCapacityFeedbackLeg {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	snapshot := make(map[currentPartyUDPRelayLegKey]partyUDPRelayCapacityFeedbackLeg, len(relay.legs))
	for key, leg := range relay.legs {
		snapshot[key] = partyUDPRelayCapacityFeedbackLeg{leg: leg, port: leg.port}
	}
	return snapshot
}

func partyUDPRelayCapacityFeedbackAssertPool(t *testing.T, relay *currentPartyUDPRelay, start, want int) {
	t.Helper()
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if relay.portCount != partyUDPRelayCapacityFeedbackPoolSize || relay.portStart != start {
		t.Fatalf("pool range = %d+%d, want %d+%d", relay.portStart, relay.portCount, start, partyUDPRelayCapacityFeedbackPoolSize)
	}
	if len(relay.legs) != want || len(relay.usedPorts) != want {
		t.Fatalf("pool count=%d legs=%d used=%d want=%d", relay.portCount, len(relay.legs), len(relay.usedPorts), want)
	}
	for _, leg := range relay.legs {
		if int(leg.port) < start || int(leg.port) >= start+partyUDPRelayCapacityFeedbackPoolSize {
			t.Fatalf("leg port %d outside 512-port range %d-%d", leg.port, start, start+partyUDPRelayCapacityFeedbackPoolSize-1)
		}
		if int(leg.port) == start {
			t.Fatalf("directed leg consumed reserved probe port %d", start)
		}
		if leg.conn == nil {
			t.Fatalf("leg %+v has no UDP socket", leg.key)
		}
		bound, ok := leg.conn.LocalAddr().(*net.UDPAddr)
		if !ok || bound.Port != int(leg.port) {
			t.Fatalf("leg %+v socket=%v, want bound loopback UDP port %d", leg.key, leg.conn.LocalAddr(), leg.port)
		}
	}
	if !partyUDPRelayCapacityFeedbackProbeReserved(relay, start) {
		t.Fatalf("probe port %d is not reserved", start)
	}
}

func partyUDPRelayCapacityFeedbackProbeReserved(relay *currentPartyUDPRelay, port int) bool {
	if relay.probeListener == nil {
		return false
	}
	_, reserved := relay.probeListener.reservedPorts[port]
	return reserved
}

func partyUDPRelayCapacityFeedbackFreePoolPorts(relay *currentPartyUDPRelay) []int {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	ports := make([]int, 0)
	for port := relay.portStart; port < relay.portStart+relay.portCount; port++ {
		if _, used := relay.usedPorts[port]; used {
			continue
		}
		if relay.probeListener != nil {
			if _, reserved := relay.probeListener.reservedPorts[port]; reserved {
				continue
			}
		}
		ports = append(ports, port)
	}
	return ports
}

func partyUDPRelayCapacityFeedbackSamePorts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func partyUDPRelayCapacityFeedbackAssertSameTopology(
	t *testing.T,
	before, after map[currentPartyUDPRelayLegKey]partyUDPRelayCapacityFeedbackLeg,
) {
	t.Helper()
	if len(after) != len(before) {
		t.Fatalf("topology size changed across failed Sync: %d -> %d", len(before), len(after))
	}
	for key, original := range before {
		current, ok := after[key]
		if !ok || current.leg != original.leg || current.port != original.port {
			t.Fatalf("topology entry %+v changed across failed Sync: before=(%p,%d) after=(%p,%d) present=%v",
				key, original.leg, original.port, current.leg, current.port, ok)
		}
	}
}

func partyUDPRelayCapacityFeedbackAssertSameTopologyExceptParty(
	t *testing.T,
	before, after map[currentPartyUDPRelayLegKey]partyUDPRelayCapacityFeedbackLeg,
	closedPartyID uint16,
) {
	t.Helper()
	for key, original := range before {
		if key.partyID == closedPartyID {
			if _, present := after[key]; present {
				t.Fatalf("closed party topology entry %+v remained", key)
			}
			continue
		}
		current, ok := after[key]
		if !ok || current.leg != original.leg || current.port != original.port {
			t.Fatalf("unrelated topology entry %+v changed across CloseParty/retry: before=(%p,%d) after=(%p,%d) present=%v",
				key, original.leg, original.port, current.leg, current.port, ok)
		}
	}
}

func partyUDPRelayCapacityFeedbackPartyPorts(
	snapshot map[currentPartyUDPRelayLegKey]partyUDPRelayCapacityFeedbackLeg,
	partyID uint16,
) []int {
	ports := make([]int, 0, 12)
	for key, leg := range snapshot {
		if key.partyID == partyID {
			ports = append(ports, int(leg.port))
		}
	}
	return ports
}

// partyUDPRelayCapacityFeedbackFreeRange finds 513 consecutive loopback ports
// and keeps the last one leased while the test runs. The pool itself is probed
// as real sockets and then released immediately before the relay binds it.
// The candidates deliberately sit below common ephemeral ranges; an unrelated
// process can still race this fixture and will produce a fixture failure.
func partyUDPRelayCapacityFeedbackFreeRange(t *testing.T) int {
	t.Helper()
	const attempts = 12
	var lastErr error
	lastRange := "not allocated"
	for attempt := 0; attempt < attempts; attempt++ {
		seed := testCurrentPartyUDPClient(t, "127.0.0.1")
		seedPort := seed.LocalAddr().(*net.UDPAddr).Port
		_ = seed.Close()
		start := 20000 + (seedPort%attempts)*1024
		lastRange = fmt.Sprintf("%d-%d", start, start+partyUDPRelayCapacityFeedbackPoolSize)

		lease, err := net.ListenUDP("udp4", &net.UDPAddr{
			IP:   net.IPv4(127, 0, 0, 1),
			Port: start + partyUDPRelayCapacityFeedbackPoolSize,
		})
		if err != nil {
			lastErr = err
			continue
		}
		sockets := make([]*net.UDPConn, 0, partyUDPRelayCapacityFeedbackPoolSize)
		for port := start; port < start+partyUDPRelayCapacityFeedbackPoolSize; port++ {
			conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
			if err != nil {
				lastErr = err
				break
			}
			sockets = append(sockets, conn)
		}
		if len(sockets) == partyUDPRelayCapacityFeedbackPoolSize {
			leased := lease
			t.Cleanup(func() { _ = leased.Close() })
			for _, conn := range sockets {
				_ = conn.Close()
			}
			if runtime.GOOS == "windows" {
				time.Sleep(20 * time.Millisecond)
			}
			return start
		}
		for _, conn := range sockets {
			_ = conn.Close()
		}
		_ = lease.Close()
	}
	t.Fatalf("could not allocate a free 512-port UDP range after %d attempts: range=%s error=%v", attempts, lastRange, lastErr)
	return 0
}
