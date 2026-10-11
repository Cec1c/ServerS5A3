package dnfbridge

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件钉住的是操作者报的「组不上队」根因：
//
// 参考实现（D:\DNF90-test\server\runtime\logs\packet_log.txt，原文行号）在**频道重连进城**
// 这条路由的收尾处发布城镇存在感，三次出现（:2760 char1 / :3146 char2 / :16452 char1）：
//
//	:2574 game-channel-reconnect-town-entry-send ...
//	:2760 game-town-presence-published source=channel_reconnect_town_actor_route_complete
//	      char_id=1 town_id=38 area_id=0 instance_key=0 position_x=1677 position_y=222
//	      direction=5 area_state=3 peer_count=0
//	:2771 game-channel-reconnect-town-entry-finished ...
//
// 而我们这条路由此前一次都不登记（`EnterArea` 的四个生产调用点里没有它）⇒ 走频道重连
// 进城的角色在 `OnlinePlayerManager` 里不存在 ⇒ `PeerInSameArea` 恒 false ⇒
// `party_online.go:128-136` 静默吞掉邀请（客户端"点了没反应"）。
//
// 用例口径：**只读日志 + 只读在线区域表 + 只读会话出站字节**，不看内部指针。

const (
	channelReconnectPresenceFirstCharacterID  = uint16(29)
	channelReconnectPresenceSecondCharacterID = uint16(30)
	channelReconnectPresenceAbsentCharacterID = uint16(31)
	channelReconnectPresenceChannelID         = 253
)

// channelReconnectPresenceFixture 是"两名角色都在频道 253、都从持久化落点进城"的最小夹具，
// 两人都通过**真实的 op4 客户端选择包**走 `sendCurrentChannelReconnectTownEntry`。
type channelReconnectPresenceFixture struct {
	service      *Service
	repositories dnfrepo.Group
	channel      channelcatalog.Channel
	first        *gameSession
	second       *gameSession
}

func newChannelReconnectPresenceFixture(t *testing.T) *channelReconnectPresenceFixture {
	t.Helper()
	service, first, repositories := newChannelReconnectTest(t)
	service.onlinePlayers = newOnlinePlayerManager()
	service.options.gameUpperClientBodyCodec = gameUpperClientBodyCodecPlain
	logger, _ := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })

	channel := channelcatalog.Channel{
		ServerID:   1,
		ID:         channelReconnectPresenceChannelID,
		Type:       1,
		Name:       "ch.253",
		NoticeName: "ch.253",
		Port:       10253,
	}
	// 落点 = 城镇 38 / 区域 0（`new_Elvengard.map`，共享区域；赛丽亚房间是 38/1）。
	const (
		townID    = 38
		areaID    = 0
		positionX = 1677
		positionY = 222
		direction = 5
		areaState = 3
	)
	for _, characterID := range []string{"29", "30"} {
		record, found, err := repositories.Character.Load(context.Background(), characterID)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			record = dnfrepo.CharacterRecord{
				CharacterID: characterID,
				AccountID:   defaultAccountPrefix + "1",
				Stats:       make(map[string]int64),
			}
		}
		record.AccountID = defaultAccountPrefix + "1"
		record.Name = "reconnect-presence-" + characterID
		record.Job = "0"
		record.Level = 90
		record.Stats["town_id"] = townID
		record.Stats["area_id"] = areaID
		record.Stats["pos_x"] = positionX
		record.Stats["pos_y"] = positionY
		record.Stats["direction"] = direction
		record.Stats["area_state"] = areaState
		record.Stats[currentDungeonTutorialCompletedKey] = currentDungeonTutorialCompleteFlag
		if err := repositories.Character.Save(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{
			CharacterID: characterID,
			Entries: map[string]dnfrepo.EquipmentEntry{
				"11": {
					SlotIndex: 11,
					ItemID:    900001,
					RawEntry:  buildInitialEquipmentRawEntry(11, 900001, 27),
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	configureReconnectPresenceSession(first, channel)
	first.conn = &bufferConn{}
	second := &gameSession{
		conn:                &bufferConn{},
		connID:              "channel-reconnect-presence-second",
		selectedCharacterID: 0,
	}
	configureReconnectPresenceSession(second, channel)
	// 这里**不预绑角色**：真实客户端的 op4 是"先武装重连路由、再由选角流程绑定角色"
	// （game_select_state.go:21 / game_select_scene_entry.go:29）。预绑会把
	// selectedCharacterID 提前写成非 0，`markCurrentChannelReconnectOnSelection` 就不再
	// 武装重连路由，测试会悄悄退化成普通登录路径（下面 runChannelReconnectTownEntry
	// 的第一条断言正是拦这个的）。
	return &channelReconnectPresenceFixture{
		service:      service,
		repositories: repositories,
		channel:      channel,
		first:        first,
		second:       second,
	}
}

// configureReconnectPresenceSession 把一条会话摆成"频道重连 op4 会落在这条连接上"的形状：
// 角色目录已提交的连接期频道、尚未选角、场景未就绪 ⇒ `markCurrentChannelReconnectOnSelection`
// 会在这条 op4 上武装重连路由（与 channel_reconnect_test.go 的既有夹具同一口径）。
func configureReconnectPresenceSession(session *gameSession, channel channelcatalog.Channel) {
	session.channel = channel
	session.residentChannel = channel
	session.connectionTownActorOwnerChannel = byte(channel.ID)
	session.townActorOwnerChannel = byte(channel.ID)
	session.currentChannelResidentNoticeSent = true
	session.gameEndpointSuccessSent = true
	session.selectedCharacterID = 0
	session.townSceneReadyCharacterID = 0
	session.townPositionSnapshot = currentTownPositionSnapshot{}
	session.initialTownRouteCharacterID = 0
	session.initialTownRouteStage = currentInitialTownRouteIdle
	session.rosterRequested = false
	session.channelReconnect = false
	session.sceneBootstrapTailDeferred = false
	session.sceneBootstrapTailSent = false
}

// runChannelReconnectTownEntry 用**真实的客户端 op4 选择包**驱动一次频道重连进城路由，
// 并断言这条路真的跑完了（否则后面的存在感断言会因为"路由没跑"而假绿）。
func runChannelReconnectTownEntry(
	t *testing.T,
	fixture *channelReconnectPresenceFixture,
	session *gameSession,
	characterID uint16,
) {
	t.Helper()
	request := make([]byte, 2)
	binary.LittleEndian.PutUint16(request, characterID)
	frame, err := dnfproto.BuildChannelPacket(
		uint16(dnfenum.UpperMsgSelectCharacter),
		request,
		2,
		dnfproto.DefaultChannelClassification,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.handleGameUpper(session, frame); err != nil {
		t.Fatalf("handle channel reconnect op4 for char %d: %v", characterID, err)
	}
	if session.channelReconnect || !session.sceneBootstrapTailSent {
		t.Fatalf(
			"channel reconnect route did not finish for char %d: route=%t tail_sent=%t",
			characterID,
			session.channelReconnect,
			session.sceneBootstrapTailSent,
		)
	}
	if session.selectedCharacterID != characterID {
		t.Fatalf(
			"channel reconnect route did not select char %d (selected=%d)",
			characterID,
			session.selectedCharacterID,
		)
	}
}

// logLineIndexContaining 返回**第一条**同时包含全部片段的行在日志里的字节偏移，找不到返回 -1。
func logLineIndexContaining(log string, parts ...string) int {
	offset := 0
	for _, line := range strings.Split(log, "\n") {
		matched := true
		for _, part := range parts {
			if !strings.Contains(line, part) {
				matched = false
				break
			}
		}
		if matched {
			return offset
		}
		offset += len(line) + 1
	}
	return -1
}

// TestChannelReconnectTownEntryPublishesTownPresence 是本修复的正面钉子：
// 走完频道重连进城路由之后
//  1. 服务端在线区域表里**真的有**这个角色，且分桶键与 op36 那条路（town_move.go:372）一致
//     （同城 + 同区 + 同频道 + 共享区域实例 0）；
//  2. 日志里出现参考实现同名的 `game-town-presence-published`
//     且 `source=channel_reconnect_town_actor_route_complete`；
//  3. 这条事件的**位置**与参考实现一致：entry-send → presence-published → entry-finished；
//  4. 与"op36 进城"的另一名角色落进同一个桶 ⇒ 双向 `PeerInSameArea` 为真。
func TestChannelReconnectTownEntryPublishesTownPresence(t *testing.T) {
	fixture := newChannelReconnectPresenceFixture(t)
	runChannelReconnectTownEntry(t, fixture, fixture.first, channelReconnectPresenceFirstCharacterID)

	info, found := fixture.service.onlinePlayers.PlayerForCharacter(channelReconnectPresenceFirstCharacterID)
	if !found {
		t.Fatalf(
			"channel reconnect town entry left char %d with no published town presence",
			channelReconnectPresenceFirstCharacterID,
		)
	}
	if info.Session != fixture.first {
		t.Fatalf("published presence belongs to another session: got=%p want=%p", info.Session, fixture.first)
	}
	if info.TownID != 38 || info.AreaID != 0 || info.ChannelID != channelReconnectPresenceChannelID || info.Instance != 0 {
		t.Fatalf(
			"channel reconnect presence key town=%d area=%d channel=%d instance=%d, want 38/0/%d/0",
			info.TownID,
			info.AreaID,
			info.ChannelID,
			info.Instance,
			channelReconnectPresenceChannelID,
		)
	}
	if info.PositionX != 1677 || info.PositionY != 222 || info.Direction != 5 || info.AreaState != 3 {
		t.Fatalf(
			"channel reconnect presence position=%d/%d dir=%d state=%d, want the persisted landing 1677/222/5/3",
			info.PositionX,
			info.PositionY,
			info.Direction,
			info.AreaState,
		)
	}

	log := testPacketLogText(t, fixture.service)
	published := logLineIndexContaining(log,
		"game-town-presence-published",
		"source="+channelReconnectTownPresenceSource,
		"char_id=29",
		"town_id=38",
		"area_id=0",
		"area_key_channel=253",
		"area_instance=0",
		"map_path=new_Elvengard.map",
	)
	if published < 0 {
		t.Fatalf("channel reconnect town entry did not publish town presence; log:\n%s", log)
	}
	entrySend := logLineIndexContaining(log, "game-channel-reconnect-town-entry-send", "char_id=29")
	entryFinished := logLineIndexContaining(log, "game-channel-reconnect-town-entry-finished", "char_id=29")
	if entrySend < 0 || entryFinished < 0 {
		t.Fatalf("channel reconnect route events missing send=%d finished=%d; log:\n%s", entrySend, entryFinished, log)
	}
	if !(entrySend < published && published < entryFinished) {
		t.Fatalf(
			"presence publish must sit between entry-send and entry-finished: send=%d published=%d finished=%d",
			entrySend,
			published,
			entryFinished,
		)
	}

	// 另一名角色走 op36 那条路（town_move.go:331-372 的同一份 playerInfo）进城：
	// 两条路必须落进**同一个桶**，否则"重连进城的人看得见 op36 进城的人"这条口径不成立。
	otherInfo := &onlinePlayerInfo{
		CharacterID: channelReconnectPresenceSecondCharacterID,
		AccountID:   defaultAccountPrefix + "1",
		Name:        "op36-arrival",
		TownID:      38,
		AreaID:      0,
		ChannelID:   currentTownPresenceChannelID(fixture.second),
		Instance:    0,
		PositionX:   1085,
		PositionY:   249,
		Direction:   5,
		AreaState:   3,
		Session:     fixture.second,
	}
	fixture.service.onlinePlayers.EnterArea(otherInfo)
	if !fixture.service.onlinePlayers.PeerInSameArea(
		channelReconnectPresenceSecondCharacterID,
		channelReconnectPresenceFirstCharacterID,
	) || !fixture.service.onlinePlayers.PeerInSameArea(
		channelReconnectPresenceFirstCharacterID,
		channelReconnectPresenceSecondCharacterID,
	) {
		t.Fatalf(
			"reconnect arrival and op36 arrival landed in different buckets: reconnect=%+v op36=%+v",
			info,
			*otherInfo,
		)
	}
}

// TestChannelReconnectTownArrivalsCanInviteEachOther 是根因的**反面证明**：
// 两名角色都只走过频道重连进城这条路，之后 op10 mode0 的真实邀请必须被接受并转发给被邀请方。
// 修复前这条会命中 `game-peer-request-target-outside-area` 且一个字节都不发。
func TestChannelReconnectTownArrivalsCanInviteEachOther(t *testing.T) {
	fixture := newChannelReconnectPresenceFixture(t)
	runChannelReconnectTownEntry(t, fixture, fixture.first, channelReconnectPresenceFirstCharacterID)
	runChannelReconnectTownEntry(t, fixture, fixture.second, channelReconnectPresenceSecondCharacterID)

	if !fixture.service.onlinePlayers.PeerInSameArea(
		channelReconnectPresenceFirstCharacterID,
		channelReconnectPresenceSecondCharacterID,
	) {
		t.Fatalf(
			"two channel reconnect town arrivals are not peers after the route; log:\n%s",
			testPacketLogText(t, fixture.service),
		)
	}

	fixture.second.conn.(*bufferConn).write.Reset()
	if err := fixture.service.handleOnlineRequestPeer(
		fixture.first,
		uint16(dnfenum.CmdPacketRequestPeer),
		requestPeerPartyInviteBody(channelReconnectPresenceSecondCharacterID),
	); err != nil {
		t.Fatalf("op10 party invite after channel reconnect town entry: %v", err)
	}

	log := testPacketLogText(t, fixture.service)
	if !logLineContaining(log,
		"game-peer-request-accepted",
		"target_id=30",
	) {
		t.Fatalf("invite gate refused two reconnect town arrivals; log:\n%s", log)
	}
	if !logLineContaining(log,
		"game-peer-request-notice-forward",
		"target_id=30",
	) {
		t.Fatalf("invite was accepted but never forwarded to the invitee; log:\n%s", log)
	}
	if logLineContaining(log, "game-peer-request-target-outside-area", "target_id=30") {
		t.Fatalf("invite still logged as outside-area; log:\n%s", log)
	}
	if fixture.second.conn.(*bufferConn).write.Len() == 0 {
		t.Fatalf("invitee received zero bytes: the invite was swallowed; log:\n%s", log)
	}
}

// TestInviteToCharacterWithoutPublishedTownPresenceIsStillRefused 是**不得回归**的另一半：
// 闸门必须继续拒真的没有城镇存在感的角色（在线、同频道，但从没走过任何进城登记）。
// 这正是修复前两名重连玩家互相看到的形状——不能被这次修复顺手放宽。
func TestInviteToCharacterWithoutPublishedTownPresenceIsStillRefused(t *testing.T) {
	fixture := newChannelReconnectPresenceFixture(t)
	runChannelReconnectTownEntry(t, fixture, fixture.first, channelReconnectPresenceFirstCharacterID)

	absent := &gameSession{
		conn:                &bufferConn{},
		connID:              "channel-reconnect-presence-absent",
		selectedCharacterID: channelReconnectPresenceAbsentCharacterID,
	}
	configureReconnectPresenceSession(absent, fixture.channel)
	absent.selectedCharacterID = channelReconnectPresenceAbsentCharacterID
	fixture.service.bindGameSessionCharacter(absent, channelReconnectPresenceAbsentCharacterID)

	if _, found := fixture.service.onlinePlayers.PlayerForCharacter(channelReconnectPresenceAbsentCharacterID); found {
		t.Fatalf("fixture registered char %d by accident", channelReconnectPresenceAbsentCharacterID)
	}
	if err := fixture.service.handleOnlineRequestPeer(
		fixture.first,
		uint16(dnfenum.CmdPacketRequestPeer),
		requestPeerPartyInviteBody(channelReconnectPresenceAbsentCharacterID),
	); err != nil {
		t.Fatalf("op10 party invite to a character without town presence: %v", err)
	}

	log := testPacketLogText(t, fixture.service)
	if !logLineContaining(log,
		"game-peer-request-target-outside-area",
		"target_id=31",
	) {
		t.Fatalf("invite to a character with no published town presence was not refused; log:\n%s", log)
	}
	if logLineContaining(log, "game-peer-request-accepted", "target_id=31") ||
		logLineContaining(log, "game-peer-request-notice-forward", "target_id=31") {
		t.Fatalf("invite to a character with no published town presence was forwarded; log:\n%s", log)
	}
	if absent.conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf(
			"refused invite still wrote %d byte(s) to the target",
			absent.conn.(*bufferConn).write.Len(),
		)
	}
}

// TestChannelReconnectTownPresencePublishRespectsRollbackSwitch 钉住回退开关：
// `DNFBRIDGE_CHANNEL_RECONNECT_TOWN_PRESENCE_PUBLISH=off` 时一次都不登记、一条都不记
// （操作者可逐字节回到改动前）。
func TestChannelReconnectTownPresencePublishRespectsRollbackSwitch(t *testing.T) {
	t.Setenv(currentChannelReconnectTownPresenceEnv, "off")
	fixture := newChannelReconnectPresenceFixture(t)
	runChannelReconnectTownEntry(t, fixture, fixture.first, channelReconnectPresenceFirstCharacterID)

	if _, found := fixture.service.onlinePlayers.PlayerForCharacter(channelReconnectPresenceFirstCharacterID); found {
		t.Fatal("rollback switch did not stop the presence publish")
	}
	log := testPacketLogText(t, fixture.service)
	if strings.Contains(log, "game-town-presence-published") {
		t.Fatalf("rollback switch still logged a presence publish; log:\n%s", log)
	}
}

// TestDungeonEntryRemovesTownPresenceSoPeerGateIsFalseInsideActiveRun 把"活跃副本里的
// `PeerInSameArea` 为什么必然是 false"这件事钉成可执行结论（本次**只报告、不修改**闸门口径）。
//
// 结论（证据链）：
//   - 生产侧 `EnterArea` 只用城镇坐标，**从不登记副本坐标**：town_move.go:372（op36 城镇过图）、
//     initial_town_private_area.go:62（登录私房）、online_player_manager.go:636（出副本回城重绑）、
//     town_copresence_actor_resend.go:340（赛丽亚出房门 / 频道重连收尾）。所以
//     "副本区域键"这个东西在在线区域表里根本不存在。
//   - 进副本这一步是**把存在摘掉**，不是换键：dungeon_navigation_runtime.go:640 →
//     leaveTownPresenceForDungeon(:911) → LeaveAreaSession(:917)。
//   - 于是"两个都在副本里"的合法队友，`PeerInSameArea` 也必然为 false——不是键不匹配，
//     而是两个人都不在表里。
//
// 这**不是**现场 `game-party-dungeon-entry-prepared party_id=0 member_count=0` 的成因：
// 那条日志的 party_id/member_count 直接来自 `runtimePartyStateForSession`
// （party_dungeon_entry_peer_endpoints.go:123-133、:195-205），与存在感无关；同一条链上
// 闸门失败会走另一条可判读路径 `game-party-dungeon-entry-blocked
// reason=party_entry_preflight_failed_before_any_member_commit`（dungeon_navigation_runtime.go:708
// → :304）。而 :708 只在**城镇选择器**这条路上被求值（进副本提交在其后，见 :223-227
// 的活跃运行时重复 ACK 短路），所以副本内的 false 目前没有生产调用者。
func TestDungeonEntryRemovesTownPresenceSoPeerGateIsFalseInsideActiveRun(t *testing.T) {
	service := &Service{
		options:       options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
		onlinePlayers: newOnlinePlayerManager(),
	}
	channel := channelcatalog.Channel{ServerID: 1, ID: channelReconnectPresenceChannelID, Type: 1, Port: 10253}
	leader := &gameSession{
		conn:                &bufferConn{},
		connID:              "dungeon-presence-leader",
		channel:             channel,
		residentChannel:     channel,
		selectedCharacterID: 99,
	}
	follower := &gameSession{
		conn:                &bufferConn{},
		connID:              "dungeon-presence-follower",
		channel:             channel,
		residentChannel:     channel,
		selectedCharacterID: 100,
	}
	for _, session := range []*gameSession{leader, follower} {
		service.onlinePlayers.EnterArea(&onlinePlayerInfo{
			CharacterID: session.selectedCharacterID,
			TownID:      38,
			AreaID:      2,
			Session:     session,
		})
	}
	if !service.onlinePlayers.PeerInSameArea(99, 100) {
		t.Fatal("fixture did not co-register both members in the town dungeon-gate area")
	}

	// 两名合法队友一起进副本：这一步把他们的城镇存在摘掉（dungeon_navigation_runtime.go:640）。
	service.leaveTownPresenceForDungeon(leader)
	service.leaveTownPresenceForDungeon(follower)

	if service.onlinePlayers.PeerInSameArea(99, 100) || service.onlinePlayers.PeerInSameArea(100, 99) {
		t.Fatal("an active dungeon run still reports the two members as town co-present")
	}
	for _, characterID := range []uint16{99, 100} {
		if info, found := service.onlinePlayers.PlayerForCharacter(characterID); found {
			t.Fatalf(
				"char %d kept an area entry (%+v) while inside the dungeon; dungeon areas must never enter the town presence table",
				characterID,
				info,
			)
		}
	}
}
