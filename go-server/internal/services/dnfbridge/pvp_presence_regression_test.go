package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"sync"
	"testing"
	"time"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// newPVPRegressionSession 造一个 PVP 频道会话。areaID 决定落点：
//   - 0 = 独立房间（`Fair_PVP/Gate_Hendon.map`，`[gate]`，每角色一间，**看不到别人**）
//   - 1 = 广场（`Fair_PVP/Hendon.map`，`[normal]`，共享，能看到别人）
func newPVPRegressionSession(service *Service, id uint16, areaID byte) *gameSession {
	session := &gameSession{
		conn:                   &bufferConn{},
		accountID:              "pvp-" + strconv.Itoa(int(id)),
		selectedCharacterID:    id,
		characterGeneration:    uint64(id),
		channel:                service.catalog.Channels()[1],
		sceneBootstrapTailSent: true,
	}
	session.channel.ID, session.channel.Type = 501, 8
	// 生产上由频道绑定写入（catalog 落点频道，恒 ≤255）。不填就是
	// `currentSceneObjectContext`(0)，`sendTownRemoteActorState` 会直接判
	// "remote town actor owner channel is not committed" 而不发 actor ——
	// 那样夹具就测不到真正的广播链路了。
	session.townActorOwnerChannel = 19
	session.townPositionSnapshot = currentTownPositionSnapshot{
		CharacterID: id, TownID: 10, AreaID: areaID,
		PositionX: 474, PositionY: 234, MovementCode: 5, PositionValid: true,
	}
	session.partyPeer = currentPartyPeerEndpointRegistration{
		InnerIPv4: [4]byte{127, 0, 0, 1}, OuterIPv4: [4]byte{127, 0, 0, 1},
		Port: 12000 + id, MTU: currentPartyPeerMinimumMTU,
	}
	if service.gameSessions == nil {
		service.gameSessions = make(map[uint16]*gameSession)
	}
	service.gameSessions[id] = session
	return session
}

// drainGameSessionPackets 取走并解析该会话累计写出的报文。
func drainGameSessionPackets(t *testing.T, session *gameSession) []dnfproto.ChannelPacket {
	t.Helper()
	conn := session.conn.(*bufferConn)
	wire := append([]byte(nil), conn.write.Bytes()...)
	conn.write.Reset()
	var packets []dnfproto.ChannelPacket
	for len(wire) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		packets = append(packets, packet)
		wire = rest
	}
	return packets
}

// syncBufferConn 是 `bufferConn` 的并发安全版本。
//
// 上游 07a8dba1「修复慢收件方阻塞」起，城镇广播不再同步写 socket，而是
// `queueTownPeerDelivery` 把投递提交到**收件方自己的 session 事件循环**
// （`p.events.loop.Submit`）。于是写操作发生在另一条 goroutine 上，
// `bufferConn`（无锁 `bytes.Buffer`）会数据竞争 —— 需要测「收件方真的收到包」
// 的用例必须换成本类型，并用 `take()` 在锁内取快照。
type syncBufferConn struct {
	bufferConn
	mu sync.Mutex
}

func (c *syncBufferConn) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.write.Write(data)
}

func (c *syncBufferConn) take() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	wire := append([]byte(nil), c.write.Bytes()...)
	c.write.Reset()
	return wire
}

// drainSyncBufferPackets 取走并解析并发安全 conn 上累计写出的报文。
func drainSyncBufferPackets(t *testing.T, conn *syncBufferConn) []dnfproto.ChannelPacket {
	t.Helper()
	wire := conn.take()
	var packets []dnfproto.ChannelPacket
	for len(wire) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		packets = append(packets, packet)
		wire = rest
	}
	return packets
}

// TestPVPMatchingPublishesPeerActor 锁定「自动匹配进决斗房间必须能看到对方」。
//
// 现场（2026-10-31）：玩家在**独立房间**（PVP 频道 area 0）点开始 → 匹配成功 →
// 进决斗房间看不到对方、技能也打不到；从广场（area 1）点开始却一切正常。
//
// 差别不在房间本身，而在**对方的 user-info 有没有被建立**：
//   - 广场是共享桶，进决斗前双方 actor 已经互相广播过，user-info 存在；
//   - 独立房间按角色分桶，双方从未互相广播，user-info 从未建立。
//
// `enterCurrentPVPRoomLocked` 在 `matchingToken != 0` 时提前 return（"Automatic
// matching publishes its roster after the native search ACK"），把它末尾那段
// actor 下发（op23 区域 / op3 用户状态 / op357 外观可见）整个跳过了 ——
// 于是两条路只有「广场」靠残留的 user-info 侥幸能用。
//
// 本用例断言匹配路径必须在 op504 之后、op505 之前补发这三类包，
// 且 op357 的 body 必须解出**对方**的 actor key。
//
// ★ 夹具站在广场（area 1）：p27 起服务端**禁止**在独立房间（area 0）发起决斗，
// 那里现在直接被拒绝，走不到配对路径。
func TestPVPMatchingPublishesPeerActor(t *testing.T) {
	service := pvpRoomTestService(t)
	service.questCatalog = buildPVPResultQuestCatalog(t)
	service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	service.pvpMapOrder = []int64{50000, 50042}
	service.gameSessions = make(map[uint16]*gameSession)

	a := newPVPRegressionSession(service, 29, 1)
	b := newPVPRegressionSession(service, 30, 1)

	request := []byte{1, 0, 0, 0, 0, 255, 255, 255, 255, 255, 255, 255, 255, 0}
	if err := service.handleGameCommand(a, byte(dnfenum.GameCmdCommand), 520, request); err != nil {
		t.Fatalf("首位玩家发起匹配：%v", err)
	}
	drainGameSessionPackets(t, a) // 丢掉搜索 ACK
	if err := service.handleGameCommand(b, byte(dnfenum.GameCmdCommand), 520, request); err != nil {
		t.Fatalf("第二位玩家发起匹配：%v", err)
	}
	if a.pvpRoom == nil || b.pvpRoom != a.pvpRoom {
		t.Fatal("匹配没有建立同一个房间")
	}

	for _, pair := range []struct{ self, peer *gameSession }{{a, b}, {b, a}} {
		packets := drainGameSessionPackets(t, pair.self)
		if len(packets) == 0 {
			t.Fatal("匹配完成后没有任何下发")
		}
		roster, completion := -1, -1
		for i, packet := range packets {
			switch packet.Header.MsgID {
			case 504:
				roster = i
			case 505:
				completion = i
			}
		}
		if roster < 0 || completion < 0 {
			t.Fatalf("缺少 op504/op505：roster=%d completion=%d", roster, completion)
		}
		if packets[len(packets)-1].Header.MsgID != 505 {
			t.Fatalf("最后一个包必须是 op505(completion)，实际 %d", packets[len(packets)-1].Header.MsgID)
		}

		areaAt, stateAt, visibilityAt := -1, -1, -1
		for i, packet := range packets {
			if i <= roster || i >= completion {
				continue
			}
			switch packet.Header.MsgID {
			case currentPVPUserAreaMsgID:
				areaAt = i
			case uint16(dnfenum.CmdPacketNotifyUserState):
				stateAt = i
			case currentActorUserStateMsgID:
				if len(packet.Body) == 3 && binary.LittleEndian.Uint16(packet.Body[0:2]) == pair.peer.selectedCharacterID {
					visibilityAt = i
				}
			}
		}
		if areaAt < 0 {
			t.Fatalf("匹配路径没在 op504 之后下发对方 op23 区域：决斗房间会看不到对方")
		}
		if stateAt < 0 {
			t.Fatalf("匹配路径没在 op504 之后下发对方 op3 用户状态")
		}
		if visibilityAt < 0 {
			t.Fatalf("匹配路径没下发对方 op357(actor=%d)：user-info 未建立，决斗房间看不到对方、技能也打不到",
				pair.peer.selectedCharacterID)
		}
	}
}

// TestPVPReturnToTownRepublishesPresence 锁定「PVP 结束返回广场能看到其他玩家」。
//
// `returnCurrentPVPMemberToTown` 原先只发 user-area / scene-transition / user-state
// 三个包，一次 `publishTownPresenceInArea` 都没调用。于是决斗结束回城后：
//   - 别人的 `PeerInSameArea` 找不到他 → 别人看不到他；
//   - 他自己的 `broadcastTownPlayerEnter` 拿不到 peer 列表 → 他看不到别人。
//
// 现场表现就是「PVP 结束以后返回广场看不到其他玩家」（2026-10-31）。
//
// 注意本用例走 `pvpRoomTestService`（有 `onlinePlayers`）；`pvpChannelInfoTestService`
// 的 `onlinePlayers` 是 nil，存在感发布会直接短路，锁不住这个回归。
//
// ★ 上游 07a8dba1 起，`broadcastTownPlayerEnter` 不再同步写 socket，而是走
// `queueTownPeerDelivery` → 收件方的 session 事件循环。所以本用例必须：
//  1. 把 peer 的 conn 换成 `syncBufferConn`（事件循环在另一条 goroutine 写）；
//  2. 用 `startGameSessionEvents` 把 peer 的事件循环真的跑起来
//     （没有循环时投递会被整条丢弃，只留一条 `recipient_queue_inactive` 日志）；
//  3. 轮询等 op23 落地，而不是假定它同步出现。
func TestPVPReturnToTownRepublishesPresence(t *testing.T) {
	service := pvpRoomTestService(t)
	service.questCatalog = buildPVPResultQuestCatalog(t)
	service.gameSessions = make(map[uint16]*gameSession)

	peer := newPVPRegressionSession(service, 30, 1)   // 广场里等着的玩家
	leaver := newPVPRegressionSession(service, 29, 1) // 刚打完决斗、要回广场的玩家
	peerConn := &syncBufferConn{}
	peer.conn = peerConn
	if err := service.startGameSessionEvents(context.Background(), peer); err != nil {
		t.Fatalf("启动 peer 事件循环：%v", err)
	}
	t.Cleanup(func() { _ = service.stopGameSessionEvents(context.Background(), peer) })

	service.publishTownPresenceInArea(peer, 10, 1, 474, 234, 5, newCharacterInitialAreaState, "test_peer_seed")
	if service.onlinePlayers.PeerInSameArea(29, 30) {
		t.Fatal("决斗中的玩家不应该已经出现在广场存在感里")
	}
	drainSyncBufferPackets(t, peerConn)

	room := &currentPVPRoom{id: 1, channelID: 501, battleMode: 2}
	member := &currentPVPMember{session: leaver, characterID: 29, position: leaver.townPositionSnapshot}
	room.members[0] = member
	leaver.pvpRoom = room

	// 生产路径上本函数的所有调用点都持有 s.pvpMu；这里同样持锁，
	// 顺带把「持锁路径里再调会自锁的普通变体」也钉住。
	service.pvpMu.Lock()
	err := service.returnCurrentPVPMemberToTown(member)
	service.pvpMu.Unlock()
	if err != nil {
		t.Fatalf("返回城镇失败：%v", err)
	}

	if !service.onlinePlayers.PeerInSameArea(29, 30) {
		t.Fatal("返回广场后没有重新发布存在感：他看不到别人，别人也看不到他")
	}

	// op23 经事件循环异步投递：轮询到它出现为止（上限 5s，远大于 createWriteTimeout）。
	deadline := time.Now().Add(5 * time.Second)
	var peerPackets []dnfproto.ChannelPacket
	for {
		peerPackets = append(peerPackets, drainSyncBufferPackets(t, peerConn)...)
		areaNotify := false
		for _, packet := range peerPackets {
			if packet.Header.MsgID == currentPVPUserAreaMsgID {
				areaNotify = true
			}
		}
		if areaNotify {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("广场里的玩家没有收到返回者的 op23 区域变更（收到 %d 个包）", len(peerPackets))
		}
		time.Sleep(2 * time.Millisecond)
	}
}
