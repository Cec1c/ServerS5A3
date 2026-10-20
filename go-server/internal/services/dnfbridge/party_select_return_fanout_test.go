package dnfbridge

import (
	"context"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件钉住运维 2026-09-29 第 3 条的服务端那一半：**队长在选择页直接返回城镇时，
// 队员必须一起回城**（现场：队员被卡在选图页 24.095 秒，直到自己发 op132）。
//
// 现场（`packet_log.txt`，party_id=1，队长 char4/`game-002700`，队员 char1/`game-002698`）：
//
//	:239204 队长 op132 回城（`has_runtime=false`, 05:50:23.830）
//	:239208 `game-dungeon-select-back-to-village-committed`（op24 只写给队长）
//	:239276 队员**自己**发 op132（05:50:47.927，落后 24.095 s）才解卡
//	中间发给队员的 5 条报文全是 `msg_id=22`（城市场景位移灌进选图页）；
//	对队员查 `blocked|skipped|suppressed` 命中 0 ⇒ 这条分支上原来**没有队友的代码**。

// partySelectReturnFixture 造两个**都停在副本选择页**的同队成员（1001 队长 / 1002 队员），
// 两个人的城镇原点都已绑定，且**都不持有副本运行时**。
func partySelectReturnFixture(t *testing.T) (*Service, *gameSession, *gameSession) {
	t.Helper()
	service, leader, member, state := newPartyFrameSessions(t, 10, currentSceneObjectContext)
	table, resolver, monsters := loadBridgeDungeonStaticData(t, bridgeDungeonPVF(false))
	repositories := dnfrepomemory.NewMemoryGroup()
	for _, characterID := range []string{"1001", "1002"} {
		if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
			CharacterID: characterID,
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
	}
	if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{
		AccountID: "account-1",
		Metadata:  map[string]string{currentRentalPointMetadataKey: "0"},
	}); err != nil {
		t.Fatal(err)
	}
	service.worldMapTable = table
	service.worldMapResolver = resolver
	service.dungeonMonsterTable = monsters
	service.options = options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain}
	service.repositoryProvider = func() (dnfrepo.Group, bool) { return repositories, true }
	storeRuntimePartyState(leader, state)
	storeRuntimePartyState(member, state)
	for _, session := range []*gameSession{leader, member} {
		session.enterSelectDungeonSent = true
		session.enterSelectDungeonAckSent = true
		session.enterSelectDungeonContextSent = true
		bindDungeonSelectorOriginForTestAt(t, service, session, 7, 3, 474, 234)
		if ready, reason := currentDungeonSelectTownReturnReady(session); !ready {
			t.Fatalf("fixture member is not selector-ready: %s", reason)
		}
	}
	return service, leader, member
}

// TestPartySelectReturnFanOutBringsTheMemberBackToTown 是第 3 条的判别力用例。
//
//	默认档 ⇒ 队员**收到自己的 op24**，选图页上下文被清掉、收尾布防被立起来；
//	`legacy` 档 ⇒ 队员**一个字节都收不到**（逐字节复现"卡在选图页"）。
func TestPartySelectReturnFanOutBringsTheMemberBackToTown(t *testing.T) {
	service, leader, member := partySelectReturnFixture(t)
	t.Setenv(dungeonPartySelectReturnFanoutEnv, "")
	leaderConn := leader.conn.(*bufferConn)
	memberConn := member.conn.(*bufferConn)
	leaderConn.write.Reset()
	memberConn.write.Reset()

	if err := service.handleDungeonBackToVillage(leader, nil); err != nil {
		t.Fatalf("leader selector op132: %v", err)
	}

	// 队长本人照旧拿到自己那条 op24。
	leaderOp24, leaderRest := splitGameServerUpperPacket(t, leaderConn.write.Bytes())
	if leaderOp24.Header.MsgID != currentSceneTransitionMsgID || len(leaderRest) != 0 {
		t.Fatalf("leader selector return header=%+v rest=%x", leaderOp24.Header, leaderRest)
	}
	// 队员也必须拿到**自己的**那一条 op24（这就是本条修复）。
	memberOp24, memberRest := splitGameServerUpperPacket(t, memberConn.write.Bytes())
	if memberOp24.Header.MsgID != currentSceneTransitionMsgID || memberOp24.Header.Classification != 0 {
		t.Fatalf("member selector return header=%+v body=%x", memberOp24.Header, memberOp24.Body)
	}
	if len(memberRest) != 0 {
		t.Fatalf("member selector return emitted trailing packets=%x", memberRest)
	}
	if member.enterSelectDungeonSent || member.enterSelectDungeonAckSent || member.enterSelectDungeonContextSent {
		t.Fatal("the fanned-out member kept its dungeon-select context")
	}
	if !member.backToVillageEnterSelectPending {
		t.Fatal("the fanned-out member did not arm the client scene-finalizer guard")
	}
	member.dungeon.mu.Lock()
	pending := member.confirmedDungeonReturnStatePending
	member.dungeon.mu.Unlock()
	if !pending {
		t.Fatal("the fanned-out member did not commit the pending town return")
	}
	if member.townSelectorOriginBound || member.townSelectorOriginSnapshot.CharacterID != 0 {
		t.Fatalf("the fanned-out member retained selector origin=%+v bound=%t",
			member.townSelectorOriginSnapshot, member.townSelectorOriginBound)
	}
	if !member.returnTownFinishLoadingAckOnly {
		t.Fatal("the fanned-out member must re-enter the town finish-loading ack-only path")
	}

	// ── 回退档：逐字节复现"队员卡在选图页" ──
	service, leader, member = partySelectReturnFixture(t)
	t.Setenv(dungeonPartySelectReturnFanoutEnv, "legacy")
	leader.conn.(*bufferConn).write.Reset()
	member.conn.(*bufferConn).write.Reset()
	if err := service.handleDungeonBackToVillage(leader, nil); err != nil {
		t.Fatalf("legacy leader selector op132: %v", err)
	}
	if leader.conn.(*bufferConn).write.Len() == 0 {
		t.Fatal("legacy tier must still answer the leader")
	}
	if got := member.conn.(*bufferConn).write.Len(); got != 0 {
		t.Fatalf("legacy tier must reproduce the stuck member, emitted %d bytes", got)
	}
	if !member.enterSelectDungeonContextSent {
		t.Fatal("legacy tier must leave the member's select context untouched")
	}
}

// TestPartySelectReturnFanOutNeverStealsAMemberThatIsInsideTheDungeon 钉住窄口径：
// 队员已经持有副本运行时（在图里）时，扇出一个字节都不发
// —— 那是通关回城/放弃回城的地盘。
func TestPartySelectReturnFanOutNeverStealsAMemberThatIsInsideTheDungeon(t *testing.T) {
	service, leader, member := partySelectReturnFixture(t)
	t.Setenv(dungeonPartySelectReturnFanoutEnv, "")
	// 借一套**真的带 Session 的**副本文档运行时，让 `holds_a_dungeon_runtime` 成立。
	_, _, otherRuntime := newBackToVillageRuntime(t)
	member.dungeon.mu.Lock()
	member.dungeon.runtime = otherRuntime
	member.dungeon.mu.Unlock()

	memberConn := member.conn.(*bufferConn)
	memberConn.write.Reset()
	if sent := service.fanOutDungeonSelectReturnToPartyMembers(leader, "test_fanout_guard"); sent != 0 {
		t.Fatalf("fanout delivered=%d want 0 while the member holds a dungeon runtime", sent)
	}
	if got := memberConn.write.Len(); got != 0 {
		t.Fatalf("fanout must not steal an in-dungeon member, emitted %d bytes", got)
	}
	if !member.enterSelectDungeonContextSent {
		t.Fatal("the in-dungeon member's select context must be untouched")
	}
}

// TestPartySelectReturnFanoutModeReadsOnlyTheOffLiterals 钉住开关语义：
// 未设置/空/非法值 = 修复档；只有 off/0/false/no/legacy 才回退。
func TestPartySelectReturnFanoutModeReadsOnlyTheOffLiterals(t *testing.T) {
	for _, value := range []string{"", " ", "on", "keep", "party", "1", "LEGACYX"} {
		t.Setenv(dungeonPartySelectReturnFanoutEnv, value)
		if !dungeonPartySelectReturnFanoutEnabled() {
			t.Fatalf("value=%q must stay in the shipped (fan-out) tier", value)
		}
	}
	for _, value := range []string{"off", "0", "false", "no", "legacy", " OFF "} {
		t.Setenv(dungeonPartySelectReturnFanoutEnv, value)
		if dungeonPartySelectReturnFanoutEnabled() {
			t.Fatalf("value=%q must roll back to the stuck-member behaviour", value)
		}
	}
}
