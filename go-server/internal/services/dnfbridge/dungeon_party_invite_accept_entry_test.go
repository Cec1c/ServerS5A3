package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/party"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 现场缺陷（HK，2026-09-26 07:05）：两名成员**真的组成队**（客户端 real
// invite → op11 accept，服务端日志
//
//	game-party-join-authoritative-snapshot-sent type=11 char_id=1 target_id=4
//	    party_id=1 leader_char_id=4 member_count=2 slot_zero_char_id=4
//
// ），紧接着队长（char 4）在门口选图进副本，**每一条** party-dungeon 事件却都是
// `party_id=0 member_count=0`：
//
//	game-party-dungeon-entry-peer-endpoints-skipped       reason=runtime_party_below_two_members_has_no_remote_rows
//	game-party-dungeon-entry-prepared                     party_id=0 leader_char_id=0 member_count=0
//	game-party-dungeon-entry-actor-scene-snapshot-skipped reason=party_below_two_members_has_no_dungeon_op800_sample
//	game-party-dungeon-start-map-fatigue-skipped          reason=single_player_dungeon_start_map_has_no_fatigue_sample
//	game-party-dungeon-room-slot-table-skipped            reason=party_below_two_members_has_no_room_slot_table_sample
//
// 本文件的用例把"组队"与"选图"接成**同一条链**（现场正是这条链）：
//   ① 用真实邀请原语 `forceInviteParty`（= HK 上 `force_invite.cmd` 走的那一条）
//      登记邀请并把原生邀请提示发给被邀请方；
//   ② 被邀请方发 op11（`handleOnlineResponsePeer`，与客户端点「是」同一个入口）；
//   ③ 队长对自己那条连接发 op16（`handleDungeonSelectUpper`）。
//
// 断言口径只读日志与行为，不看内部指针：
//   - 队长的会话必须看得见队伍（party_id != 0，member_count == 2）；
//   - `…-peer-endpoints-send` / `…-actor-scene-snapshot-send` /
//     `…-start-map-fatigue-send` / `…-room-slot-table-send` 四条必须都出现，
//     且**不得**出现对应的 `-skipped`（现场四条全是 skipped）；
//   - 队员必须被 `game-party-dungeon-entry-member-prepared` 拉进同一次进图。
//
// 另外钉住一条**不得回归**的行为：真正单人的 op16 仍然走单人路径（四条 skip 照旧），
// 不能因为修复把单人进图也拖进队伍屏障。

const (
	inviteAcceptLeaderCharacterID   = uint16(99)
	inviteAcceptFollowerCharacterID = uint16(100)
)

// inviteAcceptDungeonFixture 是"两名已上线角色 + 真档形状合成 PVF"的最小夹具。
type inviteAcceptDungeonFixture struct {
	service  *Service
	channel  channelcatalog.Channel
	leader   *gameSession
	follower *gameSession
}

func newInviteAcceptDungeonFixture(t *testing.T) *inviteAcceptDungeonFixture {
	t.Helper()
	source := bridgeDungeonPVF(false)
	source["dungeon/test.dgn"] = strings.Replace(
		source["dungeon/test.dgn"],
		"[limit party count]\n1\n",
		"[limit party count]\n4\n",
		1,
	)
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	repositories := dnfrepomemory.NewMemoryGroup()
	for _, id := range []string{"99", "100"} {
		if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
			CharacterID: id,
			AccountID:   "account-1",
			Level:       20,
			Stats: map[string]int64{
				"fatigue": 100, "town_id": 38, "area_id": 1,
				"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
			CharacterID: id,
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
	logger, _ := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	channel := channelcatalog.Channel{ServerID: 1, ID: 42, Type: 11, Name: "ch.42", Port: 10042}
	leader := &gameSession{
		conn:            &bufferConn{},
		connID:          "invite-accept-leader",
		channel:         channel,
		residentChannel: channel,
	}
	follower := &gameSession{
		conn:            &bufferConn{},
		connID:          "invite-accept-follower",
		channel:         channel,
		residentChannel: channel,
	}
	service.bindGameSessionCharacter(leader, inviteAcceptLeaderCharacterID)
	service.bindGameSessionCharacter(follower, inviteAcceptFollowerCharacterID)
	// 两人同频道同区域（38/1），这是队长 fan-out 的前置条件。
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: inviteAcceptLeaderCharacterID, TownID: 38, AreaID: 1, Session: leader,
	})
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: inviteAcceptFollowerCharacterID, TownID: 38, AreaID: 1, Session: follower,
	})
	bindDungeonSelectorOriginForTestAt(t, service, leader, 38, 1, 450, 234)
	bindDungeonSelectorOriginForTestAt(t, service, follower, 38, 1, 450, 234)
	return &inviteAcceptDungeonFixture{
		service:  service,
		channel:  channel,
		leader:   leader,
		follower: follower,
	}
}

// formPartyThroughClientAccept 走 HK 现场那条链：服务端代发真实邀请（
// `force_invite.cmd` 的 `4 1 0 0 0 0`）→ 被邀请方客户端发 op11 接受。
func (fixture *inviteAcceptDungeonFixture) formPartyThroughClientAccept(t *testing.T) {
	t.Helper()
	ok, reason, _ := fixture.service.forceInviteParty(
		inviteAcceptLeaderCharacterID,
		inviteAcceptFollowerCharacterID,
		0,
		[3]uint32{},
	)
	if !ok {
		t.Fatalf("server-side real invite was rejected: reason=%s", reason)
	}
	if err := fixture.service.handleOnlineResponsePeer(
		fixture.follower,
		uint16(dnfenum.CmdPacketResponsePeer),
		buildResponsePeerBody(inviteAcceptLeaderCharacterID, 0, 0),
	); err != nil {
		t.Fatalf("op11 accept: %v", err)
	}
}

// selectDungeonAsLeader 让指定会话（现场 = 队长那条连接）发 op16 选图。
func (fixture *inviteAcceptDungeonFixture) selectDungeon(t *testing.T, session *gameSession, dungeonID uint32) {
	t.Helper()
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body[:4], dungeonID)
	if err := fixture.service.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatalf("op16 dungeon select for char %d: %v", session.selectedCharacterID, err)
	}
}

// releaseSharedLoadingBarrier 把全队 op37 都递上去（同 dungeon_entry_packets_test.go）：
// 屏障默认档必须等所有成员到齐才放行落位与尾段。放行失败（例如合成夹具缺
// `Script.pvf` 的职业技能表）只记录，不掩盖本文件真正要看的四条 party-dungeon 断言——
// 那四条都在 op16 调用内部就发完了。
func (fixture *inviteAcceptDungeonFixture) releaseSharedLoadingBarrier(t *testing.T, sessions ...*gameSession) {
	t.Helper()
	restore := partyDungeonLoadingBarrierDispatch
	partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
	defer func() { partyDungeonLoadingBarrierDispatch = restore }()
	for _, session := range sessions {
		if err := fixture.service.sendFinishLoadingStatus(
			session,
			make([]byte, currentFinishLoadingLegacyRequestBodySize),
		); err != nil {
			t.Logf("op37 finish loading for char %d: %v", session.selectedCharacterID, err)
		}
	}
}

// logLineContaining 判断日志里是否存在**同一行**同时包含所有片段（事件名 + 字段值）。
func logLineContaining(log string, parts ...string) bool {
	for _, line := range strings.Split(log, "\n") {
		matched := true
		for _, part := range parts {
			if !strings.Contains(line, part) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// assertPartyDungeonEntryRanAsParty 断言这一条 op16 真的走了组队进图：
// 队伍可见 + 四条厂商同序动作都发了 + 队员被拉进来了。
func assertPartyDungeonEntryRanAsParty(t *testing.T, service *Service, leader *gameSession) {
	t.Helper()
	if got := runtimePartyStateSnapshot(leader); got.PartyID <= 0 || len(runtimePartyMembers(got)) != 2 {
		t.Fatalf("leader session lost the party projection: %+v (log:\n%s)", got, testPacketLogText(t, service))
	}
	log := testPacketLogText(t, service)
	for _, want := range []string{
		"game-party-dungeon-entry-peer-endpoints-send",
		"game-party-dungeon-entry-actor-scene-snapshot-send",
		"game-party-dungeon-start-map-fatigue-send",
		"game-party-dungeon-room-slot-table-send",
		"game-party-dungeon-entry-member-prepared",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("party dungeon entry did not emit %s; log:\n%s", want, log)
		}
	}
	for _, unwanted := range []string{
		"game-party-dungeon-entry-peer-endpoints-skipped",
		"game-party-dungeon-entry-actor-scene-snapshot-skipped",
		"game-party-dungeon-start-map-fatigue-skipped",
		"game-party-dungeon-room-slot-table-skipped",
	} {
		if strings.Contains(log, unwanted) {
			t.Fatalf("party dungeon entry still skipped %s; log:\n%s", unwanted, log)
		}
	}
}

// TestInviteAcceptThenLeaderDungeonSelectKeepsParty 是现场链路的直线复现：
// 同一对会话先组队、再由**同一条**队长连接选图。这条必须一直绿。
func TestInviteAcceptThenLeaderDungeonSelectKeepsParty(t *testing.T) {
	fixture := newInviteAcceptDungeonFixture(t)
	fixture.formPartyThroughClientAccept(t)

	leaderState := runtimePartyStateSnapshot(fixture.leader)
	if leaderState.PartyID <= 0 || len(runtimePartyMembers(leaderState)) != 2 ||
		leaderState.UserID != inviteAcceptLeaderCharacterID {
		t.Fatalf("leader session cannot see the party the accept path just created: %+v", leaderState)
	}

	fixture.selectDungeon(t, fixture.leader, 700)
	fixture.releaseSharedLoadingBarrier(t, fixture.leader, fixture.follower)
	assertPartyDungeonEntryRanAsParty(t, fixture.service, fixture.leader)
}

// TestInviteAcceptThenLeaderDungeonSelectFromReplacementSessionKeepsParty 复现现场
// 的形状：**队长的选图包不再落在"角色目录里那一条"会话上**。
//
// 当前客户端会给同一角色开短命的辅助连接（`online_player_manager.go:173-177`
// 记录的 cross-channel/raid directory probe），`bindGameSessionCharacter` 对这类
// 同角色第二条连接**故意不夺走角色目录**，只把当时那份队伍投影拷给它
// （`TestAuxiliarySameCharacterSessionCannotEvictResidentPartyOrTownPresence`）。
// 于是：组队发生在角色目录那条会话上，选图包却来自第二条会话
// ⇒ 如果选图侧只看会话本地缓存，就会在**队伍明明成立**的情况下退化成单人。
func TestInviteAcceptThenLeaderDungeonSelectFromReplacementSessionKeepsParty(t *testing.T) {
	fixture := newInviteAcceptDungeonFixture(t)
	// 队长的第二条（辅助）连接：绑定同一角色，角色目录仍留在第一条上。
	auxiliary := &gameSession{
		conn:            &bufferConn{},
		connID:          "invite-accept-leader-auxiliary",
		channel:         fixture.channel,
		residentChannel: fixture.channel,
	}
	fixture.service.bindGameSessionCharacter(auxiliary, inviteAcceptLeaderCharacterID)
	if canonical, ok := fixture.service.onlineGameSession(inviteAcceptLeaderCharacterID); !ok || canonical != fixture.leader {
		t.Fatalf("auxiliary bind stole the character directory: got=%p want=%p", canonical, fixture.leader)
	}
	bindDungeonSelectorOriginForTestAt(t, fixture.service, auxiliary, 38, 1, 450, 234)

	fixture.formPartyThroughClientAccept(t)
	if got := runtimePartyStateSnapshot(fixture.leader); got.PartyID <= 0 {
		t.Fatalf("the accept path did not publish the party to the character's directory session: %+v", got)
	}

	// 现场：选图那条 op16 走的是第二条连接。
	fixture.selectDungeon(t, auxiliary, 700)
	fixture.releaseSharedLoadingBarrier(t, auxiliary, fixture.follower)
	// 合成夹具里队员那条 op37 可能在"读职业技能表"那一步失败（缺 Script.pvf），
	// 屏障于是等不到全员到齐。这里用**生产同一条超时放行路径**把它放出去，
	// 断言放行仍然回到辅助那条会话（不是角色目录里的那一条）。若上面两条 op37 已经
	// 正常放行，这次 force release 是 no-op，断言同样成立。
	if fixture.service.partyDungeonPostStartMapPlacementPending(auxiliary) {
		partyState := fixture.service.runtimePartyStateForSession(auxiliary)
		fixture.service.partyDungeonLoadingBarrierForceRelease(
			uint16(partyState.PartyID),
			"test_timeout_waiting_for_all_members_op37",
		)
	}

	log := testPacketLogText(t, fixture.service)
	for _, want := range []string{
		"game-party-dungeon-entry-peer-endpoints-send",
		"game-party-dungeon-entry-actor-scene-snapshot-send",
		"game-party-dungeon-start-map-fatigue-send",
		"game-party-dungeon-room-slot-table-send",
		"game-party-dungeon-entry-member-prepared",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("party dungeon entry from the auxiliary session did not emit %s; log:\n%s", want, log)
		}
	}
	for _, unwanted := range []string{
		"game-party-dungeon-entry-peer-endpoints-skipped",
		"game-party-dungeon-entry-actor-scene-snapshot-skipped",
		"game-party-dungeon-start-map-fatigue-skipped",
		"game-party-dungeon-room-slot-table-skipped",
	} {
		if strings.Contains(log, unwanted) {
			t.Fatalf("party dungeon entry from the auxiliary session still skipped %s; log:\n%s", unwanted, log)
		}
	}
	// 读条屏障也必须回到**真正在跑这一场的那条会话**上放行：压住的落位落在那条
	// 会话上（`…-placement-deferred`），全队 op37 到齐后也必须从它放出去
	// （`…-placement-released`）。否则客户端会卡在加载条。
	if !logLineContaining(log,
		"game-party-dungeon-post-start-map-placement-deferred",
		"conn_id=invite-accept-leader-auxiliary",
	) {
		t.Fatalf("auxiliary session never deferred its post-start-map placement; log:\n%s", log)
	}
	if !logLineContaining(log,
		"game-party-dungeon-post-start-map-placement-released",
		"conn_id=invite-accept-leader-auxiliary",
	) {
		t.Fatalf("shared loading barrier did not release the auxiliary session's placement; log:\n%s", log)
	}
}

// TestSoloDungeonSelectKeepsSinglePlayerPath 是**不得回归**的另一半：真的单人
// （没有任何队伍）选图时，四条厂商动作仍然照旧跳过，单人进图照旧成立。
func TestSoloDungeonSelectKeepsSinglePlayerPath(t *testing.T) {
	fixture := newInviteAcceptDungeonFixture(t)
	fixture.selectDungeon(t, fixture.leader, 700)

	log := testPacketLogText(t, fixture.service)
	for _, want := range []string{
		"game-party-dungeon-entry-peer-endpoints-skipped",
		"game-party-dungeon-entry-actor-scene-snapshot-skipped",
		"game-party-dungeon-start-map-fatigue-send",
		"game-party-dungeon-room-slot-table-skipped",
		"game-upper-current-dungeon-start-op29-send",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("solo dungeon select did not emit %s; log:\n%s", want, log)
		}
	}
	if strings.Contains(log, "game-party-dungeon-entry-member-prepared") {
		t.Fatalf("solo dungeon select prepared a party follower; log:\n%s", log)
	}
}

// TestRuntimePartyStateForSessionPrefersAuthoritativeMembership 钉住新访问器的三条口径：
//   - 中央有该角色的队伍 ⇒ 返回中央的权威名单，并把它写回会话缓存（含
//     `game-party-session-projection-repaired` 事件）；
//   - 中央没有该角色 ⇒ 原样回退会话本地缓存（旧客户端投影 / 夹具路径，逐字节不变）；
//   - 两边都没有 ⇒ 空。
func TestRuntimePartyStateForSessionPrefersAuthoritativeMembership(t *testing.T) {
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	logger, _ := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	bind := func(id uint16) *gameSession {
		session := &gameSession{conn: &bufferConn{}, connID: "projection-" + strconv.Itoa(int(id))}
		service.bindGameSessionCharacter(session, id)
		return session
	}
	leader := bind(1001)
	sessionOnly := bind(1002)
	unrelated := bind(1003)

	manager := service.runtimePartyManagerForService()
	created := manager.Create(party.RuntimePartyMember{
		UserID:            1001,
		SessionGeneration: mustGeneration(t, service, leader),
		State:             alignedcmd.PartyMemberState{UserID: 1001, UserState: 1},
	}, alignedcmd.PartyState{NameBytes: []byte("party"), MaxMembers: 4, UserState: 1})
	if !created.OK {
		t.Fatalf("create runtime party: %+v", created)
	}
	// 同角色第二条连接的形状：会话本地缓存是空的，但角色在中央队伍里。
	if got := service.runtimePartyStateForSession(leader); got.PartyID != int(created.Party.ID) ||
		got.UserID != 1001 || len(runtimePartyMembers(got)) != 1 {
		t.Fatalf("authoritative resolution = %+v, want party %d leader 1001", got, created.Party.ID)
	}
	if got := runtimePartyStateSnapshot(leader); got.PartyID != int(created.Party.ID) {
		t.Fatalf("authoritative resolution did not repair the session projection: %+v", got)
	}
	if log := testPacketLogText(t, service); !strings.Contains(log, "game-party-session-projection-repaired") {
		t.Fatalf("repair was not logged; log:\n%s", log)
	}

	// 中央没有这个角色：会话本地缓存必须原样生效（旧行为不变）。
	storeRuntimePartyState(sessionOnly, alignedcmd.PartyState{
		PartyID: 41,
		UserID:  1002,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 1002, UserState: 1, HPPercent: 100, MPPercent: 100},
			{UserID: 1004, UserState: 1, HPPercent: 100, MPPercent: 100},
		},
	})
	if got := service.runtimePartyStateForSession(sessionOnly); got.PartyID != 41 ||
		len(runtimePartyMembers(got)) != 2 {
		t.Fatalf("session-only projection was not preserved: %+v", got)
	}
	if got := service.runtimePartyStateForSession(unrelated); got.PartyID != 0 {
		t.Fatalf("unrelated session resolved a party: %+v", got)
	}
}

// TestPublishRuntimePartySnapshotLogsStaleMemberSessionGeneration 钉住"权威名单里的会话
// 代际与当前会话不一致"这一档不再静默：过去它是无声 `continue`，现场那条
// `party_id=0` 因此无法从日志分辨。
func TestPublishRuntimePartySnapshotLogsStaleMemberSessionGeneration(t *testing.T) {
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	logger, _ := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	session := &gameSession{conn: &bufferConn{}, connID: "stale-generation"}
	service.bindGameSessionCharacter(session, 1001)

	manager := service.runtimePartyManagerForService()
	created := manager.Create(party.RuntimePartyMember{
		UserID:            1001,
		SessionGeneration: 424242,
		State:             alignedcmd.PartyMemberState{UserID: 1001, UserState: 1},
	}, alignedcmd.PartyState{NameBytes: []byte("party"), MaxMembers: 4, UserState: 1})
	if !created.OK {
		t.Fatalf("create runtime party: %+v", created)
	}
	service.publishRuntimePartySnapshot(created.Party)
	if got := runtimePartyStateSnapshot(session); got.PartyID != 0 {
		t.Fatalf("a stale-generation member must not be projected onto the live session: %+v", got)
	}
	if log := testPacketLogText(t, service); !strings.Contains(
		log,
		"game-party-snapshot-member-session-generation-stale",
	) {
		t.Fatalf("stale member session generation was not logged; log:\n%s", log)
	}
}
