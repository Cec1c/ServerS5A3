package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/party"
)

// 进副本离开城镇区域时，**仍在同一队伍**的队友必须收到 op23（原生 area 通知，客户端只移动
// actor、保留队伍名册），而不是 op6（客户端把它当 town-actor destroy + **party-member removal**）。
// 现场证据：packet_log.txt 3246/3509/3886/4687 把 op6 发给了仍在队伍里的队友。
func TestDungeonEntryLeaveSendsAreaNoticeToPartyTeammateNotOp6(t *testing.T) {
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	service.onlinePlayers = newOnlinePlayerManager()
	channel := channelcatalog.Channel{ID: 10, Type: 11, Name: "ch", Port: 10000}
	mover := &gameSession{conn: &bufferConn{}, channel: channel, selectedCharacterID: 1001}
	teammate := &gameSession{conn: &bufferConn{}, channel: channel, selectedCharacterID: 1002}
	stranger := &gameSession{conn: &bufferConn{}, channel: channel, selectedCharacterID: 1009}
	for id, session := range map[uint16]*gameSession{1001: mover, 1002: teammate, 1009: stranger} {
		service.bindGameSessionCharacter(session, id)
	}
	// 队伍：1001 + 1002（1009 是路人）。
	manager := service.runtimePartyManagerForService()
	created := manager.Create(party.RuntimePartyMember{
		UserID:            1001,
		SessionGeneration: mustGeneration(t, service, mover),
		State:             alignedcmd.PartyMemberState{UserID: 1001, UserState: 1},
	}, alignedcmd.PartyState{NameBytes: []byte("party"), MaxMembers: 4, UserState: 1})
	joined := manager.Join(created.Party.ID, party.RuntimePartyMember{
		UserID:            1002,
		SessionGeneration: mustGeneration(t, service, teammate),
		State:             alignedcmd.PartyMemberState{UserID: 1002, UserState: 1},
	})
	if !created.OK || !joined.OK {
		t.Fatalf("runtime party create=%+v join=%+v", created, joined)
	}
	service.publishRuntimePartySnapshot(joined.Party)
	// 三人都登记在同一个城镇区域。
	for _, info := range []*onlinePlayerInfo{
		{CharacterID: 1001, Name: "mover", TownID: 38, AreaID: 2, ChannelID: 10, PositionX: 1085, PositionY: 249, Direction: 5, AreaState: 3, Session: mover},
		{CharacterID: 1002, Name: "teammate", TownID: 38, AreaID: 2, ChannelID: 10, PositionX: 400, PositionY: 200, Direction: 5, AreaState: 3, Session: teammate},
		{CharacterID: 1009, Name: "stranger", TownID: 38, AreaID: 2, ChannelID: 10, PositionX: 500, PositionY: 300, Direction: 5, AreaState: 3, Session: stranger},
	} {
		service.onlinePlayers.EnterArea(info)
	}
	writeFrames(t, teammate)
	writeFrames(t, stranger)

	service.leaveTownPresenceForDungeon(mover)

	teammateFrames := writeFrames(t, teammate)
	if len(teammateFrames) != 1 {
		t.Fatalf("teammate frames=%d want 1 (op23 area notice)", len(teammateFrames))
	}
	if teammateFrames[0].msgID != currentTownUserAreaNotificationMsgID {
		t.Fatalf("teammate msg_id=%d want op23=%d (op6 is a party-member removal for this client)",
			teammateFrames[0].msgID, currentTownUserAreaNotificationMsgID)
	}
	wantArea := buildCurrentTownUserAreaNotificationBody(
		currentSceneActorObjectKey(1001), 38, 2, 400, 200, 5, 3,
	)
	if len(teammateFrames[0].body) != len(wantArea)+3 || !bytes.Equal(teammateFrames[0].body[3:], wantArea) {
		t.Fatalf("teammate op23 body=% X want fixed16 prefix + % X", teammateFrames[0].body, wantArea)
	}

	strangerFrames := writeFrames(t, stranger)
	if len(strangerFrames) != 1 || strangerFrames[0].msgID != currentTownUserLeaveMsgID {
		t.Fatalf("non-party stranger frames=%v want exactly one op6=%d", frameIDs(strangerFrames), currentTownUserLeaveMsgID)
	}
	if !bytes.Equal(strangerFrames[0].body[3:], buildTownUserLeaveBody(1001)) {
		t.Fatalf("stranger op6 body=% X want uid body=% X", strangerFrames[0].body, buildTownUserLeaveBody(1001))
	}
}

func mustGeneration(t *testing.T, service *Service, session *gameSession) uint64 {
	t.Helper()
	identity, ok := service.boundGameSessionCharacterSnapshot(session)
	if !ok {
		t.Fatalf("session %d is not bound", session.selectedCharacterID)
	}
	return identity.generation
}

func frameIDs(frames []upperFrame) []uint16 {
	ids := make([]uint16, 0, len(frames))
	for _, frame := range frames {
		ids = append(ids, frame.msgID)
	}
	return ids
}

// countFramesWithActorKey 数"某一类 op2/op9 帧里 actor key 等于给定值"的条数。
func countFramesWithActorKey(t *testing.T, frames []upperFrame, msgID uint16, actorKey uint16) int {
	t.Helper()
	count := 0
	for _, frame := range frames {
		if frame.msgID != msgID || len(frame.body) < 6 {
			continue
		}
		if uint16(frame.body[4])|uint16(frame.body[5])<<8 == actorKey {
			count++
		}
	}
	return count
}

// finish-loading 之后必须**再补一次**队友 actor 投影（进图/过图那一次落在收包方场景还没
// 就绪的窗口里 —— 现场：队长侧 actor 建出来 41ms 后就被 context removed）。
// 断言：补发只发队友（本地自身零发包）、同一房间幂等（第二次不再发）。
func TestDungeonPartyPeerActorResendAfterFinishLoadingIsIdempotentPerRoom(t *testing.T) {
	service, leader, member := peerPositionFixture(t, partyDungeonPeerPositionPeer)
	writeFrames(t, leader)
	writeFrames(t, member)

	service.resendDungeonPartyPeerActorsAfterFinishLoading(leader, "test_resend")
	// 说明：本桩没有真实角色仓储/世界地图会话，`sendDungeonPartyPeerActorState` 会在
	// "character record unavailable" 上失败（这是既有的 fail-closed 行为），所以这里
	// **不**断言 op2 真的发出；能断言的是：① 只打扰触发方自己（队友零字节）；② 同一房间幂等。
	// 真正"补发把队友 actor 重新画出来"的验收在实机 trace（`local-actor-create` 不再被
	// `actor context removed` 追尾）。
	selfFrames := countFramesWithActorKey(t, writeFrames(t, leader), 0x0002, 1001)
	if selfFrames != 0 {
		t.Fatalf("resend must never re-project the local actor itself: %d self frames", selfFrames)
	}
	// 队友那一侧不受影响。
	if got := member.conn.(*bufferConn).write.Len(); got != 0 {
		t.Fatalf("resend must only touch the triggering session, wrote %d bytes to the peer", got)
	}

	// 幂等：同一房间第二次调用不再发任何东西。
	service.resendDungeonPartyPeerActorsAfterFinishLoading(leader, "test_resend_repeat")
	if got := leader.conn.(*bufferConn).write.Len(); got != 0 {
		t.Fatalf("repeated resend in the same room wrote %d bytes, want 0 (idempotent per room)", got)
	}
}
