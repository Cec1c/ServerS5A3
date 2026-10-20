package dnfbridge

import (
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件钉住阶段二"队伍级共享副本实例"的判据、共享内容与回收时机。
// 判据复用阶段一的 `dungeonPartyRunKey`（队伍 + 副本 + 迷宫 + 房间 + 地图 + 种子），
// 不另造一套；单人/无队伍必须继续走原来的 per-session 路径。

func partyDungeonInstanceScene(x, y, mapID int64) worldmap.DungeonRoomScene {
	return worldmap.DungeonRoomScene{
		Coordinate: worldmap.RoomCoordinate{X: x, Y: y},
		Map:        worldmap.ResolvedMap{Map: worldmap.Map{ID: mapID}},
	}
}

func partyDungeonInstanceRoom(scene worldmap.DungeonRoomScene, monsterKeys ...uint32) *runtimeDungeonRoom {
	room := &runtimeDungeonRoom{
		coordinate:  scene.Coordinate,
		mapID:       scene.Map.Map.ID,
		monsters:    make([]runtimeDungeonMonster, 0, len(monsterKeys)),
		byObjectKey: make(map[uint32]int, len(monsterKeys)),
	}
	for index, objectKey := range monsterKeys {
		room.byObjectKey[objectKey] = index
		room.monsters = append(room.monsters, runtimeDungeonMonster{
			ObjectKey: objectKey,
			State:     runtimeDungeonMonsterPlanned,
		})
	}
	return room
}

// partyDungeonInstanceFixture 造一对同队同频道的会话 + 两份"解析结果相同"的运行时。
func partyDungeonInstanceFixture(t *testing.T, partyID int, seed uint32) (*Service, *gameSession, *gameSession, *runtimeDungeonState, *runtimeDungeonState, worldmap.DungeonRoomScene) {
	t.Helper()
	const channelID = byte(10)
	service, leader, follower, _ := newPartyFrameSessions(t, channelID, currentSceneObjectContext)
	state := alignedcmd.PartyState{
		PartyID:    partyID,
		UserID:     1001,
		MaxMembers: 4,
		UserState:  1,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 1001, UserState: 1, HPPercent: 100, MPPercent: 100},
			{UserID: 1002, UserState: 1, HPPercent: 100, MPPercent: 100},
		},
	}
	storeRuntimePartyState(leader, state)
	storeRuntimePartyState(follower, state)

	scene := partyDungeonInstanceScene(0, 1, 58569)
	leaderRuntime := &runtimeDungeonState{
		Dungeon:       worldmap.Dungeon{ID: 9},
		MazeIndex:     0,
		Seed:          seed,
		Room:          partyDungeonInstanceRoom(scene, 501, 502),
		NextObjectKey: 600,
		Rooms:         make(map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit),
	}
	followerRuntime := &runtimeDungeonState{
		Dungeon:       worldmap.Dungeon{ID: 9},
		MazeIndex:     0,
		Seed:          seed,
		Room:          partyDungeonInstanceRoom(scene, 501, 502),
		NextObjectKey: 600,
		Rooms:         make(map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit),
	}
	leaderRuntime.Rooms[runtimeDungeonRoomKeyFromScene(scene)] = &runtimeDungeonRoomVisit{
		Scene: scene, Room: leaderRuntime.Room, Seed: seed, DropRNG: seed,
	}
	followerRuntime.Rooms[runtimeDungeonRoomKeyFromScene(scene)] = &runtimeDungeonRoomVisit{
		Scene: scene, Room: followerRuntime.Room, Seed: seed, DropRNG: seed,
	}
	return service, leader, follower, leaderRuntime, followerRuntime, scene
}

// 同队两人解析到同一个实例键 ⇒ 同一个实例、同一个房间对象（怪物/清怪状态）、
// 同一个掉落账本、同一个 objectKey 发号器。
func TestPartyDungeonInstanceSharesRoomAndLedgerBetweenMembers(t *testing.T) {
	service, leader, follower, leaderRuntime, followerRuntime, scene := partyDungeonInstanceFixture(t, 7, 4242)
	leaderRoom := leaderRuntime.Room

	created := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene)
	if created == nil {
		t.Fatal("the first party member must publish a party dungeon instance")
	}
	if leaderRuntime.partyInstance != created {
		t.Fatalf("leader runtime instance=%p want=%p", leaderRuntime.partyInstance, created)
	}
	if created.room != leaderRoom || created.dropOwner != leaderRuntime.DropOwner {
		t.Fatalf("instance did not take the first member's room/ledger: room=%p want=%p", created.room, leaderRoom)
	}
	if got := service.partyDungeonInstanceCount(); got != 1 {
		t.Fatalf("instance count=%d want 1", got)
	}

	joined := service.adoptPartyDungeonInstance(follower, followerRuntime, scene)
	if joined != created {
		t.Fatalf("second member instance=%p want the same instance %p", joined, created)
	}
	if followerRuntime.Room != leaderRoom {
		t.Fatal("second member did not share the first member's room (monsters would be per-session)")
	}
	if followerRuntime.DropOwner != leaderRuntime.DropOwner {
		t.Fatal("second member did not share the drop ledger")
	}
	if got := service.partyDungeonInstanceCount(); got != 1 {
		t.Fatalf("instance count=%d want 1 after the join", got)
	}
	if got := created.memberCount(); got != 2 {
		t.Fatalf("instance member count=%d want 2", got)
	}

	// 怪物状态共享：一方的房间状态变化另一方立刻看到（同一个 room 对象）。
	if _, err := leaderRuntime.Room.MarkMonsterNonBlocking(0); err != nil {
		t.Fatalf("mark monster non blocking: %v", err)
	}
	snapshot := followerRuntime.Room.Snapshot()
	if len(snapshot.Monsters) == 0 || snapshot.Monsters[0].RoomClearSkipByte != 1 {
		t.Fatalf("shared room did not expose the other member's monster state: %+v", snapshot.Monsters)
	}

	// objectKey 发号器共享：一方提交后另一方从同一计数器继续。
	leaderRuntime.commitDungeonNextObjectKey(700)
	if got := followerRuntime.dungeonNextObjectKey(); got != 700 {
		t.Fatalf("shared object key allocator=%d want 700", got)
	}
	followerRuntime.commitDungeonNextObjectKey(712)
	if got := leaderRuntime.dungeonNextObjectKey(); got != 712 {
		t.Fatalf("shared object key allocator=%d want 712 after the peer commit", got)
	}
}

// 不同队伍、不同种子（以及房间对不上的后到者）都不共享。
func TestPartyDungeonInstanceSeparatesDifferentPartiesAndSeeds(t *testing.T) {
	service, leader, follower, leaderRuntime, _, scene := partyDungeonInstanceFixture(t, 7, 4242)
	first := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene)
	if first == nil {
		t.Fatal("first instance missing")
	}

	// 不同种子（同一队伍）⇒ 另一个实例。
	otherSeedRuntime := &runtimeDungeonState{
		Dungeon:       worldmap.Dungeon{ID: 9},
		MazeIndex:     0,
		Seed:          99,
		Room:          partyDungeonInstanceRoom(scene, 501, 502),
		NextObjectKey: 600,
		Rooms:         make(map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit),
	}
	if got := service.adoptPartyDungeonInstance(follower, otherSeedRuntime, scene); got == nil || got == first {
		t.Fatalf("a different seed must not share the instance: got=%p first=%p", got, first)
	}
	if got := service.partyDungeonInstanceCount(); got != 2 {
		t.Fatalf("instance count=%d want 2", got)
	}

	// 不同队伍（同一副本/种子）⇒ 另一个实例。
	otherPartyState := alignedcmd.PartyState{
		PartyID:    9,
		UserID:     1001,
		MaxMembers: 4,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 1001, UserState: 1},
			{UserID: 1002, UserState: 1},
		},
	}
	storeRuntimePartyState(follower, otherPartyState)
	otherPartyRuntime := &runtimeDungeonState{
		Dungeon:       worldmap.Dungeon{ID: 9},
		MazeIndex:     0,
		Seed:          4242,
		Room:          partyDungeonInstanceRoom(scene, 501, 502),
		NextObjectKey: 600,
		Rooms:         make(map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit),
	}
	if got := service.adoptPartyDungeonInstance(follower, otherPartyRuntime, scene); got == nil || got == first {
		t.Fatalf("a different party must not share the instance: got=%p first=%p", got, first)
	}

	// 判据包含房间：同一队伍/种子但房间不同 ⇒ 另一个实例（不是同一场）。
	storeRuntimePartyState(follower, alignedcmd.PartyState{
		PartyID: 7, UserID: 1001, MaxMembers: 4,
		Members: []alignedcmd.PartyMemberState{{UserID: 1001, UserState: 1}, {UserID: 1002, UserState: 1}},
	})
	elsewhere := partyDungeonInstanceScene(3, 4, 58570)
	elsewhereRuntime := &runtimeDungeonState{
		Dungeon:       worldmap.Dungeon{ID: 9},
		MazeIndex:     0,
		Seed:          4242,
		Room:          partyDungeonInstanceRoom(elsewhere, 601),
		NextObjectKey: 600,
		Rooms:         make(map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit),
	}
	if got := service.adoptPartyDungeonInstance(follower, elsewhereRuntime, elsewhere); got == nil || got == first {
		t.Fatalf("a different room must be a different instance: got=%p first=%p", got, first)
	}

	// fail-closed：运行时房间与会话场景对不上时，一个实例都不建、也不共享。
	mismatchRuntime := &runtimeDungeonState{
		Dungeon:       worldmap.Dungeon{ID: 9},
		MazeIndex:     0,
		Seed:          4242,
		Room:          partyDungeonInstanceRoom(scene, 501, 502),
		NextObjectKey: 600,
		Rooms:         make(map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit),
	}
	before := service.partyDungeonInstanceCount()
	if got := service.adoptPartyDungeonInstance(follower, mismatchRuntime, elsewhere); got != nil {
		t.Fatalf("a runtime whose room does not match its scene must not adopt an instance, got=%p", got)
	}
	if mismatchRuntime.partyInstance != nil {
		t.Fatal("declined adoption must not attach the runtime to an instance")
	}
	if got := service.partyDungeonInstanceCount(); got != before {
		t.Fatalf("declined adoption created an instance: count=%d want=%d", got, before)
	}
	if mismatchRuntime.NextObjectKey != 600 || mismatchRuntime.dungeonNextObjectKey() != 600 {
		t.Fatalf("declined adoption changed the local object key allocator: %d", mismatchRuntime.NextObjectKey)
	}
}

// 单人/无队伍：一个实例都不建，发号器与房间都留在本会话（原有路径不变）。
func TestSoloDungeonRuntimeKeepsPerSessionPath(t *testing.T) {
	service, leader, _, leaderRuntime, _, scene := partyDungeonInstanceFixture(t, 7, 4242)
	storeRuntimePartyState(leader, alignedcmd.PartyState{
		PartyID:    7,
		UserID:     1001,
		MaxMembers: 4,
		Members:    []alignedcmd.PartyMemberState{{UserID: 1001, UserState: 1}},
	})
	if got := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene); got != nil {
		t.Fatalf("a one-member party must not create an instance, got=%p", got)
	}
	if got := service.partyDungeonInstanceCount(); got != 0 {
		t.Fatalf("instance count=%d want 0", got)
	}
	leaderRuntime.commitDungeonNextObjectKey(900)
	if leaderRuntime.NextObjectKey != 900 || leaderRuntime.dungeonNextObjectKey() != 900 {
		t.Fatalf("per-session object key allocator=%d want 900", leaderRuntime.NextObjectKey)
	}
}

// 回收时机：全员脱离立即回收；队伍解散（缩到 1 人）回收该队伍的全部实例。
func TestPartyDungeonInstanceRecyclesWhenMembersLeaveOrPartyDisbands(t *testing.T) {
	service, leader, follower, leaderRuntime, followerRuntime, scene := partyDungeonInstanceFixture(t, 7, 4242)
	instance := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene)
	if instance == nil {
		t.Fatal("first instance missing")
	}
	if got := service.adoptPartyDungeonInstance(follower, followerRuntime, scene); got != instance {
		t.Fatal("second member did not join the instance")
	}

	// 出图/阵亡回城/换角色都会走 releasePartyDungeonInstance：成员摘到 0 人时实例回收。
	service.releasePartyDungeonInstance(followerRuntime, 1002, "test_member_leaves")
	if got := service.partyDungeonInstanceCount(); got != 1 {
		t.Fatalf("instance count=%d want 1 while one member remains", got)
	}
	if followerRuntime.partyInstance != nil {
		t.Fatal("released runtime still points at the instance")
	}
	service.releasePartyDungeonInstance(leaderRuntime, 1001, "test_last_member_leaves")
	if got := service.partyDungeonInstanceCount(); got != 0 {
		t.Fatalf("instance count=%d want 0 after the last member left", got)
	}

	// 队伍解散：即使成员还挂着，也立即回收该队伍的全部实例。
	again := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene)
	if again == nil {
		t.Fatal("instance missing after re-entry")
	}
	if got := service.adoptPartyDungeonInstance(follower, followerRuntime, scene); got != again {
		t.Fatal("second member did not rejoin the instance")
	}
	service.recyclePartyDungeonInstances(7, "party_disbanded")
	if got := service.partyDungeonInstanceCount(); got != 0 {
		t.Fatalf("instance count=%d want 0 after the party disbanded", got)
	}
}

// 内存上限：同队实例超过 currentPartyDungeonInstanceLimit 时按 lastSeenAt 淘汰最旧的。
func TestPartyDungeonInstanceCapacityPrunesOldest(t *testing.T) {
	service, leader, _, leaderRuntime, _, scene := partyDungeonInstanceFixture(t, 7, 1)
	registry := service.partyDungeonInstanceRegistryForService()
	if registry == nil {
		t.Fatal("registry missing")
	}
	// 先造满上限个"有成员"的实例（成员集合非空 ⇒ 不会被 no_members/idle 规则回收）。
	registry.mu.Lock()
	registry.byKey = make(map[dungeonPartyRunKey]*dungeonPartyInstance)
	for index := 0; index < currentPartyDungeonInstanceLimit; index++ {
		key := dungeonPartyRunKey{PartyID: uint16(100 + index), DungeonID: 9, MazeIndex: 0, Seed: uint32(index + 1)}
		createdAt := time.Now().Add(-time.Duration(currentPartyDungeonInstanceLimit-index) * time.Second)
		registry.byKey[key] = &dungeonPartyInstance{
			id:            uint64(index + 1),
			key:           key,
			createdAt:     createdAt,
			lastSeenAt:    createdAt,
			members:       map[uint16]time.Time{1001: createdAt},
			room:          partyDungeonInstanceRoom(scene, 501),
			rooms:         map[runtimeDungeonRoomKey]partyDungeonSharedRoom{},
			scene:         scene,
			nextObjectKey: 600,
		}
	}
	oldestKey := dungeonPartyRunKey{PartyID: 100, DungeonID: 9, MazeIndex: 0, Seed: 1}
	registry.mu.Unlock()

	if got := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene); got == nil {
		t.Fatal("adoption at capacity must still succeed")
	}
	if got := service.partyDungeonInstanceCount(); got > currentPartyDungeonInstanceLimit {
		t.Fatalf("instance count=%d exceeds the limit %d", got, currentPartyDungeonInstanceLimit)
	}
	registry.mu.Lock()
	_, oldestStillThere := registry.byKey[oldestKey]
	registry.mu.Unlock()
	if oldestStillThere {
		t.Fatal("the oldest instance was not evicted at capacity")
	}
}

// 后进图的队友在入口房间落位：与队长共享入口房间对象，并继承队长已经进过的房间。
func TestPartyDungeonInstanceLateJoinerInheritsVisitedRooms(t *testing.T) {
	service, leader, follower, leaderRuntime, followerRuntime, scene := partyDungeonInstanceFixture(t, 7, 4242)
	instance := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene)
	if instance == nil {
		t.Fatal("first instance missing")
	}
	entryRoom := leaderRuntime.Room

	// 队长先走进下一个房间：房间对象登记进实例（按坐标/地图共享）。
	nextScene := partyDungeonInstanceScene(1, 1, 58570)
	nextRoom := partyDungeonInstanceRoom(nextScene, 701, 702)
	leaderRuntime.Room = nextRoom
	if got := leaderRuntime.shareOrRegisterDungeonRoom(nextScene); got != nextRoom {
		t.Fatalf("leader room registration returned %p want=%p", got, nextRoom)
	}
	leaderRuntime.Rooms[runtimeDungeonRoomKeyFromScene(nextScene)] = &runtimeDungeonRoomVisit{
		Scene: nextScene, Room: nextRoom, Seed: leaderRuntime.Seed, DropRNG: leaderRuntime.Seed,
	}
	if got := instance.roomCount(); got != 2 {
		t.Fatalf("shared room count=%d want 2", got)
	}
	if _, err := nextRoom.MarkMonsterNonBlocking(0); err != nil {
		t.Fatalf("mark monster non blocking: %v", err)
	}

	// 队员在入口房间落位 ⇒ 共享入口房间对象，并继承第二个房间的共享状态。
	if got := service.adoptPartyDungeonInstance(follower, followerRuntime, scene); got != instance {
		t.Fatalf("late joiner instance=%p want=%p", got, instance)
	}
	if followerRuntime.Room != entryRoom {
		t.Fatal("late joiner did not share the party's entry room object")
	}
	inherited := followerRuntime.Rooms[runtimeDungeonRoomKeyFromScene(nextScene)]
	if inherited == nil || inherited.Room != nextRoom {
		t.Fatal("late joiner did not inherit the room the leader already visited")
	}
	if got := inherited.Room.Snapshot().Monsters[0].RoomClearSkipByte; got != 1 {
		t.Fatalf("inherited room monster state=%d want the leader's shared state", got)
	}
}
