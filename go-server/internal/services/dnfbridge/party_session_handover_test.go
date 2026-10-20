package dnfbridge

import (
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
)

func strconvItoa(id uint16) string { return strconv.Itoa(int(id)) }

// 本文件回答现场（2026-09-26 HK）剩下的那个问题：**为什么队长那条会话在选图时
// `party_id=0`，而中央管理器里明明有过 party_id=1 / member_count=2**。
//
// 三个已证事实把范围收窄到"队伍在 op16 之前被摘掉了"：
//   - 队长的 op16 会话就是角色目录 owner，且代际一致（`session_is_character_directory_owner=true`）；
//   - `game-party-session-projection-repaired` = 0 且 `SnapshotByUser(charID, 0)` 未命中
//     ⇒ 管理器里那一刻**没有**这个角色的成员记录；
//   - 客户端的帧/名册投影确实发给过队长那条会话 ⇒ 之前是有的。
//
// 摘除路径里唯一"看不到 leave 事件"的是 **detach**：`unbindGameSession` →
// `detachRuntimePartySession` 成功路径只发 op9 移除帧
// （`game-current-party-frame-op9-remove-send`），不写任何 `game-party-*` 事件。
// 而当前客户端给同一个角色同时开两条活传输（现场每个角色两条 busy 连接），
// 目录 owner 完全可能是那条**短命的辅助连接**（现场另有两条只有 8 条事件的连接）。
// 辅助连接一关，`unbindGameSession` 就把仍在主连接上玩的角色从队伍里摘掉了。
//
// 修法见 `game_sessions.go` 的 `handoverRuntimePartyToLiveSession`：关闭时若同角色还有
// 活 socket，就把目录与队伍交接过去，而不是 detach。

// newTwoSocketLeaderFixture 造"队长同一个角色两条活 socket"的现场形状：
// 两条会话都已绑定角色 99 并在活会话索引里登记（= 客户端真的开着两条传输），
// 目录 owner 是第一条（`primary`），第二条（`auxiliary`）走早退分支只拿到一份拷贝。
func newTwoSocketLeaderFixture(t *testing.T) (*inviteAcceptDungeonFixture, *gameSession) {
	t.Helper()
	fixture := newInviteAcceptDungeonFixture(t)
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
	// 两条传输都"活着"：这是现场证据（每个角色两条 busy 连接）在夹具里的对应物。
	fixture.service.registerLiveGameSession(fixture.leader)
	fixture.service.registerLiveGameSession(auxiliary)
	fixture.service.registerLiveGameSession(fixture.follower)
	t.Cleanup(func() {
		fixture.service.unregisterLiveGameSession(fixture.leader)
		fixture.service.unregisterLiveGameSession(auxiliary)
		fixture.service.unregisterLiveGameSession(fixture.follower)
	})
	return fixture, auxiliary
}

// TestInviteAcceptResolvesPartyForBothCharactersThroughTheDungeonAccessor 是现场要求的
// 那条最小回归：**真实接受路径**跑完之后，两个角色都必须能用**副本进图那条访问器**
// （`runtimePartyStateForSession`）解析出队伍 —— 队长也要能，而不是只有被邀请方。
//
// 它同时钉住"键位/代际口径没问题"：`SnapshotByUser(charID, 0)` 对两个角色都命中
// （generation 0 = 任意代际，见 runtime_manager.go:84 的 `generation != 0 &&`），
// 所以现场那种"查不到"只能是**状态**（队伍已经被摘掉），不是键位。
func TestInviteAcceptResolvesPartyForBothCharactersThroughTheDungeonAccessor(t *testing.T) {
	fixture, auxiliary := newTwoSocketLeaderFixture(t)
	fixture.formPartyThroughClientAccept(t)

	manager := fixture.service.runtimePartyManagerForService()
	for _, test := range []struct {
		name string
		id   uint16
	}{{"leader", inviteAcceptLeaderCharacterID}, {"follower", inviteAcceptFollowerCharacterID}} {
		snapshot, found := manager.SnapshotByUser(test.id, 0)
		if !found {
			t.Fatalf("%s char %d: SnapshotByUser(charID, 0) missed right after the real accept path", test.name, test.id)
		}
		if snapshot.ID == 0 || snapshot.Leader != inviteAcceptLeaderCharacterID || len(snapshot.Members) != 2 {
			t.Fatalf("%s char %d: authoritative snapshot = id=%d leader=%d members=%d, want leader %d / 2 members",
				test.name, test.id, snapshot.ID, snapshot.Leader, len(snapshot.Members), inviteAcceptLeaderCharacterID)
		}
	}
	// 同一个访问器、三条会话（队长主连接 / 队长辅助连接 / 队员），都必须看到 2 人队伍。
	for _, test := range []struct {
		name    string
		session *gameSession
	}{
		{"leader primary", fixture.leader},
		{"leader auxiliary", auxiliary},
		{"follower", fixture.follower},
	} {
		state := fixture.service.runtimePartyStateForSession(test.session)
		if state.PartyID <= 0 || state.UserID != inviteAcceptLeaderCharacterID ||
			len(runtimePartyMembers(state)) != 2 {
			t.Fatalf("%s: dungeon accessor resolved %+v, want party owned by char %d with 2 members",
				test.name, state, inviteAcceptLeaderCharacterID)
		}
	}
	if log := testPacketLogText(t, fixture.service); strings.Contains(
		log, "game-party-authoritative-membership-missing",
	) {
		t.Fatalf("authoritative lookup missed although the manager holds the party; log:\n%s", log)
	}
}

// TestClosingAuxiliaryDirectorySessionHandsThePartyToTheLiveSocket 复现现场那条链的
// 收尾：队伍在目录 owner（辅助连接）上成立 → 辅助连接关闭 → 角色其实还在主连接上玩。
// 修好之后：目录与队伍**交接**给主连接，管理器和投影都还在，队长随后选图仍是组队进图。
func TestClosingAuxiliaryDirectorySessionHandsThePartyToTheLiveSocket(t *testing.T) {
	fixture, auxiliary := newTwoSocketLeaderFixture(t)
	fixture.formPartyThroughClientAccept(t)
	if got := fixture.service.runtimePartyStateForSession(fixture.leader); got.PartyID <= 0 {
		t.Fatalf("party was not established on the directory session: %+v", got)
	}

	// 辅助连接（= 当时的目录 owner）关闭；main 连接仍然活着。
	fixture.service.unbindGameSession(fixture.leader)

	log := testPacketLogText(t, fixture.service)
	if !logLineContaining(log,
		"game-party-session-handover",
		"rebind=rebound_to_replacement_session",
		"member_count=2",
	) {
		t.Fatalf("closing the directory socket did not hand the party over; log:\n%s", log)
	}
	if strings.Contains(log, "game-party-membership-removed") {
		t.Fatalf("still-online character was removed from the party; log:\n%s", log)
	}
	if canonical, ok := fixture.service.onlineGameSession(inviteAcceptLeaderCharacterID); !ok || canonical != auxiliary {
		t.Fatalf("character directory was not handed to the live socket: got=%p want=%p", canonical, auxiliary)
	}
	if snapshot, found := fixture.service.runtimePartyManagerForService().SnapshotByUser(
		inviteAcceptLeaderCharacterID, 0,
	); !found || len(snapshot.Members) != 2 {
		t.Fatalf("party lost its leader after the auxiliary socket closed: found=%t %+v", found, snapshot)
	}
	if got := fixture.service.runtimePartyStateForSession(auxiliary); got.PartyID <= 0 ||
		len(runtimePartyMembers(got)) != 2 {
		t.Fatalf("live socket cannot resolve the party after the handover: %+v", got)
	}
	// 替代会话必须收到权威名册（op153 + op9），且**不能**重放 op11（会把 P2P 打回 connecting）。
	if !logLineContaining(log,
		"game-current-party-frame-op9-send",
		"conn_id=invite-accept-leader-auxiliary",
	) {
		t.Fatalf("handover did not redraw the roster for the live socket; log:\n%s", log)
	}

	// 收尾：活着的这条 socket 选图，必须仍然走**组队**进图。
	fixture.selectDungeon(t, auxiliary, 700)
	fixture.releaseSharedLoadingBarrier(t, auxiliary, fixture.follower)
	assertPartyDungeonEntryRanAsParty(t, fixture.service, auxiliary)
}

// TestLegacyDetachWithoutLiveSocketStillRemovesTheCharacter 是上一条的对照：**没有**第二条
// 活 socket 时（= 改动前的世界，也是真的下线），detach 必须照旧把角色摘出队伍。
// 这条同时钉住审计事件 `game-party-membership-removed` 的 `source` 口径。
func TestLegacyDetachWithoutLiveSocketStillRemovesTheCharacter(t *testing.T) {
	fixture := newInviteAcceptDungeonFixture(t)
	fixture.formPartyThroughClientAccept(t)
	if got := fixture.service.runtimePartyStateForSession(fixture.leader); got.PartyID <= 0 {
		t.Fatalf("party was not established: %+v", got)
	}
	// 不登记活会话索引 ⇒ 这个角色只有一条会话，关闭即下线。
	fixture.service.detachRuntimePartySession(fixture.leader, "test_last_socket_closed")

	if snapshot, found := fixture.service.runtimePartyManagerForService().SnapshotByUser(
		inviteAcceptLeaderCharacterID, 0,
	); found && len(snapshot.Members) > 0 {
		t.Fatalf("detach kept the character in a party: %+v", snapshot)
	}
	log := testPacketLogText(t, fixture.service)
	if !logLineContaining(log,
		"game-party-membership-removed",
		"source=detach:test_last_socket_closed",
		"char_id=99",
	) {
		t.Fatalf("membership removal was not audited with its source; log:\n%s", log)
	}
}

// TestAuthoritativeMembershipMissLogsManagerContents 钉住"查不到时到底管理器里有什么"的
// 诊断：只有管理器里**确实存在队伍**时才记（避免单人服务器刷日志），并且字段足以区分
// "状态问题（队伍已被摘掉）"与"键位问题（成员行在管理器里但查不到）"。
func TestAuthoritativeMembershipMissLogsManagerContents(t *testing.T) {
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	logger, _ := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	bind := func(id uint16) *gameSession {
		session := &gameSession{conn: &bufferConn{}, connID: "miss-probe-" + strconvItoa(id)}
		service.bindGameSessionCharacter(session, id)
		return session
	}
	leader := bind(1001)
	outsider := bind(1002)
	created := service.runtimePartyManagerForService().Create(party.RuntimePartyMember{
		UserID:            1001,
		SessionGeneration: mustGeneration(t, service, leader),
		State:             alignedcmd.PartyMemberState{UserID: 1001, UserState: 1},
	}, alignedcmd.PartyState{NameBytes: []byte("party"), MaxMembers: 4, UserState: 1})
	if !created.OK {
		t.Fatalf("create runtime party: %+v", created)
	}

	if got := service.runtimePartyStateForSession(outsider); got.PartyID != 0 {
		t.Fatalf("outsider resolved a party: %+v", got)
	}
	log := testPacketLogText(t, service)
	if !logLineContaining(log,
		"game-party-authoritative-membership-missing",
		"char_id=1002",
		"manager_party_count=1",
		"character_member_row_in_manager=false",
	) {
		t.Fatalf("miss diagnostic did not report the manager contents; log:\n%s", log)
	}
	if !strings.Contains(log, "manager_parties=1:leader=1001:members=1001") {
		t.Fatalf("miss diagnostic did not dump the manager's parties; log:\n%s", log)
	}
}

// TestAuthoritativeMembershipMissStaysSilentWithoutAnyParty 是上一条的降噪对照：
// 服务器上一支队伍都没有时（= 普通单人玩家选图），不能记这条诊断。
func TestAuthoritativeMembershipMissStaysSilentWithoutAnyParty(t *testing.T) {
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	logger, _ := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	session := &gameSession{conn: &bufferConn{}, connID: "solo-probe"}
	service.bindGameSessionCharacter(session, 1001)

	if got := service.runtimePartyStateForSession(session); got.PartyID != 0 {
		t.Fatalf("solo session resolved a party: %+v", got)
	}
	if log := testPacketLogText(t, service); strings.Contains(
		log, "game-party-authoritative-membership-missing",
	) {
		t.Fatalf("solo select logged the membership miss diagnostic; log:\n%s", log)
	}
}
