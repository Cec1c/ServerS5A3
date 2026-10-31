package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 城镇（含**副本门口**）同屏 actor 的补发：两个玩家几乎同时跨进同一城镇区域时，
// 互相投影都落在"收包方场景还没就绪"的窗口里 ⇒ 两人互相看不见，要靠重新进出该区域
// 才恢复。修法与副本那条完全一致：在收包方自己的 finish-loading 之后补发一次。
// 现场与逐行证据见 town_copresence_actor_resend.go 的文件头。
//
// 本文件钉：① 补发确实把**对端** actor 重新发出来（op2 mode0/mode1 + op9 + op23）；
// ② 绝不重发本地自身；③ 同一 (角色,城镇,区域,对端集合) 幂等；④ 副本场景交给副本那条；
// ⑤ 回退开关 `DNFBRIDGE_TOWN_COPRESENCE_ACTOR_RESEND=off` 一字节都不发。

type townResendFixture struct {
	service  *Service
	local    *gameSession
	peer     *gameSession
	frames   func() []upperFrame
	localBuf *bufferConn
	peerBuf  *bufferConn
}

func newTownResendFixture(t *testing.T) townResendFixture {
	t.Helper()
	service, local, repositories := newTownMoveTest(t)
	service.onlinePlayers = newOnlinePlayerManager()
	channel := local.channel
	peer := &gameSession{
		conn:                            &bufferConn{},
		connID:                          "town-resend-peer",
		channel:                         channel,
		residentChannel:                 channel,
		selectedCharacterID:             17,
		townSceneReadyCharacterID:       17,
		connectionTownActorOwnerChannel: byte(channel.ID),
		townActorOwnerChannel:           byte(channel.ID),
	}
	service.bindGameSessionCharacter(peer, 17)
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "17",
		AccountID:   "dnf:2",
		Name:        "peer",
		Job:         "1",
		Level:       2,
		Stats: map[string]int64{
			"town_id": 38, "area_id": 2, "pos_x": 400, "pos_y": 200,
			"direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	// 两人同频道、同城同区（38/2 = 副本门口），正是现场的窗口。
	for _, info := range []*onlinePlayerInfo{
		{CharacterID: 29, AccountID: "dnf:1", Name: "local", TownID: 38, AreaID: 2,
			PositionX: 1085, PositionY: 249, Direction: 5, AreaState: 3, Session: local},
		{CharacterID: 17, AccountID: "dnf:2", Name: "peer", TownID: 38, AreaID: 2,
			PositionX: 400, PositionY: 200, Direction: 5, AreaState: 3, Session: peer},
	} {
		service.onlinePlayers.EnterArea(info)
	}
	localBuf := local.conn.(*bufferConn)
	peerBuf := peer.conn.(*bufferConn)
	localBuf.write.Reset()
	peerBuf.write.Reset()
	return townResendFixture{
		service: service, local: local, peer: peer,
		frames: func() []upperFrame {
			return writeFrames(t, local)
		},
		localBuf: localBuf,
		peerBuf:  peerBuf,
	}
}

func TestTownCopresenceActorResendReinstallsPeerActorAfterFinishLoading(t *testing.T) {
	fixture := newTownResendFixture(t)
	fixture.service.resendTownCopresencePeerActorsAfterFinishLoading(fixture.local, "test_resend")

	frames := fixture.frames()
	mode0OrMode1 := uint16(dnfenum.CmdPacketSetUDPIPPort)
	if len(frames) != 3 || frames[0].msgID != mode0OrMode1 || frames[1].msgID != mode0OrMode1 ||
		frames[2].msgID != currentTownUserAreaNotificationMsgID {
		t.Fatalf("resend frames=%v want [op2 mode0, op2 mode1, op23]", frameIDs(frames))
	}
	// op23 携带的必须是**对端**的真实区域与坐标（重进区域后 actor 落在正确位置）。
	wantArea := buildCurrentTownUserAreaNotificationBody(
		currentSceneActorObjectKey(17), 38, 2, 400, 200, 5, 3)
	if len(frames[2].body) != len(wantArea)+3 || !bytes.Equal(frames[2].body[3:], wantArea) {
		t.Fatalf("resend op23 body=% X want fixed16 prefix + peer area % X", frames[2].body, wantArea)
	}
	// 与既有同屏投影逐字节一致（同一 builder、同一 owner 口径）——这条同时钉住
	// "补发的是对端 actor，不是本地自身"。
	peerInfo, ok := fixture.service.onlinePlayers.PlayerForCharacter(17)
	if !ok {
		t.Fatal("peer presence missing from the area bucket")
	}
	if err := fixture.service.sendTownRemoteActorState(fixture.local, peerInfo, "reference"); err != nil {
		t.Fatalf("reference copresence projection: %v", err)
	}
	reference := fixture.frames()
	if len(reference) < 2 || !bytes.Equal(reference[0].body, frames[0].body) ||
		!bytes.Equal(reference[1].body, frames[1].body) {
		t.Fatalf("resend mode0/mode1 differ from the normal copresence projection: %v", frameIDs(reference))
	}
	// 只动触发方自己，对端一个字节都不写。
	if got := fixture.peerBuf.write.Len(); got != 0 {
		t.Fatalf("resend wrote %d bytes to the peer session, want 0", got)
	}

	// 幂等：同一 (角色,城镇,区域,对端集合) 第二次不再发。
	fixture.localBuf.write.Reset()
	fixture.service.resendTownCopresencePeerActorsAfterFinishLoading(fixture.local, "test_resend_repeat")
	if got := fixture.localBuf.write.Len(); got != 0 {
		t.Fatalf("repeated resend in the same area wrote %d bytes, want 0 (idempotent)", got)
	}

	// 区域里只剩自己 ⇒ 一个字节都不发（绝不重发本地自身）。
	fixture.service.onlinePlayers.LeaveArea(17)
	fixture.localBuf.write.Reset()
	fixture.service.resendTownCopresencePeerActorsAfterFinishLoading(fixture.local, "test_resend_alone")
	if got := fixture.localBuf.write.Len(); got != 0 {
		t.Fatalf("resend with no peers wrote %d bytes, want 0 (never re-project the local actor)", got)
	}
}

func TestTownCopresenceActorResendSkipsDungeonAndDisabledSwitch(t *testing.T) {
	// 副本场景：交给 dungeon_party_peer_actor_resend.go，这里必须一个字节都不发。
	fixture := newTownResendFixture(t)
	fixture.local.dungeon.mu.Lock()
	fixture.local.dungeon.runtime = &runtimeDungeonState{Session: &worldmap.DungeonSession{}}
	fixture.local.dungeon.mu.Unlock()
	fixture.service.resendTownCopresencePeerActorsAfterFinishLoading(fixture.local, "test_resend_dungeon")
	if got := fixture.localBuf.write.Len(); got != 0 {
		t.Fatalf("town resend must not run for an active dungeon runtime, wrote %d bytes", got)
	}

	// 回退开关：off ⇒ 城镇里也不补发（旧行为逐字节不变）。
	t.Setenv(currentTownCopresenceResendEnv, "off")
	townFixture := newTownResendFixture(t)
	townFixture.service.resendTownCopresencePeerActorsAfterFinishLoading(townFixture.local, "test_resend_disabled")
	if got := townFixture.localBuf.write.Len(); got != 0 {
		t.Fatalf("disabled town resend wrote %d bytes, want 0", got)
	}
}

// 从**私有**赛丽亚房间回答 op1425 之后，存在感必须按落点重新发布到共享桶：
// 否则服务端一直以为玩家还在私有房间里，别人看不到他、他也拿不到同屏投影，
// 只有"重新进出该区域"才会被重新登记（厂商参考实现同一条口径：
// game-town-presence-suppressed → game-town-presence-published）。
func TestPrevVillageReturnFromSeriaRoomPublishesTownPresence(t *testing.T) {
	service, session, repositories := newTownMoveTest(t)
	service.onlinePlayers = newOnlinePlayerManager()
	character, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Stats["town_id"] = newCharacterInitialTownID
	character.Stats["area_id"] = newCharacterInitialAreaID
	character.Stats["pos_x"] = newCharacterInitialPosX
	character.Stats["pos_y"] = newCharacterInitialPosY
	character.Stats["direction"] = newCharacterInitialDirection
	character.Stats["area_state"] = 3
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	// 本地会话此刻登记在**私有**赛丽亚房间桶里（Instance = 角色 ID），
	// 对端（17）在共享的街道桶里 —— 两人本来不该互相可见。
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 29, TownID: 38, AreaID: 1, Instance: 29,
		PositionX: newCharacterInitialPosX, PositionY: newCharacterInitialPosY,
		Direction: 5, AreaState: 3, Session: session,
	})
	peer := &gameSession{
		conn: &bufferConn{}, connID: "presence-peer", channel: session.channel,
		residentChannel: session.channel, selectedCharacterID: 17, townSceneReadyCharacterID: 17,
		townActorOwnerChannel: byte(session.channel.ID),
	}
	service.bindGameSessionCharacter(peer, 17)
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "17", AccountID: "dnf:2", Name: "peer", Job: "1", Level: 2,
		Stats: map[string]int64{
			"town_id": 38, "area_id": 0, "pos_x": 622, "pos_y": 196,
			"direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	peerInfo := onlinePlayerInfo{
		CharacterID: 17, TownID: 38, AreaID: 0, PositionX: 622, PositionY: 196,
		Direction: 5, AreaState: 3, Session: peer,
	}
	startTownPeerTest(t, service, peer, &peerInfo)
	if service.onlinePlayers.PeerInSameArea(29, 17) {
		t.Fatal("fixture must start with the local player inside the private Seria-room instance")
	}
	peerFrameBuffer := peer.conn.(*bufferConn)
	peerFrameBuffer.write.Reset()
	// 房间的外门口（PVF 派生）——op1425 的落点就是它，存在感也要发布到这里。
	service.seriaRoomDoorMapReader = fakeDoorMapReader(t, map[string][][6]int64{
		"new_Elvengard.map": {{622, 196, 0, 0, 38, 1}},
	})

	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketPrevVillage),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	drainTownPeerTest(t, service, peer)
	published, ok := service.onlinePlayers.PlayerForCharacter(29)
	if !ok || published.TownID != 38 || published.AreaID != 0 || published.Instance != 0 {
		t.Fatalf("presence after Seria-room return=%+v ok=%t want the shared town area 38/0 instance 0",
			published, ok)
	}
	if !service.onlinePlayers.PeerInSameArea(29, 17) {
		t.Fatal("after the return the two players must share one town area bucket (they can see each other)")
	}
	if peerFrameBuffer.write.Len() == 0 {
		t.Fatal("the peer already in the street must receive the returning player's actor")
	}

	// 回退开关：off ⇒ 存在感照旧留在私有房间桶（旧行为）。
	t.Setenv(currentTownSeriaExitPresenceEnv, "off")
	legacyService, legacySession, legacyRepositories := newTownMoveTest(t)
	legacyService.onlinePlayers = newOnlinePlayerManager()
	legacyCharacter, _, _ := legacyRepositories.Character.Load(context.Background(), "29")
	legacyCharacter.Stats["area_state"] = 3
	if err := legacyRepositories.Character.Save(context.Background(), legacyCharacter); err != nil {
		t.Fatal(err)
	}
	legacyService.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 29, TownID: 38, AreaID: 1, Instance: 29,
		PositionX: newCharacterInitialPosX, PositionY: newCharacterInitialPosY,
		Direction: 5, AreaState: 3, Session: legacySession,
	})
	legacyService.seriaRoomDoorMapReader = fakeDoorMapReader(t, map[string][][6]int64{
		"new_Elvengard.map": {{622, 196, 0, 0, 38, 1}},
	})
	if err := legacyService.handleGameCommand(
		legacySession,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketPrevVillage),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	kept, ok := legacyService.onlinePlayers.PlayerForCharacter(29)
	if !ok || kept.Instance != 29 || kept.AreaID != 1 {
		t.Fatalf("disabled presence publish changed the bucket: %+v ok=%t", kept, ok)
	}
}
