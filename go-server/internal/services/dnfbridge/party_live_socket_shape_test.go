package dnfbridge

import (
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
)

// 本文件回答现场（2026-09-26 HK，`conn_id=game-000016`）的那个问题：
// **为什么"队伍确实成立"却让队长选图时 `party_id=0`**。
//
// # 现场形状（packet_log 原文）
//
//	game-party-join-authoritative-snapshot-sent conn_id=game-000005 type=11 char_id=1 target_id=4
//	      party_id=1 leader_char_id=4 member_count=2 slot_zero_char_id=4
//	game-dungeon-select-party-context conn_id=game-000016 channel_id=253 char_id=4
//	      party_id=0 party_leader_id=0 party_member_count=0 is_leader=false
//	      session_generation=5 session_is_character_directory_owner=true
//	      directory_owner_conn_id=game-000016 directory_owner_generation=5
//
// 两个客户端却有 **四条都在忙的连接**（game-000016 490 / game-000005 404 /
// game-000008 392 / game-000013 363 事件）⇒ 每个角色同时握着两条活 socket。
//
// # 本文件钉住的三件事
//
//  1. `TestLiveSocketShapePartySurvivesEverySingleSocketClose`：在**四条活 socket**
//     这个现场形状下，**任何一条** socket 单独关闭都不得让权威队伍消失，队长随后从
//     "当时的角色目录属主会话"发 op16 仍必须走组队进图（四条 `-send`、队员被拉起）。
//     这是现场缺陷的回归位：现场那次正是"目录属主 socket 关闭，把还在另一条 socket 上
//     玩的角色从队伍里摘掉"，而当时的二进制（`41b26859…`，构建 07:22:07）里
//     `handoverRuntimePartyToLiveSession` 还不存在（`game_sessions.go` mtime 07:40:12
//     **晚于**构建时刻）⇒ 修复前这条会失败，修复后恒绿。
//
//  2. `TestDirectorySocketCloseWithoutLiveSiblingReproducesTheLiveFailure`：把
//     `Service.liveGameSessions` 索引**留空**（= 改动前的判据世界：关闭时无从知道
//     这个角色还有别的活 socket），同一条链就会**逐字复现现场**：队伍被摘掉、
//     `game-party-membership-removed source=detach:…`、随后队长选图 `party_id=0`
//     member_count=0 且四条厂商动作全部 `-skipped`。它是"现场那条机制"的可执行证明，
//     同时证明修复的**前置条件就是那个活会话索引**。
//
//  3. `TestRuntimePartyStateForSessionStaysCharacterKeyedAcrossSocketReplacement`：
//     把"为什么键法/代际不是原因"钉死 —— 记录里的 `SessionGeneration` 是**入队时那条
//     socket** 的，而进图访问器按**角色**查（代际 0 = 通配），所以换 socket 之后
//     （队长第二条连接成为目录属主）查询仍然命中，并且会就地把投影补回该会话
//     （`game-party-session-projection-repaired`）。
//     真被移除时（第 2 条那种世界）查询才落空 —— 那一档现在必定留一条
//     `game-party-authoritative-membership-missing`，单行就能判读"管理器里当时有什么"。
//     注：现场那份日志里这条诊断**不可能出现**（它随 `party_manager_runtime.go`
//     mtime 07:38:43 才落地，晚于产生该日志的 07:22:07 构建），所以"计数 = 0"
//     对那次事故**不构成"没被移除"的证据**。

// liveSocketShape 是现场"两个角色 × 每个角色两条活 socket"的夹具。
type liveSocketShape struct {
	fixture *inviteAcceptDungeonFixture
	// leaderDir / leaderAux 都绑定队长角色；只有 Dir 是角色目录属主。
	leaderDir   *gameSession
	leaderAux   *gameSession
	followerDir *gameSession
	followerAux *gameSession
}

func (shape *liveSocketShape) sockets() []*gameSession {
	return []*gameSession{shape.leaderDir, shape.leaderAux, shape.followerDir, shape.followerAux}
}

// newLiveSocketShape 造出四条 socket，并（可选）把它们登记进活会话索引。
// `registerLive` = true 就是**修复后**的生产判据（`game.go:294` 对每条连接都登记）；
// false = 改动前的判据世界（关闭时不知道同角色还有别的活 socket）。
func newLiveSocketShape(t *testing.T, registerLive bool) *liveSocketShape {
	t.Helper()
	fixture := newInviteAcceptDungeonFixture(t)
	shape := &liveSocketShape{
		fixture:     fixture,
		leaderDir:   fixture.leader,
		followerDir: fixture.follower,
		leaderAux: &gameSession{
			conn:            &bufferConn{},
			connID:          "live-shape-leader-aux",
			channel:         fixture.channel,
			residentChannel: fixture.channel,
		},
		followerAux: &gameSession{
			conn:            &bufferConn{},
			connID:          "live-shape-follower-aux",
			channel:         fixture.channel,
			residentChannel: fixture.channel,
		},
	}
	if registerLive {
		for _, session := range shape.sockets() {
			fixture.service.registerLiveGameSession(session)
		}
		t.Cleanup(func() {
			for _, session := range shape.sockets() {
				fixture.service.unregisterLiveGameSession(session)
			}
		})
	}
	// 现场绑定顺序：先来的一条成为角色目录属主，后来的走
	// `bindGameSessionCharacter` 的早退分支（只拿一份当时的投影拷贝）。
	for _, session := range shape.sockets() {
		characterID := inviteAcceptLeaderCharacterID
		if session == shape.followerDir || session == shape.followerAux {
			characterID = inviteAcceptFollowerCharacterID
		}
		fixture.service.bindGameSessionCharacter(session, characterID)
		bindDungeonSelectorOriginForTestAt(t, fixture.service, session, 38, 1, 450, 234)
	}
	return shape
}

// directorySession 返回该角色**当前**的角色目录属主会话（现场日志里
// `session_is_character_directory_owner=true` 的那一条）。
func (shape *liveSocketShape) directorySession(t *testing.T, characterID uint16) *gameSession {
	t.Helper()
	session, ok := shape.fixture.service.onlineGameSession(characterID)
	if !ok || session == nil {
		t.Fatalf("character %d has no directory session", characterID)
	}
	return session
}

// assertAuthorityIntact 断言权威管理器与两条访问器都还看得见这支两人队。
func (shape *liveSocketShape) assertAuthorityIntact(t *testing.T, tag string) {
	t.Helper()
	service := shape.fixture.service
	snapshot, found := service.runtimePartyManagerForService().SnapshotByUser(inviteAcceptLeaderCharacterID, 0)
	if !found || len(snapshot.Members) != 2 {
		t.Fatalf("%s: authoritative party lost: found=%t %+v", tag, found, snapshot)
	}
	for _, characterID := range []uint16{inviteAcceptLeaderCharacterID, inviteAcceptFollowerCharacterID} {
		session := shape.directorySession(t, characterID)
		state := service.runtimePartyStateForSession(session)
		if state.PartyID <= 0 || len(runtimePartyMembers(state)) != 2 {
			t.Fatalf("%s: dungeon accessor lost the party for char %d on %s: %+v",
				tag, characterID, session.connID, state)
		}
	}
}

// TestLiveSocketShapePartySurvivesEverySingleSocketClose：现场四条活 socket 的形状下，
// 任意一条 socket 单独关闭都不得摘掉队伍；关闭之后，队长从**当前目录属主会话**发 op16
// 仍必须走组队进图。
func TestLiveSocketShapePartySurvivesEverySingleSocketClose(t *testing.T) {
	for _, test := range []struct {
		name   string
		closer func(shape *liveSocketShape) *gameSession
	}{
		{"leader_directory_socket_closes", func(shape *liveSocketShape) *gameSession { return shape.leaderDir }},
		{"leader_second_socket_closes", func(shape *liveSocketShape) *gameSession { return shape.leaderAux }},
		{"follower_directory_socket_closes", func(shape *liveSocketShape) *gameSession { return shape.followerDir }},
		{"follower_second_socket_closes", func(shape *liveSocketShape) *gameSession { return shape.followerAux }},
	} {
		t.Run(test.name, func(t *testing.T) {
			shape := newLiveSocketShape(t, true)
			shape.fixture.formPartyThroughClientAccept(t)
			shape.assertAuthorityIntact(t, "after accept")

			closing := test.closer(shape)
			shape.fixture.service.unbindGameSession(closing)

			log := testPacketLogText(t, shape.fixture.service)
			if strings.Contains(log, "game-party-membership-removed") {
				t.Fatalf("closing %s removed a still-online character from the party; log:\n%s",
					closing.connID, log)
			}
			shape.assertAuthorityIntact(t, "after close")

			// 现场：选图那条 op16 走的正是"角色目录属主会话"。
			leaderSession := shape.directorySession(t, inviteAcceptLeaderCharacterID)
			followerSession := shape.directorySession(t, inviteAcceptFollowerCharacterID)
			shape.fixture.selectDungeon(t, leaderSession, 700)
			shape.fixture.releaseSharedLoadingBarrier(t, leaderSession, followerSession)
			assertPartyDungeonEntryRanAsParty(t, shape.fixture.service, leaderSession)
		})
	}
}

// TestDirectorySocketCloseWithoutLiveSiblingReproducesTheLiveFailure 是现场那条机制的
// **可执行复现**：把活会话索引留空（= 产生现场日志的那个二进制里没有交接判据），
// 目录属主 socket 一关就把仍然在另一条 socket 上玩的角色从队伍里摘掉，
// 于是队长随后选图时服务端看不见队伍 —— 与 HK 日志逐字同形：
//
//	game-party-dungeon-entry-prepared party_id=0 leader_char_id=0 member_count=0
//	game-party-dungeon-entry-peer-endpoints-skipped reason=runtime_party_below_two_members_has_no_remote_rows
//
// 这条同时钉住修复的前置条件：交接（`game-party-session-handover`）只在
// `Service.liveGameSessions` 里确实还有同角色的活 socket 时才发生。
func TestDirectorySocketCloseWithoutLiveSiblingReproducesTheLiveFailure(t *testing.T) {
	shape := newLiveSocketShape(t, false)
	shape.fixture.formPartyThroughClientAccept(t)
	shape.assertAuthorityIntact(t, "after accept")

	// 目录属主 socket 关闭（另一条 socket 其实还开着，但索引里没有它）。
	shape.fixture.service.unbindGameSession(shape.leaderDir)

	log := testPacketLogText(t, shape.fixture.service)
	if !logLineContaining(log,
		"game-party-membership-removed",
		"source=detach:game_session_unbound_or_disconnected",
	) {
		t.Fatalf("detach without a live sibling did not audit the removal; log:\n%s", log)
	}
	if strings.Contains(log, "game-party-session-handover") {
		t.Fatalf("handover fired without a registered live sibling; log:\n%s", log)
	}
	if _, found := shape.fixture.service.runtimePartyManagerForService().SnapshotByUser(
		inviteAcceptLeaderCharacterID, 0,
	); found {
		t.Fatal("the character survived a detach that had no live replacement socket")
	}

	// 现在队长在**新的**目录属主会话上（现场 `directory_owner_conn_id=game-000016`、
	// `session_generation=5`：目录被摘空之后，客户端在另一条 socket 上重新绑定角色，
	// 于是分配到一个新的代际）。
	shape.fixture.service.bindGameSessionCharacter(shape.leaderAux, inviteAcceptLeaderCharacterID)
	bindDungeonSelectorOriginForTestAt(t, shape.fixture.service, shape.leaderAux, 38, 1, 450, 234)
	leaderSession := shape.directorySession(t, inviteAcceptLeaderCharacterID)
	if leaderSession == shape.leaderDir {
		t.Fatal("fixture bug: the closed socket is still the directory owner")
	}
	shape.fixture.selectDungeon(t, leaderSession, 700)

	log = testPacketLogText(t, shape.fixture.service)
	for _, want := range []string{
		"game-party-dungeon-entry-peer-endpoints-skipped",
		"game-party-dungeon-entry-actor-scene-snapshot-skipped",
		"game-party-dungeon-start-map-fatigue-send",
		"game-party-dungeon-room-slot-table-skipped",
		"game-party-dungeon-entry-prepared",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("the degraded run did not reproduce the live shape (missing %s); log:\n%s", want, log)
		}
	}
	if !logLineContaining(log,
		"game-party-dungeon-entry-prepared",
		"party_id=0",
		"leader_char_id=0",
		"member_count=0",
	) {
		t.Fatalf("the degraded run did not reproduce the live `party_id=0` line; log:\n%s", log)
	}
	if !logLineContaining(log, "game-party-dungeon-entry-peer-endpoints-skipped", "reason=runtime_party_below_two_members_has_no_remote_rows") {
		t.Fatalf("the degraded run skipped the peer endpoints with a different reason; log:\n%s", log)
	}
	if strings.Contains(log, "game-party-dungeon-entry-member-prepared") {
		t.Fatalf("the degraded run still pulled the follower into the dungeon; log:\n%s", log)
	}
	// 关键判读点：修复后这一档**必定**留下管理器实际内容的诊断（现场那份日志里
	// 这条事件还不存在 —— 它随 party_manager_runtime.go 07:38:43 才落地，
	// 晚于产生日志的 07:22:07 构建）。一次复测单看这一行就能区分
	// "队伍真的被摘掉了（这里是 party 2 / leader 1 / members 1）" 与 "键法/代际口径错"。
	if !logLineContaining(log,
		"game-party-authoritative-membership-missing",
		"lookup_id_space=character_id",
		"character_member_row_in_manager=false",
	) {
		t.Fatalf("the degraded run did not leave the authoritative-membership miss diagnostic; log:\n%s", log)
	}
	if !strings.Contains(log, "manager_parties=2:leader=100:members=100") &&
		!strings.Contains(log, "leader="+strconvItoa(inviteAcceptFollowerCharacterID)) {
		t.Fatalf("the miss diagnostic did not dump the surviving party; log:\n%s", log)
	}
}

// TestRuntimePartyStateForSessionStaysCharacterKeyedAcrossSocketReplacement 钉住键法口径：
// 权威成员行的 `SessionGeneration` 是**入队时那条 socket** 的代际，而进图访问器按
// **角色**查（`SnapshotByUser(charID, 0)`，代际 0 = 通配，`runtime_manager.go:84`），
// 所以在"目录属主换成同角色另一条 socket"之后查询仍然命中，并把权威名单就地写回
// 该会话（`game-party-session-projection-repaired`）—— 这正是现场那次**没有**触发的原因
// 不在键法上的证据。
//
// 对照组：权威成员关系真的被摘掉时，访问器回落会话缓存（空），并留一条
// `game-party-authoritative-membership-missing` 且打印管理器实际内容 ——
// 现场那份日志**不可能**有这一行（该诊断随 `party_manager_runtime.go` 07:38:43 才落地，
// 晚于产生日志的 07:22:07 构建），所以它缺失**不能**当作"队伍没被移除"的证据。
func TestRuntimePartyStateForSessionStaysCharacterKeyedAcrossSocketReplacement(t *testing.T) {
	service := &Service{
		options:       options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
		onlinePlayers: newOnlinePlayerManager(),
	}
	logger, _ := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })

	// 目录属主 socket。
	first := &gameSession{conn: &bufferConn{}, connID: "keyed-first"}
	service.bindGameSessionCharacter(first, 1001)
	// 同角色的第二条活 socket：`bindGameSessionCharacter` 的早退分支刻意不夺目录，
	// 只把**那一刻**的投影拷给它（此刻还没有队伍 ⇒ 拷贝是空的）。
	replacement := &gameSession{conn: &bufferConn{}, connID: "keyed-replacement"}
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 1001, TownID: 38, AreaID: 1, Session: first,
	})
	service.bindGameSessionCharacter(replacement, 1001)
	if owner, ok := service.onlineGameSession(1001); !ok || owner != first {
		t.Fatalf("second socket stole the character directory: got=%p want=%p", owner, first)
	}
	if got := runtimePartyStateSnapshot(replacement); got.PartyID != 0 {
		t.Fatalf("fixture: the second socket already had a party projection: %+v", got)
	}

	// 队伍在**那次绑定之后**才成立：成员行的代际记的是目录属主 socket 的代际。
	manager := service.runtimePartyManagerForService()
	created := manager.Create(party.RuntimePartyMember{
		UserID:            1001,
		SessionGeneration: mustGeneration(t, service, first),
		State:             alignedcmd.PartyMemberState{UserID: 1001, UserState: 1},
	}, alignedcmd.PartyState{NameBytes: []byte("party"), MaxMembers: 4, UserState: 1})
	if !created.OK {
		t.Fatalf("create runtime party: %+v", created)
	}
	recordedGeneration := created.Party.Members[0].SessionGeneration
	if recordedGeneration != first.characterGeneration {
		t.Fatalf("fixture: recorded generation %d != directory socket generation %d",
			recordedGeneration, first.characterGeneration)
	}

	if snapshot, found := manager.SnapshotByUser(1001, 0); !found || snapshot.ID != created.Party.ID {
		t.Fatalf("generation-0 lookup is not a wildcard: found=%t %+v", found, snapshot)
	}
	// 这正是"带代际过滤会把要找的队伍滤掉"的实证：另一条 socket 的代际查不到。
	if _, found := manager.SnapshotByUser(1001, replacement.characterGeneration); found {
		t.Fatal("a lookup pinned to the other socket's generation should miss the recorded member row")
	}
	state := service.runtimePartyStateForSession(replacement)
	if state.PartyID != int(created.Party.ID) || len(runtimePartyMembers(state)) != 1 {
		t.Fatalf("accessor lost the party across a socket replacement: %+v", state)
	}
	if got := runtimePartyStateSnapshot(replacement); got.PartyID != int(created.Party.ID) {
		t.Fatalf("accessor did not repair the replacement socket projection: %+v", got)
	}
	if log := testPacketLogText(t, service); !strings.Contains(log, "game-party-session-projection-repaired") {
		t.Fatalf("projection repair was not logged; log:\n%s", log)
	}

	// 对照：真正的移除（无活兄弟 socket 的 detach）之后，被摘掉的那条**目录**会话的
	// 投影被清空，访问器于是回落到空值 —— 与现场 `party_id=0 member_count=0` 同形。
	service.detachRuntimePartySession(first, "test_last_socket_closed")
	if _, found := manager.SnapshotByUser(1001, 0); found {
		t.Fatal("the manager still holds the character after its last socket detached")
	}
	if got := service.runtimePartyStateForSession(first); got.PartyID != 0 {
		t.Fatalf("the removed directory session still resolved a party: %+v", got)
	}
	// 残余（本轮只记录、不修）：**非目录**的同角色会话不在任何清理路径的名单里
	// （`clearRuntimePartyProjection` / `broadcastRuntimePartyMemberRemoved` 都只按
	// `s.gameSessions[charID]` 取那一会话），所以它会一直保留最后那份权威投影。
	// 今天它不会把队伍误 fan-out 出去（成员数 1 ⇒ `runtimePartyMemberIndex` 不命中 ⇒
	// 仍走单人路径），但如果这类 socket 之后成为目录属主，就会出现"party_id 非零、
	// 名单只有一行"的第三种形状。先钉住安全的那一半。
	if got := service.runtimePartyStateForSession(replacement); got.PartyID > 0 &&
		len(runtimePartyMembers(got)) > 1 {
		t.Fatalf("a stale sibling-session cache fanned a disbanded party into a multi-member entry: %+v", got)
	}
}

// TestRuntimePartyManagerIsASingleLazyInstancePerService 钉住现场被排掉的那条怀疑：
// 管理器**只有一个实例**（`service.go:229` 构造 + `runtimePartyManagerForService`
// 懒创建返回同一个字段）。这是"帧/名册路径算得出队伍、进图访问器算不出"这一类
// 猜测的否证位：任何"第二实例/懒创建出第二个单例"的改动都会让这条失败。
func TestRuntimePartyManagerIsASingleLazyInstancePerService(t *testing.T) {
	service := &Service{}
	first := service.runtimePartyManagerForService()
	if first == nil {
		t.Fatal("lazy manager was not created")
	}
	if second := service.runtimePartyManagerForService(); second != first {
		t.Fatalf("runtimePartyManagerForService returned a different instance: %p vs %p", first, second)
	}
	// 生产组合根在构造时就装好同一个字段，懒创建不得把它换掉。
	initialized := &Service{runtimePartyManager: party.NewRuntimePartyManager()}
	installed := initialized.runtimePartyManager
	if got := initialized.runtimePartyManagerForService(); got != installed {
		t.Fatalf("lazy getter replaced the composition-root manager: %p vs %p", got, installed)
	}
}
