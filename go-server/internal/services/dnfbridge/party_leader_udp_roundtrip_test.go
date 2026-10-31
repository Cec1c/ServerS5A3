package dnfbridge

// This regression combines the op11/op16/op37 lifecycle with loopback UDP.
// UDP payloads below are synthetic transport bytes; they make no claim about
// the game P2P wire format or native movement behavior.

import (
	"bytes"
	"net"
	"testing"
	"time"
)

const (
	leaderUDPDraftReadWindow = 100 * time.Millisecond
	leaderUDPDraftBudget     = 3 * time.Second
)

type leaderUDPDraftEndpoints struct {
	abAddress [4]byte
	abPort    uint16
	baAddress [4]byte
	baPort    uint16
}

// TestLeaderHandoffAfterCompletedRunKeepsUDPBothWaysBothRounds keeps the
// existing two-round lifecycle and adds a loopback transport assertion after
// each pair of real op37 packets. Synthetic characters are exactly 99 and 100.
func TestLeaderHandoffAfterCompletedRunKeepsUDPBothWaysBothRounds(t *testing.T) {
	f := newLeaderHandoffRoundtripFixture(t)
	if f.leader.selectedCharacterID != inviteAcceptLeaderCharacterID ||
		f.follower.selectedCharacterID != inviteAcceptFollowerCharacterID {
		t.Fatalf("synthetic characters=%d/%d want=%d/%d",
			f.leader.selectedCharacterID,
			f.follower.selectedCharacterID,
			inviteAcceptLeaderCharacterID,
			inviteAcceptFollowerCharacterID)
	}

	// syncRuntimePartyUDPRelay requires a concrete IPv4 RemoteAddr from each
	// member's TCP session. Install those buffers before party formation so the
	// real op11 accept path itself creates the relay room.
	loopbackIPv4 := net.IPv4(127, 0, 0, 1)
	f.leader.conn = &tcpAddrBufferConn{
		addr: &net.TCPAddr{IP: loopbackIPv4, Port: 12001},
	}
	f.follower.conn = &tcpAddrBufferConn{
		addr: &net.TCPAddr{IP: loopbackIPv4, Port: 12002},
	}
	relay, err := newCurrentPartyUDPRelay(
		true,
		"127.0.0.1",
		testCurrentPartyUDPRelayPortStart(t),
		12,
		nil,
	)
	if err != nil {
		t.Fatalf("create loopback party UDP relay: %v", err)
	}
	t.Cleanup(func() {
		if err := relay.Close(); err != nil {
			t.Errorf("close loopback party UDP relay: %v", err)
		}
	})
	f.service.mu.Lock()
	f.service.partyUDPRelay = relay
	f.service.mu.Unlock()

	f.formPartyThroughClientAccept(t)

	oldLeader := f.leader
	newLeader := f.follower
	formed := leaderHandoffRequireParty(t, f.service, oldLeader, inviteAcceptLeaderCharacterID, 0)
	partyID := uint16(formed.PartyID)
	endpoints := leaderUDPDraftCaptureEndpoints(t, relay, partyID)

	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	t.Cleanup(func() {
		if err := clientA.Close(); err != nil {
			t.Errorf("close synthetic UDP client 99: %v", err)
		}
	})
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	t.Cleanup(func() {
		if err := clientB.Close(); err != nil {
			t.Errorf("close synthetic UDP client 100: %v", err)
		}
	})
	endpointAB := &net.UDPAddr{
		IP:   net.IPv4(endpoints.abAddress[0], endpoints.abAddress[1], endpoints.abAddress[2], endpoints.abAddress[3]),
		Port: int(endpoints.abPort),
	}
	endpointBA := &net.UDPAddr{
		IP:   net.IPv4(endpoints.baAddress[0], endpoints.baAddress[1], endpoints.baAddress[2], endpoints.baAddress[3]),
		Port: int(endpoints.baPort),
	}

	// First real run.
	f.selectDungeon(t, oldLeader, 700)
	leaderHandoffRequireHeldForLoading(t, f.service, partyID, oldLeader, newLeader)
	f.finishLeaderHandoffLoading(t, oldLeader, newLeader)
	leaderHandoffRequireLoadingReleased(t, f.service, partyID, oldLeader, newLeader)
	oldInstance := leaderHandoffSharedInstance(t, oldLeader, newLeader)
	leaderUDPDraftRoundtrip(t, clientA, clientB, endpointAB, endpointBA, "round-1")
	leaderUDPDraftRequireEndpoints(t, relay, partyID, endpoints, "after first-round UDP")

	// Complete both returns through the existing helper semantics, then restore
	// the real town selector origins before changing the authoritative leader.
	returnFixture := completionReturnFixture{service: f.service}
	oldLeaderRuntime := leaderHandoffRequireRuntime(t, oldLeader, "first completed return")
	newLeaderRuntime := leaderHandoffRequireRuntime(t, newLeader, "first completed return")
	returnFixture.completeRunAndReturnToTown(t, oldLeader, oldLeaderRuntime)
	if got := snapshotPartyDungeonLoadingOwner(oldLeader).runtime; got != nil {
		t.Fatalf("old leader runtime was not detached after completed return: got=%p", got)
	}
	if got := oldInstance.memberCount(); got != 1 {
		t.Fatalf("old instance member count after the first completed return=%d want 1", got)
	}
	returnFixture.completeRunAndReturnToTown(t, newLeader, newLeaderRuntime)
	if got := snapshotPartyDungeonLoadingOwner(newLeader).runtime; got != nil {
		t.Fatalf("new leader runtime was not detached after completed return: got=%p", got)
	}
	if got := oldInstance.memberCount(); got != 0 {
		t.Fatalf("old instance member count after both completed returns=%d want 0", got)
	}
	if got := f.service.partyDungeonInstanceCount(); got != 0 {
		t.Fatalf("party dungeon instance count after both returns=%d want 0", got)
	}
	leaderUDPDraftRequireEndpoints(t, relay, partyID, endpoints, "after both completed returns")

	bindDungeonSelectorOriginForTestAt(t, f.service, oldLeader, 38, 1, 450, 234)
	bindDungeonSelectorOriginForTestAt(t, f.service, newLeader, 38, 1, 450, 234)
	leaderHandoffRequireParty(t, f.service, oldLeader, inviteAcceptLeaderCharacterID, partyID)
	if err := f.service.handleOnlineChangePartyHost(oldLeader, []byte{1}); err != nil {
		t.Fatalf("change host through selected slot 1: %v", err)
	}
	leaderHandoffRequireParty(t, f.service, oldLeader, inviteAcceptFollowerCharacterID, partyID)
	leaderUDPDraftRequireEndpoints(t, relay, partyID, endpoints, "after leader transfer")

	// Second real run: the delegated leader selects dungeon 700 and both members
	// submit the real legacy op37 body.
	f.selectDungeon(t, newLeader, 700)
	leaderHandoffRequireHeldForLoading(t, f.service, partyID, oldLeader, newLeader)
	f.finishLeaderHandoffLoading(t, oldLeader, newLeader)
	leaderHandoffRequireLoadingReleased(t, f.service, partyID, oldLeader, newLeader)
	newInstance := leaderHandoffSharedInstance(t, oldLeader, newLeader)
	if newInstance == oldInstance {
		t.Fatalf("second run reused the retired shared instance %p", oldInstance)
	}
	if got := f.service.partyDungeonInstanceCount(); got != 1 {
		t.Fatalf("party dungeon instance count on the second run=%d want 1", got)
	}
	leaderUDPDraftRoundtrip(t, clientA, clientB, endpointAB, endpointBA, "round-2")
	leaderUDPDraftRequireEndpoints(t, relay, partyID, endpoints, "after second-round UDP")
	leaderHandoffRequireParty(t, f.service, oldLeader, inviteAcceptFollowerCharacterID, partyID)
}

func leaderUDPDraftCaptureEndpoints(
	t *testing.T,
	relay *currentPartyUDPRelay,
	partyID uint16,
) leaderUDPDraftEndpoints {
	t.Helper()
	abAddress, abPort, ok := relay.Endpoint(partyID, inviteAcceptLeaderCharacterID, inviteAcceptFollowerCharacterID)
	if !ok {
		t.Fatalf("missing 99->100 relay endpoint after party formation")
	}
	baAddress, baPort, ok := relay.Endpoint(partyID, inviteAcceptFollowerCharacterID, inviteAcceptLeaderCharacterID)
	if !ok {
		t.Fatalf("missing 100->99 relay endpoint after party formation")
	}
	if abPort == 0 || baPort == 0 || abPort == baPort {
		t.Fatalf("invalid directed relay ports: 99->100=%d 100->99=%d", abPort, baPort)
	}
	return leaderUDPDraftEndpoints{
		abAddress: abAddress,
		abPort:    abPort,
		baAddress: baAddress,
		baPort:    baPort,
	}
}

func leaderUDPDraftRequireEndpoints(
	t *testing.T,
	relay *currentPartyUDPRelay,
	partyID uint16,
	want leaderUDPDraftEndpoints,
	stage string,
) {
	t.Helper()
	abAddress, abPort, ok := relay.Endpoint(partyID, inviteAcceptLeaderCharacterID, inviteAcceptFollowerCharacterID)
	if !ok {
		t.Fatalf("%s: 99->100 relay endpoint disappeared", stage)
	}
	baAddress, baPort, ok := relay.Endpoint(partyID, inviteAcceptFollowerCharacterID, inviteAcceptLeaderCharacterID)
	if !ok {
		t.Fatalf("%s: 100->99 relay endpoint disappeared", stage)
	}
	got := leaderUDPDraftEndpoints{
		abAddress: abAddress,
		abPort:    abPort,
		baAddress: baAddress,
		baPort:    baPort,
	}
	if got != want {
		t.Fatalf("%s: relay endpoints changed: got=%+v want=%+v", stage, got, want)
	}
}

// leaderUDPDraftRoundtrip proves both directions independently. For 99->100,
// B first sends to BA so the relay learns B's source endpoint; the delivered
// datagram must then originate from BA. The symmetric check applies to 100->99.
func leaderUDPDraftRoundtrip(
	t *testing.T,
	client99 *net.UDPConn,
	client100 *net.UDPConn,
	endpoint99To100 *net.UDPAddr,
	endpoint100To99 *net.UDPAddr,
	round string,
) {
	t.Helper()
	payload99To100 := []byte("synthetic-udp-" + round + "-99-to-100")
	payload100To99 := []byte("synthetic-udp-" + round + "-100-to-99")
	handshake100 := []byte("synthetic-udp-" + round + "-100-handshake")

	leaderUDPDraftExchange(
		t,
		client100,
		func() error {
			if _, err := client100.WriteToUDP(handshake100, endpoint100To99); err != nil {
				return err
			}
			_, err := client99.WriteToUDP(payload99To100, endpoint99To100)
			return err
		},
		payload99To100,
		endpoint100To99.Port,
		round+" synthetic 99->100",
	)

	leaderUDPDraftExchange(
		t,
		client99,
		func() error {
			_, err := client100.WriteToUDP(payload100To99, endpoint100To99)
			return err
		},
		payload100To99,
		endpoint99To100.Port,
		round+" synthetic 100->99",
	)
}

func leaderUDPDraftExchange(
	t *testing.T,
	receiver *net.UDPConn,
	resend func() error,
	wantPayload []byte,
	wantSourcePort int,
	stage string,
) {
	t.Helper()
	deadline := time.Now().Add(leaderUDPDraftBudget)
	buffer := make([]byte, 256)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("%s: no matching UDP datagram within %s", stage, leaderUDPDraftBudget)
		}
		if err := resend(); err != nil {
			t.Fatalf("%s: send synthetic UDP payload: %v", stage, err)
		}
		if err := receiver.SetReadDeadline(time.Now().Add(leaderUDPDraftReadWindow)); err != nil {
			t.Fatalf("%s: set UDP read deadline: %v", stage, err)
		}
		n, source, err := receiver.ReadFromUDP(buffer)
		if err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				if time.Now().After(deadline) {
					t.Fatalf("%s: no matching UDP datagram within %s: %v", stage, leaderUDPDraftBudget, err)
				}
				continue
			}
			t.Fatalf("%s: receive synthetic UDP payload: %v", stage, err)
		}
		if !bytes.Equal(buffer[:n], wantPayload) {
			continue
		}
		if !source.IP.Equal(net.IPv4(127, 0, 0, 1)) {
			t.Fatalf("%s: relayed UDP source IP=%s want=127.0.0.1", stage, source.IP)
		}
		if source.Port != wantSourcePort {
			t.Fatalf("%s: relayed UDP source port=%d want=%d", stage, source.Port, wantSourcePort)
		}
		return
	}
}
