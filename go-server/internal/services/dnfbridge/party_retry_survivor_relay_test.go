package dnfbridge

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type partyRetryRelayConn struct {
	*partyDungeonSyncConn
	ip byte
}

func (c *partyRetryRelayConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, c.ip), Port: 10000 + int(c.ip)}
}

// Exercise every current three-member update path that removes 1P while 2P and
// 3P remain in the same dungeon instance. The retained 2P/3P relay sockets must
// still carry both directions without re-learning replacement ports, even
// though leadership and the visible roster changed.
func TestThreeMemberRetryThenLeaderDisconnectKeepsSurvivorTraffic(t *testing.T) {
	restoreDispatch := partyDungeonLoadingBarrierDispatch
	partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
	defer func() { partyDungeonLoadingBarrierDispatch = restoreDispatch }()
	t.Run("retry_then_disconnect", func(t *testing.T) {
		testThreeMemberLeaderExitKeepsSurvivorTraffic(t, true, func(t *testing.T, s *Service, leader *gameSession) {
			s.unbindGameSession(leader)
		})
	})
	t.Run("first_entry_op42_town_confirmation", func(t *testing.T) {
		testThreeMemberLeaderExitKeepsSurvivorTraffic(t, false, func(t *testing.T, s *Service, leader *gameSession) {
			leaderExitThroughTownSceneConfirmation(
				t,
				s,
				leader,
				dnfenum.CmdPacketGiveupGame,
				"party_retry_survivor_test_op42_town_confirmation",
			)
		})
	})
	t.Run("first_entry_op13_town_confirmation", func(t *testing.T) {
		testThreeMemberLeaderExitKeepsSurvivorTraffic(t, false, func(t *testing.T, s *Service, leader *gameSession) {
			leaderExitThroughTownSceneConfirmation(
				t,
				s,
				leader,
				dnfenum.CmdPacketLeaveParty,
				"party_retry_survivor_test_op13_town_confirmation",
			)
		})
	})
}

func testThreeMemberLeaderExitKeepsSurvivorTraffic(
	t *testing.T,
	exerciseRetry bool,
	exitLeader func(*testing.T, *Service, *gameSession),
) {
	t.Helper()
	s, leader, second, leaderConn, secondConn, trace := partyDungeonSyncFixture(t)
	s.options.currentClientVersion = "official-24.9"
	leader.conn = &partyRetryRelayConn{leaderConn, 1}
	second.conn = &partyRetryRelayConn{secondConn, 2}
	repos, _ := s.repositoryGroup()
	character, _, err := repos.Character.Load(context.Background(), "100")
	if err != nil {
		t.Fatal(err)
	}
	character = dnfrepo.CloneCharacter(character)
	character.CharacterID = "101"
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "101", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	third := &gameSession{conn: &partyRetryRelayConn{&partyDungeonSyncConn{charID: 101, trace: trace}, 3}, channel: leader.channel, residentChannel: leader.channel}
	s.bindGameSessionCharacter(third, 101)
	state := runtimePartyStateSnapshot(leader)
	state.Members = append(state.Members, alignedcmd.PartyMemberState{UserID: 101, UserState: 1, HPPercent: 100, MPPercent: 100})
	for _, peer := range []*gameSession{leader, second, third} {
		storeRuntimePartyState(peer, state)
	}
	if party, ok := s.bootstrapManagedRuntimePartyForSession(leader); !ok || len(party.Members) != 3 {
		t.Fatal("fixture did not register its three-member lobby")
	}
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 101, TownID: 38, AreaID: 1, Session: third})
	bindDungeonSelectorOriginForTestAt(t, s, third, 38, 1, 450, 234)
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 12, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.partyUDPRelay = relay
	t.Cleanup(func() { _ = relay.Close() })
	runPartyDungeonSyncEntry(t, s, leader)
	finishLoading := func() {
		t.Helper()
		for _, peer := range []*gameSession{leader, second, third} {
			if err := s.sendFinishLoadingStatus(peer, make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
				t.Fatal(err)
			}
		}
		for _, peer := range []*gameSession{leader, second, third} {
			if s.partyDungeonPostStartMapPlacementPending(peer) || !peer.finishLoadingGateSnapshot().State {
				t.Fatalf("member %d did not finish loading", peer.selectedCharacterID)
			}
		}
	}
	finishLoading()
	before, found := s.runtimePartyManagerForService().SnapshotByUser(99, 0)
	if !found || len(before.Members) != 3 {
		t.Fatal("fixture did not enter a managed three-member party")
	}
	if leader.dungeon.runtime == nil || second.dungeon.runtime == nil || third.dungeon.runtime == nil {
		t.Fatal("fixture did not enter all three members")
	}
	firstInstance := leader.dungeon.runtime.partyInstance
	if firstInstance == nil {
		t.Fatal("fixture did not enter a shared three-member instance")
	}
	instance := firstInstance
	if exerciseRetry {
		for _, peer := range []*gameSession{leader, second, third} {
			completePartyRetryFixture(t, s, peer)
		}
		if err := s.handleGameCommand(leader, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketEplpCommand), []byte{1, 0}); err != nil {
			t.Fatal(err)
		}
		if leader.dungeon.runtime == nil {
			t.Fatal("retry removed the leader from its dungeon")
		}
		finishLoading()
		instance = leader.dungeon.runtime.partyInstance
		if instance == nil || instance == firstInstance || instance.key.PartyID != before.ID {
			t.Fatal("retry did not replace the completed instance inside the same party")
		}
	}
	for _, peer := range []*gameSession{leader, second, third} {
		runtime := peer.dungeon.runtime
		if runtime == nil || runtime.partyInstance != instance || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || runtime.townReturnPending {
			t.Fatalf("entry path split member %d from the active instance", peer.selectedCharacterID)
		}
	}
	if !s.syncRuntimePartyUDPRelay(runtimePartyStateSnapshot(second)) {
		t.Fatal("three-member relay was not ready")
	}
	_, secondPort, secondOK := relay.Endpoint(before.ID, 100, 101)
	_, thirdPort, thirdOK := relay.Endpoint(before.ID, 101, 100)
	if !secondOK || !thirdOK {
		t.Fatal("survivor relay pair missing before leader exit")
	}
	secondUDP := testCurrentPartyUDPClient(t, "127.0.0.2")
	thirdUDP := testCurrentPartyUDPClient(t, "127.0.0.3")
	t.Cleanup(func() { _ = secondUDP.Close(); _ = thirdUDP.Close() })
	assertTraffic := func(label string) {
		t.Helper()
		secondTarget := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(secondPort)}
		thirdTarget := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(thirdPort)}
		fromSecond, fromThird := []byte(label+"-2P"), []byte(label+"-3P")
		gotSecond, gotThird := false, false
		deadline := time.Now().Add(5 * time.Second)
		buffer := make([]byte, 128)
		for time.Now().Before(deadline) && (!gotSecond || !gotThird) {
			if _, err := secondUDP.WriteToUDP(fromSecond, secondTarget); err != nil {
				t.Fatal(err)
			}
			if _, err := thirdUDP.WriteToUDP(fromThird, thirdTarget); err != nil {
				t.Fatal(err)
			}
			_ = secondUDP.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			if n, _, err := secondUDP.ReadFromUDP(buffer); err == nil && bytes.Equal(buffer[:n], fromThird) {
				gotSecond = true
			}
			_ = thirdUDP.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			if n, _, err := thirdUDP.ReadFromUDP(buffer); err == nil && bytes.Equal(buffer[:n], fromSecond) {
				gotThird = true
			}
		}
		if !gotSecond || !gotThird {
			t.Fatalf("%s traffic: 2P received 3P=%t; 3P received 2P=%t", label, gotSecond, gotThird)
		}
	}
	assertTraffic("before-leader-exit")
	exitLeader(t, s, leader)
	after, found := s.runtimePartyManagerForService().SnapshotByUser(100, 0)
	if !found || after.ID != before.ID || after.Leader != 100 || len(after.Members) != 2 {
		t.Fatal("leader exit replaced the surviving party")
	}
	members := make(map[uint16]bool, len(after.Members))
	for _, member := range after.Members {
		members[member.UserID] = true
	}
	if !members[100] || !members[101] || members[99] {
		t.Fatalf("surviving party members=%v want 100 and 101", members)
	}
	for _, peer := range []*gameSession{second, third} {
		peerParty, found := s.runtimePartyManagerForService().SnapshotByUser(peer.selectedCharacterID, 0)
		if !found || peerParty.ID != after.ID || peerParty.Leader != after.Leader || len(peerParty.Members) != len(after.Members) {
			t.Fatal("survivor does not observe the transferred party")
		}
		key, found := s.currentDungeonPartyRunKey(peer)
		if !found || key.PartyID != after.ID || peer.dungeon.runtime.partyInstance != instance || peer.dungeon.runtime.townReturnPending {
			t.Fatal("survivor run no longer belongs to its party")
		}
	}
	_, secondAfter, secondOK := relay.Endpoint(before.ID, 100, 101)
	_, thirdAfter, thirdOK := relay.Endpoint(before.ID, 101, 100)
	if !secondOK || !thirdOK || secondAfter != secondPort || thirdAfter != thirdPort {
		t.Fatal("leader exit replaced the established survivor relay ports")
	}
	if _, _, ok := relay.Endpoint(before.ID, 99, 100); ok {
		t.Fatal("departed leader retained a routable relay leg")
	}
	if len(dungeonPartyInstanceMemberSessions(instance, 0)) != 2 {
		t.Fatal("departed leader retained a shared-instance occupant")
	}
	assertTraffic("after-leader-exit")
}

func leaderExitThroughTownSceneConfirmation(
	t *testing.T,
	s *Service,
	leader *gameSession,
	request dnfenum.CmdPacket,
	source string,
) {
	t.Helper()
	var err error
	switch request {
	case dnfenum.CmdPacketGiveupGame:
		// The current client sends active-run op42 as a bodyless upper packet.
		// Keep op13 on its already-passing legacy fixture path.
		packet, buildErr := dnfproto.BuildChannelPacket(
			uint16(request),
			[]byte{},
			0,
			dnfproto.DefaultChannelClassification,
		)
		if buildErr != nil {
			t.Fatalf("build op%d request: %v", request, buildErr)
		}
		err = s.handleGameUpper(leader, packet)
	case dnfenum.CmdPacketLeaveParty:
		err = s.handleGameCommand(leader, byte(dnfenum.GameCmdCommand), uint16(request), nil)
	default:
		t.Fatalf("unsupported leader exit opcode %d", request)
	}
	if err != nil {
		t.Fatalf("handle leader exit op%d: %v", request, err)
	}
	runtime := leader.dungeon.runtime
	runtimePresent := runtime != nil
	townReturnPending := false
	townReturnRequestMsgID := uint16(0)
	townReturnOp24Sent := false
	if runtime != nil {
		townReturnPending = runtime.townReturnPending
		townReturnRequestMsgID = runtime.townReturnRequestMsgID
		townReturnOp24Sent = runtime.townReturnOp24Sent
	}
	if !runtimePresent || !townReturnPending || townReturnRequestMsgID != uint16(request) {
		t.Fatalf(
			"op%d did not retain the active run pending town evidence: runtime_present=%t town_return_pending=%t town_return_request_msgid=%d town_return_op24_sent=%t",
			request,
			runtimePresent,
			townReturnPending,
			townReturnRequestMsgID,
			townReturnOp24Sent,
		)
	}

	// The current client confirms a pending op24 only through a town-side scene
	// request. This helper is the same commit point used by the existing giveup
	// regression; the synthetic fixture may stop later in the actor rebuild.
	if err := s.commitPendingDungeonReturnForSceneRequest(leader, source); err != nil {
		t.Logf("town actor rebuild fixture dependency after op%d confirmation: %v", request, err)
	}
	if leader.dungeon.runtime != nil {
		t.Fatalf("op%d town confirmation retained the leader dungeon runtime", request)
	}
	if status := runtime.Session.Snapshot().Run.Status; status != worldmap.DungeonRunAbandoned {
		t.Fatalf("op%d town confirmation run status=%s want=%s", request, status, worldmap.DungeonRunAbandoned)
	}
}
