package dnfbridge

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
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

// Exercise the actual settlement retry command before losing 1P. The retained
// 2P/3P relay sockets must still carry both directions without re-learning a
// replacement party, even though leadership and the visible roster changed.
func TestThreeMemberRetryThenLeaderDisconnectKeepsSurvivorTraffic(t *testing.T) {
	restoreDispatch := partyDungeonLoadingBarrierDispatch
	partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
	defer func() { partyDungeonLoadingBarrierDispatch = restoreDispatch }()
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
	oldInstance := leader.dungeon.runtime.partyInstance
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
	instance := leader.dungeon.runtime.partyInstance
	if instance == nil || instance == oldInstance || instance.key.PartyID != before.ID {
		t.Fatal("retry did not replace the completed instance inside the same party")
	}
	for _, peer := range []*gameSession{leader, second, third} {
		runtime := peer.dungeon.runtime
		if runtime == nil || runtime.partyInstance != instance || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || runtime.townReturnPending {
			t.Fatalf("retry split member %d from the active instance", peer.selectedCharacterID)
		}
	}
	if !s.syncRuntimePartyUDPRelay(runtimePartyStateSnapshot(second)) {
		t.Fatal("three-member relay was not ready")
	}
	_, secondPort, secondOK := relay.Endpoint(before.ID, 100, 101)
	_, thirdPort, thirdOK := relay.Endpoint(before.ID, 101, 100)
	if !secondOK || !thirdOK {
		t.Fatal("survivor relay pair missing before disconnect")
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
	assertTraffic("before-disconnect")
	s.unbindGameSession(leader)
	for _, peer := range []*gameSession{second, third} {
		after, found := s.runtimePartyManagerForService().SnapshotByUser(peer.selectedCharacterID, 0)
		if !found || after.ID != before.ID || after.Leader != 100 || len(after.Members) != 2 {
			t.Fatal("leader disconnect replaced the surviving party")
		}
		key, found := s.currentDungeonPartyRunKey(peer)
		if !found || key.PartyID != after.ID || peer.dungeon.runtime.partyInstance != instance || peer.dungeon.runtime.townReturnPending {
			t.Fatal("survivor run no longer belongs to its party")
		}
	}
	_, secondAfter, secondOK := relay.Endpoint(before.ID, 100, 101)
	_, thirdAfter, thirdOK := relay.Endpoint(before.ID, 101, 100)
	if !secondOK || !thirdOK || secondAfter != secondPort || thirdAfter != thirdPort {
		t.Fatal("disconnect replaced the established survivor relay ports")
	}
	if _, _, ok := relay.Endpoint(before.ID, 99, 100); ok {
		t.Fatal("departed leader retained a routable relay leg")
	}
	if len(dungeonPartyInstanceMemberSessions(instance, 0)) != 2 {
		t.Fatal("departed leader retained a shared-instance occupant")
	}
	assertTraffic("after-disconnect")
}
