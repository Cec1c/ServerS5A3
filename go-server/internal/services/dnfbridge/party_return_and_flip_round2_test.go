package dnfbridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件钉住第二轮三条报障的修复（falsify-first：每一条都能被"把开关设成回退档"
// 或"把修复点中和"打红）：
//
//	(3) 时装：**每一次**进图都必须投影队友的完整外观（含 subtype0 载体），
//	    不能只有连接期内的第一次；见 dungeon_party_peer_projection_generation.go。
//	(4) 通关回城：队伍保留 + **每个成员都被带回城镇**（服务端扇出）+
//	    回城后**不需要**离城再进城就能互相看见；见
//	    dungeon_party_completion_return_fanout.go / _town_presence.go。
//	(7) 翻牌：队友收到的那一帧必须把"翻开的牌位"归属到**翻牌人的队伍槽位**，
//	    而不是恒 0（恒 0 = 收包者自己）；见 dungeon_party_card_flip_row_model.go。

// ── (3) 时装：第二次进图必须和第一次等价 ─────────────────────────────────────

// peerProjectionEntryFixture 造"两人一队 + 同一场副本"的最小夹具，并显式把两个会话
// 摆成"刚登录、还没有任何投影世代"的状态（现场 char1/char6 的连接期起点）。
func peerProjectionEntryFixture(t *testing.T) (*Service, *gameSession, *gameSession) {
	t.Helper()
	service, leader, follower, _ := peerSubtype0Fixture(t)
	for _, session := range []*gameSession{leader, follower} {
		session.dungeonPartyRunMu.Lock()
		session.dungeonPartyRun = dungeonPartyRunKey{}
		session.dungeonPartyRunValid = false
		session.dungeonPeerProjected = nil
		session.dungeonOwnerOverrideLogged = nil
		session.dungeonPeerActorResent = nil
		session.dungeonPartyRunMu.Unlock()
	}
	return service, leader, follower
}

// enterDungeonRun 走**真实写入点** `recordDungeonPartyRunKey`（`scene_post_start_map.go:578`
// 的唯一调用）记录这一场的运行键，返回本次的运行键。
func enterDungeonRun(t *testing.T, service *Service, sessions []*gameSession, seed uint32) dungeonPartyRunKey {
	t.Helper()
	runtime := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 9}, MazeIndex: 0, Seed: seed}
	scene := worldmap.DungeonRoomScene{
		Coordinate: worldmap.RoomCoordinate{X: 0, Y: 1},
		Map:        worldmap.ResolvedMap{Map: worldmap.Map{ID: 58569}},
	}
	for _, session := range sessions {
		service.recordDungeonPartyRunKey(session, runtime, scene)
	}
	key, ok := service.currentDungeonPartyRunKey(sessions[0])
	if !ok {
		t.Fatalf("char %d has no run key after landing", sessions[0].selectedCharacterID)
	}
	return key
}

// TestPeerAppearanceProjectionFiresOnEveryDungeonEntry 是操作者第 3 条的判定用例：
// **两个连续进图周期都必须发出队友外观载体**，而不是只有第一次。
//
// 现场（packet_log.txt）：第一次进图 instance 1/party 3 两个方向都在，
// 16:10:40 之后（回城 + 再次进图）`party_dungeon_shared_loading_barrier_*` 一条都没有。
func TestPeerAppearanceProjectionFiresOnEveryDungeonEntry(t *testing.T) {
	run := func(t *testing.T, generationSwitch string) (firstCarriers, secondCarriers int, log string) {
		t.Helper()
		t.Setenv(dungeonPartyPeerProjectionGenerationEnv, generationSwitch)
		logPath := filepath.Join(t.TempDir(), "packet.log")
		logger, err := openPacketLogger(logPath)
		if err != nil {
			t.Fatal(err)
		}
		service, leader, follower := peerProjectionEntryFixture(t)
		service.packetLog = logger

		// 第一次进图：两个会话都落位。
		enterDungeonRun(t, service, []*gameSession{leader, follower}, 7)
		if err := service.projectDungeonPartyPeerActors(leader, "entry1_party_peer_copresence"); err != nil {
			t.Fatalf("entry 1 projection: %v", err)
		}
		firstCarriers = strings.Count(testPacketLogText(t, service), "appearance_subtype0_sent=true")
		firstBytes := leader.conn.(*bufferConn).write.Len() + follower.conn.(*bufferConn).write.Len()
		if firstBytes == 0 {
			t.Fatal("entry 1 projected no peer actor bytes at all")
		}
		leader.conn.(*bufferConn).write.Reset()
		follower.conn.(*bufferConn).write.Reset()

		// 回城：现场走的是"放弃回城 ⇒ 解散"，两处都调既有清键点
		// （`dungeon_party_run_key_town_reset.go:110` = 城镇上下文收口）。
		// 这一步就是"上一条运行键被作废、投影集合必须跟着换代"的那一下。
		// 先按真实回城把副本运行时摘掉（那道收口只对"已无活跃副本运行时"的会话动手）。
		for _, session := range []*gameSession{leader, follower} {
			session.dungeon.mu.Lock()
			session.dungeon.runtime = nil
			session.dungeon.mu.Unlock()
			service.forgetStaleDungeonPartyRunKeyOutsideDungeon(session, "test_town_return")
		}
		if _, ready := service.currentDungeonPartyRunKey(leader); ready {
			t.Fatal("the town return did not invalidate the run key")
		}

		// 第二次进图：新的种子 ⇒ 新的运行键。
		enterDungeonRun(t, service, []*gameSession{leader, follower}, 8)
		if err := service.projectDungeonPartyPeerActors(leader, "entry2_party_peer_copresence"); err != nil {
			t.Fatalf("entry 2 projection: %v", err)
		}
		secondCarriers = strings.Count(testPacketLogText(t, service), "appearance_subtype0_sent=true") - firstCarriers
		secondBytes := leader.conn.(*bufferConn).write.Len() + follower.conn.(*bufferConn).write.Len()
		if secondBytes == 0 && secondCarriers == 0 {
			t.Logf("entry 2 wrote nothing (leader=%d follower=%d)",
				leader.conn.(*bufferConn).write.Len(), follower.conn.(*bufferConn).write.Len())
		}
		if err := logger.Close(); err != nil {
			t.Fatal(err)
		}
		logged, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		return firstCarriers, secondCarriers, string(logged)
	}

	first, second, log := run(t, "")
	if first != 1 || second != 1 {
		t.Fatalf(
			"peer appearance carriers per entry=%d/%d want 1/1 (the second entry must be equivalent to the first)\n%s",
			first, second, log,
		)
	}
	if !strings.Contains(log, "kind=game-party-dungeon-peer-projection-generation-reset") {
		t.Fatalf("the town return did not end the peer projection generation\n%s", log)
	}
	if !strings.Contains(log, "switch="+dungeonPartyPeerProjectionGenerationEnv) ||
		!strings.Contains(log, "mode=on") {
		t.Fatalf("generation reset event does not name the switch/mode\n%s", log)
	}

	// 回退档：集合跨世代残留 ⇒ 第二次进图一个字节都不发（现场缺陷逐字节复现）。
	legacyFirst, legacySecond, legacyLog := run(t, "legacy")
	if legacyFirst != 1 {
		t.Fatalf("legacy entry 1 carriers=%d want 1", legacyFirst)
	}
	if legacySecond != 0 {
		t.Fatalf("legacy entry 2 carriers=%d want 0 (the stale generation must suppress it)\n%s",
			legacySecond, legacyLog)
	}
	if strings.Contains(legacyLog, "kind=game-party-dungeon-peer-projection-generation-reset") {
		t.Fatalf("legacy mode logged a generation reset\n%s", legacyLog)
	}
}

// markPeerSceneStableForTest 把"这一方的场景重建已经收尾"三面旗立起来
// （与 `scene_finish_loading.go:32-51/169-195` 同一组），用于复现"先落位的一方"。
func markPeerSceneStableForTest(session *gameSession) {
	session.currentFinishLoadingStateSent = true
	session.currentFinishLoadingCompletionSent = true
	session.postFinishLoadingPlayerStateSent = true
}

// TestPeerAppearanceProjectionSurvivesDisbandAndReForm 是操作者追加报障
// 「重新组队第二次进图 时装丢失」的判定用例，走**完整一圈**：
//
//	组队 → 进图（两次落位）→ 放弃出图（解散 + 清键 + 换代）→ **重新组队** → 再进图
//
// 判据全部落在**发出去的帧**上：
//   - 两次进图都必须出现队友的完整外观载体（`appearance_subtype0_sent=true`，即克隆的
//     subtype0 列表 + mode0 里的 8 行链接）；
//   - 第二次进图时，"先落位的一方"（现场 = 队长，要看队员克隆时装的那一方）场景已经收尾
//     ⇒ 后落位的一方必须**再推一次**（`game-party-dungeon-peer-actor-landing-resend
//     decision=resent delivered=true`），并且对方连接上必须真的多出一整套
//     `mode0 + mode1 + subtype0` 帧；
//   - 回退档：不补推（该方连接上只有第一套帧），但两次进图的外观载体照旧 —— 逐字节回到
//     "只有先落位一方拿不到补发"的今天。
func TestPeerAppearanceProjectionSurvivesDisbandAndReForm(t *testing.T) {
	run := func(t *testing.T, landingSwitch string) (entry1Carriers, entry2Carriers, landingResends, leaderEntry2Mode2 int, log string) {
		t.Helper()
		t.Setenv(dungeonPartyPeerActorLandingResendEnv, landingSwitch)
		logPath := filepath.Join(t.TempDir(), "packet.log")
		logger, err := openPacketLogger(logPath)
		if err != nil {
			t.Fatal(err)
		}
		service, leader, follower := peerProjectionEntryFixture(t)
		service.packetLog = logger
		leaderConn := leader.conn.(*bufferConn)
		followerConn := follower.conn.(*bufferConn)

		// ── 第一圈：两人都落位，后落位的一方把两个方向补齐 ──────────────────────
		enterDungeonRun(t, service, []*gameSession{leader, follower}, 7)
		if err := service.projectDungeonPartyPeerActors(follower, "entry1_party_peer_copresence"); err != nil {
			t.Fatalf("entry 1 projection: %v", err)
		}
		entry1Carriers = strings.Count(testPacketLogText(t, service), "appearance_subtype0_sent=true")
		if entry1Carriers != 1 {
			t.Fatalf("entry 1 carriers=%d want 1 (the first entry must stay unchanged)", entry1Carriers)
		}

		// ── 放弃出图：解散 ⇒ 既有清键点把两个人一起清掉并结束投影世代 ─────────────
		for _, session := range []*gameSession{leader, follower} {
			session.dungeon.mu.Lock()
			session.dungeon.runtime = nil
			session.dungeon.mu.Unlock()
			service.forgetStaleDungeonPartyRunKeyOutsideDungeon(session, "test_back_to_village")
		}
		if _, ready := service.currentDungeonPartyRunKey(leader); ready {
			t.Fatal("the abandon return did not invalidate the run key")
		}

		// ── 重新组队：新的队伍号 + 新的运行键（不同种子） ────────────────────────
		reformed := runtimePartyStateSnapshot(leader)
		reformed.PartyID++
		for _, session := range []*gameSession{leader, follower} {
			storeRuntimePartyState(session, reformed)
		}
		enterDungeonRun(t, service, []*gameSession{leader, follower}, 8)
		// 先落位的一方（队长）场景已经收尾；后落位的一方（队员）再落位并投影。
		markPeerSceneStableForTest(leader)
		leaderConn.write.Reset()
		followerConn.write.Reset()
		before := strings.Count(testPacketLogText(t, service), "appearance_subtype0_sent=true")
		beforeResends := strings.Count(testPacketLogText(t, service), "kind=game-party-dungeon-peer-actor-landing-resend")
		if err := service.projectDungeonPartyPeerActors(follower, "entry2_party_peer_copresence"); err != nil {
			t.Fatalf("entry 2 projection: %v", err)
		}
		log = testPacketLogText(t, service)
		entry2Carriers = strings.Count(log, "appearance_subtype0_sent=true") - before
		landingResends = strings.Count(log, "kind=game-party-dungeon-peer-actor-landing-resend") - beforeResends
		for _, frame := range splitUpperFrames(t, leaderConn.write.Bytes()) {
			if frame.msgID == 2 {
				leaderEntry2Mode2++
			}
		}
		if err := logger.Close(); err != nil {
			t.Fatal(err)
		}
		logged, readErr := os.ReadFile(logPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return entry1Carriers, entry2Carriers, landingResends, leaderEntry2Mode2, string(logged)
	}

	first, second, resends, mode2Frames, log := run(t, "")
	if first != 1 {
		t.Fatalf("entry 1 carriers=%d want 1", first)
	}
	// 修复档：第二次进图 = 正常投影 1 条 + 给"先落位一方"的补推 1 条。
	if second != 2 {
		t.Fatalf("entry 2 carriers=%d want 2 (normal projection + landing resend)\n%s", second, log)
	}
	if !logLineContaining(log, "kind=game-party-dungeon-peer-actor-landing-resend", "decision=deferred") {
		t.Fatalf("the first entry must defer the landing resend (the peer scene is still rebuilding)\n%s", log)
	}
	if resends != 1 {
		t.Fatalf("landing resend events=%d want 1\n%s", resends, log)
	}
	for _, want := range []string{
		"kind=game-party-dungeon-peer-actor-landing-resend",
		"peer_scene_stable=true",
		"decision=resent",
		"delivered=true",
		"run_key_seed=8",
		"appearance_event_source=",
		"switch=" + dungeonPartyPeerActorLandingResendEnv,
		"mode=on",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("landing resend event is missing %q\n%s", want, log)
		}
	}
	// 字节级：被补推的一方（队长）连接上必须有**两套** mode0/mode1/subtype0。
	if mode2Frames != 8 {
		t.Fatalf("the already-landed member received %d msg_id=2 frames want 8 (two full carriers with clone replay)\n%s",
			mode2Frames, log)
	}

	// 回退档：不退还名额、不补推 —— 先落位一方只有一套帧，但两次进图的外观载体照旧。
	legacyFirst, legacySecond, legacyResends, legacyMode2, legacyLog := run(t, "legacy")
	if legacyFirst != 1 || legacySecond != 1 {
		t.Fatalf("legacy carriers=%d/%d want 1/1\n%s", legacyFirst, legacySecond, legacyLog)
	}
	if legacyResends != 0 {
		t.Fatalf("legacy mode logged %d landing resends\n%s", legacyResends, legacyLog)
	}
	if legacyMode2 != 4 {
		t.Fatalf("legacy landing-resend mode sent %d msg_id=2 frames to the already-landed member want 4 (one carrier with clone replay)\n%s",
			legacyMode2, legacyLog)
	}
}

// ── (4) 通关回城：扇出 + 回城同屏 ───────────────────────────────────────────

// recordCompletionReturnRunKeys 按唯一写入点 `recordDungeonPartyRunKey`
// （`scene_post_start_map.go:578`）给两名成员都记上本场运行键，返回后即可断言
// "通关回城有没有把本会话的运行键作废"。
func recordCompletionReturnRunKeys(t *testing.T, fixture completionReturnFixture) {
	t.Helper()
	for _, entry := range []struct {
		session *gameSession
		runtime *runtimeDungeonState
	}{
		{fixture.leader, fixture.leaderRuntime},
		{fixture.member, fixture.memberRuntime},
	} {
		scene, ok := entry.runtime.Session.Scene()
		if !ok {
			t.Fatalf("char %d runtime has no scene", entry.session.selectedCharacterID)
		}
		fixture.service.recordDungeonPartyRunKey(entry.session, entry.runtime, scene)
		if _, ready := fixture.service.currentDungeonPartyRunKey(entry.session); !ready {
			t.Fatalf("char %d has no run key after landing", entry.session.selectedCharacterID)
		}
	}
}

// registerCompletionReturnTownPresence 把两名成员按**生产同一条键**
// （`currentTownPresenceChannelID(session)`）登记进他们所在的城镇区域 —— 夹具直接
// `EnterArea` 时没有填频道，桶不同会让"同屏"永远找不到对端（与线上无关，纯夹具前置）。
func registerCompletionReturnTownPresence(t *testing.T, fixture completionReturnFixture) {
	t.Helper()
	for _, session := range []*gameSession{fixture.leader, fixture.member} {
		fixture.service.onlinePlayers.LeaveArea(session.selectedCharacterID)
		fixture.service.onlinePlayers.EnterArea(&onlinePlayerInfo{
			CharacterID: session.selectedCharacterID,
			TownID:      7,
			AreaID:      3,
			PositionX:   474,
			PositionY:   234,
			Direction:   5,
			AreaState:   3,
			ChannelID:   currentTownPresenceChannelID(session),
			Session:     session,
		})
	}
}

// TestCompletedReturnFansTheSettledMemberOutAndPublishesTownCoPresence 是操作者第 4 条的判定用例：
// 通关回城之后队伍保留、**还在图内且已结算完的队员被服务端一起带回城镇**，
// 并且两个人**不需要**离城/再进城就能互相看见。
func TestCompletedReturnFansTheSettledMemberOutAndPublishesTownCoPresence(t *testing.T) {
	fixture := newCompletionReturnPartyFixture(t)
	// 队员已经消费完自己的结算结果屏 —— 与客户端接受它自己 op72 的那道闸门同一判据
	// （`dungeon_card_flow.go:290 runtime.settlementClearRewardSent`）。
	fixture.memberRuntime.settlementClearRewardSent = true
	fixture.leaderConn.write.Reset()
	fixture.memberConn.write.Reset()

	// 现场口径：队长 16:14:33 点返回城镇时，**队员那一条 run 也已经是 completed**
	// （它 16:14:17 就翻完了两行牌 = 结算计划已冻结；16:14:45 它自己那条 op42 也是按
	// completed_town_route 提交的，`dungeon_return_town.go:493` 的闸门要求 completed）。
	if err := fixture.memberRuntime.Session.CompleteCurrentRoom(); err != nil {
		t.Fatalf("complete the member run: %v", err)
	}
	recordCompletionReturnRunKeys(t, fixture)
	// 两名角色的"城镇 actor owner 上下文"在生产里由登录/过图提交
	// （`character_protocol_constants.go:38`）；本夹具直接建会话，这里补齐，
	// 否则 `sendTownRemoteActorState` 会按设计 fail-closed（owner==0 ⇒ 不建 actor）。
	for _, session := range []*gameSession{fixture.leader, fixture.member} {
		session.townMu.Lock()
		// 两个字段都要提交：`connectionTownActorOwnerChannel` 是登录/过图那条真源
		// （`character_protocol_constants.go:38` 的返回值），`townActorOwnerChannel`
		// 是它落到会话上的那份（`dungeon_return_town.go:511` 会按前者重写后者）。
		session.connectionTownActorOwnerChannel = byte(session.channel.ID)
		session.townActorOwnerChannel = byte(session.channel.ID)
		session.townMu.Unlock()
	}
	registerCompletionReturnTownPresence(t, fixture)

	fixture.completeRunAndReturnToTown(t, fixture.leader, fixture.leaderRuntime)

	log := testPacketLogText(t, fixture.service)
	for _, want := range []string{
		"kind=game-party-dungeon-completion-return-party-kept",
		"kind=game-party-dungeon-completion-return-fanout",
		"member_char_id=100",
		"decision=fan_out",
		"delivered=true",
		"member_settlement_clear_reward_sent=true",
		"switch=" + dungeonPartyCompletionReturnFanoutEnv,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("completion-return fan-out event is missing %q\n%s", want, log)
		}
	}
	// 队员被带回城镇：它自己的运行时被摘掉（= 出了本场实例），并收到自己那条
	// completed_town_route 的城镇帧。
	if fixture.member.dungeon.runtime != nil {
		t.Fatal("the settled member was not brought out of the instance by the leader's return")
	}
	if fixture.memberConn.write.Len() == 0 {
		t.Fatal("the member received no town-route packets from the fan-out")
	}
	if !logLineContaining(log,
		"kind=game-dungeon-completed-town-route-committed",
		"char_id=100",
	) {
		t.Fatalf("the member's fan-out did not commit its own completed town route\n%s", log)
	}
	// 两个人都在城镇里被登记 ⇒ 同屏在**没有任何离城/再进城**的情况下发生。
	if fixture.leaderConn.write.Len() == 0 {
		t.Fatalf("the leader received no town packets\n%s", log)
	}
	if !strings.Contains(log, "kind=game-town-copresence-actor-state-send") {
		t.Fatalf("the party was not re-projected in town after the completed return\n%s", log)
	}
	for _, want := range []string{
		"kind=game-dungeon-completed-town-route-town-presence-published",
		"char_id=99",
		"char_id=100",
		"stale_run_key_cleared=true",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("town presence publishing is missing %q\n%s", want, log)
		}
	}
	// 队伍仍在：中央管理器里两名成员都还在。
	for _, characterID := range []uint16{completionReturnLeaderCharacterID, completionReturnFollowerCharacterID} {
		if _, found := fixture.service.runtimePartyManagerForService().SnapshotByUser(characterID, 0); !found {
			t.Fatalf("char %d left the party on the completed return", characterID)
		}
	}
	if strings.Contains(log, "kind=game-party-dungeon-leader-exit-disband") {
		t.Fatalf("the completed return ran the disband path\n%s", log)
	}
}

// TestCompletedReturnFanoutRollbackLiteralKeepsTheMemberInTheInstance 钉住回退档：
// 两个新开关都设成回退字面量时，队员留在图内、不登记城镇、不清理运行键 —— 与改动前逐字节相同。
func TestCompletedReturnFanoutRollbackLiteralKeepsTheMemberInTheInstance(t *testing.T) {
	t.Setenv(dungeonPartyCompletionReturnFanoutEnv, "legacy")
	t.Setenv(dungeonPartyCompletionReturnTownCopresenceEnv, "legacy")
	t.Setenv(dungeonPartyPeerProjectionGenerationEnv, "legacy")
	fixture := newCompletionReturnPartyFixture(t)
	fixture.memberRuntime.settlementClearRewardSent = true
	if err := fixture.memberRuntime.Session.CompleteCurrentRoom(); err != nil {
		t.Fatalf("complete the member run: %v", err)
	}
	recordCompletionReturnRunKeys(t, fixture)
	fixture.leaderConn.write.Reset()
	fixture.memberConn.write.Reset()

	fixture.completeRunAndReturnToTown(t, fixture.leader, fixture.leaderRuntime)

	if fixture.member.dungeon.runtime != fixture.memberRuntime {
		t.Fatal("the rollback literal still moved the member out of its instance")
	}
	log := testPacketLogText(t, fixture.service)
	for _, unwanted := range []string{
		"kind=game-party-dungeon-completion-return-fanout",
		"kind=game-dungeon-completed-town-route-town-presence-published",
		"kind=game-party-dungeon-peer-projection-generation-reset",
	} {
		if strings.Contains(log, unwanted) {
			t.Fatalf("rollback literal still logged %q\n%s", unwanted, log)
		}
	}
	if fixture.memberConn.write.Len() != 0 {
		t.Fatalf("rollback literal wrote %d bytes to the member", fixture.memberConn.write.Len())
	}
	if _, ready := fixture.service.currentDungeonPartyRunKey(fixture.leader); !ready {
		t.Fatal("the rollback literal cleared the leader's run key")
	}
	// 两个开关的默认档与回退字面量口径。
	if dungeonPartyCompletionReturnFanoutEnabled() {
		t.Fatal("the legacy literal must disable the fan-out")
	}
	if dungeonPartyCompletionReturnTownCopresenceEnabled() {
		t.Fatal("the legacy literal must disable the town-presence publishing")
	}
	if dungeonPartyPeerProjectionGenerationEnabled() {
		t.Fatal("the legacy literal must disable the peer-projection generation reset")
	}
	t.Setenv(dungeonPartyCompletionReturnFanoutEnv, "")
	if !dungeonPartyCompletionReturnFanoutEnabled() {
		t.Fatal("the default fan-out mode must be the fixed one")
	}
	t.Setenv(dungeonPartyCompletionReturnTownCopresenceEnv, "")
	if !dungeonPartyCompletionReturnTownCopresenceEnabled() {
		t.Fatal("the default town-presence mode must be the fixed one")
	}
	t.Setenv(dungeonPartyPeerProjectionGenerationEnv, "")
	if !dungeonPartyPeerProjectionGenerationEnabled() {
		t.Fatal("the default peer-projection generation mode must be the fixed one")
	}
}

// ── (7) 翻牌：行状态字节 = 翻牌人的队伍槽位 ─────────────────────────────────

// TestCardFlipRowCarriesTheFlipperPartySlot 钉住 op71 的行语义：
// 下标 = 牌位（`request.ValueB`），值 = **翻牌人的队伍槽位**（厂商
// `CardRewardCoordinator.cs:1073-1142 freeSelectors[selectedFree] = partySlot`）。
// 回退档必须逐字节回到"值恒 0"，而"值恒 0"正是队友的牌被算到收包者头上的成因。
func TestCardFlipRowCarriesTheFlipperPartySlot(t *testing.T) {
	// 开关口径：会话在队伍名册里的下标才是客户端认的那个"队伍槽位"。
	// 名册 = 既有夹具 `newPartyFrameSessions`（1001 在 0 号位、1002 在 1 号位）。
	for _, test := range []struct {
		value string
		want  byte
	}{
		{value: "", want: 1},
		{value: dungeonPartyCardFlipRowModelVendor.String(), want: 1},
		{value: "ON", want: 1},
		{value: dungeonPartyCardFlipRowModelLegacy, want: 0},
		{value: " LEGACY ", want: 0},
	} {
		t.Setenv(dungeonPartyCardFlipRowModelEnv, test.value)
		_, _, follower, state := newPartyFrameSessions(t, 10, currentSceneObjectContext)
		storeRuntimePartyState(follower, state)
		if got := currentDungeonCardFlipRowStateValue(follower); got != test.want {
			t.Fatalf("read(%q) row state value=%d want %d", test.value, got, test.want)
		}
	}
	t.Setenv(dungeonPartyCardFlipRowModelEnv, "")

	build := func(t *testing.T, flipPartySlot byte) []byte {
		t.Helper()
		state, err := newDungeonCardState(dungeonCardRewardPlan{
			ID:          "round2-plan",
			CharacterID: "100",
			Source:      "round2_test",
			Sides: [dungeonCardSideCount]dungeonCardRewardBundle{
				{Gold: 429},
				{Gold: 466},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		runtime := &runtimeDungeonState{
			Dungeon:                   worldmap.Dungeon{ID: 310},
			settlementCardRewardState: state,
			settlementCardLayoutSent:  true,
			settlementClearRewardSent: true,
		}
		runtime.settlementCardSideSelectionKnown[dungeonCardSideFree] = true
		runtime.settlementCardSideMember[dungeonCardSideFree] = 1
		runtime.settlementCardSideSelectionKnown[dungeonCardSidePaid] = true
		runtime.settlementCardSideMember[dungeonCardSidePaid] = 1
		slots, _ := currentDungeonSelectedCardSlots(runtime, flipPartySlot)
		body, err := buildCurrentDungeonOp71SuccessBody(slots)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}

	// 线上真值：队员 char6 = 槽位 1，牌位也是 1（op71 请求体 `{0,1}` / `{1,1}`）。
	fixed := build(t, 1)
	if fixed[0] != 1 {
		t.Fatalf("op71 body must start with the success byte: %x", fixed)
	}
	if state := op71SlotState(t, fixed, 0, dungeonCardSideFree); state != 0xff {
		t.Fatalf("an untouched card slot must stay unflipped: state=%d body=%x", state, fixed)
	}
	// 牌位 1 落在第 1 槽：free 行 = 1（翻牌人槽位），paid 行 = 1。
	assertOp71SlotFlipped(t, fixed, 1, dungeonCardSideFree, 1, "fix free row of the flipper")
	assertOp71SlotFlipped(t, fixed, 1, dungeonCardSidePaid, 1, "fix paid row of the flipper")

	// 回退档写进去的值恒 0（= 改动前的字节）：这一行是"队友的牌算到 0 号位"的复现。
	legacy := build(t, 0)
	assertOp71SlotFlipped(t, legacy, 1, dungeonCardSideFree, 0, "legacy free row keeps today's constant 0")
	assertOp71SlotFlipped(t, legacy, 1, dungeonCardSidePaid, 0, "legacy paid row keeps today's constant 0")
	// 单人/槽位 0 在修复档下也必须与回退档逐字节相同（不改单人的字节）。
	if solo := build(t, 0); string(solo) != string(legacy) {
		t.Fatalf("party slot 0 must stay byte-identical to the legacy body\n fix=%x\n legacy=%x", solo, legacy)
	}
	if string(fixed) == string(legacy) {
		t.Fatalf("the fixed body must differ from the legacy body only in the flipper slot value: %x", fixed)
	}
	// 非法值落回修复档。
	if got := readDungeonPartyCardFlipRowModel("LEGACY "); got != dungeonPartyCardFlipRowModelLegacyValue {
		t.Fatalf("legacy literal with case/space resolved to %s", got)
	}
	if got := readDungeonPartyCardFlipRowModel(""); got != dungeonPartyCardFlipRowModelVendor {
		t.Fatalf("empty literal resolved to %s want vendor", got)
	}
	if got := readDungeonPartyCardFlipRowModel("bogus"); got != dungeonPartyCardFlipRowModelVendor {
		t.Fatalf("an unknown literal must fall back to the fixed vendor model, got %s", got)
	}
}

// op71SlotState 是测试侧对 op71 体的独立解析：体 = 1 字节成功 + 8 槽 ×
// (StateA, StateB, count, count×8B, terminal)；返回第 wireIndex 槽指定行的状态字节。
func op71SlotState(t *testing.T, body []byte, wireIndex int, side dungeonCardSide) byte {
	t.Helper()
	if len(body) < 1 {
		t.Fatalf("empty op71 body")
	}
	offset := 1
	for index := 0; index < dungeonCardWireSlotCount; index++ {
		if offset+3 > len(body) {
			t.Fatalf("truncated op71 body=%x", body)
		}
		stateA, stateB, count := body[offset], body[offset+1], int(body[offset+2])
		offset += 3 + count*8 + 1
		if index != wireIndex {
			continue
		}
		if side == dungeonCardSidePaid {
			return stateB
		}
		return stateA
	}
	t.Fatalf("op71 body has no slot %d (body=%x)", wireIndex, body)
	return 0
}

// TestPartyCardFlipProjectionCarriesTheFlipperSlotToThePartner 端到端：
// 走真实入口 `selectCurrentDungeonCardLocked`，队友收到的那一帧里
// **翻牌人自己的牌位必须归属到翻牌人的队伍槽位**（而不是 0 = 收包者）。
func TestPartyCardFlipProjectionCarriesTheFlipperSlotToThePartner(t *testing.T) {
	service, leader, follower, leaderRuntime, followerRuntime, scene := partyDungeonInstanceFixture(t, 31, 909)
	leader.dungeon.runtime = leaderRuntime
	follower.dungeon.runtime = followerRuntime
	if service.adoptPartyDungeonInstance(leader, leaderRuntime, scene) == nil {
		t.Fatal("leader did not adopt the shared instance")
	}
	if service.adoptPartyDungeonInstance(follower, followerRuntime, scene) == nil {
		t.Fatal("follower did not adopt the shared instance")
	}
	freezeDungeonCardStateForTest(t, leaderRuntime, "301")
	freezeDungeonCardStateForTest(t, followerRuntime, "302")
	for _, runtime := range []*runtimeDungeonState{leaderRuntime, followerRuntime} {
		runtime.settlementCardLayoutSent = true
		runtime.settlementClearRewardSent = true
		runtime.advanceSettlementPhase(currentDungeonSettlementPhaseCardsRevealed)
	}
	leaderConn := leader.conn.(*bufferConn)
	followerConn := follower.conn.(*bufferConn)
	leaderConn.write.Reset()
	followerConn.write.Reset()
	// 判读串必须在测试里也有钉子（`partyDungeonInstanceFixture` 默认不装包日志）。
	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service.packetLog = logger

	// 队友（槽位 1）翻免费行第 2 张牌（牌位 1）—— 现场 char6 的 `{0,1}`。
	slot := byte(1)
	followerRuntime.settlementCardSideSelectionKnown[dungeonCardSideFree] = false
	follower.dungeon.mu.Lock()
	flipErr := service.selectCurrentDungeonCardLocked(
		follower, followerRuntime, dungeonCardSideFree, slot, "round2_follower_free_flip",
	)
	follower.dungeon.mu.Unlock()
	if flipErr != nil {
		t.Fatalf("follower free flip: %v", flipErr)
	}
	leaderPeer, _ := splitGameServerUpperPacket(t, leaderConn.write.Bytes())
	if leaderPeer.Header.MsgID != uint16(dnfenum.CmdPacketSelectCard) {
		t.Fatalf("the partner must receive the op71 projection: header=%+v", leaderPeer.Header)
	}
	if got := op71SlotState(t, leaderPeer.Body, int(slot), dungeonCardSideFree); got != 1 {
		t.Fatalf(
			"partner projection of card slot %d state=%d want the flipper's party slot 1 (body=%x)",
			slot, got, leaderPeer.Body,
		)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	log := string(logged)
	for _, want := range []string{
		"kind=game-dungeon-card-flip-party-broadcast",
		"flip_party_slot=1",
		"card_index=1",
		"delivered=1",
		"row_model=" + dungeonPartyCardFlipRowModelVendor.String(),
		"row_model_env=" + dungeonPartyCardFlipRowModelEnv,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("card flip broadcast event is missing %q\n%s", want, log)
		}
	}
}
