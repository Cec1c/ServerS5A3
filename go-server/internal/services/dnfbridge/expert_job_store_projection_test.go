package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 缺陷（操作者原话）：「分解机自己看正常 自己分解正常 **其他角色看不到你的分解机**」。
//
// 现场（/opt/dnf90/logs/packet_log.txt，20:20 之后那一份）：
//
//	:554 game-town-presence-published char=3 town=38 area=0 peers=0
//	:579 RECV type=598 CREATE_EXPERT_JOB_STORE（A 在共享城镇摆机器）→ :581 ACK 01
//	     ⇒ A 自己看得见；此刻区域里只有他自己 ⇒ 建店广播列表为空，**一个 538 都没发**。
//	:899 game-town-presence-published char=4 town=38 area=0 same_area_peers=1
//	     source=prev_village_return_from_private_seria_room（B 走进了 A 所在区域）
//	     ⇒ 仍然**没有任何 msg_id=538** ⇒ B 的客户端根本没收到这台机器。
//	:919/:921 SEND msg_id=539 同时发给 owner 与 B ⇒ 同区域扇出本身是通的。
//
// 本文件钉住修好后的行为：
//  1. 后进这个区域的人（op36 / 赛丽亚房间出房门 / 出副本回城重绑三条路径都经过
//     `broadcastTownPlayerEnter`）**必须**收到一次 NOTI538，字节与建店广播完全一致；
//  2. 只补对端——摆机器的人不会收到自己的店；别的区域/频道/实例一个字节都不发；
//  3. op36 路径上 town_move.go 的旧重放不会把同一台机器投第二次；
//  4. 回退开关 `DNFBRIDGE_EXPERT_JOB_STORE_PROJECTION=off` 恢复改动前行为
//     （补投影一次都不发，旧重放照旧发一次）。

type expertJobStoreProjectionFixture struct {
	service  *Service
	owner    *gameSession
	peer     *gameSession
	channel  int
	ownerBuf *bufferConn
	peerBuf  *bufferConn
}

// newExpertJobStoreProjectionFixture 造出现场那一幕：owner（角色 29）已经在共享城镇
// 38/0 摆好一台分解机；peer（角色 17）稍后从自己的私有赛丽亚房走进这个共享区域。
func newExpertJobStoreProjectionFixture(t *testing.T) expertJobStoreProjectionFixture {
	t.Helper()
	service, owner, repositories := newTownMoveTest(t)
	service.onlinePlayers = newOnlinePlayerManager()
	channel := owner.channel
	peer := &gameSession{
		conn:                            &bufferConn{},
		connID:                          "expert-job-store-peer",
		channel:                         channel,
		residentChannel:                 channel,
		selectedCharacterID:             17,
		townSceneReadyCharacterID:       17,
		connectionTownActorOwnerChannel: byte(channel.ID),
		townActorOwnerChannel:           byte(channel.ID),
	}
	startTownPeerTest(t, service, owner, &onlinePlayerInfo{
		CharacterID: 29, AccountID: "dnf:1", Name: "owner",
		TownID: 38, AreaID: 0, PositionX: 651, PositionY: 197,
		Direction: 5, AreaState: 3, Session: owner,
	})
	startTownPeerTest(t, service, peer, nil)
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "17",
		AccountID:   "dnf:2",
		Name:        "peer",
		Job:         "1",
		Level:       2,
		Stats: map[string]int64{
			"town_id": 38, "area_id": 0, "pos_x": 400, "pos_y": 200,
			"direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	service.expertJobStores = map[uint16]*currentExpertJobStore{
		29: newExpertJobStoreProjectionStore(29, owner, 38, 0, 0, channel.ID),
	}
	ownerBuf := owner.conn.(*bufferConn)
	peerBuf := peer.conn.(*bufferConn)
	ownerBuf.write.Reset()
	peerBuf.write.Reset()
	return expertJobStoreProjectionFixture{
		service: service, owner: owner, peer: peer, channel: channel.ID,
		ownerBuf: ownerBuf, peerBuf: peerBuf,
	}
}

func newExpertJobStoreProjectionStore(
	ownerCharacterID uint16,
	ownerSession *gameSession,
	townID, areaID byte,
	instance uint32,
	channelID int,
) *currentExpertJobStore {
	return &currentExpertJobStore{
		OwnerCharacterID: ownerCharacterID,
		OwnerSession:     ownerSession,
		Kind:             currentExpertJobDisjointStoreKind,
		Name:             []byte("1"),
		Cost:             1,
		TownID:           townID,
		AreaID:           areaID,
		ChannelID:        channelID,
		Instance:         instance,
		PositionX:        651,
		PositionY:        197,
		MachineGrade:     1,
		Endurance:        300,
	}
}

// enterTownArea 复刻入场路径的公共出口：先登记存在感，再调 broadcastTownPlayerEnter
// （op36 / 赛丽亚出房门 / 出副本回城重绑都走它）。
func (f expertJobStoreProjectionFixture) enterTownArea(
	t *testing.T,
	session *gameSession,
	characterID uint16,
	name string,
	townID, areaID byte,
	instance uint32,
) {
	t.Helper()
	info := &onlinePlayerInfo{
		CharacterID: characterID, AccountID: "dnf:2", Name: name,
		TownID: townID, AreaID: areaID, Instance: instance,
		PositionX: 622, PositionY: 196, Direction: 5, AreaState: 3,
		Session: session,
	}
	others := f.service.onlinePlayers.EnterArea(info)
	f.service.broadcastTownPlayerEnter(info, others)
	drainTownPeerTest(t, f.service, f.owner)
	drainTownPeerTest(t, f.service, f.peer)
}

// storeProjectionFrames 取出（并清空）某个会话收到的 NOTI538 投影 body。
func storeProjectionFrames(t *testing.T, session *gameSession) [][]byte {
	t.Helper()
	bodies := make([][]byte, 0, 2)
	for _, frame := range writeFrames(t, session) {
		if frame.msgID == currentExpertJobStoreCreateNotification {
			bodies = append(bodies, frame.body)
		}
	}
	return bodies
}

func TestExpertJobStoreProjectionReachesLateAreaJoiner(t *testing.T) {
	fixture := newExpertJobStoreProjectionFixture(t)
	logPath := filepath.Join(t.TempDir(), "packet_log.txt")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.packetLog = logger

	// B 从自己的私有赛丽亚房（38/1）走出房门，进入 A 摆机器的那个共享区域 —— 正是现场
	// :899 `source=prev_village_return_from_private_seria_room` 那一步。
	fixture.service.publishTownPresenceAfterSeriaRoomReturn(fixture.peer, 38, 0, 622, 196, 5, 3)
	drainTownPeerTest(t, fixture.service, fixture.owner)
	drainTownPeerTest(t, fixture.service, fixture.peer)

	// 先把日志落盘再断言：断言的 t.Fatal 会跳过后续语句，不能把 Close 留到断言之后。
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)

	if fixture.ownerBuf.write.Len() == 0 {
		t.Fatal("fixture never projected the newcomer actor to the owner; test setup is wrong")
	}
	ownerFrames := storeProjectionFrames(t, fixture.owner)
	if len(ownerFrames) != 0 {
		t.Fatalf("owner received %d own-store projection(s), want 0", len(ownerFrames))
	}
	store := fixture.service.expertJobStores[29]
	if store == nil {
		t.Fatal("fixture lost the placed store")
	}
	want := buildCurrentExpertJobStoreCreateNotification(store)
	bodies := storeProjectionFrames(t, fixture.peer)
	if len(bodies) != 1 || !bytes.Equal(bodies[0], want) {
		t.Fatalf("late joiner store projection bodies=% X want exactly one % X", bodies, want)
	}
	// The disjoint-store body ends with the native attachment mode. Freeze the
	// owner-bound value independently of the builder: mode 2 spawns a second
	// uncostumed actor in the peer's view.
	if got := bodies[0][len(bodies[0])-1]; got != 1 {
		t.Fatalf("late joiner store attachment mode=%d, want owner-bound mode 1", got)
	}
	for _, needle := range []string{
		"kind=game-expert-job-store-projection",
		"store_owner_char_ids=[29]",
		"store_count=1",
		"msg_id=538",
		"body_hex=" + hex.EncodeToString(want),
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("projection log is missing %q\n--- log ---\n%s", needle, text)
		}
	}
}

func TestExpertJobStoreProjectionSkipsOwnStoreAndForeignChannels(t *testing.T) {
	fixture := newExpertJobStoreProjectionFixture(t)
	// 同一区域里另有一条频道上的店，以及另一个区域里的店：两者都不该被投到这里。
	fixture.service.expertJobStores[30] = newExpertJobStoreProjectionStore(
		30, fixture.owner, 38, 0, 0, fixture.channel+1)
	fixture.service.expertJobStores[31] = newExpertJobStoreProjectionStore(
		31, fixture.owner, 38, 2, 0, fixture.channel)

	// ① 摆机器的人自己走进同一区域：自己的店不投给自己，另两台被频道/区域过滤掉。
	fixture.enterTownArea(t, fixture.owner, 29, "owner", 38, 0, 0)
	if frames := storeProjectionFrames(t, fixture.owner); len(frames) != 0 {
		t.Fatalf("owner got %d own-store projection(s), want 0", len(frames))
	}
	// ② B 进入 38/0：只应拿到 29 那一台。
	fixture.enterTownArea(t, fixture.peer, 17, "peer", 38, 0, 0)
	bodies := storeProjectionFrames(t, fixture.peer)
	want := buildCurrentExpertJobStoreCreateNotification(fixture.service.expertJobStores[29])
	if len(bodies) != 1 || !bytes.Equal(bodies[0], want) {
		t.Fatalf("same-area joiner bodies=% X want exactly the area store % X", bodies, want)
	}
	// ③ B 再走进 38/2：只应拿到那里的那一台。
	fixture.enterTownArea(t, fixture.peer, 17, "peer", 38, 2, 0)
	bodies = storeProjectionFrames(t, fixture.peer)
	want = buildCurrentExpertJobStoreCreateNotification(fixture.service.expertJobStores[31])
	if len(bodies) != 1 || !bytes.Equal(bodies[0], want) {
		t.Fatalf("second-area joiner bodies=% X want exactly that area's store % X", bodies, want)
	}
}

func TestExpertJobStoreProjectionIsInstanceScoped(t *testing.T) {
	fixture := newExpertJobStoreProjectionFixture(t)
	// 第二个人在 38/1 摆了一台，但那是"每个角色一份"的私有房间（实例 = 他的角色 ID 30）。
	fixture.service.expertJobStores[30] = newExpertJobStoreProjectionStore(
		30, fixture.owner, 38, 1, 30, fixture.channel)

	// B（角色 17）进的是自己那份私房（实例 17）：既看不到别人私房里的机器，
	// 也不该看到共享城镇里的那台。
	fixture.enterTownArea(t, fixture.peer, 17, "peer", 38, 1, 17)
	if frames := storeProjectionFrames(t, fixture.peer); len(frames) != 0 {
		t.Fatalf("private-room joiner got %d foreign store projection(s), want 0", len(frames))
	}
	// 同一个人走进共享城镇：只应看到共享城镇那一台（29），看不到私房那一台（30）。
	fixture.enterTownArea(t, fixture.peer, 17, "peer", 38, 0, 0)
	bodies := storeProjectionFrames(t, fixture.peer)
	want := buildCurrentExpertJobStoreCreateNotification(fixture.service.expertJobStores[29])
	if len(bodies) != 1 || !bytes.Equal(bodies[0], want) {
		t.Fatalf("shared-town joiner bodies=% X want exactly the shared store % X", bodies, want)
	}
}

// op36 那条路径上 town_move.go 还会调一次旧重放（replayCurrentExpertJobStores），
// 修好档下它必须变成空操作 —— 否则客户端会把同一台机器建两次。
func TestExpertJobStoreProjectionDoesNotDoubleSendOnSetUserArea(t *testing.T) {
	fixture := newExpertJobStoreProjectionFixture(t)
	fixture.enterTownArea(t, fixture.peer, 17, "peer", 38, 0, 0)
	fixture.service.replayCurrentExpertJobStores(fixture.peer, 38, 0)
	drainTownPeerTest(t, fixture.service, fixture.peer)
	if bodies := storeProjectionFrames(t, fixture.peer); len(bodies) != 1 {
		t.Fatalf("op36 entry produced %d store projections, want exactly 1", len(bodies))
	}
	// 重新进出同一区域（新的一次入场）仍然会重投一次：保持改动前"重进区域即恢复"的行为。
	fixture.enterTownArea(t, fixture.peer, 17, "peer", 38, 0, 0)
	if bodies := storeProjectionFrames(t, fixture.peer); len(bodies) != 1 {
		t.Fatalf("re-entering the same area produced %d store projections, want 1", len(bodies))
	}
}

func TestExpertJobStoreProjectionLegacyRollback(t *testing.T) {
	t.Setenv(currentExpertJobStoreProjectionEnv, "off")
	if currentExpertJobStoreProjectionEnabled() {
		t.Fatal("rollback switch must disable the late-joiner projection")
	}
	fixture := newExpertJobStoreProjectionFixture(t)
	// 回退档：入场补投影一次都不发……
	fixture.enterTownArea(t, fixture.peer, 17, "peer", 38, 0, 0)
	if frames := storeProjectionFrames(t, fixture.peer); len(frames) != 0 {
		t.Fatalf("rollback still projected %d store frame(s) on area entry", len(frames))
	}
	// ……旧重放（op36 路径）恢复改动前的行为：无条件发一次、逐字节同形状。
	fixture.service.replayCurrentExpertJobStores(fixture.peer, 38, 0)
	drainTownPeerTest(t, fixture.service, fixture.peer)
	bodies := storeProjectionFrames(t, fixture.peer)
	want := buildCurrentExpertJobStoreCreateNotification(fixture.service.expertJobStores[29])
	if len(bodies) != 1 || !bytes.Equal(bodies[0], want) {
		t.Fatalf("legacy replay bodies=% X want exactly one % X", bodies, want)
	}
}

// 建店瞬间的广播只覆盖"那一刻已经在本区域"的会话（现场 :554 peers=0 ⇒ 当时一个字节
// 都没发）；这条钉住它对在场者的扇出与包体，以及对不同频道对端的隔离。
func TestExpertJobStoreCreateBroadcastReachesPresentPeersOnly(t *testing.T) {
	fixture := newExpertJobStoreProjectionFixture(t)
	store := fixture.service.expertJobStores[29]
	body := buildCurrentExpertJobStoreCreateNotification(store)

	// B 已经在场（同频道、同城同区）。
	fixture.service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 17, AccountID: "dnf:2", Name: "peer",
		TownID: 38, AreaID: 0, PositionX: 400, PositionY: 200,
		Direction: 5, AreaState: 3, Session: fixture.peer,
	})
	fixture.service.broadcastCurrentExpertJobStoreCreate(store, body, "test_store_create")
	drainTownPeerTest(t, fixture.service, fixture.peer)
	drainTownPeerTest(t, fixture.service, fixture.owner)
	frames := storeProjectionFrames(t, fixture.peer)
	if len(frames) != 1 || !bytes.Equal(frames[0], body) {
		t.Fatalf("present peer store broadcast=% X want exactly one % X", frames, body)
	}
	if got := frames[0][len(frames[0])-1]; got != 1 {
		t.Fatalf("present peer store attachment mode=%d, want owner-bound mode 1", got)
	}
	if frames := storeProjectionFrames(t, fixture.owner); len(frames) != 0 {
		t.Fatalf("create broadcast leaked %d frame(s) to the owner", len(frames))
	}
}
