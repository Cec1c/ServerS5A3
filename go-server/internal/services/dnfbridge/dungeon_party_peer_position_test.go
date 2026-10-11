package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/party"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// peerPositionFixture 造两个**同场副本**里的队伍成员（1001 队长 / 1002 队员），
// 各自带一份副本运行时与已上报坐标，供 op22 队友位移转发的回归使用。
func peerPositionFixture(t *testing.T, mode partyDungeonPeerPositionMode) (*Service, *gameSession, *gameSession) {
	t.Helper()
	service := &Service{options: options{
		gameUpperBodyCodec:       gameUpperBodyCodecPlain,
		partyDungeonPeerPosition: mode,
	}}
	channel := channelcatalog.Channel{ID: 10, Type: 11, Name: "ch", Port: 10000}
	leader := &gameSession{conn: &bufferConn{}, channel: channel, selectedCharacterID: 1001, townActorOwnerChannel: currentSceneObjectContext}
	member := &gameSession{conn: &bufferConn{}, channel: channel, selectedCharacterID: 1002, townActorOwnerChannel: currentSceneObjectContext}
	service.bindGameSessionCharacter(leader, 1001)
	service.bindGameSessionCharacter(member, 1002)
	manager := service.runtimePartyManagerForService()
	created := manager.Create(party.RuntimePartyMember{
		UserID:            1001,
		SessionGeneration: service.characterGenerationForTest(leader),
		State:             alignedcmd.PartyMemberState{UserID: 1001, UserState: 1, HPPercent: 100, MPPercent: 100},
	}, alignedcmd.PartyState{NameBytes: []byte("party"), MaxMembers: 4, UserState: 1})
	joined := manager.Join(created.Party.ID, party.RuntimePartyMember{
		UserID:            1002,
		SessionGeneration: service.characterGenerationForTest(member),
		State:             alignedcmd.PartyMemberState{UserID: 1002, UserState: 1, HPPercent: 100, MPPercent: 100},
	})
	if !created.OK || !joined.OK {
		t.Fatalf("runtime party create=%+v join=%+v", created, joined)
	}
	service.publishRuntimePartySnapshot(joined.Party)
	for _, session := range []*gameSession{leader, member} {
		runtime := &runtimeDungeonState{
			Dungeon:   worldmap.Dungeon{ID: 3},
			MazeIndex: 0,
			Seed:      1234,
		}
		session.dungeon.mu.Lock()
		session.dungeon.runtime = runtime
		session.dungeon.mu.Unlock()
	}
	// 运行键必须相等（同一场），自己手写两份键，避免依赖世界地图数据。
	runKey := dungeonPartyRunKey{PartyID: uint16(joined.Party.ID), DungeonID: 3, MazeIndex: 0, RoomX: 0, RoomY: 1, MapID: 58548, Seed: 1234}
	for _, session := range []*gameSession{leader, member} {
		session.dungeonPartyRunMu.Lock()
		session.dungeonPartyRun = runKey
		session.dungeonPartyRunValid = true
		session.dungeonPartyRunMu.Unlock()
	}
	leader.dungeon.runtime.actorPositionX = 1085
	leader.dungeon.runtime.actorPositionY = 249
	leader.dungeon.runtime.actorDirection = 5
	leader.dungeon.runtime.actorPositionValid = true
	member.dungeon.runtime.actorPositionX = 409
	member.dungeon.runtime.actorPositionY = 260
	member.dungeon.runtime.actorDirection = 3
	member.dungeon.runtime.actorPositionValid = true
	return service, leader, member
}

func (s *Service) characterGenerationForTest(session *gameSession) uint64 {
	identity, _ := s.boundGameSessionCharacterSnapshot(session)
	return identity.generation
}

// 默认 off：副本内一条队友位移都不发（与现行为逐字节相同）。
func TestDungeonPartyPeerPositionDisabledByDefaultEmitsNothing(t *testing.T) {
	service, leader, member := peerPositionFixture(t, partyDungeonPeerPositionDisabled)
	if sent := service.broadcastDungeonPartyPeerPosition(leader, "test_disabled"); sent != 0 {
		t.Fatalf("disabled mode sent %d peer positions", sent)
	}
	if leader.conn.(*bufferConn).write.Len() != 0 || member.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("disabled mode must not write any bytes")
	}
}

// 开启后：队友的位移转发给**其他**队员，形状与城镇 town_copresence_move_0x0016 一致
// （u16 actor_key, u16 x, u16 y, u8 direction, u16 movement_rate），owner = 副本上下文 0，
// 且**绝不回发给上报者本人**。
func TestDungeonPartyPeerPositionForwardsMoverToTeammatesOnly(t *testing.T) {
	service, leader, member := peerPositionFixture(t, partyDungeonPeerPositionPeer)
	senderBody := buildCurrentTownUserPositionNotificationBody(
		currentSceneActorObjectKey(1002), 409, 260, 3,
	)
	if sent := service.broadcastDungeonPartyPeerPosition(member, "test_peer_move"); sent != 1 {
		t.Fatalf("peer mode sent %d frames, want exactly 1", sent)
	}
	if got := member.conn.(*bufferConn).write.Len(); got != 0 {
		t.Fatalf("mover must never receive its own position back: %d bytes", got)
	}
	frames := writeFrames(t, leader)
	if len(frames) != 1 {
		t.Fatalf("leader frames=%d want 1", len(frames))
	}
	if frames[0].msgID != currentTownUserPositionNotificationMsgID {
		t.Fatalf("frame msg_id=%d want %d", frames[0].msgID, currentTownUserPositionNotificationMsgID)
	}
	if len(frames[0].body) != len(senderBody)+3 || !bytes.Equal(frames[0].body[3:], senderBody) {
		t.Fatalf("peer position body=% X want fixed16 prefix/marker + % X", frames[0].body, senderBody)
	}
	if !bytes.Equal(frames[0].body[:3], []byte{0, 0, 0}) {
		t.Fatalf("peer position fixed16 prefix=% X want 00 00 00 (same shape as town town_copresence_move_0x0016)", frames[0].body[:3])
	}
}

// 不在同一场副本（运行键不同）时不发。
func TestDungeonPartyPeerPositionRequiresSameRunKey(t *testing.T) {
	service, leader, member := peerPositionFixture(t, partyDungeonPeerPositionPeer)
	member.dungeonPartyRunMu.Lock()
	member.dungeonPartyRun.Seed = 999
	member.dungeonPartyRunMu.Unlock()
	if sent := service.broadcastDungeonPartyPeerPosition(member, "test_other_run"); sent != 0 {
		t.Fatalf("cross-run broadcast sent %d frames, want 0", sent)
	}
	if leader.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("cross-run broadcast must not write to the other session")
	}
}

// 副本内 op36（SET_USER_AREA）分支：body[2:6] 的 u16 坐标逐字节读成队友位置，
// 写进运行时与共享实例，并用 noti0x16 转发给**其他**队员、绝不回发上报者本人。
func TestDungeonActiveSetUserAreaRecordsAndForwardsReportedPosition(t *testing.T) {
	service, leader, member := peerPositionFixture(t, partyDungeonPeerPositionPeer)
	// 真实时序：角色进副本前城镇场景已经 finalize（handleTownSetUserArea 第 3 步就是它的门）。
	member.townMu.Lock()
	member.townSceneReadyCharacterID = member.selectedCharacterID
	member.townMu.Unlock()
	// 上报者在**副本内**上报 body[2:4]=0x04cf(1231) body[4:6]=0x00dd(221) body[6]=5。
	body := make([]byte, currentTownSetUserAreaBodySize)
	body[0] = 3
	body[1] = 7
	binary.LittleEndian.PutUint16(body[2:4], 1231)
	binary.LittleEndian.PutUint16(body[4:6], 221)
	body[6] = 5
	if err := service.handleTownSetUserArea(member, body); err != nil {
		t.Fatalf("op36 dungeon branch returned %v", err)
	}
	member.dungeon.mu.Lock()
	runtime := member.dungeon.runtime
	gotX, gotY, gotDir, valid := runtime.actorPositionX, runtime.actorPositionY, runtime.actorDirection, runtime.actorPositionValid
	member.dungeon.mu.Unlock()
	if !valid || gotX != 1231 || gotY != 221 || gotDir != 5 {
		t.Fatalf("recorded dungeon position valid=%t x=%d y=%d dir=%d, want 1231/221/5", valid, gotX, gotY, gotDir)
	}
	// 副本里那份运行时仍然是"在副本中"：城镇位置快照/城镇过图没有被写。
	member.townMu.Lock()
	snapshot := member.townPositionSnapshot
	member.townMu.Unlock()
	if snapshot.PositionValid || snapshot.CharacterID != 0 {
		t.Fatalf("dungeon op36 must not write a town position snapshot, got %+v", snapshot)
	}
	// 上报者自己零字节。
	if got := member.conn.(*bufferConn).write.Len(); got != 0 {
		t.Fatalf("mover must never receive its own position back: %d bytes", got)
	}
	frames := writeFrames(t, leader)
	if len(frames) != 1 {
		t.Fatalf("leader frames=%d want 1", len(frames))
	}
	if frames[0].msgID != currentTownUserPositionNotificationMsgID {
		t.Fatalf("frame msg_id=%d want %d", frames[0].msgID, currentTownUserPositionNotificationMsgID)
	}
	want := buildCurrentTownUserPositionNotificationBody(
		currentSceneActorObjectKey(1002), 1231, 221, 5,
	)
	if len(frames[0].body) != len(want)+3 || !bytes.Equal(frames[0].body[3:], want) {
		t.Fatalf("forwarded body=% X want fixed16 prefix + % X", frames[0].body, want)
	}
	if !bytes.Equal(frames[0].body[:3], []byte{0, 0, 0}) {
		t.Fatalf("fixed16 prefix=% X want 00 00 00", frames[0].body[:3])
	}
}

// 坐标为 (0,0) 的副本内 op36 不携带任何位置信息：不覆盖已有坐标、不转发。
func TestDungeonActiveSetUserAreaZeroPositionDoesNotRecord(t *testing.T) {
	service, leader, member := peerPositionFixture(t, partyDungeonPeerPositionPeer)
	member.townMu.Lock()
	member.townSceneReadyCharacterID = member.selectedCharacterID
	member.townMu.Unlock()
	body := make([]byte, currentTownSetUserAreaBodySize)
	if err := service.handleTownSetUserArea(member, body); err != nil {
		t.Fatalf("op36 dungeon branch returned %v", err)
	}
	member.dungeon.mu.Lock()
	gotX, gotY := member.dungeon.runtime.actorPositionX, member.dungeon.runtime.actorPositionY
	member.dungeon.mu.Unlock()
	if gotX != 409 || gotY != 260 {
		t.Fatalf("zero report overwrote the recorded position: x=%d y=%d", gotX, gotY)
	}
	if leader.conn.(*bufferConn).write.Len() != 0 || member.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("zero-coordinate report must not forward or echo any frame")
	}
}

// 开关 off 时副本内 op36 分支仍然记录坐标（判据不变），但一个字节都不发。
func TestDungeonActiveSetUserAreaDisabledModeEmitsNothing(t *testing.T) {
	service, leader, member := peerPositionFixture(t, partyDungeonPeerPositionDisabled)
	member.townMu.Lock()
	member.townSceneReadyCharacterID = member.selectedCharacterID
	member.townMu.Unlock()
	body := make([]byte, currentTownSetUserAreaBodySize)
	binary.LittleEndian.PutUint16(body[2:4], 700)
	binary.LittleEndian.PutUint16(body[4:6], 260)
	body[6] = 2
	if err := service.handleTownSetUserArea(member, body); err != nil {
		t.Fatalf("op36 dungeon branch returned %v", err)
	}
	member.dungeon.mu.Lock()
	gotX, gotY, valid := member.dungeon.runtime.actorPositionX, member.dungeon.runtime.actorPositionY, member.dungeon.runtime.actorPositionValid
	member.dungeon.mu.Unlock()
	if !valid || gotX != 700 || gotY != 260 {
		t.Fatalf("position record valid=%t x=%d y=%d, want 700/260", valid, gotX, gotY)
	}
	if leader.conn.(*bufferConn).write.Len() != 0 || member.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("disabled mode must not write any bytes")
	}
}
