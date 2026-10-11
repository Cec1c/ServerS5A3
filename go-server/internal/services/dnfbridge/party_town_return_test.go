package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 出图后"队友看不见"的修复点是这个门：**必须等同一队伍的每个成员都消费完自己的
// op132 场景收尾**（`currentPartyTownReturnSceneReady`）才重放队友角色，否则会把
// 还没收尾的队友选择器弄坏。这条用例钉住"有一个成员没就绪就一个包都不发"。
func TestRebuildRuntimePartyTownCoPresenceWaitsForEveryMember(t *testing.T) {
	repositories, service, source, target, sourceConn, targetConn := newTownCoPresenceTestService(t)
	storeRuntimePartyState(source, townCoPresencePartyState())
	storeRuntimePartyState(target, townCoPresencePartyState())
	if !currentPartyTownReturnSceneReady(source) {
		t.Fatal("source session fixture is not town-ready")
	}
	// 目标成员还没收尾（例如还没回到城镇场景）。
	target.townMu.Lock()
	target.townSceneReadyCharacterID = 0
	target.townMu.Unlock()
	_ = repositories

	sourceConn.write.Reset()
	targetConn.write.Reset()
	if err := service.rebuildRuntimePartyTownCoPresenceAfterReturn(source, "test_wait_for_all"); err != nil {
		t.Fatalf("co-presence rebuild: %v", err)
	}
	if sourceConn.write.Len() != 0 || targetConn.write.Len() != 0 {
		t.Fatalf("a not-yet-consumed peer was replayed: source=%d target=%d",
			sourceConn.write.Len(), targetConn.write.Len())
	}
}

// 全部成员都收尾之后：两个客户端都要收到**对方的**城镇角色（mode0/mode1）+ 区域通知 + 实时名单，
// 这样才谈得上"出图后互相可见"。
func TestRebuildRuntimePartyTownCoPresenceRebuildsBothTownActors(t *testing.T) {
	_, service, source, target, sourceConn, targetConn := newTownCoPresenceTestService(t)
	storeRuntimePartyState(source, townCoPresencePartyState())
	storeRuntimePartyState(target, townCoPresencePartyState())
	for _, session := range []*gameSession{source, target} {
		if !currentPartyTownReturnSceneReady(session) {
			t.Fatalf("session %d fixture is not town-ready", session.selectedCharacterID)
		}
	}

	sourceConn.write.Reset()
	targetConn.write.Reset()
	if err := service.rebuildRuntimePartyTownCoPresenceAfterReturn(source, "test_rebuild"); err != nil {
		t.Fatalf("co-presence rebuild: %v", err)
	}
	if sourceConn.write.Len() == 0 {
		t.Fatal("source received no peer town actor after the return")
	}
	if targetConn.write.Len() == 0 {
		t.Fatal("target received no peer town actor after the return")
	}
	// 双方都必须拿到"对方"的区域通知：op 与 body 由 buildCurrentTownUserAreaNotificationBody 生成，
	// 这里只确认两个连接都收到了非空数据，细节形状由既有 town 用例覆盖。
	if sourceConn.write.Len() == targetConn.write.Len() {
		t.Logf("source/target co-presence byte counts happen to match: %d", sourceConn.write.Len())
	}
}

func TestDirectTownReturnRestoresPartyAfterBothActorGenerations(t *testing.T) {
	drain := func(connection *bufferConn) []upperFrame {
		raw := append([]byte(nil), connection.write.Bytes()...)
		connection.write.Reset()
		var frames []upperFrame
		for len(raw) > 0 {
			packet, rest := splitLongHengGameServerUpperPacket(t, raw)
			frames = append(frames, upperFrame{msgID: packet.Header.MsgID, body: packet.Body})
			raw = rest
		}
		return frames
	}
	repositories, service, leader, member, leaderConn, memberConn := newTownCoPresenceTestService(t)
	character, _, _ := repositories.Character.Load(context.Background(), "1002")
	character.Job = "0"
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	enableTownMoveSkillProjection(t, service, repositories, "1001")
	enableTownMoveSkillProjection(t, service, repositories, "1002")
	for _, session := range []*gameSession{leader, member} {
		storeRuntimePartyState(session, townCoPresencePartyState())
		session.confirmedDungeonReturnStatePending = true
		session.backToVillageEnterSelectPending = true
	}
	if err := service.ensureCurrentConfirmedDungeonReturnPlayerState(leader, "test_op35_leader"); err != nil {
		t.Fatal(err)
	}
	for index, connection := range []*bufferConn{leaderConn, memberConn} {
		selfID := []*gameSession{leader, member}[index].selectedCharacterID
		for _, packet := range drain(connection) {
			// Peer projections belong to the existing town reconstruction. The
			// final realtime table and receiving actor's own party record wait
			// until both actor generations have finished.
			if packet.msgID == 153 || (packet.msgID == 9 && len(packet.body) > 9 && packet.body[9] == 0 && binary.LittleEndian.Uint16(packet.body[4:6]) == selfID) {
				t.Fatalf("receiver %d party restored before follower actor was ready: op=%d body=%x", index, packet.msgID, packet.body)
			}
		}
	}
	if err := service.ensureCurrentConfirmedDungeonReturnPlayerState(member, "test_op35_member"); err != nil {
		t.Fatal(err)
	}
	for index, connection := range []*bufferConn{leaderConn, memberConn} {
		packets := drain(connection)
		if len(packets) < 2 || packets[len(packets)-2].msgID != 153 || packets[len(packets)-1].msgID != 9 {
			t.Fatalf("receiver %d missing final realtime HP then party owner: %+v", index, packets)
		}
		rows := parseRealtimeInfoRows(t, packets[len(packets)-2].body)
		if len(rows) != 2 || rows[0].hp != 100 || rows[1].hp != 100 {
			t.Fatalf("receiver %d party HP rows=%+v", index, rows)
		}
	}
	if !leader.backToVillageEnterSelectPending || !member.backToVillageEnterSelectPending {
		t.Fatal("direct return must retain suppression of the trailing scene-finalizer op15")
	}
}

func townCoPresencePartyState() alignedcmd.PartyState {
	return alignedcmd.PartyState{
		PartyID:    77,
		UserID:     1001,
		IsLeader:   true,
		UserState:  1,
		MaxMembers: 4,
		NameBytes:  []byte("party"),
		Members: []alignedcmd.PartyMemberState{
			{UserID: 1001, UserState: 1, HPPercent: 100, MPPercent: 100},
			{UserID: 1002, UserState: 1, HPPercent: 100, MPPercent: 100},
		},
	}
}

func newTownCoPresenceTestService(t *testing.T) (dnfrepo.Group, *Service, *gameSession, *gameSession, *bufferConn, *bufferConn) {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	for _, account := range []dnfrepo.AccountRecord{
		{AccountID: "dnf:1", State: "active"},
		{AccountID: "dnf:2", State: "active"},
	} {
		if err := repositories.Account.Save(ctx, account); err != nil {
			t.Fatalf("save account: %v", err)
		}
	}
	for _, character := range []dnfrepo.CharacterRecord{
		{CharacterID: "1001", AccountID: "dnf:1", Name: "leader", Job: "0", Level: 90},
		{CharacterID: "1002", AccountID: "dnf:2", Name: "member", Job: "1", Level: 90},
	} {
		if err := repositories.Character.Save(ctx, character); err != nil {
			t.Fatalf("save character: %v", err)
		}
	}
	service := &Service{
		options:             options{gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		onlinePlayers:       newOnlinePlayerManager(),
		adventureGroupTable: loadAdventureGroupTestTables(t),
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	channel := channelcatalog.Channel{ID: 42, Type: 1, Name: "ch.42", Port: 10042}
	sourceConn, targetConn := &bufferConn{}, &bufferConn{}
	source := &gameSession{
		conn: sourceConn, channel: channel, accountID: "dnf:1", selectedCharacterID: 1001,
		townActorOwnerChannel: 42, connectionTownActorOwnerChannel: 42,
	}
	target := &gameSession{
		conn: targetConn, channel: channel, accountID: "dnf:2", selectedCharacterID: 1002,
		townActorOwnerChannel: 42, connectionTownActorOwnerChannel: 42,
	}
	service.bindGameSessionCharacter(source, 1001)
	service.bindGameSessionCharacter(target, 1002)
	for _, session := range []*gameSession{source, target} {
		session.townMu.Lock()
		session.townSceneReadyCharacterID = session.selectedCharacterID
		session.townPositionSnapshot = currentTownPositionSnapshot{
			CharacterID:   session.selectedCharacterID,
			PositionValid: true,
			PositionX:     450,
			PositionY:     234,
		}
		session.townMu.Unlock()
	}
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 1001, AccountID: "dnf:1", Name: "leader", TownID: 38, AreaID: 2,
		PositionX: 450, PositionY: 234, Direction: 5, AreaState: 3, Session: source,
	})
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 1002, AccountID: "dnf:2", Name: "member", TownID: 38, AreaID: 2,
		PositionX: 452, PositionY: 236, Direction: 5, AreaState: 3, Session: target,
	})
	return repositories, service, source, target, sourceConn, targetConn
}
