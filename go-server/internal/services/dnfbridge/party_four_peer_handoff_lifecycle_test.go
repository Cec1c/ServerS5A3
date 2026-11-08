package dnfbridge

// Four synthetic characters exercise the server lifecycle and loopback UDP
// transport only. These bytes are not native movement packets, and a passing
// test cannot establish that a game client renders peer models or actions.

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var fourPeerIDs = []uint16{99, 100, 101, 102}

type fourPeerLeg struct{ owner, peer uint16 }
type fourPeerEndpoint struct {
	address [4]byte
	port    uint16
}

func TestFourPeerHandoffCompletedReentryAndDirectedUDP(t *testing.T) {
	runFourPeerHandoffLifecycle(t, 0)
}

func TestFourPeerTownRepeatedReformationBeforeFirstEntry(t *testing.T) {
	runFourPeerHandoffLifecycle(t, 4)
}

func runFourPeerHandoffLifecycle(t *testing.T, townReformations int) {
	t.Helper()
	f := newLeaderHandoffRoundtripFixture(t)
	logger, logPath := newPeerEndpointTestLogger(t)
	f.service.packetLog = logger
	t.Cleanup(func() {
		logged := readPeerEndpointTestLog(t, logger, logPath)
		if t.Failed() {
			for _, line := range strings.Split(logged, "\n") {
				if strings.Contains(line, "error=") {
					t.Log(line)
				}
			}
		}
	})
	f.service.options.partyFormationOrder = partyFormationOrderVendorWire
	f.service.options.partyPeerEndpointsAfterRoster = partyPeerEndpointsAfterRosterRepeat
	// Empty is the official-24.9 production default, and exercises the same-party
	// town suppression while the real invite and host-change paths publish rosters.
	t.Setenv(townCopresenceSkipSamePartyFrameEnv, "")

	sessions := map[uint16]*gameSession{99: f.leader, 100: f.follower}
	repos, ok := f.service.repositoryGroup()
	if !ok || repos.Character == nil || repos.Inventory == nil {
		t.Fatal("four-peer fixture lacks character/inventory repositories")
	}
	for _, id := range []uint16{101, 102} {
		character, found, err := repos.Character.Load(context.Background(), "100")
		if err != nil || !found {
			t.Fatalf("load synthetic character template: found=%t err=%v", found, err)
		}
		character = dnfrepo.CloneCharacter(character)
		character.CharacterID = fmt.Sprint(id)
		character.Name = fmt.Sprintf("synthetic-%d", id)
		if err := repos.Character.Save(context.Background(), character); err != nil {
			t.Fatalf("save synthetic character %d: %v", id, err)
		}
		if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
			CharacterID: fmt.Sprint(id), Slots: map[string]dnfrepo.ItemStack{},
		}); err != nil {
			t.Fatalf("save synthetic inventory %d: %v", id, err)
		}
		enableTownMoveSkillProjection(t, f.service, repos, fmt.Sprint(id))
		session := &gameSession{connID: fmt.Sprintf("four-peer-%d", id), channel: f.channel, residentChannel: f.channel}
		f.service.bindGameSessionCharacter(session, id)
		f.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: id, TownID: 38, AreaID: 1, Session: session})
		bindDungeonSelectorOriginForTestAt(t, f.service, session, 38, 1, 450, 234)
		sessions[id] = session
	}

	for _, id := range fourPeerIDs {
		sessions[id].conn = &tcpAddrBufferConn{addr: &net.TCPAddr{
			IP: net.IPv4(127, 0, 0, 1), Port: int(12000 + id),
		}}
	}
	// Rebinding slot 3 allocates six new legs before closing its old six.
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 24, nil)
	if err != nil {
		t.Fatalf("create four-peer loopback relay: %v", err)
	}
	t.Cleanup(func() {
		if err := relay.Close(); err != nil {
			t.Errorf("close relay: %v", err)
		}
	})
	f.service.mu.Lock()
	f.service.partyUDPRelay = relay
	f.service.mu.Unlock()

	f.formPartyThroughClientAccept(t)
	for _, id := range []uint16{101, 102} {
		fourPeerInviteAccept(t, f.service, 99, sessions[id])
	}
	partyID := fourPeerRequireRoster(t, f.service, sessions, 99, 0)
	endpoints := fourPeerRequireEndpoints(t, relay, partyID, fourPeerIDs)
	clients := make(map[uint16]*net.UDPConn, 4)
	for _, id := range fourPeerIDs {
		id := id
		clients[id] = testCurrentPartyUDPClient(t, "127.0.0.1")
		t.Cleanup(func() { _ = clients[id].Close() })
	}

	// Every op121 names a fixed party slot, including a transfer back to slot 0.
	// Fully dissolve and recreate the four-person party repeatedly, without any
	// dungeon entry between formations. No old party may retain relay legs.
	for cycle := 0; cycle < townReformations; cycle++ {
		for _, step := range []struct {
			from, to uint16
			slot     byte
		}{{99, 102, 3}, {102, 100, 1}, {100, 101, 2}, {101, 99, 0}} {
			fourPeerTransfer(t, f.service, sessions, partyID, step.from, step.to, step.slot)
			fourPeerRequireSameEndpoints(t, relay, partyID, endpoints)
		}
		oldPartyID := partyID
		for _, id := range []uint16{102, 101, 100, 99} {
			if err := f.service.handleOnlineLeaveParty(sessions[id], uint16(dnfenum.CmdPacketLeaveParty)); err != nil {
				t.Fatalf("town reformation %d leave %d: %v", cycle, id, err)
			}
		}
		for _, id := range fourPeerIDs {
			if _, found := f.service.runtimePartyManagerForService().SnapshotByUser(id, 0); found {
				t.Fatalf("town reformation %d retained member %d", cycle, id)
			}
		}
		for leg := range endpoints {
			if _, _, found := relay.Endpoint(oldPartyID, leg.owner, leg.peer); found {
				t.Fatalf("town reformation %d retained old relay leg %+v", cycle, leg)
			}
		}
		f.formPartyThroughClientAccept(t)
		for _, id := range []uint16{101, 102} {
			fourPeerInviteAccept(t, f.service, 99, sessions[id])
		}
		partyID = fourPeerRequireRoster(t, f.service, sessions, 99, 0)
		if partyID == oldPartyID {
			t.Fatalf("town reformation %d reused dissolved party identity %d", cycle, partyID)
		}
		endpoints = fourPeerRequireEndpoints(t, relay, partyID, fourPeerIDs)
		if got := f.service.partyDungeonInstanceCount(); got != 0 {
			t.Fatalf("town reformation %d unexpectedly created %d dungeon instances", cycle, got)
		}
	}
	for _, step := range []struct {
		from, to uint16
		slot     byte
	}{
		{99, 101, 2}, {101, 100, 1}, {100, 102, 3}, {102, 99, 0},
	} {
		fourPeerTransfer(t, f.service, sessions, partyID, step.from, step.to, step.slot)
		fourPeerRequireSameEndpoints(t, relay, partyID, endpoints)
	}

	var previous *dungeonPartyInstance
	for round, leader := range []uint16{99, 100} {
		if round == 1 {
			for _, step := range []struct {
				from, to uint16
				slot     byte
			}{
				{99, 102, 3}, {102, 101, 2}, {101, 100, 1},
			} {
				fourPeerTransfer(t, f.service, sessions, partyID, step.from, step.to, step.slot)
				fourPeerRequireSameEndpoints(t, relay, partyID, endpoints)
			}
		}
		for _, id := range fourPeerIDs {
			sessions[id].conn.(*tcpAddrBufferConn).write.Reset()
		}
		f.selectDungeon(t, sessions[leader], 700)
		fourPeerRequireRoster(t, f.service, sessions, leader, partyID)
		fourPeerRequireLoading(t, f.service, partyID, sessions, false)
		for index, id := range fourPeerIDs {
			if err := f.service.sendFinishLoadingStatus(sessions[id], make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
				t.Fatalf("round %d real op37 char %d: %v", round+1, id, err)
			}
			if index < len(fourPeerIDs)-1 {
				f.service.partyDungeonLoadingMu.Lock()
				cycle := f.service.partyDungeonLoadingCycles[partyID]
				f.service.partyDungeonLoadingMu.Unlock()
				if cycle == nil || cycle.Released {
					t.Fatalf("round %d loading released after only %d of 4 op37 requests", round+1, index+1)
				}
				for _, peerID := range fourPeerIDs {
					if !f.service.partyDungeonPostStartMapPlacementPending(sessions[peerID]) {
						t.Fatalf("round %d char %d placed before all four op37 requests", round+1, peerID)
					}
				}
			}
		}
		fourPeerRequireLoading(t, f.service, partyID, sessions, true)
		for _, id := range fourPeerIDs {
			requireInitialFourPeerPlacements(t, sessions[id], id)
		}
		// A repeated loading request must not reapply the native HP/MP-setting
		// placements after the initial room entry has already completed.
		for _, id := range fourPeerIDs {
			connection := sessions[id].conn.(*tcpAddrBufferConn)
			offset := connection.write.Len()
			if err := f.service.sendFinishLoadingStatus(sessions[id], make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
				t.Fatal(err)
			}
			data := connection.write.Bytes()[offset:]
			for len(data) > 0 {
				packet, rest := splitCurrentGameServerUpperPacketAuto(t, data)
				data = rest
				if packet.Header.Classification == 0 && packet.Header.MsgID == 120 {
					t.Fatalf("duplicate op37 repeated initial placement for %d", id)
				}
			}
		}
		var instance *dungeonPartyInstance
		for _, id := range fourPeerIDs {
			runtime := leaderHandoffRequireRuntime(t, sessions[id], "four-peer entry")
			if runtime.partyInstance == nil {
				t.Fatalf("round %d char %d has no shared instance", round+1, id)
			}
			if instance == nil {
				instance = runtime.partyInstance
			} else if instance != runtime.partyInstance {
				t.Fatalf("round %d char %d entered another instance", round+1, id)
			}
		}
		if instance == previous || instance.memberCount() != 4 {
			t.Fatalf("round %d instance=%p previous=%p members=%d", round+1, instance, previous, instance.memberCount())
		}
		fourPeerExchangeAllDirections(t, clients, endpoints, fmt.Sprintf("round-%d", round+1))
		fourPeerRequireSameEndpoints(t, relay, partyID, endpoints)
		returner := completionReturnFixture{service: f.service}
		for index, id := range fourPeerIDs {
			returner.completeRunAndReturnToTown(t, sessions[id], leaderHandoffRequireRuntime(t, sessions[id], "completed return"))
			if got := instance.memberCount(); got != 3-index {
				t.Fatalf("round %d returned char %d: instance members=%d want=%d", round+1, id, got, 3-index)
			}
			bindDungeonSelectorOriginForTestAt(t, f.service, sessions[id], 38, 1, 450, 234)
		}
		if got := f.service.partyDungeonInstanceCount(); got != 0 {
			t.Fatalf("round %d retained %d dungeon instances", round+1, got)
		}
		fourPeerRequireRoster(t, f.service, sessions, leader, partyID)
		fourPeerRequireSameEndpoints(t, relay, partyID, endpoints)
		previous = instance
	}

	// Leaving slot 3 must leave the other three routable and preserve their
	// fixed slots. A new TCP session for char 102 then joins through op11; its
	// relay legs must be a new generation while survivor ports remain stable.
	oldIdentity, bound := f.service.boundGameSessionCharacterSnapshot(sessions[102])
	if !bound {
		t.Fatal("slot-3 session was not bound before leave")
	}
	if err := f.service.handleOnlineLeaveParty(sessions[102], uint16(dnfenum.CmdPacketLeaveParty)); err != nil {
		t.Fatalf("slot-3 op13 leave: %v", err)
	}
	if _, found := f.service.runtimePartyManagerForService().SnapshotByUser(102, 0); found {
		t.Fatal("char 102 remained in party after op13")
	}
	survivors, found := f.service.runtimePartyManagerForService().SnapshotByID(partyID)
	if !found || survivors.Leader != 100 || len(survivors.Members) != 3 {
		t.Fatalf("slot-3 leave changed survivor party: found=%t snapshot=%+v", found, survivors)
	}
	for _, id := range []uint16{99, 100, 101} {
		kept := false
		for _, member := range survivors.Members {
			if member.UserID == id && member.Slot == byte(id-99) {
				kept = true
			}
		}
		if !kept {
			t.Fatalf("slot-3 leave moved survivor %d: %+v", id, survivors.Members)
		}
	}
	for leg := range endpoints {
		_, _, present := relay.Endpoint(partyID, leg.owner, leg.peer)
		if leg.owner == 102 || leg.peer == 102 {
			if present {
				t.Fatalf("retired slot-3 leg %+v remains", leg)
			}
		} else if !present {
			t.Fatalf("survivor leg %+v disappeared after slot-3 leave", leg)
		}
	}
	if _, _, left := f.service.onlinePlayers.LeaveAreaSession(102, sessions[102]); !left {
		t.Fatal("departed char 102 still lacked its old town presence")
	}
	newSession := &gameSession{conn: &tcpAddrBufferConn{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 13002}}, connID: "four-peer-102-new-generation", channel: f.channel, residentChannel: f.channel}
	f.service.bindGameSessionCharacter(newSession, 102)
	newIdentity, bound := f.service.boundGameSessionCharacterSnapshot(newSession)
	if !bound || newIdentity.generation == oldIdentity.generation {
		t.Fatalf("slot-3 replacement did not advance session generation: old=%d new=%d bound=%t", oldIdentity.generation, newIdentity.generation, bound)
	}
	f.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 102, TownID: 38, AreaID: 1, Session: newSession})
	bindDungeonSelectorOriginForTestAt(t, f.service, newSession, 38, 1, 450, 234)
	sessions[102] = newSession
	fourPeerInviteAccept(t, f.service, 100, newSession)
	fourPeerRequireRoster(t, f.service, sessions, 100, partyID)
	rejoinedSnapshot, found := f.service.runtimePartyManagerForService().SnapshotByUser(102, newIdentity.generation)
	if !found || rejoinedSnapshot.ID != partyID {
		t.Fatalf("new-generation member did not join original party: found=%t snapshot=%+v", found, rejoinedSnapshot)
	}
	rejoined := fourPeerRequireEndpoints(t, relay, partyID, fourPeerIDs)
	for leg, old := range endpoints {
		if leg.owner != 102 && leg.peer != 102 && rejoined[leg] != old {
			t.Fatalf("survivor endpoint %+v changed across slot-3 rejoin: %v -> %v", leg, old, rejoined[leg])
		}
	}
	fourPeerExchangeAllDirections(t, clients, rejoined, "rejoined-generation")
}

// Assert actual outbound bytes from the full accept/handoff/entry flow. A
// server log saying "each stable slot" is not a substitute for these packets.
func requireInitialFourPeerPlacements(t *testing.T, session *gameSession, local uint16) {
	t.Helper()
	data := session.conn.(*tcpAddrBufferConn).write.Bytes()
	var placed []byte
	profiles := map[uint16]bool{}
	for len(data) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, data)
		data = rest
		if packet.Header.Classification != 0 {
			continue
		}
		body := packet.Body
		if packet.Header.MsgID == 2 && len(body) >= currentDungeonPeerActorMode0KeyOffset+2 && body[0] == 0 && len(placed) == 0 {
			profiles[binary.LittleEndian.Uint16(body[currentDungeonPeerActorMode0KeyOffset:])] = true
		}
		if packet.Header.MsgID != 120 {
			continue
		}
		if len(body) != 2 || body[1] != 0 {
			t.Fatalf("placement body=%x", body)
		}
		if len(placed) == 0 {
			for _, peer := range fourPeerIDs {
				if peer != local && !profiles[peer] {
					t.Fatalf("local %d placed before peer %d descriptor", local, peer)
				}
			}
		}
		placed = append(placed, body[0])
	}
	if len(placed) < 5 || !reflect.DeepEqual(placed[:4], []byte{0, 1, 2, 3}) || placed[4] != byte(local-99) {
		t.Fatalf("local %d slot placement=%v", local, placed)
	}
}

func fourPeerInviteAccept(t *testing.T, service *Service, inviter uint16, joiner *gameSession) {
	t.Helper()
	if ok, reason, _ := service.forceInviteParty(inviter, joiner.selectedCharacterID, 0, [3]uint32{}); !ok {
		t.Fatalf("invite %d -> %d rejected: %s", inviter, joiner.selectedCharacterID, reason)
	}
	if err := service.handleOnlineResponsePeer(joiner, uint16(dnfenum.CmdPacketResponsePeer), buildResponsePeerBody(inviter, 0, 0)); err != nil {
		t.Fatalf("char %d real op11 accept from %d: %v", joiner.selectedCharacterID, inviter, err)
	}
}

func fourPeerRequireRoster(t *testing.T, service *Service, sessions map[uint16]*gameSession, leader, priorID uint16) uint16 {
	t.Helper()
	snapshot, found := service.runtimePartyManagerForService().SnapshotByUser(leader, 0)
	if !found || snapshot.ID == 0 || snapshot.Leader != leader || len(snapshot.Members) != 4 || priorID != 0 && snapshot.ID != priorID {
		t.Fatalf("four-peer manager roster: found=%t id=%d leader=%d members=%+v prior=%d", found, snapshot.ID, snapshot.Leader, snapshot.Members, priorID)
	}
	for _, id := range fourPeerIDs {
		state := runtimePartyStateSnapshot(sessions[id])
		if state.PartyID != int(snapshot.ID) || state.UserID != leader || len(runtimePartyMembers(state)) != 4 {
			t.Fatalf("char %d projection party/leader/members=%d/%d/%d", id, state.PartyID, state.UserID, len(runtimePartyMembers(state)))
		}
		foundSlot := false
		for _, member := range snapshot.Members {
			if member.UserID == id && member.Slot == byte(id-99) {
				foundSlot = true
			}
		}
		if !foundSlot {
			t.Fatalf("char %d lost fixed slot %d: %+v", id, id-99, snapshot.Members)
		}
	}
	return snapshot.ID
}

func fourPeerTransfer(t *testing.T, service *Service, sessions map[uint16]*gameSession, partyID, from, to uint16, slot byte) {
	t.Helper()
	if err := service.handleOnlineChangePartyHost(sessions[from], []byte{slot}); err != nil {
		t.Fatalf("op121 %d -> %d slot %d: %v", from, to, slot, err)
	}
	fourPeerRequireRoster(t, service, sessions, to, partyID)
}

func fourPeerRequireLoading(t *testing.T, service *Service, partyID uint16, sessions map[uint16]*gameSession, released bool) {
	t.Helper()
	service.partyDungeonLoadingMu.Lock()
	cycle := service.partyDungeonLoadingCycles[partyID]
	service.partyDungeonLoadingMu.Unlock()
	if cycle == nil || len(cycle.membersAt) != 4 || cycle.Released != released {
		t.Fatalf("four-peer loading cycle: %+v released want=%t", cycle, released)
	}
	for _, id := range fourPeerIDs {
		pending := service.partyDungeonPostStartMapPlacementPending(sessions[id])
		gates := sessions[id].finishLoadingGateSnapshot()
		if pending == released || released && (!gates.State || !gates.Completion || !gates.PostFinish || !cycle.Ready[id]) || !released && gates != (finishLoadingGates{}) {
			t.Fatalf("char %d loading: released=%t pending=%t gates=%+v ready=%t", id, released, pending, gates, cycle.Ready[id])
		}
	}
}

func fourPeerRequireEndpoints(t *testing.T, relay *currentPartyUDPRelay, partyID uint16, ids []uint16) map[fourPeerLeg]fourPeerEndpoint {
	t.Helper()
	result := make(map[fourPeerLeg]fourPeerEndpoint, len(ids)*(len(ids)-1))
	ports := make(map[uint16]fourPeerLeg)
	for _, owner := range ids {
		for _, peer := range ids {
			if owner == peer {
				continue
			}
			address, port, ok := relay.Endpoint(partyID, owner, peer)
			if !ok || port == 0 || address != [4]byte{127, 0, 0, 1} {
				t.Fatalf("missing/invalid relay endpoint %d -> %d: %v:%d ok=%t", owner, peer, address, port, ok)
			}
			leg := fourPeerLeg{owner, peer}
			if prior, duplicate := ports[port]; duplicate {
				t.Fatalf("directed legs %+v and %+v share port %d", prior, leg, port)
			}
			ports[port] = leg
			result[leg] = fourPeerEndpoint{address, port}
		}
	}
	if len(result) != 12 {
		t.Fatalf("relay has %d directed endpoints, want 12", len(result))
	}
	return result
}

func fourPeerRequireSameEndpoints(t *testing.T, relay *currentPartyUDPRelay, partyID uint16, want map[fourPeerLeg]fourPeerEndpoint) {
	t.Helper()
	got := fourPeerRequireEndpoints(t, relay, partyID, fourPeerIDs)
	for leg, endpoint := range want {
		if got[leg] != endpoint {
			t.Fatalf("endpoint %+v changed: %v -> %v", leg, endpoint, got[leg])
		}
	}
}

func fourPeerExchangeAllDirections(t *testing.T, clients map[uint16]*net.UDPConn, endpoints map[fourPeerLeg]fourPeerEndpoint, stage string) {
	t.Helper()
	for _, owner := range fourPeerIDs {
		for _, peer := range fourPeerIDs {
			if owner == peer {
				continue
			}
			forward := endpoints[fourPeerLeg{owner, peer}]
			reverse := endpoints[fourPeerLeg{peer, owner}]
			forwardAddr := &net.UDPAddr{IP: net.IPv4(forward.address[0], forward.address[1], forward.address[2], forward.address[3]), Port: int(forward.port)}
			reverseAddr := &net.UDPAddr{IP: net.IPv4(reverse.address[0], reverse.address[1], reverse.address[2], reverse.address[3]), Port: int(reverse.port)}
			payload := []byte(fmt.Sprintf("%s-%d-to-%d", stage, owner, peer))
			label := fmt.Sprintf("%s %d->%d", stage, owner, peer)
			leaderUDPDraftExchange(t, clients[peer], func() error {
				if _, err := clients[peer].WriteToUDP([]byte(label+"-ready"), reverseAddr); err != nil {
					return err
				}
				_, err := clients[owner].WriteToUDP(payload, forwardAddr)
				return err
			}, payload, int(reverse.port), label)
		}
	}
}
