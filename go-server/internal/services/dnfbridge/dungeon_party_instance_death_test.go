package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件钉住阶段二"共享一场副本"的**权威怪物死亡**口径（操作者双开复测症状：卡白怪）：
//
//  1. 任一成员提交的怪物死亡被接受后，必须把同一份 op38 死亡通知**广播给同一实例内的队友**，
//     并把同一份权威清怪状态应用到队友自己的副本场景（否则队友那边怪还活着 → 各自判死 →
//     后到者被拒且没有任何下行 → 客户端留白色残影）；
//  2. 已经 defeated 的**重复 op39 不能只回错误**：要把既有权威死亡状态幂等重放给请求方；
//  3. 单人/无队伍不进实例，重复 op39 仍然保持原来的静默行为（由 dungeon_runtime_test.go 钉住）。
func partyDungeonDeathFixture(t *testing.T, counts ...int) (*Service, *gameSession, *gameSession, *runtimeDungeonState, *runtimeDungeonState) {
	t.Helper()
	source := bridgeDungeonPVF(false)
	if len(counts) > 0 {
		source["map/dungeon/test/start.map"] = "[map name]\n`start`\n[dungeon]\n700\n[type]\n`[start]`\n[monster]\n" + strings.Repeat("3001 10 0 100 200 0 0 0 `[fixed]` `[normal]`\n", counts[0])
	}
	// 合成副本默认 `[limit party count] 1`；两人同场必须是 2 人以上。
	source["dungeon/test.dgn"] = strings.Replace(
		source["dungeon/test.dgn"],
		"[limit party count]\n1",
		"[limit party count]\n4",
		1,
	)
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	for _, characterID := range []string{"99", "100"} {
		if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
			CharacterID: characterID,
			AccountID:   "account-1",
			Level:       20,
			Stats:       map[string]int64{"fatigue": 100},
		}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
			CharacterID: characterID,
			Slots:       map[string]dnfrepo.ItemStack{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	service := &Service{
		options:             options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		onlinePlayers:       newOnlinePlayerManager(),
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return 0x12345678, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	channel := channelcatalog.Channel{ServerID: 1, ID: 121, Type: 11, Name: "ch.121", Port: 10121}
	leader := &gameSession{conn: &bufferConn{}, channel: channel, residentChannel: channel, selectedCharacterID: 99}
	follower := &gameSession{conn: &bufferConn{}, channel: channel, residentChannel: channel, selectedCharacterID: 100}
	service.bindGameSessionCharacter(leader, 99)
	service.bindGameSessionCharacter(follower, 100)
	state := alignedcmd.PartyState{
		PartyID: 12,
		UserID:  99,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 99, UserState: 1, HPPercent: 100, MPPercent: 100},
			{UserID: 100, UserState: 1, HPPercent: 100, MPPercent: 100},
		},
	}
	storeRuntimePartyState(leader, state)
	storeRuntimePartyState(follower, state)

	runtimes := make([]*runtimeDungeonState, 0, 2)
	for _, session := range []*gameSession{leader, follower} {
		runtime, _, err := service.prepareDungeonRuntime(
			context.Background(),
			session,
			dungeoncmd.SelectDungeonRequest{DungeonID: 700, Difficulty: 1},
		)
		if err != nil {
			t.Fatalf("prepare party member %d dungeon runtime: %v", session.selectedCharacterID, err)
		}
		scene, ok := runtime.Session.Scene()
		if !ok {
			t.Fatalf("party member %d runtime has no scene", session.selectedCharacterID)
		}
		if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
			t.Fatalf("announce party member %d actors: %v", session.selectedCharacterID, err)
		}
		session.dungeon.mu.Lock()
		session.dungeon.runtime = runtime
		session.dungeon.mu.Unlock()
		if instance := service.adoptPartyDungeonInstance(session, runtime, scene); instance == nil {
			t.Fatalf("party member %d did not join the shared dungeon instance", session.selectedCharacterID)
		}
		runtimes = append(runtimes, runtime)
	}
	if got := service.partyDungeonInstanceCount(); got != 1 {
		t.Fatalf("party dungeon instance count=%d want 1 (both members must share one run)", got)
	}
	for _, session := range []*gameSession{leader, follower} {
		scene, _ := session.dungeon.runtime.Session.Scene()
		service.recordDungeonPartyRunKey(session, session.dungeon.runtime, scene)
	}
	return service, leader, follower, runtimes[0], runtimes[1]
}

// 同一坐标/地图的房间在两个会话里必须是**同一个对象、同一组 object key**：
// 各建一份会让 key 序列分叉，权威死亡/清怪状态就落不到对方场景里
// （现场：`peer_scene_marked=false`、`runtime object is not found: object_key=418/420`、
// 房间永远不清 ⇒ 无法通关）。
func TestPartyDungeonInstanceSharesOneRoomObjectPerCoordinate(t *testing.T) {
	service, _, _, leaderRuntime, followerRuntime := partyDungeonDeathFixture(t)
	catalog, err := service.dungeonMonsterCatalog()
	if err != nil {
		t.Fatalf("monster catalog: %v", err)
	}
	entryScene, ok := leaderRuntime.Session.Scene()
	if !ok {
		t.Fatal("leader scene missing")
	}
	target := entryScene
	target.Coordinate = worldmap.RoomCoordinate{X: 1, Y: 1}

	leaderRoom, _, err := service.prepareRuntimeDungeonTargetRoom(leaderRuntime, target, catalog, "test_leader_room")
	if err != nil {
		t.Fatalf("leader target room: %v", err)
	}
	followerRoom, followerNext, err := service.prepareRuntimeDungeonTargetRoom(followerRuntime, target, catalog, "test_follower_room")
	if err != nil {
		t.Fatalf("follower target room: %v", err)
	}
	if leaderRoom != followerRoom {
		t.Fatal("two party members built two different room objects for the same coordinate/map")
	}
	// 复用方拿到的 objectKey 计数器必须已经越过房间里的怪，否则新发的 key 会和怪撞车。
	highKey := leaderRoom.highObjectKey()
	if highKey == 0 {
		t.Fatal("shared room reported no object keys")
	}
	if followerNext < highKey {
		t.Fatalf("reused room key base=%d is below the room's high key %d", followerNext, highKey)
	}
	if got := followerRuntime.dungeonNextObjectKey(); got < highKey {
		t.Fatalf("shared allocator=%d is below the room's high key %d", got, highKey)
	}
	// 房间对象只有一份：两个会话查到的都是它。
	if shared, ok := followerRuntime.partyDungeonSharedRoom(target); !ok || shared != leaderRoom {
		t.Fatalf("follower lookup shared=%p ok=%t want the same room %p", shared, ok, leaderRoom)
	}
	// 同一份房间对象 ⇒ 怪物 object key 天然一致（这正是权威死亡能落到两边的前提）。
	roomSnapshot := leaderRoom.Snapshot()
	if len(roomSnapshot.Monsters) == 0 {
		t.Fatal("shared room has no monsters")
	}
	for _, monster := range roomSnapshot.Monsters {
		if monster.ObjectKey == 0 || monster.ObjectKey >= highKey {
			t.Fatalf("room monster key=%d outside [1,%d)", monster.ObjectKey, highKey)
		}
	}
	if got := service.partyDungeonInstanceCount(); got != 1 {
		t.Fatalf("room sharing created an extra instance: count=%d", got)
	}
}

// 队友 actor 投影**只发对象创建三帧**：mode0（USERINFO subtype0 外观/建对象）→ mode1
// （subtype1 完整属性/装备/技能）→ op9（队伍记录，副本上下文 owner=0 带身份块）。
//
// 2026-09-25 定形：op120（0x78，body 只有 actor_slot+seed、不带 actor key）与 op23
// （0x17 USER_AREA）两条落位包都已在 07-05 / 02:12 两版复测过而无效，且同族实现
// （S4A21）在副本里没有任何"远端队友 actor 落位"原语 ⇒ 已移除。这条测试防止再被加回来。
func TestDungeonPartyPeerActorStateSendsCreationFramesWithoutPlacement(t *testing.T) {
	service, leader, follower, _, _ := partyDungeonDeathFixture(t)
	followerConn := follower.conn.(*bufferConn)
	followerConn.write.Reset()
	if err := service.sendDungeonPartyPeerActorState(
		follower,
		leader,
		"test_peer_actor_state",
	); err != nil {
		t.Fatalf("send peer actor state: %v", err)
	}
	frames := splitUpperFrames(t, followerConn.write.Bytes())
	if len(frames) != 3 {
		t.Fatalf("frames=%+v want exactly mode0 + mode1 + op9 (no placement packet)", frames)
	}
	if frames[0].msgID != uint16(dnfenum.CmdPacketSetUDPIPPort) ||
		frames[1].msgID != uint16(dnfenum.CmdPacketSetUDPIPPort) {
		t.Fatalf("mode0/mode1 frames=%+v want two USERINFO (0x0002) frames", frames[:2])
	}
	if frames[2].msgID != uint16(dnfenum.CmdPacketRecoverStamina) {
		t.Fatalf("third frame=%+v want the op9 party record", frames[2])
	}
	for _, frame := range frames {
		if frame.msgID == uint16(dnfenum.CmdPacketRequestBlacklist) {
			t.Fatalf("peer projection must not send op120 scene commit any more: %+v", frame)
		}
	}
}

// 投影必须在"本人进图落位"这一次调用里完成，且**不能**去取队友会话的 session.dungeon.mu：
// 队伍进图是队长事件循环 callGameSession(队员) 的互等结构，跨会话取锁迟早会锁死
// （dungeon_party_instance.go `partyDungeonInstancePeers` 的锁口径）。
// 这里模拟"调用方已经持有本会话 dungeon.mu"的最坏情况，投影仍必须在看门狗时间内跑完。
func TestDungeonPartyPeerProjectionTakesNoPeerSessionLock(t *testing.T) {
	service, leader, follower, leaderRuntime, followerRuntime := partyDungeonDeathFixture(t)
	leaderRuntime.actorPositionX = 42
	leaderRuntime.actorPositionY = 24
	leaderRuntime.actorPositionValid = true
	leaderRuntime.recordPartyDungeonActorPosition()
	followerRuntime.actorPositionX = 11
	followerRuntime.actorPositionY = 12
	followerRuntime.actorPositionValid = true
	followerRuntime.recordPartyDungeonActorPosition()

	// 队友（leader）会话的 dungeon.mu 被"另一个事件循环"持有着：投影不能等它。
	leader.dungeon.mu.Lock()
	defer leader.dungeon.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- service.projectDungeonPartyPeerActors(follower, "test_no_peer_session_lock")
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("project peer actors: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("projectDungeonPartyPeerActors blocked on the peer session's dungeon.mu")
	}
}

// 投影发生时队友还没上报坐标（进图落位有先后），投影帧里就不会有坐标提交；坐标到达后必须
// 副本内队伍帧的对称性：每个会话都必须拿到"自己的 + 队友的" op9 队伍记录（城镇那条靠
// 区域在线表补发，副本里 `leaveTownPresenceForDungeon` 已经把双方摘掉，所以必须由这里补），
// 且血条表 op153 与 op9 用同一份槽位顺序。
func TestDungeonPartyFrameRosterIsSymmetricPerSession(t *testing.T) {
	service, leader, follower, leaderRuntime, followerRuntime := partyDungeonDeathFixture(t)
	leaderScene, leaderSceneOK := leaderRuntime.Session.Scene()
	followerScene, followerSceneOK := followerRuntime.Session.Scene()
	if !leaderSceneOK || !followerSceneOK {
		t.Fatalf("fixture scenes missing: leader=%t follower=%t", leaderSceneOK, followerSceneOK)
	}
	service.recordDungeonPartyRunKey(leader, leaderRuntime, leaderScene)
	service.recordDungeonPartyRunKey(follower, followerRuntime, followerScene)
	if _, ready := service.currentDungeonPartyRunKey(follower); !ready {
		t.Fatalf("follower dungeon run key was not recorded by the fixture")
	}
	state := runtimePartyStateSnapshot(leader)
	leaderConn := leader.conn.(*bufferConn)
	followerConn := follower.conn.(*bufferConn)
	leaderConn.write.Reset()
	followerConn.write.Reset()

	if delivered := service.sendDungeonPartyFrameRoster(state, "test_dungeon_roster"); delivered != 4 {
		t.Fatalf("delivered=%d want 2 sessions x 2 op9 records", delivered)
	}
	for name, conn := range map[string]*bufferConn{"leader": leaderConn, "follower": followerConn} {
		frames := splitUpperFrames(t, conn.write.Bytes())
		realtimeFrames := 0
		recordFrames := 0
		covered := map[uint16]bool{}
		for _, frame := range frames {
			switch frame.msgID {
			case 0x0099:
				realtimeFrames++
			case 9:
				recordFrames++
				if len(frame.body) >= 6 {
					covered[binary.LittleEndian.Uint16(frame.body[4:6])] = true
				}
			}
		}
		if realtimeFrames != 1 || recordFrames != 2 {
			t.Fatalf("%s frames op153=%d op9=%d want 1 op153 and 2 op9 records (frames=%+v)",
				name, realtimeFrames, recordFrames, frames)
		}
		if !covered[currentSceneActorObjectKey(99)] || !covered[currentSceneActorObjectKey(100)] {
			t.Fatalf("%s op9 records cover keys=%v want both members", name, covered)
		}
	}
}

// 非敌对 actor 退役后会被移出房间索引：权威通知体必须仍然按 object key 取回
// （重复 op39 只能靠这份缓存重放，否则客户端把 actor 留在半死状态）。
func TestPartyDungeonRoomDeathResponseBodySurvivesRetiredActor(t *testing.T) {
	scene := partyDungeonInstanceScene(0, 1, 58569)
	room := partyDungeonInstanceRoom(scene, 402)
	body := []byte{1, 2, 3, 4, 5, 6, 7}
	room.RecordDeathResponseBody(402, body)
	cached, ok := room.DeathResponseBody(402)
	if !ok || !bytes.Equal(cached, body) {
		t.Fatalf("cached death body=% X ok=%t want=% X", cached, ok, body)
	}
	if _, ok := room.DefeatedDeathTarget(402); ok {
		t.Fatal("a planned monster must not report a defeated death target")
	}
	if _, ok := room.DeathResponseBody(999); ok {
		t.Fatal("unknown object key reported a cached death body")
	}
}

// 一个成员打死的怪：权威死亡通知必须广播给同实例队友，队友的副本场景也按同一份权威状态清怪；
// 队友随后那次"我自己也打死了它"的 op39 必须幂等重放，而不是只回错误。
func TestPartyDungeonInstanceBroadcastsAuthoritativeMonsterDeath(t *testing.T) {
	service, leader, follower, leaderRuntime, followerRuntime := partyDungeonDeathFixture(t)
	leaderConn := leader.conn.(*bufferConn)
	followerConn := follower.conn.(*bufferConn)

	monster := leaderRuntime.Room.Snapshot().Monsters[0]
	if monster.State != runtimeDungeonMonsterAnnounced {
		t.Fatalf("fixture monster state=%s want announced", monster.State)
	}
	leaderConn.write.Reset()
	followerConn.write.Reset()

	deathBody := make([]byte, 62)
	binary.LittleEndian.PutUint32(deathBody[0:4], monster.ObjectKey)
	binary.LittleEndian.PutUint16(deathBody[4:6], currentSceneActorObjectKey(leader.selectedCharacterID))
	if err := service.handleDungeonMonsterDeath(leader, deathBody); err != nil {
		t.Fatalf("leader monster death: %v", err)
	}

	if got := leaderRuntime.Room.Snapshot().Monsters[0].State; got != runtimeDungeonMonsterDefeated {
		t.Fatalf("shared room monster state=%s want defeated", got)
	}
	leaderFrames := splitUpperFrames(t, leaderConn.write.Bytes())
	var authoritativeBody []byte
	for _, frame := range leaderFrames {
		if frame.msgID == uint16(dnfenum.CmdPacketNotifyDieMonster) {
			authoritativeBody = frame.body
			break
		}
	}
	if len(authoritativeBody) == 0 {
		t.Fatalf("killer death frames=%+v want an op38 death notification", leaderFrames)
	}

	// 广播只发 op38；随后（同一份权威死亡把队友场景清空时）服务端会替队友跑一次
	// 最终房间完成，因此这里按 msgID 计数，不假设"只有一帧"。
	peerFrames := splitUpperFrames(t, followerConn.write.Bytes())
	if got := countPartyDeathTestFrames(peerFrames, uint16(dnfenum.CmdPacketNotifyDieMonster)); got != 1 {
		t.Fatalf("peer frames=%+v want exactly one broadcast op38 death notification", peerFrames)
	}
	peerBroadcastBody := partyDeathTestFrameBody(peerFrames, uint16(dnfenum.CmdPacketNotifyDieMonster))
	if !bytes.Equal(peerBroadcastBody, authoritativeBody) {
		t.Fatalf("peer death body=% X want the same authoritative body=% X", peerBroadcastBody, authoritativeBody)
	}
	peerScene, ok := followerRuntime.Session.Scene()
	if !ok {
		t.Fatal("peer scene missing")
	}
	defeated := false
	for _, objectKey := range peerScene.DefeatedObjects {
		if objectKey == monster.ObjectKey {
			defeated = true
		}
	}
	if !defeated {
		t.Fatalf("peer scene defeated objects=%v want the authoritative object %d (room clear must follow the same state)",
			peerScene.DefeatedObjects, monster.ObjectKey)
	}

	// 队友自己那条 op39：共享房间里这一怪已经 defeated ⇒ 幂等重放同一形状的死亡通知。
	followerConn.write.Reset()
	leaderConn.write.Reset()
	peerDeathBody := make([]byte, 62)
	binary.LittleEndian.PutUint32(peerDeathBody[0:4], monster.ObjectKey)
	binary.LittleEndian.PutUint16(peerDeathBody[4:6], currentSceneActorObjectKey(follower.selectedCharacterID))
	if err := service.handleDungeonMonsterDeath(follower, peerDeathBody); err != nil {
		t.Fatalf("peer duplicate monster death: %v", err)
	}
	replayFrames := splitUpperFrames(t, followerConn.write.Bytes())
	if got := countPartyDeathTestFrames(replayFrames, uint16(dnfenum.CmdPacketNotifyDieMonster)); got != 1 {
		t.Fatalf("peer replay frames=%+v want one op38 replay", replayFrames)
	}
	replayBody := partyDeathTestFrameBody(replayFrames, uint16(dnfenum.CmdPacketNotifyDieMonster))
	wantReplay, err := buildCurrentDungeonDeathNotificationBodyWithDrops(
		monster.ObjectKey,
		currentDungeonDeathResponseMonster,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(replayBody, wantReplay) {
		t.Fatalf("peer replay body=% X want drop-free authoritative death body=% X", replayBody, wantReplay)
	}
	if got := leaderRuntime.Room.Snapshot().Monsters[0].State; got != runtimeDungeonMonsterDefeated {
		t.Fatalf("duplicate op39 changed the authoritative state to %s", got)
	}
	if leaderConn.write.Len() != 0 {
		t.Fatalf("duplicate op39 wrote to the killer: % X", leaderConn.write.Bytes())
	}
}

func countPartyDeathTestFrames(frames []upperFrame, msgID uint16) int {
	count := 0
	for _, frame := range frames {
		if frame.msgID == msgID {
			count++
		}
	}
	return count
}

func partyDeathTestFrameBody(frames []upperFrame, msgID uint16) []byte {
	for _, frame := range frames {
		if frame.msgID == msgID {
			return frame.body
		}
	}
	return nil
}
