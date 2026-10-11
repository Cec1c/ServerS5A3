package dnfbridge

import (
	"context"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件钉住"**放弃副本 → 双方回城 → 重新组队 → 队长进图，队员卡在城镇**"
// （操作者 2026-09-28 报障）。机制、现场逐行与修法见 dungeon_party_regroup_entry.go。
//
// 现场链（packet_log.txt）：
//
//	:144725 game-party-enter-select-follower-synchronized conn_id=game-000028 party_id=2 char_id=6
//	  ⇒ 队伍 2 进图前，队员侧选择页（op23 area=0xff / op2 mode0+mode1 / op15 成功 / op5 / op27）装好了
//	:144853 game-upper-current-dungeon-context-op27-duplicate-skipped conn_id=game-000028
//	  ⇒ 队伍 2 进图：合法跳过
//	:145712 同一条 duplicate-skipped 再次出现（队伍 3）
//	  ⇒ 旗子是**上一场留下的谎话**：队员侧选择页投影整批没发，客户端因此没进选择页
//
// 所以本文件的判据不是内部指针，而是"队员这条连接到底收没收到选择页投影"。

// abandonFollowerDungeonRun 造出"队员正在副本里、已经发起待决回城、随后被城镇侧请求确认"
// 这条**真实提交链**（`commitPendingDungeonReturnForSceneRequest` 就是现场
// packet_log.txt:145341 那条 `run_status=abandoned` 的提交点）。
func abandonFollowerDungeonRun(
	t *testing.T,
	fixture *inviteAcceptDungeonFixture,
	session *gameSession,
) {
	t.Helper()
	runtime, _, err := fixture.service.prepareDungeonRuntime(
		context.Background(),
		session,
		dungeoncmd.SelectDungeonRequest{DungeonID: 700, Difficulty: 1},
	)
	if err != nil {
		t.Fatalf("prepare char %d dungeon runtime: %v", session.selectedCharacterID, err)
	}
	session.dungeon.mu.Lock()
	session.dungeon.runtime = runtime
	runtime.townReturnPending = true
	runtime.townReturnOp24Sent = true
	session.dungeon.mu.Unlock()
	// 入口本身会继续走城镇侧 actor 重建（夹具没有真实装备/PVF 时可能返回错误）；
	// 与缺陷有关的是提交点必须作废选图页上下文，所以这里只忽略返回值。
	_ = fixture.service.commitPendingDungeonReturnForSceneRequest(
		session,
		"test_current_exe_op35_position_after_pending_town_return",
	)
	if session.dungeon.runtime != nil {
		t.Fatal("the abandon commit did not detach the runtime")
	}
	// 现场这一步之后是"双方都返回城镇"（packet_log.txt:145341 附近
	// `game-dungeon-return-copresence-rebound … same_area_peers=1`）：客户端回到城镇
	// 并上报区域，两人因此重新落在同一区域里。夹具把这一步补齐，否则队长进图会在
	// `party member 100 is not present in the leader town channel/area` 前置处就停下。
	fixture.service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: session.selectedCharacterID,
		TownID:      38,
		AreaID:      1,
		Session:     session,
	})
	session.townMu.Lock()
	setCurrentTownPositionSceneLocked(session, session.selectedCharacterID, 38, 1)
	session.townPositionSnapshot.PositionValid = true
	session.townMu.Unlock()
	fixture.service.markCurrentTownSceneReady(session)
	if !fixture.service.onlinePlayers.PeerInSameArea(inviteAcceptLeaderCharacterID, session.selectedCharacterID) {
		t.Fatal("the fixture did not restore the shared town area after the return")
	}
}

// openFollowerSelectorBeforeEntry 复现"队员在进本前自己开过选图页"：
// 三面旗为 true + 城镇选择原点已绑定（现场 `:144724/:144725`）。
func openFollowerSelectorBeforeEntry(
	t *testing.T,
	fixture *inviteAcceptDungeonFixture,
	session *gameSession,
) {
	t.Helper()
	session.enterSelectDungeonSent = true
	session.enterSelectDungeonAckSent = true
	session.enterSelectDungeonContextSent = true
	bindDungeonSelectorOriginForTestAt(t, fixture.service, session, 38, 1, 450, 234)
}

// TestRegroupEntryAfterAbandonReinstallsFollowerSelectorProjection 是修复档：
// 放弃回城提交必须作废选图页上下文，重新组队进图时队员侧选择页投影必须**真的重发**。
func TestRegroupEntryAfterAbandonReinstallsFollowerSelectorProjection(t *testing.T) {
	fixture := newInviteAcceptDungeonFixture(t)
	fixture.formPartyThroughClientAccept(t)
	openFollowerSelectorBeforeEntry(t, fixture, fixture.follower)
	fixture.follower.conn.(*bufferConn).write.Reset()

	abandonFollowerDungeonRun(t, fixture, fixture.follower)

	// 1) 提交点必须作废三面旗 + 城镇选择原点，并留一条可 grep 的事件。
	if fixture.follower.enterSelectDungeonSent ||
		fixture.follower.enterSelectDungeonAckSent ||
		fixture.follower.enterSelectDungeonContextSent ||
		fixture.follower.townSelectorOriginBound {
		t.Fatalf("stale selector context survived the abandon return: sent=%t ack=%t context=%t origin=%t",
			fixture.follower.enterSelectDungeonSent,
			fixture.follower.enterSelectDungeonAckSent,
			fixture.follower.enterSelectDungeonContextSent,
			fixture.follower.townSelectorOriginBound,
		)
	}
	log := testPacketLogText(t, fixture.service)
	for _, want := range []string{
		"kind=game-party-dungeon-selector-context-reset-after-return",
		"char_id=100",
		"had_enter_select_sent=true",
		"had_enter_select_context_sent=true",
		"had_town_selector_origin=true",
		"selector_context_cleared=true",
		"switch=" + dungeonPartySelectorContextResetEnv,
		"mode=on",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("selector-context reset event is missing %q\n%s", want, log)
		}
	}

	// 2) 重新组队（同一对会话仍在同一支队伍里）后队长进图：队员侧选择页投影必须重发。
	fixture.follower.conn.(*bufferConn).write.Reset()
	fixture.selectDungeon(t, fixture.leader, 700)
	fixture.releaseSharedLoadingBarrier(t, fixture.leader, fixture.follower)

	log = testPacketLogText(t, fixture.service)
	if !logLineContaining(log,
		"kind=game-party-enter-select-follower-synchronized",
		"conn_id=invite-accept-follower",
	) {
		t.Fatalf("the follower selector projection was not re-installed after the regroup\n%s", log)
	}
	if logLineContaining(log,
		"kind=game-party-enter-select-follower-deferred",
		"conn_id=invite-accept-follower",
	) {
		t.Fatalf("the follower selector was deferred instead of installed\n%s", log)
	}
	// 判据落到**字节**上：op27（选图页本体）必须真的写进队员这条连接。
	if frames := followerSelectorPageFrameCount(t, fixture.follower.conn.(*bufferConn)); frames == 0 {
		t.Fatalf("the follower connection received no op27 selector page after the regroup (%d frames)",
			frames)
	}
	if !logLineContaining(log,
		"kind=game-party-dungeon-entry-member-prepared",
		"conn_id=invite-accept-follower",
	) {
		t.Fatalf("the follower was not pulled into the leader's dungeon entry\n%s", log)
	}
	// 3) 队员必须真的落进同一场实例（不是只发了几条帧）。
	if fixture.follower.dungeon.runtime == nil {
		t.Fatal("the follower never got a dungeon runtime")
	}
	leaderInstance := fixture.leader.dungeon.runtime.partyInstance
	followerInstance := fixture.follower.dungeon.runtime.partyInstance
	if leaderInstance == nil || leaderInstance != followerInstance {
		t.Fatalf("the regrouped party did not share one instance: leader=%p follower=%p",
			leaderInstance, followerInstance)
	}
	if memberCount := leaderInstance.memberCount(); memberCount != 2 {
		t.Fatalf("shared instance member_count=%d want 2", memberCount)
	}
}

// TestRegroupEntryRollbackLiteralKeepsTheStaleSelectorContext 是回退档：逐字节回到改动前
// ——旗子留着、不记事件，进图时队员侧选择页投影**整批被跳过**（现场缺陷原样复现）。
func TestRegroupEntryRollbackLiteralKeepsTheStaleSelectorContext(t *testing.T) {
	t.Setenv(dungeonPartySelectorContextResetEnv, "legacy")
	fixture := newInviteAcceptDungeonFixture(t)
	fixture.formPartyThroughClientAccept(t)
	openFollowerSelectorBeforeEntry(t, fixture, fixture.follower)

	abandonFollowerDungeonRun(t, fixture, fixture.follower)

	if !fixture.follower.enterSelectDungeonSent ||
		!fixture.follower.enterSelectDungeonAckSent ||
		!fixture.follower.enterSelectDungeonContextSent {
		t.Fatal("the rollback literal cleared the selector context")
	}
	if strings.Contains(testPacketLogText(t, fixture.service),
		"kind=game-party-dungeon-selector-context-reset-after-return") {
		t.Fatal("the rollback literal logged the reset event")
	}

	fixture.follower.conn.(*bufferConn).write.Reset()
	fixture.selectDungeon(t, fixture.leader, 700)
	fixture.releaseSharedLoadingBarrier(t, fixture.leader, fixture.follower)

	log := testPacketLogText(t, fixture.service)
	if logLineContaining(log,
		"kind=game-party-enter-select-follower-synchronized",
		"conn_id=invite-accept-follower",
	) {
		t.Fatalf("the rollback literal re-installed the follower selector (the defect would be fixed)\n%s", log)
	}
	// 现场缺陷的**字节级**复现：队员这条连接一条 op27 都没有 ⇒ 客户端根本没进选择页。
	if frames := followerSelectorPageFrameCount(t, fixture.follower.conn.(*bufferConn)); frames != 0 {
		t.Fatalf("the rollback literal wrote %d op27 selector page frames to the follower, want 0", frames)
	}
	if !logLineContaining(log,
		"kind=game-party-dungeon-entry-member-prepared",
		"conn_id=invite-accept-follower",
	) {
		t.Fatalf("the rollback literal must still prepare the member server-side (live: instance created/joined)\n%s", log)
	}
}

// followerSelectorPageFrameCount 数"写进这条连接的 op27 选图页帧"。
func followerSelectorPageFrameCount(t *testing.T, conn *bufferConn) int {
	t.Helper()
	count := 0
	for _, frame := range splitUpperFrames(t, conn.write.Bytes()) {
		if frame.msgID == currentDungeonContextMsgID {
			count++
		}
	}
	return count
}

// 干净会话（从未开过选图页）在同一个提交点上**一个字段都不改、一行日志都不记**：
// 绝大多数单人回城走的就是这一档，必须与改动前逐字节相同。
func TestAbandonReturnWithoutSelectorContextWritesNothing(t *testing.T) {
	fixture := newInviteAcceptDungeonFixture(t)
	fixture.formPartyThroughClientAccept(t)
	// 夹具的 `bindDungeonSelectorOriginForTestAt` 会把城镇选择原点绑上（那是"开过选图页"
	// 的记账）；这里要造的是"**从未开过选图页**的干净会话"，所以先把它清掉。
	clearCurrentTownSelectorOrigin(fixture.follower)
	fixture.follower.conn.(*bufferConn).write.Reset()

	abandonFollowerDungeonRun(t, fixture, fixture.follower)

	log := testPacketLogText(t, fixture.service)
	if strings.Contains(log, "kind=game-party-dungeon-selector-context-reset-after-return") {
		t.Fatalf("a clean session logged the selector-context reset\n%s", log)
	}
	if fixture.follower.enterSelectDungeonSent || fixture.follower.enterSelectDungeonContextSent {
		t.Fatal("a clean session was mutated")
	}
}

// 两种字面量口径：默认（空/非法）= 修复档；off/0/false/no/legacy = 回退档。
func TestDungeonPartySelectorContextResetLiterals(t *testing.T) {
	for _, value := range []string{"", "  ", "junk", "on", "true", "1"} {
		t.Setenv(dungeonPartySelectorContextResetEnv, value)
		if !dungeonPartySelectorContextResetEnabled() {
			t.Fatalf("literal %q must resolve to the fixed tier (on)", value)
		}
	}
	for _, value := range []string{"off", "0", "false", "no", "legacy", " LEGACY "} {
		t.Setenv(dungeonPartySelectorContextResetEnv, value)
		if dungeonPartySelectorContextResetEnabled() {
			t.Fatalf("literal %q must resolve to the rollback tier (off)", value)
		}
	}
}

// 未用的导入护栏：本文件必须能独立编译（与包内既有夹具共用 repository 装配）。
var _ = func() (dnfrepo.Group, bool) { return dnfrepomemory.NewMemoryGroup(), true }
var _ = alignedcmd.PartyState{}
