package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

func partyMovePreflightFixture(t *testing.T) (*Service, *gameSession, *gameSession) {
	t.Helper()
	service, leaderRuntime := prepareSyntheticMoveRuntime(t, false)
	_, followerRuntime := prepareSyntheticMoveRuntime(t, false)
	service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	leader := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: leaderRuntime}}
	follower := &gameSession{conn: &bufferConn{}, selectedCharacterID: 100, dungeon: dungeonSessionState{runtime: followerRuntime}}
	service.bindGameSessionCharacter(leader, 99)
	service.bindGameSessionCharacter(follower, 100)
	state := alignedcmd.PartyState{PartyID: 7, UserID: 99, MaxMembers: 4,
		Members: []alignedcmd.PartyMemberState{{UserID: 99, UserState: 1}, {UserID: 100, UserState: 1}}}
	storeRuntimePartyState(leader, state)
	storeRuntimePartyState(follower, state)
	followerRuntime.Character.CharacterID = "100"
	repositories, _ := service.repositoryProvider()
	if err := repositories.Character.Save(context.Background(), followerRuntime.Character); err != nil {
		t.Fatal(err)
	}
	scene, _ := leaderRuntime.Session.Scene()
	instance := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene)
	if instance == nil || service.adoptPartyDungeonInstance(follower, followerRuntime, scene) != instance {
		t.Fatal("fixture did not establish one shared party run")
	}
	return service, leader, follower
}

func TestPartyMoveOwnerPreflightBlocksLeaderBeforeAnyPacketOrCommit(t *testing.T) {
	cases := []struct {
		name       string
		breakOwner func(*Service, *gameSession)
	}{
		{"missing runtime", func(_ *Service, f *gameSession) { f.dungeon.runtime = nil }},
		{"missing resident", func(s *Service, f *gameSession) { delete(s.gameSessions, f.selectedCharacterID) }},
		{"changed party", func(_ *Service, f *gameSession) {
			st := runtimePartyStateSnapshot(f)
			st.PartyID++
			storeRuntimePartyState(f, st)
		}},
		{"different dungeon", func(_ *Service, f *gameSession) { f.dungeon.runtime.Dungeon.ID++ }},
		{"different maze", func(_ *Service, f *gameSession) { f.dungeon.runtime.MazeIndex++ }},
		{"different seed", func(_ *Service, f *gameSession) { f.dungeon.runtime.Seed++ }},
		{"previous party run", func(_ *Service, f *gameSession) { f.dungeon.runtime.partyInstance = &dungeonPartyInstance{} }},
		{"missing cached room", func(_ *Service, f *gameSession) { f.dungeon.runtime.Rooms = nil }},
		{"different room object", func(_ *Service, f *gameSession) { f.dungeon.runtime.Room = &runtimeDungeonRoom{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, leader, follower := partyMovePreflightFixture(t)
			before := leader.dungeon.runtime.Session.Snapshot()
			beforeRoom, beforeSeed, beforeKey := leader.dungeon.runtime.Room, leader.dungeon.runtime.Seed, leader.dungeon.runtime.NextObjectKey
			tc.breakOwner(service, follower)
			body := make([]byte, dungeoncmd.MoveMapRequestSize)
			body[0] = 1
			if err := service.handleDungeonMoveMap(leader, body); err != nil {
				t.Fatal(err)
			}
			if leader.conn.(*bufferConn).write.Len() != 0 || follower.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("failed peer ownership allowed a packet before party validation")
			}
			after := leader.dungeon.runtime.Session.Snapshot()
			if after.Scene.Coordinate != before.Scene.Coordinate || after.Scene.Map.Map.ID != before.Scene.Map.Map.ID ||
				leader.dungeon.runtime.Room != beforeRoom || leader.dungeon.runtime.Seed != beforeSeed || leader.dungeon.runtime.NextObjectKey != beforeKey {
				t.Fatal("failed peer ownership committed the leader's room")
			}
		})
	}
}

func TestPartyMoveOwnerPreflightPreservesSuccessfulTwoMemberTransition(t *testing.T) {
	service, leader, follower := partyMovePreflightFixture(t)
	body := make([]byte, dungeoncmd.MoveMapRequestSize)
	body[0] = 1
	if err := service.handleDungeonMoveMap(leader, body); err != nil {
		t.Fatal(err)
	}
	for _, session := range []*gameSession{leader, follower} {
		scene, ok := session.dungeon.runtime.Session.Scene()
		if !ok || scene.Coordinate.X != 1 || scene.Map.Map.ID != 101 {
			t.Fatalf("member %d did not commit target room: %+v", session.selectedCharacterID, scene)
		}
		packets := splitAllFinishBridgePackets(t, session.conn.(*bufferConn).write.Bytes())
		if len(packets) == 0 || packets[0].Header.MsgID != currentDungeonStartNotification {
			t.Fatalf("member %d lost leading start-map packet: %+v", session.selectedCharacterID, packets)
		}
	}
	if leader.dungeon.runtime.partyInstance != follower.dungeon.runtime.partyInstance || leader.dungeon.runtime.Room != follower.dungeon.runtime.Room {
		t.Fatal("successful transition split shared party room ownership")
	}
}

func TestPartyMoveOwnerPreflightPreservesSingleAndSameCoordinatePaths(t *testing.T) {
	service, leader, follower := partyMovePreflightFixture(t)
	follower.dungeon.runtime = nil
	if err := service.preflightRuntimePartyRoomOwners(leader, dungeoncmd.MoveMapRequest{}); err != nil {
		t.Fatalf("same-coordinate path changed: %v", err)
	}
	storeRuntimePartyState(leader, alignedcmd.PartyState{})
	if err := service.preflightRuntimePartyRoomOwners(leader, dungeoncmd.MoveMapRequest{NextX: 1}); err != nil {
		t.Fatalf("single-member path changed: %v", err)
	}
}

func TestPartyMoveFollowerRefusalCannotReportSuccess(t *testing.T) {
	service, leader, follower := partyMovePreflightFixture(t)
	body := make([]byte, dungeoncmd.MoveMapRequestSize)
	body[0] = 2 // Outside the two-room fixture; existing client refusal is silent.
	plan := runtimePartyDungeonMovePlan{target: runtimeDungeonRoomKey{X: 2, MapID: 102}, seed: 1}
	if err := service.moveRuntimePartyFollowers(leader, body, plan); err == nil {
		t.Fatal("internal target refusal was reported as a successful party move")
	}
	if follower.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("invalid target sent a packet")
	}
	scene, _ := follower.dungeon.runtime.Session.Scene()
	if scene.Coordinate.X != 0 || scene.Map.Map.ID != 100 {
		t.Fatal("invalid target committed follower room")
	}
	follower.dungeon.runtime = nil
	if err := service.moveRuntimePartyFollowers(leader, body, plan); err == nil {
		t.Fatal("missing follower runtime was reported as a successful party move")
	}
}
