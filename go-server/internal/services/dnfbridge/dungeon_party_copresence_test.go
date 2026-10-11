package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件钉住副本内"队友 actor 同屏投影"的判据与线格式（症状②③的回归）：
//   - 只有两个会话**确实在同一场副本**（运行键相等）才投影；
//   - op9 记录的 owner 字节必须用**收包者频道**（不是副本上下文 0），
//     这样客户端按 current-scene-create / 同频道成员行解析，和城镇同屏投影一致。

func dungeonRunKeyFixture(t *testing.T, channelID byte) (*Service, *gameSession, *gameSession, alignedcmd.PartyState) {
	t.Helper()
	service, leader, follower, state := newPartyFrameSessions(t, channelID, currentSceneObjectContext)
	storeRuntimePartyState(leader, state)
	storeRuntimePartyState(follower, state)
	key := dungeonPartyRunKey{
		PartyID:   1,
		DungeonID: 9,
		MazeIndex: 0,
		RoomX:     0,
		RoomY:     1,
		MapID:     58569,
		Seed:      7,
	}
	for _, session := range []*gameSession{leader, follower} {
		session.dungeonPartyRun = key
		session.dungeonPartyRunValid = true
		session.dungeonPeerProjected = make(map[uint16]bool, 2)
	}
	return service, leader, follower, state
}

// 队友还没落位（没有运行键）或落在别的场（种子/地图不同）时，一个字节都不能发，
// 而且不能提前标记"画过了"，否则后落位的一方永远不会补发。
func TestDungeonPartyPeerProjectionRequiresSameRun(t *testing.T) {
	service, leader, follower, _ := dungeonRunKeyFixture(t, 10)

	follower.dungeonPartyRunValid = false
	if err := service.projectDungeonPartyPeerActors(leader, "test_peer_not_landed"); err != nil {
		t.Fatalf("projection without peer run key: %v", err)
	}
	if leader.conn.(*bufferConn).write.Len() != 0 || follower.conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("projection wrote packets before the peer landed: leader=%d follower=%d",
			leader.conn.(*bufferConn).write.Len(), follower.conn.(*bufferConn).write.Len())
	}
	if leader.dungeonPeerProjected[1002] {
		t.Fatal("peer projection was claimed before the peer run key existed")
	}

	follower.dungeonPartyRunValid = true
	follower.dungeonPartyRun.Seed = 8
	if err := service.projectDungeonPartyPeerActors(leader, "test_different_run"); err != nil {
		t.Fatalf("projection across different runs: %v", err)
	}
	if leader.conn.(*bufferConn).write.Len() != 0 || follower.conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("projection crossed dungeon runs: leader=%d follower=%d",
			leader.conn.(*bufferConn).write.Len(), follower.conn.(*bufferConn).write.Len())
	}

	// 单人队伍不构成"同一场"判据。
	follower.dungeonPartyRun.Seed = 7
	storeRuntimePartyState(leader, alignedcmd.PartyState{
		PartyID:    1,
		UserID:     1001,
		MaxMembers: 4,
		Members:    []alignedcmd.PartyMemberState{{UserID: 1001, UserState: 1}},
	})
	if err := service.projectDungeonPartyPeerActors(leader, "test_solo_party"); err != nil {
		t.Fatalf("projection for a solo party: %v", err)
	}
	if leader.conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("solo party projected a peer: %d bytes", leader.conn.(*bufferConn).write.Len())
	}
}

// 副本队友的 op9 记录：owner 字节 = **副本场景上下文 0**（与本地 actor 那条记录同形），
// 而客户端实际所选频道是登录频道（10），两者不等 ⇒ 每个成员行**必须带跨频道身份块**，
// 客户端才认这个队友 actor（复测症状：owner 取频道时 member_identity_block=false → 互相看不见）。
// 0 号位仍然是权威队长、被投影的队友在 1 号位。
func TestDungeonPartyPeerFrameCarriesIdentityBlockForClientChannel(t *testing.T) {
	const channelID = byte(10)
	service, leader, follower, state := dungeonRunKeyFixture(t, channelID)

	if sent, err := service.sendCurrentPartyActorFrameProjectionInDungeonContext(
		leader, follower, state, "test_dungeon_peer_op9"); err != nil || !sent {
		t.Fatalf("dungeon peer op9 sent=%t err=%v", sent, err)
	}
	frames := splitUpperFrames(t, leader.conn.(*bufferConn).write.Bytes())
	if len(frames) != 1 || frames[0].msgID != 0x0009 {
		t.Fatalf("dungeon peer op9 frames=%+v want one op9", frames)
	}
	body := frames[0].body
	if body[7] != byte(currentSceneObjectContext) {
		t.Fatalf("dungeon peer op9 owner byte=%d want dungeon scene context %d body=% X",
			body[7], currentSceneObjectContext, body)
	}
	if got := currentClientSelectedChannel(leader); got != channelID {
		t.Fatalf("client selected channel=%d want %d", got, channelID)
	}
	// 客户端读法：记录 owner(0) != 客户端频道(10) ⇒ 走跨频道身份块分支。
	if body[7] == currentClientSelectedChannel(leader) {
		t.Fatal("dungeon peer op9 owner must differ from the client channel, otherwise the identity block is skipped")
	}
	rows, leaderSlot := parseOp9PartyFrame(t, body, true)
	if len(rows) != 2 || rows[0].uid != 1001 || rows[1].uid != 1002 {
		t.Fatalf("dungeon peer op9 rows=%+v want leader 1001 in slot 0", rows)
	}
	for index, row := range rows {
		if !row.hasIdentity {
			t.Fatalf("dungeon peer op9 row[%d] lost the inline identity block body=% X", index, body)
		}
	}
	// 队友那一行必须带上自己的名字（身份块的存在证据）。
	if rows[1].name == "" {
		t.Fatalf("dungeon peer op9 projected row carries no inline identity name: %+v", rows[1])
	}
	// 队长标记行 = 0 号位（权威队长），不能是"被投影的队友那一行"。
	if leaderSlot != 0 || rows[leaderSlot].uid != 1001 {
		t.Fatalf("dungeon peer op9 leader slot=%d (uid=%d) want 0 = leader 1001",
			leaderSlot, rows[leaderSlot].uid)
	}
}

// 运行键必须同时区分副本/迷宫/房间/地图/种子，并拒绝单人队伍。
func TestDungeonPartyRunKeyIdentity(t *testing.T) {
	_, leader, _, _ := dungeonRunKeyFixture(t, 10)
	runtime := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 9}, MazeIndex: 0, Seed: 7}
	scene := worldmap.DungeonRoomScene{
		Coordinate: worldmap.RoomCoordinate{X: 0, Y: 1},
		Map:        worldmap.ResolvedMap{Map: worldmap.Map{ID: 58569}},
	}
	key, ok := dungeonPartyRunKeyFrom(leader, runtime, scene)
	if !ok {
		t.Fatal("two-member party in a dungeon must produce a run key")
	}
	want := dungeonPartyRunKey{PartyID: 1, DungeonID: 9, MazeIndex: 0, RoomX: 0, RoomY: 1, MapID: 58569, Seed: 7}
	if key != want {
		t.Fatalf("run key=%+v want %+v", key, want)
	}
	otherRoom := scene
	otherRoom.Coordinate = worldmap.RoomCoordinate{X: 1, Y: 1}
	if otherKey, _ := dungeonPartyRunKeyFrom(leader, runtime, otherRoom); otherKey == key {
		t.Fatal("a different room must not share the run key")
	}
	otherSeed := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 9}, MazeIndex: 0, Seed: 8}
	if otherKey, _ := dungeonPartyRunKeyFrom(leader, otherSeed, scene); otherKey == key {
		t.Fatal("a different dungeon seed must not share the run key")
	}

	storeRuntimePartyState(leader, alignedcmd.PartyState{
		PartyID:    1,
		UserID:     1001,
		MaxMembers: 4,
		Members:    []alignedcmd.PartyMemberState{{UserID: 1001, UserState: 1}},
	})
	if _, ok := dungeonPartyRunKeyFrom(leader, runtime, scene); ok {
		t.Fatal("a one-member party must not produce a dungeon run key")
	}
}
