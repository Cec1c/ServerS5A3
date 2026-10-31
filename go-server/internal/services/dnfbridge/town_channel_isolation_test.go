package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 现场报障：双开时"**10 线的玩家会见到 121 线的玩家**"（不同频道的玩家在城镇里
// 互相可见/互相影响）。
//
// 线上封包证据（`/opt/dnf90/logs/packet_log.20260924-224523.txt`，操作者最近一次双开）：
//   - `game-connect conn_id=game-000021 local=…:10010 channel_id=10`
//     `game-connect conn_id=game-000008 local=…:10121 channel_id=121`
//   - 两条 `game-town-set-user-area-committed` 都落在 `town_id=38 area_id=2`
//   - `game-town-copresence-actor-state-send conn_id=game-000021 channel_id=10
//     source=town_copresence_actor_state_newcomer_to_existing target_char_id=3 actor_char_id=5`
//     与反向的 `conn_id=game-000008 channel_id=121 … actor_char_id=3`
//     → 服务端把 121 线的角色 5 推给了 10 线的客户端，也把 10 线的角色 3 推给了 121 线。
//   - 轮转日志里还有 44 次 `town_copresence_move_0x0016`（互相影响：连走路都在互相广播）。
//
// 根因：`OnlinePlayerManager` 的分桶键是 `(TownID, AreaID, Instance)`——**频道不在键里**，
// 于是同城同区不同频道的两个人落进同一个桶，所有同屏广播（mode0/mode1/op9 actor、
// 0x0017 区域通知、0x0016 移动、0x0006 离开、op24 名册）都不分频道。
//
// 本文件的用例把修复后的口径钉死：**彻底隔离**（操作者 2026-09-24 拍板）——
// 同频道同区域互相可见、可组队、可交互；跨频道既不可见也不可组队（邀请/接受/入队
// 明确回失败包）；私房仍按角色私有。

func townChannelSession(channelID int, port int) *gameSession {
	return &gameSession{
		conn:            &bufferConn{},
		channel:         channelcatalog.Channel{ID: channelID, Type: 11, Name: "ch", Port: port},
		residentChannel: channelcatalog.Channel{ID: channelID, Type: 11, Name: "ch", Port: port},
	}
}

// 回归①：同城同区**不同频道**的两个会话 → 互不可见、不互相广播。
func TestTownAreaBucketSeparatesChannels(t *testing.T) {
	manager := newOnlinePlayerManager()
	ch10 := townChannelSession(10, 10010)
	ch121 := townChannelSession(121, 10121)

	if others := manager.EnterArea(&onlinePlayerInfo{CharacterID: 3, Name: "test2", TownID: 38, AreaID: 2, Session: ch10}); len(others) != 0 {
		t.Fatalf("first entrant on channel 10 must see nobody, got %d", len(others))
	}
	others := manager.EnterArea(&onlinePlayerInfo{CharacterID: 5, Name: "runner", TownID: 38, AreaID: 2, Session: ch121})
	if len(others) != 0 {
		t.Fatalf("121 线玩家进了同一 (town,area) 却看到了 %d 个 10 线玩家: %+v", len(others), others)
	}

	// 两个方向都不能互相看见 / 不能互相交互。
	if manager.PeerInSameArea(3, 5) || manager.PeerInSameArea(5, 3) {
		t.Fatal("cross-channel peers must not be treated as being in the same area")
	}
	if peers := manager.PeersInSameArea(3); len(peers) != 0 {
		t.Fatalf("channel 10 peer projection leaked %d cross-channel actors", len(peers))
	}
	if peers := manager.PeersInSameArea(5); len(peers) != 0 {
		t.Fatalf("channel 121 peer projection leaked %d cross-channel actors", len(peers))
	}
	// 移动广播（0x0016）也一样：跨频道拿不到对方的 actor 列表。
	if others := manager.UpdatePosition(3, 900, 250); len(others) != 0 {
		t.Fatalf("cross-channel movement broadcast reached %d peers", len(others))
	}
	// 区域人数（op24 名册的数据源）按频道枚举。
	if players := manager.GetAreaPlayers(38, 2, 10); len(players) != 1 || players[0].CharacterID != 3 {
		t.Fatalf("channel 10 area roster = %+v, want exactly character 3", players)
	}
	if players := manager.GetAreaPlayers(38, 2, 121); len(players) != 1 || players[0].CharacterID != 5 {
		t.Fatalf("channel 121 area roster = %+v, want exactly character 5", players)
	}
	// 每次登记都必须把解析出来的频道写回存在里（日志/判定都用它）。
	if info, ok := manager.PlayerForCharacter(3); !ok || info.ChannelID != 10 {
		t.Fatalf("character 3 presence channel = %+v ok=%t, want 10", info, ok)
	}
	if info, ok := manager.PlayerForCharacter(5); !ok || info.ChannelID != 121 {
		t.Fatalf("character 5 presence channel = %+v ok=%t, want 121", info, ok)
	}
}

// 回归②：同城同区**同频道** → 仍然互相可见（人数 = 2），移动照旧互相广播。
func TestSameChannelSameTownAreaKeepsCoPresence(t *testing.T) {
	manager := newOnlinePlayerManager()
	first := townChannelSession(10, 10010)
	second := townChannelSession(10, 10010)

	if others := manager.EnterArea(&onlinePlayerInfo{CharacterID: 3, TownID: 38, AreaID: 2, PositionX: 55, PositionY: 267, Session: first}); len(others) != 0 {
		t.Fatalf("first entrant must see nobody, got %d", len(others))
	}
	others := manager.EnterArea(&onlinePlayerInfo{CharacterID: 5, TownID: 38, AreaID: 2, Session: second})
	if len(others) != 1 || others[0].CharacterID != 3 {
		t.Fatalf("same-channel same-area newcomer must see the incumbent, got %+v", others)
	}
	if !manager.PeerInSameArea(3, 5) || !manager.PeerInSameArea(5, 3) {
		t.Fatal("same-channel same-area peers must stay mutually visible")
	}
	if peers := manager.PeersInSameArea(5); len(peers) != 1 || peers[0].CharacterID != 3 {
		t.Fatalf("same-channel peer projection = %+v, want character 3", peers)
	}
	if got := manager.GetAreaPlayers(38, 2, 10); len(got) != 2 {
		t.Fatalf("same-channel area roster = %d players, want 2", len(got))
	}
	if others := manager.UpdatePosition(5, 1085, 249); len(others) != 1 || others[0].CharacterID != 3 {
		t.Fatalf("same-channel movement broadcast = %+v, want character 3", others)
	}
	// 换频道必须换桶：本人重新登记到 121 线后，10 线的旧同伴不再看得到他。
	if others := manager.EnterArea(&onlinePlayerInfo{CharacterID: 5, TownID: 38, AreaID: 2, Session: townChannelSession(121, 10121)}); len(others) != 0 {
		t.Fatalf("channel switch must move the presence into the new channel bucket, got %d peers", len(others))
	}
	if manager.PeerInSameArea(3, 5) {
		t.Fatal("a channel switch must detach the old same-area peer")
	}
	if got := manager.GetAreaPlayers(38, 2, 10); len(got) != 1 {
		t.Fatalf("channel 10 roster after the switch = %d players, want 1", len(got))
	}
}

// 回归③：私房（Instance≠0）仍然按角色私有，并且频道分量不会把它变回共享。
func TestPrivateInstanceStaysPrivateWithinTheSameChannel(t *testing.T) {
	manager := newOnlinePlayerManager()
	ch10 := townChannelSession(10, 10010)

	if others := manager.EnterArea(&onlinePlayerInfo{CharacterID: 3, TownID: 38, AreaID: 1, Instance: 3, Session: ch10}); len(others) != 0 {
		t.Fatalf("first private-room entrant must see nobody, got %d", len(others))
	}
	if others := manager.EnterArea(&onlinePlayerInfo{CharacterID: 2, TownID: 38, AreaID: 1, Instance: 2, Session: ch10}); len(others) != 0 {
		t.Fatalf("a second character entered somebody else's private room (same channel) and saw %d", len(others))
	}
	if manager.PeerInSameArea(2, 3) {
		t.Fatal("private-room owners must never be peers of each other")
	}
	// 共享区域枚举（Instance 固定 0）看不到私房占有者。
	if players := manager.GetAreaPlayers(38, 1, 10); len(players) != 0 {
		t.Fatalf("shared-area enumeration exposed %d private-room occupants", len(players))
	}
	// 同一角色重进自己的私房仍正常，且仍看不到别人。
	if others := manager.EnterArea(&onlinePlayerInfo{CharacterID: 3, TownID: 38, AreaID: 1, Instance: 3, Session: ch10}); len(others) != 0 {
		t.Fatalf("re-entering own private room must not see others, got %d", len(others))
	}
}

// 口径（操作者 2026-09-24 拍板）：**彻底隔离——跨频道完全不能组队**。
// 所以跨频道既不是同屏同伴，也不是可组队/可交互对象；唯一的合法关系是
// "同频道 + 同城镇 + 同区域 + 同私有实例"。本用例钉住两个方向：
// 跨频道一律 false，同频道同区域仍然 true（既有组队/交互闸门不退化）。
func TestCrossChannelPeersAreNeitherCoPresentNorPartyable(t *testing.T) {
	manager := newOnlinePlayerManager()
	if others := manager.EnterArea(&onlinePlayerInfo{CharacterID: 3, TownID: 38, AreaID: 2, Session: townChannelSession(10, 10010)}); len(others) != 0 {
		t.Fatalf("first entrant must see nobody, got %d", len(others))
	}
	if others := manager.EnterArea(&onlinePlayerInfo{CharacterID: 5, TownID: 38, AreaID: 2, Session: townChannelSession(121, 10121)}); len(others) != 0 {
		t.Fatalf("cross-channel peer must not be co-present, got %d", len(others))
	}
	if manager.PeerInSameArea(3, 5) || manager.PeerInSameArea(5, 3) {
		t.Fatal("cross-channel peers must never be a valid party/interaction target")
	}
	// 不同城镇（或不同区域、不同私有实例）在任何情况下也不是同一场景。
	manager2 := newOnlinePlayerManager()
	manager2.EnterArea(&onlinePlayerInfo{CharacterID: 3, TownID: 38, AreaID: 2, Session: townChannelSession(10, 10010)})
	manager2.EnterArea(&onlinePlayerInfo{CharacterID: 5, TownID: 38, AreaID: 0, Session: townChannelSession(10, 10010)})
	if manager2.PeerInSameArea(3, 5) {
		t.Fatal("characters in different town areas must not be peers")
	}
	// 同频道同区域仍是合法伙伴（组队/交易/查看信息都靠这条）。
	manager3 := newOnlinePlayerManager()
	manager3.EnterArea(&onlinePlayerInfo{CharacterID: 3, TownID: 38, AreaID: 2, Session: townChannelSession(10, 10010)})
	manager3.EnterArea(&onlinePlayerInfo{CharacterID: 5, TownID: 38, AreaID: 2, Session: townChannelSession(10, 10010)})
	if !manager3.PeerInSameArea(3, 5) || !manager3.PeerInSameArea(5, 3) {
		t.Fatal("same-channel same-area peers must stay valid party targets")
	}
}

// 跨频道组队在**唯一入口**上被拒：op705（直接入队）与 op90（队伍目录快速加入）都汇到
// createOrJoinManagedRuntimeParty，且双方队伍状态完全不变（不产生"跨频道队伍"）。
func TestCrossChannelPartyJoinIsRejectedAtTheChokePoint(t *testing.T) {
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	leader := &gameSession{conn: &bufferConn{}, channel: channelcatalog.Channel{ID: 10, Type: 11, Name: "ch.10", Port: 10010}, selectedCharacterID: 1001}
	joiner := &gameSession{conn: &bufferConn{}, channel: channelcatalog.Channel{ID: 121, Type: 22, Name: "ch.121", Port: 10121}, selectedCharacterID: 1002}
	service.bindGameSessionCharacter(leader, 1001)
	service.bindGameSessionCharacter(joiner, 1002)

	if _, result, joined := service.createOrJoinManagedRuntimeParty(joiner, leader); joined || result.OK {
		t.Fatalf("cross-channel join must be rejected: joined=%t result=%+v", joined, result)
	} else if result.Reason != partyMemberChannelMismatchReason {
		t.Fatalf("cross-channel join reason = %q, want %q", result.Reason, partyMemberChannelMismatchReason)
	}
	if state := runtimePartyStateSnapshot(leader); state.PartyID != 0 || len(runtimePartyMembers(state)) != 0 {
		t.Fatalf("leader gained a party from a cross-channel join: %+v", state)
	}
	if state := runtimePartyStateSnapshot(joiner); state.PartyID != 0 || len(runtimePartyMembers(state)) != 0 {
		t.Fatalf("joiner gained a party from a cross-channel join: %+v", state)
	}
	identity, bound := service.boundGameSessionCharacterSnapshot(leader)
	if !bound {
		t.Fatal("leader identity was not bound")
	}
	if snapshot, found := service.runtimePartyManagerForService().SnapshotByUser(1001, identity.generation); found {
		t.Fatalf("cross-channel join created a managed party: %+v", snapshot)
	}
}

// 同频道入队不受影响（③ 既有功能不回退）。
func TestSameChannelPartyJoinStillSucceeds(t *testing.T) {
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	channel := channelcatalog.Channel{ID: 10, Type: 11, Name: "ch.10", Port: 10010}
	leader := &gameSession{conn: &bufferConn{}, channel: channel, selectedCharacterID: 1001}
	joiner := &gameSession{conn: &bufferConn{}, channel: channel, selectedCharacterID: 1002}
	service.bindGameSessionCharacter(leader, 1001)
	service.bindGameSessionCharacter(joiner, 1002)

	state, result, joined := service.createOrJoinManagedRuntimeParty(joiner, leader)
	if !joined || !result.OK {
		t.Fatalf("same-channel join must succeed: joined=%t result=%+v", joined, result)
	}
	if state.UserID != 1001 || len(runtimePartyMembers(state)) != 2 {
		t.Fatalf("same-channel party projection = %+v, want leader 1001 with two members", state)
	}
	if members := runtimePartyMembers(state); members[0].UserID != 1001 || members[1].UserID != 1002 {
		t.Fatalf("same-channel party order = %+v, want inviter then joiner", members)
	}
}

// 回归①（邀请/接受）：跨频道的 op10 邀请与 op11 接受都必须**明确失败并回失败包**，
// 目标不得收到任何提示包，也不得留下队伍。
func TestCrossChannelPartyInviteAndAcceptAreRejectedWithFailurePacket(t *testing.T) {
	service := &Service{
		options:       options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
		onlinePlayers: newOnlinePlayerManager(),
	}
	inviterConn, inviteeConn := &bufferConn{}, &bufferConn{}
	inviter := &gameSession{
		conn: inviterConn, connID: "chan-inviter",
		channel:             channelcatalog.Channel{ID: 10, Type: 11, Name: "ch.10", Port: 10010},
		selectedCharacterID: 1001,
	}
	invitee := &gameSession{
		conn: inviteeConn, connID: "chan-invitee",
		channel:             channelcatalog.Channel{ID: 121, Type: 22, Name: "ch.121", Port: 10121},
		selectedCharacterID: 1002,
	}
	// 两个角色都在 38/2，但频道不同——正是"10 线 vs 121 线"的现场形状。
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 1001, TownID: 38, AreaID: 2, Session: inviter})
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 1002, TownID: 38, AreaID: 2, Session: invitee})
	service.bindGameSessionCharacter(inviter, 1001)
	service.bindGameSessionCharacter(invitee, 1002)

	// op10 mode 0：u16 target + u8 mode + u32 value0 + u16 value1 + u32 value2。
	handled, err := service.handleAlignedGameCommand(
		inviter,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketRequestPeer),
		[]byte{0xea, 0x03, 0, 0x44, 0x33, 0x22, 0x11, 0x66, 0x55, 0xaa, 0x99, 0x88, 0x77},
	)
	if err != nil {
		t.Fatalf("cross-channel request peer: %v", err)
	}
	if !handled {
		t.Fatal("RequestPeer must stay handled by the online peer bridge")
	}
	packet, rest := splitGameServerUpperPacket(t, inviterConn.write.Bytes())
	if packet.Header.Classification != dnfproto.DefaultChannelClassification ||
		packet.Header.MsgID != uint16(dnfenum.CmdPacketRequestPeer) ||
		!bytes.Equal(packet.Body, []byte{0, partyCommandFailureGeneric}) {
		t.Fatalf("cross-channel invite must answer a failure packet, got class=%d msg=%d body=% X",
			packet.Header.Classification, packet.Header.MsgID, packet.Body)
	}
	if len(rest) != 0 {
		t.Fatalf("cross-channel invite wrote extra packets after the failure: %x", rest)
	}
	if got := inviteeConn.write.Bytes(); len(got) != 0 {
		t.Fatalf("cross-channel invite must not prompt the target, got %x", got)
	}
	if state := runtimePartyStateSnapshot(inviter); state.PartyID != 0 {
		t.Fatalf("cross-channel invite created a party: %+v", state)
	}

	// 接受侧：同样明确失败，不改队伍、也不通知邀请方。
	// 先清空两边写缓冲，这样下面的断言只看"接受这一步新写了什么"。
	inviterConn.write.Reset()
	inviteeConn.write.Reset()
	if err := service.handleOnlineResponsePeer(invitee, uint16(dnfenum.CmdPacketResponsePeer),
		buildResponsePeerBody(1001, 0, 0)); err != nil {
		t.Fatalf("cross-channel response peer: %v", err)
	}
	acceptPacket, acceptRest := splitGameServerUpperPacket(t, inviteeConn.write.Bytes())
	if acceptPacket.Header.Classification != dnfproto.DefaultChannelClassification ||
		acceptPacket.Header.MsgID != uint16(dnfenum.CmdPacketResponsePeer) ||
		!bytes.Equal(acceptPacket.Body, []byte{0, partyCommandFailureGeneric}) {
		t.Fatalf("cross-channel accept must answer a failure packet, got class=%d msg=%d body=% X",
			acceptPacket.Header.Classification, acceptPacket.Header.MsgID, acceptPacket.Body)
	}
	if len(acceptRest) != 0 {
		t.Fatalf("cross-channel accept wrote extra packets after the failure: %x", acceptRest)
	}
	if got := inviterConn.write.Bytes(); len(got) != 0 {
		t.Fatalf("cross-channel accept must not notify the inviter, got %x", got)
	}
	if state := runtimePartyStateSnapshot(invitee); state.PartyID != 0 {
		t.Fatalf("cross-channel accept created a party: %+v", state)
	}
}

// 回归①（直接入队 op705）：跨频道回通用失败码（不是"队伍已满"），双方队伍不变。
func TestCrossChannelEntryIntoPartyIsRejectedWithFailurePacket(t *testing.T) {
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	conn := &bufferConn{}
	session := &gameSession{
		conn: conn, connID: "chan-entry",
		channel:             channelcatalog.Channel{ID: 10, Type: 11, Name: "ch.10", Port: 10010},
		selectedCharacterID: 1001,
	}
	targetConn := &bufferConn{}
	target := &gameSession{
		conn: targetConn, connID: "chan-entry-target",
		channel:             channelcatalog.Channel{ID: 121, Type: 22, Name: "ch.121", Port: 10121},
		selectedCharacterID: 1002,
	}
	service.bindGameSessionCharacter(session, 1001)
	service.bindGameSessionCharacter(target, 1002)

	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, 1002)
	if err := service.handleOnlineEntryIntoParty(session, uint16(dnfenum.CmdPacketEntryIntoParty), body); err != nil {
		t.Fatalf("cross-channel entry into party: %v", err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.Classification != dnfproto.DefaultChannelClassification ||
		packet.Header.MsgID != uint16(dnfenum.CmdPacketEntryIntoParty) ||
		!bytes.Equal(packet.Body, []byte{0, entryIntoPartyFailureGeneric}) {
		t.Fatalf("cross-channel entry must answer the generic failure, got class=%d msg=%d body=% X",
			packet.Header.Classification, packet.Header.MsgID, packet.Body)
	}
	if len(rest) != 0 {
		t.Fatalf("cross-channel entry wrote extra packets: %x", rest)
	}
	if state := runtimePartyStateSnapshot(session); state.PartyID != 0 || len(runtimePartyMembers(state)) != 0 {
		t.Fatalf("cross-channel entry created a party: %+v", state)
	}
	if got := targetConn.write.Bytes(); len(got) != 0 {
		t.Fatalf("cross-channel entry must not touch the target: %x", got)
	}
}

// 回归③（回城队友 actor 投影）：同频道异区域队友照旧被投影（mode0/mode1/op9/op23），
// 跨频道队友**一个包都不发**（否则 op23 会把他声明成本地区域的一员）。
func TestTownRemotePartyActorProjectionSkipsOtherChannels(t *testing.T) {
	service, receiver, repositories := newTownMoveTest(t)
	service.onlinePlayers = newOnlinePlayerManager()
	service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	startTownPeerTest(t, service, receiver, &onlinePlayerInfo{
		CharacterID: 29, TownID: 38, AreaID: 0, PositionX: 450, PositionY: 234,
		Direction: 5, AreaState: 3, Session: receiver,
	})

	peerConn := &bufferConn{}
	peer := &gameSession{
		conn:                      peerConn,
		connID:                    "same-channel-peer",
		channel:                   receiver.channel,
		residentChannel:           receiver.residentChannel,
		selectedCharacterID:       17,
		townSceneReadyCharacterID: 17,
		townActorOwnerChannel:     byte(receiver.channel.ID),
	}
	service.bindGameSessionCharacter(peer, 17)
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "17",
		AccountID:   "dnf:2",
		Name:        "peer",
		Job:         "1",
		Level:       2,
		Stats: map[string]int64{
			"town_id": 38, "area_id": 1, "pos_x": 400, "pos_y": 200,
			"direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	// 同频道（19 线）队友，在另一个区域（38/1）：应当被投影。
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 17, AccountID: "dnf:2", Name: "peer", Job: 1, Level: 2,
		TownID: 38, AreaID: 1, PositionX: 400, PositionY: 200, Direction: 5, AreaState: 3, Session: peer,
	})

	state := alignedcmd.PartyState{
		PartyID: 1, UserID: 29, UserState: 1, IsLeader: true, MaxMembers: 4,
		Members: []alignedcmd.PartyMemberState{{UserID: 29, UserState: 1}, {UserID: 17, UserState: 1}},
	}
	receiver.conn.(*bufferConn).write.Reset()
	if err := service.sendTownRemotePartyActors(receiver, state); err != nil {
		t.Fatalf("same-channel remote party actor projection: %v", err)
	}
	drainTownPeerTest(t, service, receiver)
	if len(receiver.conn.(*bufferConn).write.Bytes()) == 0 {
		t.Fatal("same-channel other-area party member must still be projected into the party frame")
	}

	// 同一个队友换到 10 线：立刻不再被投影（跨频道队友不进城市场景）。
	otherChannel := &gameSession{
		conn:                      &bufferConn{},
		connID:                    "other-channel-peer",
		channel:                   channelcatalog.Channel{ID: 10, Type: 11, Name: "ch.10", Port: 10010},
		residentChannel:           channelcatalog.Channel{ID: 10, Type: 11, Name: "ch.10", Port: 10010},
		selectedCharacterID:       17,
		townSceneReadyCharacterID: 17,
	}
	service.bindGameSessionCharacter(otherChannel, 17)
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 17, AccountID: "dnf:2", Name: "peer", Job: 1, Level: 2,
		TownID: 38, AreaID: 2, PositionX: 1085, PositionY: 249, Direction: 5, AreaState: 3, Session: otherChannel,
	})
	receiver.conn.(*bufferConn).write.Reset()
	if err := service.sendTownRemotePartyActors(receiver, state); err != nil {
		t.Fatalf("cross-channel remote party actor projection: %v", err)
	}
	drainTownPeerTest(t, service, receiver)
	if got := receiver.conn.(*bufferConn).write.Bytes(); len(got) != 0 {
		t.Fatalf("cross-channel party member must not be projected into the local town scene: %x", got)
	}
}

// 回归④之一：op36 SET_USER_AREA 的 op24 名册只列本频道的同区玩家
// （跨频道的人不能被塞进本地客户端的区域名册）。
func TestTownSetUserAreaRosterIsChannelScoped(t *testing.T) {
	service, session, _ := newTownMoveTest(t)
	service.onlinePlayers = newOnlinePlayerManager()
	localChannel := channelcatalog.Channel{ID: 19, Type: 1, Name: "ch.19", Port: 10019}
	// 别的频道（10 线）的同城同区玩家：没有会话也能登记（显式频道键）。
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 17,
		TownID:      38,
		AreaID:      0,
		ChannelID:   10,
		PositionX:   320,
		PositionY:   210,
		Direction:   1,
		AreaState:   4,
	})
	// 本频道（19 线）的同区玩家：也必须进名册。
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 23,
		TownID:      38,
		AreaID:      0,
		ChannelID:   localChannel.ID,
		PositionX:   700,
		PositionY:   260,
		Direction:   5,
		AreaState:   3,
	})

	if err := service.handleTownSetUserArea(session, buildTownMoveRequest(38, 0, 900, 250, 5)); err != nil {
		t.Fatal(err)
	}
	transition, _ := splitTownAreaTransitionSequence(
		t,
		session.conn.(*bufferConn).write.Bytes(),
		29,
		38,
		0,
		900,
		250,
		5,
		3,
	)
	selfKey := currentSceneActorObjectKey(29)
	localKey := currentSceneActorObjectKey(23)
	otherChannelKey := currentSceneActorObjectKey(17)
	if !townRosterContainsActor(transition.Body, localKey) {
		t.Fatalf("op24 roster dropped the same-channel peer %d: %x", localKey, transition.Body)
	}
	if townRosterContainsActor(transition.Body, otherChannelKey) {
		t.Fatalf("op24 roster leaked the channel-10 peer %d into the channel-19 scene: %x", otherChannelKey, transition.Body)
	}
	if !townRosterContainsActor(transition.Body, selfKey) {
		t.Fatalf("op24 roster lost the selected actor: %x", transition.Body)
	}
	// 本人登记后必须落在自己的频道桶里。
	if info, ok := service.onlinePlayers.PlayerForCharacter(29); !ok || info.ChannelID != localChannel.ID {
		t.Fatalf("town presence after op36 = %+v ok=%t, want channel %d", info, ok, localChannel.ID)
	}
}

// townRosterContainsActor 在 op24 body（u8 town, u8 area, u16 count, count×9 字节行）
// 里找一行 actor key。
func townRosterContainsActor(body []byte, actorKey uint16) bool {
	if len(body) < 4 {
		return false
	}
	count := int(body[2]) | int(body[3])<<8
	for i := 0; i < count; i++ {
		offset := 4 + i*9
		if offset+2 > len(body) {
			return false
		}
		if int(body[offset])|int(body[offset+1])<<8 == int(actorKey) {
			return true
		}
	}
	return false
}

// 回归④之二：出图重绑（地下城回城）与登录初始入城都不串桶——登记时用的频道
// 必须是**本会话**的频道，而不是副本期的 0 上下文，也不是别人的频道。
func TestDungeonReturnRebindAndLoginRegistrationKeepSessionChannel(t *testing.T) {
	service, session, repositories := newTownMoveTest(t)
	service.onlinePlayers = newOnlinePlayerManager()
	service.bindGameSessionCharacter(session, session.selectedCharacterID)

	// 模拟进副本：本地 town 上下文被置 0（这与客户端频道 19 不等）。
	session.townActorOwnerChannel = currentSceneObjectContext
	session.connectionTownActorOwnerChannel = 0

	service.rebindTownCopresenceAfterDungeonReturn(session, 38, 0, "regression_test_dungeon_return")
	info, ok := service.onlinePlayers.PlayerForCharacter(29)
	if !ok {
		t.Fatal("dungeon return rebind did not register the town presence")
	}
	if info.ChannelID != 19 {
		t.Fatalf("dungeon return rebind presence channel = %d, want the session channel 19", info.ChannelID)
	}
	if info.TownID != 38 || info.AreaID != 0 {
		t.Fatalf("dungeon return rebind presence = %+v, want town 38 area 0", info)
	}

	// 登录初始入城（私房）走同一条解析：Instance=本人 + ChannelID=本会话频道。
	character, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	registered := service.registerInitialTownPrivateAreaPresence(
		session,
		29,
		character,
		38,
		1,
		"Cataclysm/Town/Elvengard/new_seria_room.map",
		currentSceneTransitionRow{Value1: 450, Value2: 234, Value3: 5, Value4: 3},
		"regression_test_login_initial_town_route",
	)
	if !registered {
		t.Fatal("login into the private Seria room must register presence")
	}
	privateInfo, ok := service.onlinePlayers.PlayerForCharacter(29)
	if !ok || privateInfo.Instance != 29 || privateInfo.ChannelID != 19 {
		t.Fatalf("login private-room presence = %+v ok=%t, want instance 29 on channel 19", privateInfo, ok)
	}
	// 私房在本频道里也不参与共享枚举，跨频道更看不到。
	if players := service.onlinePlayers.GetAreaPlayers(38, 1, 19); len(players) != 0 {
		t.Fatalf("private room leaked into the channel-19 shared enumeration: %+v", players)
	}
	if players := service.onlinePlayers.GetAreaPlayers(38, 0, 10); len(players) != 0 {
		t.Fatalf("channel-10 enumeration saw channel-19 presence: %+v", players)
	}
}
