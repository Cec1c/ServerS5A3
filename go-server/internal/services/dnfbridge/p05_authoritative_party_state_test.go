package dnfbridge

import (
	"bytes"
	"context"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func newP05ExtraTownSession(
	t *testing.T,
	service *Service,
	repositories dnfrepo.Group,
	source *gameSession,
	characterID uint16,
) (*gameSession, onlinePlayerInfo) {
	t.Helper()
	characterKey := strconv.Itoa(int(characterID))
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: characterKey,
		AccountID:   "dnf:3",
		Name:        "stranger",
		Job:         "1",
		Level:       50,
		Stats: map[string]int64{
			"town_id": 38, "area_id": 1, "pos_x": 300, "pos_y": 180,
			"direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatalf("save stranger character: %v", err)
	}
	peer := &gameSession{
		conn:                            &bufferConn{},
		connID:                          "p05-stranger",
		channel:                         source.channel,
		residentChannel:                 source.residentChannel,
		accountID:                       "dnf:3",
		selectedCharacterID:             characterID,
		townSceneReadyCharacterID:       characterID,
		townActorOwnerChannel:           byte(source.channel.ID),
		connectionTownActorOwnerChannel: byte(source.channel.ID),
	}
	service.bindGameSessionCharacter(peer, characterID)
	info := onlinePlayerInfo{
		CharacterID: characterID, AccountID: "dnf:3", Name: "stranger",
		Job: 1, Level: 50, TownID: 38, AreaID: 1,
		PositionX: 300, PositionY: 180, Direction: 5, AreaState: 3, Session: peer,
	}
	service.onlinePlayers.EnterArea(&info)
	return peer, info
}

func p05MoverInfo(session *gameSession, townID, areaID byte) onlinePlayerInfo {
	return onlinePlayerInfo{
		CharacterID: session.selectedCharacterID,
		Name:        "mover",
		TownID:      townID,
		AreaID:      areaID,
		PositionX:   600,
		PositionY:   300,
		Direction:   5,
		AreaState:   3,
		Session:     session,
	}
}

func p05Frames(t *testing.T, service *Service, session *gameSession) []upperFrame {
	t.Helper()
	connection := session.conn.(*bufferConn)
	raw := append([]byte(nil), connection.write.Bytes()...)
	connection.write.Reset()
	frames := make([]upperFrame, 0, 8)
	for len(raw) > 0 {
		packet, rest := splitGameServerUpperPacketWithHeader(t, raw, service.gameUpperHeaderSize())
		frames = append(frames, upperFrame{msgID: packet.Header.MsgID, body: packet.Body})
		raw = rest
	}
	return frames
}

func TestP05TownAreaChangeUsesAuthoritativePartyForEmptySessionCache(t *testing.T) {
	t.Setenv(townCopresencePartyAuthoritativeEnv, "on")
	fixture := newTownPartyActorHPFixture(t, true)
	storeRuntimePartyState(fixture.target, alignedcmd.PartyState{})
	storeRuntimePartyState(fixture.actor, alignedcmd.PartyState{})
	mover := p05MoverInfo(fixture.target, 38, 0)

	fixture.service.broadcastTownPlayerAreaChange(&mover, []onlinePlayerInfo{fixture.actorInfo}, alignedcmd.PartyState{})

	frames := p05Frames(t, fixture.service, fixture.actor)
	if len(frames) != 1 || frames[0].msgID != currentTownUserAreaNotificationMsgID {
		t.Fatalf("empty-cache same-party area change frames=%v, want one op23", frameIDs(frames))
	}
}

func TestP05TownAreaChangeUsesAuthoritativeNewPartyOverOldSessionCache(t *testing.T) {
	t.Setenv(townCopresencePartyAuthoritativeEnv, "on")
	fixture := newTownPartyActorHPFixture(t, true)
	repositories, ok := fixture.service.repositoryGroup()
	if !ok {
		t.Fatal("test repository provider unavailable")
	}
	stranger, strangerInfo := newP05ExtraTownSession(t, fixture.service, repositories, fixture.target, 18)
	oldState := alignedcmd.PartyState{
		PartyID: 901, UserID: fixture.target.selectedCharacterID, UserState: 1, IsLeader: true, MaxMembers: 4,
		Members: []alignedcmd.PartyMemberState{
			{UserID: fixture.target.selectedCharacterID, UserState: 1},
			{UserID: stranger.selectedCharacterID, UserState: 1},
		},
	}
	storeRuntimePartyState(fixture.target, oldState)
	storeRuntimePartyState(stranger, oldState)
	strangerInfo.Session = stranger
	mover := p05MoverInfo(fixture.target, 38, 0)

	fixture.service.broadcastTownPlayerAreaChange(
		&mover,
		[]onlinePlayerInfo{strangerInfo, fixture.actorInfo},
		oldState,
	)

	strangerFrames := p05Frames(t, fixture.service, stranger)
	if len(strangerFrames) != 1 || strangerFrames[0].msgID != currentTownUserLeaveMsgID {
		t.Fatalf("old-session peer/stranger frames=%v, want one op6", frameIDs(strangerFrames))
	}
	currentFrames := p05Frames(t, fixture.service, fixture.actor)
	if len(currentFrames) != 1 || currentFrames[0].msgID != currentTownUserAreaNotificationMsgID {
		t.Fatalf("current-party peer frames=%v, want one op23", frameIDs(currentFrames))
	}
}

func TestP05TownMovePublishesAuthoritativeFinalHPAndRosterWithEmptyCache(t *testing.T) {
	t.Setenv(townCopresencePartyAuthoritativeEnv, "on")
	fixture := newTownPartyActorHPFixture(t, true)
	fixture.actorInfo.AreaID = 2
	fixture.service.onlinePlayers.EnterArea(&fixture.actorInfo)
	storeRuntimePartyState(fixture.target, alignedcmd.PartyState{})
	storeRuntimePartyState(fixture.actor, alignedcmd.PartyState{})
	fixture.target.townMu.Lock()
	fixture.target.townPositionSnapshot = currentTownPositionSnapshot{
		CharacterID: fixture.target.selectedCharacterID, TownID: 38, AreaID: 1,
		PositionX: 450, PositionY: 234, MovementCode: 5, PositionValid: true,
	}
	fixture.target.townMu.Unlock()

	if err := fixture.service.handleTownSetUserArea(fixture.target, buildTownMoveRequest(38, 0, 900, 250, 5)); err != nil {
		t.Fatalf("town area move: %v", err)
	}
	requireP05FinalHPAndRoster(t, fixture.service, fixture.target)
}

func TestP05ReturnRebindUsesAuthoritativePartyWithEmptySessionCache(t *testing.T) {
	_, service, leader, member, leaderConn, memberConn := newTownCoPresenceTestService(t)
	t.Setenv(townCopresencePartyAuthoritativeEnv, "on")
	service.options.currentClientVersion = "official-24.9"
	ok, _, reason, members := service.forceManagedRuntimeParty(leader.selectedCharacterID, member.selectedCharacterID)
	if !ok || members != 2 {
		t.Fatalf("create authoritative return party: ok=%t reason=%s members=%d", ok, reason, members)
	}
	storeRuntimePartyState(leader, alignedcmd.PartyState{})
	storeRuntimePartyState(member, alignedcmd.PartyState{})
	leaderConn.write.Reset()
	memberConn.write.Reset()

	if ok := service.rebindTownCopresenceAfterDungeonReturn(leader, 38, 0, "test_p05_return_rebind"); !ok {
		t.Fatal("return rebind rejected authoritative party")
	}
	requireP05FinalHPAndRoster(t, service, leader)
	memberFrames := p05Frames(t, service, member)
	if len(memberFrames) != 1 || memberFrames[0].msgID != currentTownUserAreaNotificationMsgID {
		t.Fatalf("return rebind peer frames=%v, want one op23", frameIDs(memberFrames))
	}
}

func TestP05ReturnRebuildUsesAuthoritativePartyWithEmptySessionCache(t *testing.T) {
	_, service, leader, member, leaderConn, memberConn := newTownCoPresenceTestService(t)
	t.Setenv(townCopresencePartyAuthoritativeEnv, "on")
	service.options.currentClientVersion = "official-24.9"
	ok, _, reason, members := service.forceManagedRuntimeParty(leader.selectedCharacterID, member.selectedCharacterID)
	if !ok || members != 2 {
		t.Fatalf("create authoritative rebuild party: ok=%t reason=%s members=%d", ok, reason, members)
	}
	storeRuntimePartyState(leader, alignedcmd.PartyState{})
	storeRuntimePartyState(member, alignedcmd.PartyState{})
	leaderConn.write.Reset()
	memberConn.write.Reset()

	if err := service.rebuildRuntimePartyTownCoPresenceAfterReturn(leader, "test_p05_return_rebuild"); err != nil {
		t.Fatalf("return rebuild: %v", err)
	}
	requireP05FinalHPAndRoster(t, service, leader)
	requireP05FinalHPAndRoster(t, service, member)
}

func requireP05FinalHPAndRoster(t *testing.T, service *Service, session *gameSession) {
	t.Helper()
	frames := p05Frames(t, service, session)
	if len(frames) < 2 || frames[len(frames)-2].msgID != 0x0099 || frames[len(frames)-1].msgID != 9 {
		t.Fatalf("session %d final frames=%v, want op153 then op9", session.selectedCharacterID, frameIDs(frames))
	}
	want := runtimePartyRealtimeInfoBody(service.townCopresenceActorPartyState(session))
	if !bytes.Equal(frames[len(frames)-2].body, want) {
		t.Fatalf("session %d final op153 body differs from authoritative roster\n got: % X\nwant: % X",
			session.selectedCharacterID, frames[len(frames)-2].body, want)
	}
}
