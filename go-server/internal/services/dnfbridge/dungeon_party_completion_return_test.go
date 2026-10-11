package dnfbridge

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/party"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件钉住两条**出图路径必须分开**的行为（操作者口径）：
//
//	通关回城（队长点右上角「返回城镇」）⇒ **保留队伍**，队员由自己的
//	  completed_town_route 回城，最后一个成员到位后重建城镇同屏；
//	中途放弃 ⇒ 沿用上一轮的解散（dungeon_party_leader_exit.go），
//	  队员留在本场实例内。
//
// 现场两条路径的日志见 dungeon_party_completion_return.go 的文件头。

const (
	completionReturnLeaderCharacterID   = uint16(99)
	completionReturnFollowerCharacterID = uint16(100)
)

type completionReturnFixture struct {
	service       *Service
	leader        *gameSession
	member        *gameSession
	leaderRuntime *runtimeDungeonState
	memberRuntime *runtimeDungeonState
	instance      *dungeonPartyInstance
	leaderConn    *bufferConn
	memberConn    *bufferConn
	partyIDText   string
}

// newCompletionReturnPartyFixture 造"两人一队 + 同一场副本实例 + 两个会话都能跑
// 通关城镇路线"的夹具：城镇统计来自 newBackToVillageRuntimeAtOrigin（既有完工路线
// 用例的同一份前置），队伍与实例沿用 partyDungeonDeathFixture 的组装方式。
func newCompletionReturnPartyFixture(t *testing.T) completionReturnFixture {
	t.Helper()
	service, leader, leaderRuntime := newBackToVillageRuntimeAtOrigin(t, 474, 234)
	// 队伍要登记进中央管理器 ⇒ 会话必须先绑定角色（bootstrap 的既有前置，
	// 与 partyDungeonDeathFixture 同一条）。
	service.bindGameSessionCharacter(leader, completionReturnLeaderCharacterID)
	// `newBackToVillageRuntimeAtOrigin` 不带同屏管理器（它的既有用例用不到）；
	// 本夹具要断言"回到城镇后队伍互相可见"，所以按 newTownCoPresenceTestService 补上。
	service.onlinePlayers = newOnlinePlayerManager()
	// 判读串必须在测试里也有钉子：接一份包日志。
	logger, _ := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("completion-return repository group unavailable")
	}
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: strconv.Itoa(int(completionReturnFollowerCharacterID)),
		AccountID:   "account-1",
		Level:       20,
		Stats: map[string]int64{
			"fatigue":    100,
			"town_id":    7,
			"area_id":    3,
			"pos_x":      474,
			"pos_y":      234,
			"direction":  5,
			"area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	enableTownMoveSkillProjection(t, service, repositories, strconv.Itoa(int(completionReturnFollowerCharacterID)))

	member := &gameSession{
		conn:                &bufferConn{},
		connID:              "completion-return-member",
		channel:             leader.channel,
		residentChannel:     leader.channel,
		selectedCharacterID: completionReturnFollowerCharacterID,
	}
	service.bindGameSessionCharacter(member, completionReturnFollowerCharacterID)
	bindDungeonSelectorOriginForTestAt(t, service, member, 7, 3, 474, 234)
	memberRuntime, _, err := service.prepareDungeonRuntime(
		ctx,
		member,
		dungeoncmd.SelectDungeonRequest{DungeonID: 700, Difficulty: 1},
	)
	if err != nil {
		t.Fatalf("prepare the second party member runtime: %v", err)
	}
	if err := freezeCurrentDungeonTownReturnOrigin(member, memberRuntime); err != nil {
		t.Fatal(err)
	}
	member.dungeon.mu.Lock()
	member.dungeon.runtime = memberRuntime
	member.dungeon.mu.Unlock()

	// 队伍：两份会话都先落投影，再由中央管理器接管（现场口径：管理器持有队伍）。
	state := alignedcmd.PartyState{
		PartyID: 12,
		UserID:  completionReturnLeaderCharacterID,
		Members: []alignedcmd.PartyMemberState{
			{UserID: completionReturnLeaderCharacterID, UserState: 1, HPPercent: 100, MPPercent: 100},
			{UserID: completionReturnFollowerCharacterID, UserState: 1, HPPercent: 100, MPPercent: 100},
		},
	}
	storeRuntimePartyState(leader, state)
	storeRuntimePartyState(member, state)
	snapshot, bootstrapped := service.bootstrapManagedRuntimePartyForSession(leader)
	if !bootstrapped || len(snapshot.Members) != 2 {
		t.Fatalf("bootstrap party snapshot=%+v ok=%t want 2 members", snapshot, bootstrapped)
	}

	// 一场共享实例：队长先落位（建实例），队员接进同一个键。
	var instance *dungeonPartyInstance
	for _, entry := range []struct {
		session *gameSession
		runtime *runtimeDungeonState
	}{
		{leader, leaderRuntime},
		{member, memberRuntime},
	} {
		scene, ok := entry.runtime.Session.Scene()
		if !ok {
			t.Fatalf("party member %d runtime has no scene", entry.session.selectedCharacterID)
		}
		instance = service.adoptPartyDungeonInstance(entry.session, entry.runtime, scene)
		if instance == nil {
			t.Fatalf("party member %d did not join the shared dungeon instance",
				entry.session.selectedCharacterID)
		}
	}
	if got := service.partyDungeonInstanceCount(); got != 1 {
		t.Fatalf("party dungeon instance count=%d want 1", got)
	}

	// 城镇同屏登记 + 两个会话的城镇就绪态（同屏重建的前置）。
	for _, session := range []*gameSession{leader, member} {
		service.onlinePlayers.EnterArea(&onlinePlayerInfo{
			CharacterID: session.selectedCharacterID,
			TownID:      7,
			AreaID:      3,
			PositionX:   474,
			PositionY:   234,
			Direction:   5,
			AreaState:   3,
			Session:     session,
		})
	}

	return completionReturnFixture{
		service:       service,
		leader:        leader,
		member:        member,
		leaderRuntime: leaderRuntime,
		memberRuntime: memberRuntime,
		instance:      instance,
		leaderConn:    leader.conn.(*bufferConn),
		memberConn:    member.conn.(*bufferConn),
		partyIDText:   strconv.Itoa(int(snapshot.ID)),
	}
}

// completeRunAndReturnToTown 把这条会话的副本跑成 completed，然后走**真实入口**
// `sendCurrentCompletedDungeonReturnToTown`（现场 source=current_exe_op72_card_exit_return_to_town）。
func (fixture completionReturnFixture) completeRunAndReturnToTown(
	t *testing.T,
	session *gameSession,
	runtime *runtimeDungeonState,
) {
	t.Helper()
	if err := runtime.Session.CompleteCurrentRoom(); err != nil {
		t.Fatalf("complete char %d run: %v", session.selectedCharacterID, err)
	}
	transition, err := fixture.service.prepareCurrentDungeonTownTransitionForSession(
		context.Background(),
		session,
		session.selectedCharacterID,
	)
	if err != nil {
		t.Fatalf("prepare char %d town transition: %v", session.selectedCharacterID, err)
	}
	if err := fixture.service.sendCurrentCompletedDungeonReturnToTown(
		session,
		runtime,
		transition,
		uint16(dnfenum.CmdPacketEplpCommand),
		"test_current_exe_op72_card_exit_return_to_town",
	); err != nil {
		t.Fatalf("char %d completed town route: %v", session.selectedCharacterID, err)
	}
}

// TestDungeonCompletionReturnKeepsPartyAndReturnsEveryMemberToTown 是操作者报障那条：
// 通关后队长点「返回城镇」**不得解散队伍**，而两个人都要回到城镇（同屏重建）。
func TestDungeonCompletionReturnKeepsPartyAndReturnsEveryMemberToTown(t *testing.T) {
	fixture := newCompletionReturnPartyFixture(t)
	fixture.leaderConn.write.Reset()
	fixture.memberConn.write.Reset()

	// 队长先回城：队员还在图内，这一刻**一个队伍帧都不许发**。
	fixture.completeRunAndReturnToTown(t, fixture.leader, fixture.leaderRuntime)

	manager := fixture.service.runtimePartyManagerForService()
	for _, characterID := range []uint16{completionReturnLeaderCharacterID, completionReturnFollowerCharacterID} {
		if _, found := manager.SnapshotByUser(characterID, 0); !found {
			t.Fatalf("the completed return disbanded the party: char %d is gone from the manager", characterID)
		}
	}
	if state := runtimePartyStateSnapshot(fixture.member); state.PartyID <= 0 ||
		len(runtimePartyMembers(state)) != 2 {
		t.Fatalf("member party projection was cleared by the completed return: %+v", state)
	}
	memberFrames := splitUpperFrames(t, fixture.memberConn.write.Bytes())
	if assertOp9Kind3RemovalFor(memberFrames, completionReturnLeaderCharacterID) {
		t.Fatalf("the completed return removed the leader actor from the member's dungeon scene: %+v", memberFrames)
	}
	for _, frame := range memberFrames {
		if frame.msgID == 0x0099 && string(frame.body) == string(party.BuildEmptyRealtimeInfo()) {
			t.Fatalf("the completed return cleared the member's party roster frame: %+v", frame)
		}
	}
	if fixture.member.dungeon.runtime != fixture.memberRuntime ||
		fixture.memberRuntime.partyInstance != fixture.instance {
		t.Fatalf("the completed return touched the member's runtime/instance: runtime=%p want %p instance=%p want %p",
			fixture.member.dungeon.runtime, fixture.memberRuntime,
			fixture.memberRuntime.partyInstance, fixture.instance)
	}

	// 队员自己回城：这时才轮到"一起回城"的最后一环——既有同屏重建。
	fixture.memberConn.write.Reset()
	fixture.leaderConn.write.Reset()
	fixture.completeRunAndReturnToTown(t, fixture.member, fixture.memberRuntime)

	log := testPacketLogText(t, fixture.service)
	for _, want := range []string{
		"kind=game-party-dungeon-completion-return-party-kept",
		"source=completed_town_route",
		"party_id=" + fixture.partyIDText,
		"leader_char_id=99",
		"leaving_char_id=99",
		"leaving_is_leader=true",
		"member_count=2",
		"members_still_in_dungeon=[100]",
		"party_preserved=true",
		"roster_frames_sent=0",
		"member_instance_retained=true",
		"mode=keep",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("completion-return keep event is missing %q\n%s", want, log)
		}
	}
	if strings.Contains(log, "kind=game-party-dungeon-leader-exit-disband") ||
		strings.Contains(log, "kind=game-party-dungeon-leader-exit-peer-actor-removal") {
		t.Fatalf("the completed return ran the disband path\n%s", log)
	}
	// 第二个成员到位 ⇒ 队伍还在，**既有**的城镇同屏重建这才有可能跑起来
	// （`party_town_return.go:17`；队伍一旦被解散，`state.PartyID <= 0` 直接返回 nil）。
	// 现场客户端回到城镇后会上报城区坐标（op35/op36），夹具把这一步补齐。
	if logLineContaining(log, "kind=game-party-dungeon-completion-return-party-kept", "leaving_char_id=100") == false {
		t.Fatalf("the member's own completed return did not record the kept party\n%s", log)
	}
	if fixture.member.dungeon.runtime != nil {
		t.Fatal("the member never left its dungeon runtime on its own completed return")
	}
	fixture.reportTownPosition(t, fixture.leader)
	fixture.reportTownPosition(t, fixture.member)
	fixture.leaderConn.write.Reset()
	fixture.memberConn.write.Reset()
	if err := fixture.service.rebuildRuntimePartyTownCoPresenceAfterReturn(
		fixture.member,
		"test_party_town_copresence_after_completed_return",
	); err != nil {
		t.Fatalf("town co-presence rebuild: %v", err)
	}
	log = testPacketLogText(t, fixture.service)
	if !strings.Contains(log, "kind=game-party-town-return-copresence-rebuilt") {
		t.Fatalf("the preserved party was not re-projected in town\n%s", log)
	}
	if fixture.leaderConn.write.Len() == 0 || fixture.memberConn.write.Len() == 0 {
		t.Fatalf("town co-presence rebuild wrote nothing: leader=%d member=%d",
			fixture.leaderConn.write.Len(), fixture.memberConn.write.Len())
	}
}

// reportTownPosition 复现客户端回到城镇后的城区坐标上报（`town_position.go:202-203`
// 的落点）：城镇场景就绪 + 位置有效。没有它，同屏重建按设计一个包都不发。
func (fixture completionReturnFixture) reportTownPosition(t *testing.T, session *gameSession) {
	t.Helper()
	session.townMu.Lock()
	setCurrentTownPositionSceneLocked(session, session.selectedCharacterID, 7, 3)
	session.townPositionSnapshot.PositionX = 474
	session.townPositionSnapshot.PositionY = 234
	session.townPositionSnapshot.PositionValid = true
	session.townMu.Unlock()
	fixture.service.markCurrentTownSceneReady(session)
	if !currentPartyTownReturnSceneReady(session) {
		t.Fatalf("char %d is not town-ready after the position report", session.selectedCharacterID)
	}
}

// 回退档逐字节回到改动前：通关回城与放弃回城一样走解散（同一函数、同一 source）。
func TestDungeonCompletionReturnRollbackLiteralDisbandsByteForByte(t *testing.T) {
	t.Setenv(dungeonPartyCompletionReturnEnv, dungeonPartyCompletionReturnValueDisband)
	fixture := newCompletionReturnPartyFixture(t)
	fixture.leaderConn.write.Reset()
	fixture.memberConn.write.Reset()

	fixture.completeRunAndReturnToTown(t, fixture.leader, fixture.leaderRuntime)

	manager := fixture.service.runtimePartyManagerForService()
	for _, characterID := range []uint16{completionReturnLeaderCharacterID, completionReturnFollowerCharacterID} {
		if snapshot, found := manager.SnapshotByUser(characterID, 0); found {
			t.Fatalf("rollback literal left char %d in a managed party: %+v", characterID, snapshot)
		}
	}
	memberFrames := splitUpperFrames(t, fixture.memberConn.write.Bytes())
	if !assertOp9Kind3RemovalFor(memberFrames, completionReturnLeaderCharacterID) {
		t.Fatalf("rollback literal did not send the leader-actor removal to the member: %+v", memberFrames)
	}
	emptyRealtime := false
	for _, frame := range memberFrames {
		if frame.msgID == 0x0099 && string(frame.body) == string(emptyRealtimeRosterBody()) {
			emptyRealtime = true
		}
	}
	if !emptyRealtime {
		t.Fatalf("rollback literal did not clear the member roster: %+v", memberFrames)
	}
	log := testPacketLogText(t, fixture.service)
	for _, want := range []string{
		"kind=game-party-dungeon-leader-exit-disband",
		"source=completed_town_route",
		"member_count_before=2",
		"disbanded=true",
		"members_still_in_dungeon=[100]",
		"mode=disband",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("rollback literal is missing %q\n%s", want, log)
		}
	}
	if strings.Contains(log, "kind=game-party-dungeon-completion-return-party-kept") {
		t.Fatalf("rollback literal still logged the keep event\n%s", log)
	}
	// 对照面：队伍已经不存在 ⇒ 即便两个客户端都已回到城镇，**既有**同屏重建
	// 也永远发不出任何东西（`state.PartyID <= 0` 直接返回）。
	fixture.member.dungeon.mu.Lock()
	fixture.member.dungeon.runtime = nil
	fixture.member.dungeon.mu.Unlock()
	fixture.reportTownPosition(t, fixture.leader)
	fixture.reportTownPosition(t, fixture.member)
	fixture.leaderConn.write.Reset()
	fixture.memberConn.write.Reset()
	if err := fixture.service.rebuildRuntimePartyTownCoPresenceAfterReturn(
		fixture.member,
		"test_party_town_copresence_after_disbanded_completed_return",
	); err != nil {
		t.Fatalf("town co-presence rebuild after the disband: %v", err)
	}
	if fixture.leaderConn.write.Len() != 0 || fixture.memberConn.write.Len() != 0 {
		t.Fatalf("a disbanded party was re-projected in town: leader=%d member=%d",
			fixture.leaderConn.write.Len(), fixture.memberConn.write.Len())
	}
}

// 本开关**不**接受兄弟开关的字面量 `legacy`：它必须落回修复档（不能"设了以为回退了"）。
func TestDungeonCompletionReturnRejectsTheSiblingLegacyLiteral(t *testing.T) {
	t.Setenv(dungeonPartyCompletionReturnEnv, "legacy")
	if got := readDungeonPartyCompletionReturnMode("legacy"); got != dungeonPartyCompletionReturnKeep {
		t.Fatalf("legacy literal resolved to %s want %s (keep)", got, dungeonPartyCompletionReturnKeep)
	}
	if got := readDungeonPartyCompletionReturnMode("  DISBAND "); got != dungeonPartyCompletionReturnDisband {
		t.Fatalf("disband literal resolved to %s want disband", got)
	}
	if got := readDungeonPartyCompletionReturnMode(""); got != dungeonPartyCompletionReturnKeep {
		t.Fatalf("empty literal resolved to %s want keep", got)
	}

	fixture := newCompletionReturnPartyFixture(t)
	fixture.leaderConn.write.Reset()
	fixture.memberConn.write.Reset()
	fixture.completeRunAndReturnToTown(t, fixture.leader, fixture.leaderRuntime)

	if _, found := fixture.service.runtimePartyManagerForService().
		SnapshotByUser(completionReturnLeaderCharacterID, 0); !found {
		t.Fatal("the legacy literal wrongly rolled the completion path back to disband")
	}
	if strings.Contains(testPacketLogText(t, fixture.service), "kind=game-party-dungeon-leader-exit-disband") {
		t.Fatal("the legacy literal wrongly ran the disband path")
	}
}

// 单人 / 没有活队伍时通关回城与改动前逐字节相同：不发包、不记本文件的事件。
func TestDungeonCompletionReturnWithoutPartyWritesNothing(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	logger, logPath := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	session.conn.(*bufferConn).write.Reset()

	if err := runtime.Session.CompleteCurrentRoom(); err != nil {
		t.Fatal(err)
	}
	transition, err := service.prepareCurrentDungeonTownTransitionForSession(
		context.Background(), session, session.selectedCharacterID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.sendCurrentCompletedDungeonReturnToTown(
		session, runtime, transition, uint16(dnfenum.CmdPacketEplpCommand),
		"test_solo_completed_return",
	); err != nil {
		t.Fatal(err)
	}
	text := readPeerEndpointTestLog(t, logger, logPath)
	if strings.Contains(text, "kind=game-party-dungeon-completion-return") {
		t.Fatalf("a solo completed return logged the party event\n%s", text)
	}
	if strings.Contains(text, "kind=game-party-dungeon-leader-exit") {
		t.Fatalf("a solo completed return logged a leader-exit event\n%s", text)
	}
}

func emptyRealtimeRosterBody() []byte {
	return party.BuildEmptyRealtimeInfo()
}
