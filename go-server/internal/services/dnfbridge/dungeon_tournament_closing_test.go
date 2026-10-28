package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// ─────────────────────────────────────────────────────────────────────────────
// 收尾接线批（任务 #1 / #2 / #3 / #4）的用例
//
// 覆盖：
//   #1 「选奖结束 ⇒ 通关结算」这一跳（冠军才提交；被淘汰永不提交）
//   #2 「玩家死亡 ⇒ 淘汰」（死亡倒计时到期后判定淘汰 vs 回城）
//   #3 458/459 的离线恢复补发 + 3000ms 自动选奖计时器
//   #4 `[required item]` 的入场消耗扣减（桥接层开关 + 拒不静默扣 0）
//
// 每个入口都额外钉住"默认档不可达 / 不改控制流"。
// ─────────────────────────────────────────────────────────────────────────────

// championTournamentRuntimeForTest 造一个已经打到冠军、并绑好 actor sequence 的赛制运行时。
func championTournamentRuntimeForTest(t *testing.T, mapID int64) *tournamentDungeonRuntime {
	t.Helper()
	definition := tournamentFlowFixtureDefinition(mapID)
	runtime, reason, ok := newTournamentDungeonRuntime(&definition, 1, deterministicTournamentNext(11))
	if !ok {
		t.Fatalf("fixture runtime rejected: %s", reason)
	}
	if !runtime.tryBindFirstActorSequence(uint16(firstDungeonMonsterObjectKey)) {
		t.Fatal("bind failed")
	}
	for offset := 0; offset < tournamentRoundCount; offset++ {
		if transition := runtime.tryApplyActorDeath(int(firstDungeonMonsterObjectKey) + offset); !transition.accepted() {
			t.Fatalf("death %d rejected: %+v", offset, transition)
		}
	}
	if !runtime.isChampion() {
		t.Fatalf("fixture did not reach champion: %+v", runtime.snapshot())
	}
	return runtime
}

// tournamentRewardStateForTest 造一份"四张卡都已选完"的冠军奖励态。
func tournamentRewardStateForTest(t *testing.T, selected bool) *tournamentRewardState {
	t.Helper()
	cards := []tournamentCardReward{
		{IsGold: true, GoldAmount: 1234},
		{ItemID: 3329, StackCount: 1},
		{IsGold: true, GoldAmount: 1234},
		{ItemID: 3329, StackCount: 2},
	}
	state, err := newTournamentRewardState(cards, 1, 0, 200000, tournamentFinalRound, true)
	if err != nil {
		t.Fatalf("reward state: %v", err)
	}
	if selected {
		for cardType := byte(0); cardType < tournamentCardTypeCount; cardType++ {
			state.Selected[cardType] = 0
		}
	}
	return &state
}

// ── 任务 #1 ──────────────────────────────────────────────────────────────────

// 389 体的逐段字节账：8 字节头 + 每排（u8 count + count×10）。
// 这条是**独立的字节账核对**，防止"长度断言自己算错"（本用例第一版就把
// 每排算成 21+21 却断言 50，实际每排只占 21，总长 8+21+21=50 与 48 的差异
// 必须靠逐字节 walk 才能定位）。这里把 walk 与期望完全分开写。
func TestTournamentClearRewardBodyByteLedger(t *testing.T) {
	cards := []tournamentCardReward{
		{IsGold: true, GoldAmount: 1234},
		{ItemID: 3329, StackCount: 1},
		{IsGold: true, GoldAmount: 1234},
		{ItemID: 3329, StackCount: 2},
	}
	state, err := newTournamentRewardState(cards, 1, 0, 200000, tournamentFinalRound, true)
	if err != nil {
		t.Fatal(err)
	}
	body := buildTournamentClearRewardBody(state)
	// 手写的期望字节（不调用任何被测函数）。每张卡 = i32 id + i32 数量 + u16 0 = 10 字节：
	//   04                completedRounds
	//   40 0d 03 00       200000 (u32 LE)
	//   00                保留字节
	//   02                 type0 cardCount
	//   00 00 00 00 | d2 04 00 00 | 00 00     金币卡（id=0, amount=1234, pad=0）
	//   01 0d 00 00 | 01 00 00 00 | 00 00     道具卡（id=3329, count=1, pad=0）
	//   02                 type1 cardCount
	//   00 00 00 00 | d2 04 00 00 | 00 00
	//   01 0d 00 00 | 02 00 00 00 | 00 00     道具卡（count=2）
	want := []byte{
		0x04, 0x40, 0x0d, 0x03, 0x00, 0x00,
		0x02,
		0x00, 0x00, 0x00, 0x00, 0xd2, 0x04, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x0d, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x02,
		0x00, 0x00, 0x00, 0x00, 0xd2, 0x04, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x0d, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("389 body len=%d\n got=%x\nwant=%x", len(body), body, want)
	}
	// 固定账（**以逐字节手写的 `want` 为准**）：
	//   6（头：u8 完成轮数 + u32 经验 + u8 保留）
	// + 1 + 2*10（type0：u8 count + 两张卡 × 10 字节） = 21
	// + 1 + 2*10（type1：u8 count + 两张卡 × 10 字节） = 21
	// = 48
	if len(want) != 48 || len(body) != 48 {
		t.Fatalf("389 fixed ledger len=%d/%d want 48", len(want), len(body))
	}
	// walk 也必须从**头之后**（offset 6）开始 —— 头是 1+4+1，不是 8。
	offset := currentTournamentClearRewardBodyHeaderSize
	for cardType := 0; cardType < tournamentCardTypeCount; cardType++ {
		if offset >= len(body) {
			t.Fatalf("389 type%d count byte missing at %d/%d", cardType, offset, len(body))
		}
		count := int(body[offset])
		if count != tournamentCardsPerType {
			t.Fatalf("389 type%d cardCount=%d want=%d at %d", cardType, count, tournamentCardsPerType, offset)
		}
		offset += 1 + count*10
	}
	if offset != len(body) {
		t.Fatalf("389 card section consumed %d of %d bytes", offset, len(body))
	}
	// 世界：第二排两张**道具卡**（真档 120 的冠军下排就是 `[reward item rate]` 抽出来的道具卡）。
	itemCards := []tournamentCardReward{
		{IsGold: true, GoldAmount: 1000},
		{IsGold: true, GoldAmount: 1000},
		{ItemID: 3329, StackCount: 1},
		{ItemID: 3329, StackCount: 1},
	}
	itemState, err := newTournamentRewardState(itemCards, 1, 0, 200000, tournamentFinalRound, true)
	if err != nil {
		t.Fatal(err)
	}
	itemBody := buildTournamentClearRewardBody(itemState)
	if len(itemBody) != len(want) {
		t.Fatalf("gold+item 389 body len=%d want=%d (同一 4 张卡 ⇒ 长度必须相同) body=%x",
			len(itemBody), len(want), itemBody)
	}
}

// 通关提交门：默认档恒放行；vendor 档只有在赛制落定（冠军）之后才放行。
// **被淘汰不是"还没落定"** —— 它落定成 Eliminated，本门放行（真正的拒绝在
// `evaluateCurrentTournamentClearIntentLocked` 里，见下一个用例）。
func TestShouldDeferTournamentClearCommitContract(t *testing.T) {
	if shouldDeferTournamentClearCommit(nil) {
		t.Fatal("nil runtime must not defer")
	}
	if shouldDeferTournamentClearCommit(&runtimeDungeonState{}) {
		t.Fatal("default mode must not defer")
	}
	active := &runtimeDungeonState{
		IgnoreDefaultDungeonClear: true,
		Tournament:                &tournamentDungeonRuntime{currentRound: 1},
	}
	if !shouldDeferTournamentClearCommit(active) {
		t.Fatal("an undecided tournament must defer the dungeon clear commit")
	}
	champion := &runtimeDungeonState{
		IgnoreDefaultDungeonClear: true,
		Tournament:                championTournamentRuntimeForTest(t, 12000),
	}
	if shouldDeferTournamentClearCommit(champion) {
		t.Fatal("a decided championship must not defer (deadlock guard)")
	}
	eliminated := championTournamentRuntimeForTest(t, 12000)
	eliminated.tryEliminate()
	decided := &runtimeDungeonState{IgnoreDefaultDungeonClear: true, Tournament: eliminated}
	if shouldDeferTournamentClearCommit(decided) {
		t.Fatal("an eliminated tournament is a terminal result and must not defer")
	}
	// `IgnoreDefaultDungeonClear == false`（默认档字段值）时该门必须整体失效。
	if shouldDeferTournamentClearCommit(&runtimeDungeonState{Tournament: &tournamentDungeonRuntime{}}) {
		t.Fatal("the gate must be inert while IgnoreDefaultDungeonClear is false")
	}
}

// `TryCreateClearIntent` 的三条硬条件 + "被淘汰永不提交"。
func TestEvaluateCurrentTournamentClearIntentContract(t *testing.T) {
	session := &gameSession{connID: "tournament-clear-intent", selectedCharacterID: 99}

	// 默认档（非锦标赛）：恒放行 —— 调用方控制流不变。
	if decision := evaluateCurrentTournamentClearIntentLocked(session, &runtimeDungeonState{}); !decision.Allowed {
		t.Fatalf("default mode decision = %+v want allowed", decision)
	}
	if decision := evaluateCurrentTournamentClearIntentLocked(session, nil); !decision.Allowed {
		t.Fatalf("nil runtime decision = %+v want allowed", decision)
	}

	vendor := func(tournament *tournamentDungeonRuntime, rewards *tournamentRewardState) *runtimeDungeonState {
		return &runtimeDungeonState{
			IgnoreDefaultDungeonClear: true,
			Tournament:                tournament,
			tournamentReward:          rewards,
		}
	}

	// 赛制还 Active（第 4 轮没打完）⇒ 拒绝。
	undecided := vendor(&tournamentDungeonRuntime{currentRound: 2}, tournamentRewardStateForTest(t, true))
	if decision := evaluateCurrentTournamentClearIntentLocked(session, undecided); decision.Allowed ||
		decision.Reason != "tournament_is_not_decided_yet" {
		t.Fatalf("undecided decision = %+v", decision)
	}

	// 冠军但奖励态还没冻结 ⇒ 拒绝。
	noRewards := vendor(championTournamentRuntimeForTest(t, 12000), nil)
	if decision := evaluateCurrentTournamentClearIntentLocked(session, noRewards); decision.Allowed ||
		decision.Reason != "participant_reward_state_is_not_frozen" {
		t.Fatalf("no-reward decision = %+v", decision)
	}

	// 冠军、奖励态已冻结，但选奖还没选完 ⇒ **仍然放行**（本批解环；理由见
	// `evaluateCurrentTournamentClearIntentLocked` 的注释与
	// `90原神开源0.1.8\...\changlog.md:438-440`：真机客户端要先看到通关
	// 才会进宝箱界面并发 bodyless 457，票据不能反向依赖客户端选奖）。
	partial := vendor(championTournamentRuntimeForTest(t, 12000), tournamentRewardStateForTest(t, false))
	if decision := evaluateCurrentTournamentClearIntentLocked(session, partial); !decision.Allowed {
		t.Fatalf("champion-with-pending-selection decision = %+v want allowed", decision)
	}

	// 冠军 + 选奖结束 ⇒ 放行。
	ready := vendor(championTournamentRuntimeForTest(t, 12000), tournamentRewardStateForTest(t, true))
	if decision := evaluateCurrentTournamentClearIntentLocked(session, ready); !decision.Allowed {
		t.Fatalf("ready decision = %+v want allowed", decision)
	}
	// 幂等：已经提交过 ⇒ 具名拒绝，不重复提交。
	ready.bossDieCheckAccepted = true
	if decision := evaluateCurrentTournamentClearIntentLocked(session, ready); decision.Allowed ||
		decision.Reason != "dungeon_clear_was_already_committed" {
		t.Fatalf("already-committed decision = %+v", decision)
	}

	// **被淘汰：即使把奖励选完也不许提交通关**（厂商 `:593-602` 只记日志）。
	// 注意终止态不可回退：冠军之后再淘汰是 no-op（`TournamentDungeonRuntime.cs:486-488`），
	// 所以这里单独造一个还在 Active 的运行时。
	active := &tournamentDungeonRuntime{
		Definition:        &tournamentDefinition{PartyLimit: 1},
		acceptedSequences: map[int]struct{}{},
		firstSequence:     uint16(firstDungeonMonsterObjectKey),
		currentRound:      3,
	}
	if _, accepted := active.tryEliminate(); !accepted || !active.isEliminated() {
		t.Fatalf("active elimination state = %+v", active.snapshot())
	}
	eliminatedDecision := evaluateCurrentTournamentClearIntentLocked(
		session,
		vendor(active, tournamentRewardStateForTest(t, true)),
	)
	if eliminatedDecision.Allowed || eliminatedDecision.Reason != "tournament_result_is_eliminated" {
		t.Fatalf("eliminated decision = %+v want a named rejection (淘汰绝不产出清关事实)", eliminatedDecision)
	}
}

// clearTournamentRoomForTest 按 op39 路径同一条权威死亡提交把当前房间清掉：// `Room.AnnounceAllActors` → `Room.CommitActorDeathReport`
// （与 `TestBossDieCheckDefersWhileExplicitLayerRemains` 同一套夹具用法）。
// 这是"冠军"的真实前提，也让 `Session.Complete()` 的"当前房间已清"前校验成立。
func clearTournamentRoomForTest(t *testing.T, runtime *runtimeDungeonState) {
	t.Helper()
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatalf("announce start-map actors: %v", err)
	}
	roomSnapshot := runtime.Room.Snapshot()
	if len(roomSnapshot.Monsters) == 0 {
		t.Fatal("fixture room has no monsters")
	}
	for _, monster := range roomSnapshot.Monsters {
		_, cleared, markErr := runtime.Room.CommitActorDeathReport(monster.ObjectKey, runtime.Session)
		if markErr != nil {
			t.Fatalf("commit actor %d death: %v", monster.ObjectKey, markErr)
		}
		if !cleared {
			t.Fatalf("actor %d death did not clear the room", monster.ObjectKey)
		}
	}
}

// 冠军 + 选奖结束 ⇒ 真的提交：run 被推到 Completed，清关 receipt 与 op115/op31 走既有链。
func TestSubmitTournamentClearCommitCompletesRunAndKeepsOwnerFlags(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	service, session, runtime := newBackToVillageRuntime(t)

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	if session.dungeon.runtime != runtime {
		t.Fatal("fixture runtime is not the session owner")
	}
	runtime.Tournament = championTournamentRuntimeForTest(t, runtime.Session.Snapshot().Scene.Map.Map.ID)
	runtime.IgnoreDefaultDungeonClear = true
	runtime.tournamentReward = tournamentRewardStateForTest(t, true)

	snapshot := runtime.Session.Snapshot()
	if snapshot.Run.Status != worldmap.DungeonRunActive {
		t.Fatalf("fixture run status = %s want active", snapshot.Run.Status)
	}
	// 冠军的前提就是"最后一轮的对手也被权威打死了"：先把房间清掉，
	// 否则 `Session.Complete()` 的"当前房间已清"前校验会拒绝（这是既有行为，不是本批引入）。
	clearTournamentRoomForTest(t, runtime)
	targetObjectKey := uint32(runtime.Tournament.boundFirstSequence()) +
		uint32(len(runtime.Tournament.PathActors)) - 1

	if err := service.submitCurrentTournamentClearCommitLocked(
		session, runtime, targetObjectKey, "test_tournament_reward_selection_complete",
	); err != nil {
		t.Fatalf("submit tournament clear commit: %v", err)
	}
	completed := runtime.Session.Snapshot()
	if completed.Run.Status != worldmap.DungeonRunCompleted {
		t.Fatalf("run status = %s want completed (选奖结束必须真的通关，不是停在奖励界面)",
			completed.Run.Status)
	}
	if !runtime.ordinaryFinalRoomClearAccepted || !runtime.bossDieCheckAccepted {
		t.Fatalf("clear owner flags = %+v want both accepted", runtime)
	}
	if !runtime.settlementEntrySent || runtime.settlementPhase < currentDungeonSettlementPhaseClearEnabled {
		t.Fatalf("settlement entry = %+v want op31 already sent", runtime)
	}
	if runtime.bossDieCheckTargetObjectKey != uint16(targetObjectKey) {
		t.Fatalf("target object key = %d want %d (厂商 finalSequence = first + pathActors - 1)",
			runtime.bossDieCheckTargetObjectKey, targetObjectKey)
	}
	if runtime.clearMapCompletionKey == "" {
		t.Fatal("clear-map receipt key must be frozen by the shared completion owner")
	}
	if !runtime.clearMapCompletionPhaseAPersisted {
		t.Fatal("clear-map Phase A must be closed by the shared completion owner")
	}
	// 幂等：再提交一次不会再改任何东西。
	accepted := runtime.bossDieCheckAccepted
	if err := service.submitCurrentTournamentClearCommitLocked(
		session, runtime, targetObjectKey, "test_tournament_clear_replay",
	); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !accepted || !runtime.bossDieCheckAccepted {
		t.Fatal("replay changed the clear owner flags")
	}
}

// 被淘汰 ⇒ 绝不提交：run 保持 Active、receipt 保持空、不产出清关事实。
func TestEliminatedTournamentNeverCommitsAClear(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	service, session, runtime := newBackToVillageRuntime(t)

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	active := &tournamentDungeonRuntime{
		Definition:        &tournamentDefinition{PartyLimit: 1},
		acceptedSequences: map[int]struct{}{},
		firstSequence:     uint16(firstDungeonMonsterObjectKey),
		currentRound:      3,
	}
	if _, accepted := active.tryEliminate(); !accepted {
		t.Fatal("tryEliminate must accept on an active tournament")
	}
	runtime.Tournament = active
	runtime.IgnoreDefaultDungeonClear = true

	decision := evaluateCurrentTournamentClearIntentLocked(session, runtime)
	if decision.Allowed {
		t.Fatalf("eliminated state was allowed to commit a clear: %+v", decision)
	}
	// 即使有人误调提交入口，也要被拒（run 不完成、receipt 不冻结、没有 op31）。
	if err := service.completeCurrentTournamentResultLocked(session, runtime); err != nil {
		t.Fatalf("completeCurrentTournamentResultLocked: %v", err)
	}
	if runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		t.Fatal("an eliminated tournament must not complete the run")
	}
	if runtime.bossDieCheckAccepted || runtime.ordinaryFinalRoomClearAccepted || runtime.settlementEntrySent {
		t.Fatalf("eliminated tournament leaked clear-owner state: %+v", runtime)
	}
	if runtime.clearMapCompletionKey != "" {
		t.Fatalf("eliminated tournament froze a clear receipt: %q", runtime.clearMapCompletionKey)
	}
}

// ── 任务 #2 ──────────────────────────────────────────────────────────────────

// 死亡倒计时到期：默认档回城；vendor 档锦标赛走淘汰/终止态，**不回城**。
func TestResolveCurrentTournamentDeathDeadlineContract(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	queue := newFakeCurrentDungeonDeathTimerQueue()
	service.gameplayTimers = queue

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()

	// 默认档（非锦标赛）：恒回城 —— 这是"不许破坏普通副本死亡回城"的硬断言。
	if action := service.resolveCurrentTournamentDeathDeadline(session, runtime); action != tournamentDeathDeadlineReturnToTown {
		t.Fatalf("default action = %d want ReturnToTown", action)
	}

	// vendor 档 + 锦标赛 + Active：淘汰，进奖励界面。
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	active := &tournamentDungeonRuntime{
		Definition:        &tournamentDefinition{PartyLimit: 1},
		acceptedSequences: map[int]struct{}{},
		firstSequence:     uint16(firstDungeonMonsterObjectKey),
		currentRound:      3,
	}
	runtime.Tournament = active
	runtime.IgnoreDefaultDungeonClear = true
	action := service.resolveCurrentTournamentDeathDeadline(session, runtime)
	if action != tournamentDeathDeadlinePresentTournamentRewards {
		t.Fatalf("active tournament action = %d want PresentTournamentRewards", action)
	}
	if !active.isEliminated() {
		t.Fatal("the death deadline must eliminate the player")
	}
	snapshot := active.snapshot()
	if snapshot.CompletedRounds != 2 {
		t.Fatalf("completed rounds = %d want 2 (厂商 TryEliminate: max(0, CurrentRound-1))",
			snapshot.CompletedRounds)
	}
	// 重复到期：终止态不可回退，仍然是 PresentTournamentRewards（不是回城）。
	if again := service.resolveCurrentTournamentDeathDeadline(session, runtime); again != tournamentDeathDeadlinePresentTournamentRewards {
		t.Fatalf("repeated deadline action = %d want PresentTournamentRewards", again)
	}
	if completed := active.snapshot().CompletedRounds; completed != 2 {
		t.Fatalf("repeated deadline changed completed rounds to %d", completed)
	}
}

// 夺冠之后玩家在颁奖期间死亡：仍然不回城（厂商 `:97` 的 IsTournamentTerminated 分支），
// 但**不能**把冠军状态改坏（冠军的 clear intent 仍然可以提交）。
func TestChampionDeathDeadlineDoesNotReturnToTownOrBreakTheTitle(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	service, session, runtime := newBackToVillageRuntime(t)
	service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	champion := championTournamentRuntimeForTest(t, runtime.Session.Snapshot().Scene.Map.Map.ID)
	runtime.Tournament = champion
	runtime.IgnoreDefaultDungeonClear = true
	if action := service.resolveCurrentTournamentDeathDeadline(session, runtime); action != tournamentDeathDeadlinePresentTournamentRewards {
		t.Fatalf("champion death action = %d want PresentTournamentRewards", action)
	}
	if !champion.isChampion() {
		t.Fatal("the champion terminal result must be immutable")
	}
	if champion.isEliminated() {
		t.Fatal("a champion must never be converted into an elimination")
	}
}

// 到期回调本身：锦标赛档下**不**产生回城包，且取消死亡倒计时。
func TestTournamentDeathTimerFirePresentsRewardsInsteadOfTownReturn(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	service, session, runtime := newBackToVillageRuntime(t)
	queue := newFakeCurrentDungeonDeathTimerQueue()
	service.gameplayTimers = queue

	active := &tournamentDungeonRuntime{
		Definition:        &tournamentDefinition{PartyLimit: 1},
		acceptedSequences: map[int]struct{}{},
		firstSequence:     uint16(firstDungeonMonsterObjectKey),
		currentRound:      1,
	}
	session.dungeon.mu.Lock()
	runtime.Tournament = active
	runtime.IgnoreDefaultDungeonClear = true
	session.dungeon.mu.Unlock()

	if err := service.handleDungeonDieCharacter(session, make([]byte, currentDungeonDieCharacterBodySize)); err != nil {
		t.Fatal(err)
	}
	task := queue.task(t, 0)
	if task.delay != currentDungeonDeathReturnDelay {
		t.Fatalf("death timer delay = %s want %s", task.delay, currentDungeonDeathReturnDelay)
	}
	session.conn.(*bufferConn).write.Reset()
	queue.fire(task, false)

	if !active.isEliminated() {
		t.Fatal("firing the death timer must eliminate the player in a tournament")
	}
	if runtime.townReturnPending || runtime.townReturnOp24Sent {
		t.Fatalf("tournament death must not start a town return: %+v", runtime)
	}
	if runtime.deathReturnWaiting {
		t.Fatal("the tournament death deadline must cancel the death-return timer")
	}
	if written := session.conn.(*bufferConn).write.Bytes(); len(written) != 0 {
		t.Fatalf("tournament death emitted %d bytes, want the reward UI path with no town return: %x",
			len(written), written)
	}
}

// 默认档的到期回调必须与改动前逐字节相同：仍然发同一条 op24 回城体。
// helper：跑一次"普通副本死亡 → 倒计时到期"，返回整条下行字节流。
func runOrdinaryDeathReturnStreamForTournamentControl(t *testing.T) []byte {
	t.Helper()
	service, session, _ := newBackToVillageRuntime(t)
	queue := newFakeCurrentDungeonDeathTimerQueue()
	service.gameplayTimers = queue
	session.conn.(*bufferConn).write.Reset()
	if err := service.handleDungeonDieCharacter(session, make([]byte, currentDungeonDieCharacterBodySize)); err != nil {
		t.Fatal(err)
	}
	task := queue.task(t, 0)
	if task.delay != currentDungeonDeathReturnDelay {
		t.Fatalf("death timer delay = %s want %s", task.delay, currentDungeonDeathReturnDelay)
	}
	session.conn.(*bufferConn).write.Reset()
	queue.fire(task, false)
	return append([]byte(nil), session.conn.(*bufferConn).write.Bytes()...)
}

func TestDefaultModeDeathTimerStillReturnsToTownByteForByte(t *testing.T) {
	streams := map[string][]byte{}
	for _, mode := range []string{"", "off", dungeonTournamentModeVendor} {
		t.Setenv(dungeonTournamentEnvKey, mode)
		streams[mode] = runOrdinaryDeathReturnStreamForTournamentControl(t)
	}
	baseline := streams[""]
	if len(baseline) == 0 {
		t.Fatal("the ordinary death return produced no packets")
	}
	// 真的走完了 op24 回城（防"空流对比通过"的假绿）。
	packet, rest := splitGameServerUpperPacket(t, baseline)
	if packet.Header.MsgID != currentSceneTransitionMsgID || packet.Header.Classification != 0 ||
		!bytes.Equal(packet.Body, wantDungeonTownTransitionBody()) || len(rest) != 0 {
		t.Fatalf("death return header=%+v body=%x rest=%x",
			packet.Header, packet.Body, rest)
	}
	for mode, stream := range streams {
		if !bytes.Equal(stream, baseline) {
			t.Fatalf("mode=%q changed the ordinary death return:\n got=%x\nwant=%x",
				mode, stream, baseline)
		}
	}
}

// ── 任务 #3 ──────────────────────────────────────────────────────────────────

// 3000ms 自动选奖计时器：布防一次（不重复布防）、延迟是厂商常量、
// 到期只为"启用且尚未选择"的卡类选 index 0。
func TestTournamentRewardAutoSelectTimerContract(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	service, session, runtime := newBackToVillageRuntime(t)
	queue := newFakeCurrentDungeonDeathTimerQueue()
	service.gameplayTimers = queue

	session.dungeon.mu.Lock()
	runtime.Tournament = championTournamentRuntimeForTest(t, runtime.Session.Snapshot().Scene.Map.Map.ID)
	runtime.IgnoreDefaultDungeonClear = true
	state := tournamentRewardStateForTest(t, false)
	runtime.tournamentReward = state
	clearTournamentRoomForTest(t, runtime)

	service.armCurrentTournamentRewardAutoSelectTimerLocked(session, runtime)
	scheduled, _, _ := queue.counts()
	if scheduled != 1 {
		session.dungeon.mu.Unlock()
		t.Fatalf("scheduled = %d want 1", scheduled)
	}
	task := queue.task(t, 0)
	if task.delay != currentTournamentRewardAutoSelectDelay {
		session.dungeon.mu.Unlock()
		t.Fatalf("auto-select delay = %s want %s (厂商 RewardSelectionTimeoutMilliseconds = 3000)",
			task.delay, currentTournamentRewardAutoSelectDelay)
	}
	if currentTournamentRewardAutoSelectDelay.Milliseconds() != 3000 {
		session.dungeon.mu.Unlock()
		t.Fatalf("auto-select delay = %dms want exactly 3000ms",
			currentTournamentRewardAutoSelectDelay.Milliseconds())
	}
	// 已有票 ⇒ 不重复布防（厂商 `:408-413 TryGetCurrentTicket ⇒ return`）。
	service.armCurrentTournamentRewardAutoSelectTimerLocked(session, runtime)
	if scheduled, _, _ = queue.counts(); scheduled != 1 {
		session.dungeon.mu.Unlock()
		t.Fatalf("re-arm scheduled = %d want still 1", scheduled)
	}
	session.dungeon.mu.Unlock()

	// 到期：两个卡类都选 index 0（黄金卡与道具卡都不产生背包变更 ⇒ 不碰仓储）。
	// ⚠️ 必须**在锁外**触发：真实回调自己会取 `session.dungeon.mu`
	// （`armCurrentTournamentRewardAutoSelectTimerLocked` 里的闭包），
	// 持锁 fire 会自锁（这也是本用例第一版卡死的原因）。
	queue.fire(task, false)

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	if state.Selected[0] != 0 || state.Selected[1] != 0 {
		t.Fatalf("auto-selected = %+v want index 0 for both groups", state.Selected)
	}
	if !state.isSelectionComplete() {
		t.Fatal("auto-select must complete the selection")
	}
	if runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		t.Fatalf("run status = %s want completed (选奖结束后冠军必须真的通关)",
			runtime.Session.Snapshot().Run.Status)
	}
}

// 自动选奖只在"赛制已落定 + 奖励态仍在位 + run 还 Active"时才跑；
// 并且默认档（非锦标赛）恒不跑。
func TestTournamentRewardAutoSelectIsGuardedAndInertByDefault(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	state := tournamentRewardStateForTest(t, false)

	// 默认档：不是锦标赛。
	if deferred, reason := shouldDeferTournamentRewardAutoSelect(session, runtime, state); !deferred ||
		reason != "active_run_is_not_a_tournament" {
		t.Fatalf("default guard = %t/%q", deferred, reason)
	}
	if deferred, _ := shouldDeferTournamentRewardAutoSelect(session, runtime, nil); !deferred {
		t.Fatal("a nil reward state must defer")
	}

	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	runtime.Tournament = &tournamentDungeonRuntime{
		Definition:        &tournamentDefinition{PartyLimit: 1},
		acceptedSequences: map[int]struct{}{},
		firstSequence:     uint16(firstDungeonMonsterObjectKey),
		currentRound:      2,
	}
	runtime.IgnoreDefaultDungeonClear = true
	runtime.tournamentReward = state
	if deferred, reason := shouldDeferTournamentRewardAutoSelect(session, runtime, state); !deferred ||
		reason != "tournament_is_not_decided_yet" {
		t.Fatalf("undecided guard = %t/%q", deferred, reason)
	}
	// 奖励态被换掉 ⇒ 旧计时器的身份校验必须拒绝（这时赛制已经落定，
	// 所以判据必须命中 `tournament_reward_state_was_replaced` 而不是 Active 判据）。
	replacement := tournamentRewardStateForTest(t, false)
	champion := championTournamentRuntimeForTest(t, runtime.Session.Snapshot().Scene.Map.Map.ID)
	runtime.Tournament = champion
	runtime.tournamentReward = replacement
	if deferred, reason := shouldDeferTournamentRewardAutoSelect(session, runtime, state); !deferred ||
		reason != "tournament_reward_state_was_replaced" {
		t.Fatalf("replaced guard = %t/%q", deferred, reason)
	}
	// 已经选完 ⇒ 没有可自动选的卡类。
	done := tournamentRewardStateForTest(t, true)
	runtime.tournamentReward = done
	if deferred, reason := shouldDeferTournamentRewardAutoSelect(session, runtime, done); !deferred ||
		reason != "reward_selection_is_already_complete" {
		t.Fatalf("complete guard = %t/%q", deferred, reason)
	}

	queue := newFakeCurrentDungeonDeathTimerQueue()
	service.gameplayTimers = queue
	service.armCurrentTournamentRewardAutoSelectTimerLocked(session, runtime)
	if scheduled, _, _ := queue.counts(); scheduled != 0 {
		t.Fatalf("an already-complete selection armed %d timers", scheduled)
	}
}

// 默认档：arm / cancel 都不排队、不改 runtime（逐字节零变化）。
func TestTournamentRewardAutoSelectTimerIsInertByIdDefaultMode(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	queue := newFakeCurrentDungeonDeathTimerQueue()
	service.gameplayTimers = queue

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	service.armCurrentTournamentRewardAutoSelectTimerLocked(session, runtime)
	service.cancelCurrentTournamentRewardAutoSelectTimerLocked(session, runtime, "test_default_mode")
	if scheduled, cancelled, _ := queue.counts(); scheduled != 0 || cancelled != 0 {
		t.Fatalf("default mode scheduled=%d cancelled=%d want 0/0", scheduled, cancelled)
	}
	if runtime.tournamentRewardAutoSelectTimerName != "" || runtime.tournamentRewardAutoSelectGeneration != 0 {
		t.Fatalf("default mode mutated the timer fields: %+v", runtime)
	}
}

// 空 body 的选奖态请求（**unsolicited 的 457 应答**）= **只回那 9 字节选奖态** + 布防计时器。
//
// 这一段在"摘掉下行 458"那批里成立，本批**保持不变**，但理由被更正：真机 90CN
// **有** 458 下行 handler（派发表 0x1CA → sub_1CF5010，见 dungeon_tournament_config.go），
// 不能发的是**未受邀**的 458 —— 线上证据（2026-09-27 22:23–22:25 CN conn game-000003）
// 就是"附带 selection 之后约 80 秒客户端 `exception code=0xC0000005`"。
// 458 现在**只回在客户端自己发来 2 字节选奖的 solicited 路径上**（见本用例第二段）。
func TestTournamentRewardStateRequestSendsOnlyStateAndArmsTimer(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	service, session, runtime := newBackToVillageRuntime(t)
	queue := newFakeCurrentDungeonDeathTimerQueue()
	service.gameplayTimers = queue

	session.dungeon.mu.Lock()
	runtime.Tournament = championTournamentRuntimeForTest(t, runtime.Session.Snapshot().Scene.Map.Map.ID)
	runtime.IgnoreDefaultDungeonClear = true
	runtime.tournamentReward = tournamentRewardStateForTest(t, false)
	session.dungeon.mu.Unlock()

	session.conn.(*bufferConn).write.Reset()
	handled, err := service.handleCurrentTournamentRewardCommand(
		session, a21CmdTournamentRewardSelectState, nil)
	if err != nil {
		t.Fatalf("state request: %v", err)
	}
	if !handled {
		t.Fatal("a bodyless state request with a pending reward must be handled")
	}
	stream := session.conn.(*bufferConn).write.Bytes()
	state, rest := splitGameServerUpperPacket(t, stream)
	if state.Header.MsgID != a21CmdTournamentRewardSelectState {
		t.Fatalf("first packet = %d want %d", state.Header.MsgID, a21CmdTournamentRewardSelectState)
	}
	if len(state.Body) != 9 || state.Body[0] != 1 {
		t.Fatalf("state body = %x want the 9-byte success + 2x4 slot projection", state.Body)
	}
	if len(rest) != 0 {
		t.Fatalf("the state answer must be the ONLY packet (真机无 458 下行 handler): %x", rest)
	}
	if scheduled, _, _ := queue.counts(); scheduled != 1 {
		t.Fatalf("state request scheduled %d auto-select timers want 1", scheduled)
	}

	// 2 字节选奖提交 = **solicited**（客户端自己发来的真机形状，RV 0x1D81860）：
	// 修复档（默认 `DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_ACK=select`）必须**先回 458 选中
	// 投影**（7 字节），选奖未完成时再补一条 9 字节 457 选奖态 —— 与族内 0.1.8
	// `handleCurrentDungeonTournamentRewardSelect` 逐行同形。
	//
	// ⚠️ 这条断言在"摘掉下行 458"那批里是**反向**的（当时要求"整个下行流里不允许出现
	// 458"）。本批用真机派发表取证推翻了那条判据的依据（90CN 客户端**有** 458 下行
	// handler：0x1CA → sub_1CF5010），并把 458 收窄到**只回在 solicited 路径**上；
	// 3s 兜底自动选奖（unsolicited）与 457 应答仍一个字节都不发。
	// 奖励照发（走背包事务边界）。
	session.conn.(*bufferConn).write.Reset()
	handled, err = service.handleCurrentTournamentRewardCommand(
		session, a21CmdTournamentRewardSelect, []byte{0, 0})
	if err != nil || !handled {
		t.Fatalf("select handled=%t err=%v", handled, err)
	}
	rest = session.conn.(*bufferConn).write.Bytes()
	projectionBody := ""
	stateAnswers := 0
	order := make([]uint16, 0, 2)
	for len(rest) > 0 {
		packet, trailing := splitGameServerUpperPacket(t, rest)
		rest = trailing
		// 落奖路径上还会有别的下行（op14 背包刷新等），这里只盯选奖握手的**相对顺序**。
		if packet.Header.MsgID != a21CmdTournamentRewardSelect &&
			packet.Header.MsgID != a21CmdTournamentRewardSelectState {
			continue
		}
		order = append(order, packet.Header.MsgID)
		switch packet.Header.MsgID {
		case a21CmdTournamentRewardSelect:
			projectionBody = hex.EncodeToString(packet.Body)
			if len(packet.Body) != 7 {
				t.Fatalf("the solicited selection ack body = %x want the 7-byte projection", packet.Body)
			}
		case a21CmdTournamentRewardSelectState:
			stateAnswers++
			if len(packet.Body) != 9 || packet.Body[0] != 1 {
				t.Fatalf("state answer body = %x want the 9-byte success + 2x4 slot projection", packet.Body)
			}
		}
	}
	// 只剩第一排的第 0 张被本槽位拿走 ⇒ `01 | 02 | 00 ff | 02 | ff ff`。
	if projectionBody != "010200ff02ffff" {
		t.Fatalf("458 projection = %q want %q", projectionBody, "010200ff02ffff")
	}
	if len(order) != 2 || order[0] != a21CmdTournamentRewardSelect ||
		order[1] != a21CmdTournamentRewardSelectState {
		t.Fatalf("downlink order = %v want [458 投影, 457 选奖态]", order)
	}
	if stateAnswers != 1 {
		t.Fatalf("state answers = %d want exactly 1（未完成 ⇒ 重发 457 选奖态）", stateAnswers)
	}
	stateAfter := runtime.tournamentReward
	if stateAfter.Selected[0] != 0 {
		t.Fatalf("selection = %+v want group 0 committed", stateAfter.Selected)
	}
	// 手动只提交了下排（cardType 0）；上排还没选 ⇒ 选奖未完成、run 还不能通关。
	// 剩下的那一组由 3000ms 自动选奖计时器补齐（前一个用例已钉住那条路）。
	if stateAfter.isSelectionComplete() {
		t.Fatal("one manual group must not complete a two-group selection")
	}
	if stateAfter.Selected[1] != tournamentUnselectedCard {
		t.Fatalf("group 1 selection = %d want unselected", stateAfter.Selected[1])
	}
	if runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		t.Fatalf("run status = %s want active until the whole selection is complete",
			runtime.Session.Snapshot().Run.Status)
	}
}

// ── 任务 #4 ──────────────────────────────────────────────────────────────────

// 默认档逐字节零变化（任务 #4 的强证据）：**同一个声明了 `[required item]` 的副本**，
// 开关关闭时整条 op16→op5→op28→op29 流必须与改动前完全相同。
//
// 这是"新入口在默认档全部不可达"的端到端证明：只要开关是默认档，
// `consumeDungeonEntryItemsWithTournament` 第一行就返回，连背包都不读。
func TestDungeonTournamentEntryConsumeKeepsDefaultStreamByteIdentical(t *testing.T) {
	streams := map[string][]byte{}
	for _, mode := range []string{"", "off", "OFF", "sausages"} {
		t.Setenv(dungeonTournamentEnvKey, mode)
		streams[mode] = runRequiredItemDungeonSelectStreamForTournamentControl(t)
	}
	baseline := streams[""]
	if len(baseline) == 0 {
		t.Fatal("the `[required item]` control entry produced no packets")
	}
	// 反假绿：基线必须真的走完 op16 ACK → op5 → op28 → op29。
	wantMsgIDs := []uint16{
		uint16(dnfenum.CmdPacketSelectDungeon),
		currentDungeonResourceStateMsgID,
		currentDungeonInfoNotification,
		currentDungeonStartNotification,
	}
	rest := baseline
	for _, want := range wantMsgIDs {
		found := false
		for len(rest) > 0 {
			packet, trailing := splitGameServerUpperPacket(t, rest)
			rest = trailing
			if packet.Header.MsgID == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("baseline stream misses msg %d", want)
		}
	}
	for mode, stream := range streams {
		if !bytes.Equal(stream, baseline) {
			t.Fatalf("switch=%q changed the `[required item]` entry stream:\n got=%x\nwant=%x",
				mode, stream, baseline)
		}
	}
}

// runRequiredItemDungeonSelectStreamForTournamentControl 与
// `runDungeonSelectStreamForTournamentControl` 同一夹具，但**在 DGN 里声明
// `[required item] 4726 1 1`**（真档 120 的原样三元组），用来钉住"开关关闭时
// 声明了证物的副本仍然走同一条字节流"。
func runRequiredItemDungeonSelectStreamForTournamentControl(t *testing.T) []byte {
	t.Helper()
	source := bridgeDungeonPVF(false)
	source["dungeon/test.dgn"] += "[required item]\n4726 1 1\n[/required item]\n"
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "99",
		AccountID:   "account-1",
		Level:       20,
		Stats: map[string]int64{
			"fatigue": 100, "town_id": 38, "area_id": 1,
			"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	// 背包**是空的**：默认档不许因为"没证物"而拒绝（校验根本不跑），
	// 也不许因为新代码去读/写这份背包。
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:             options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return 1, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	connection := &bufferConn{}
	session := &gameSession{
		conn:                          connection,
		selectedCharacterID:           99,
		enterSelectDungeonContextSent: true,
	}
	bindDungeonSelectorOriginForTestAt(t, service, session, 38, 1, 450, 234)
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body[0:4], 700)
	binary.LittleEndian.PutUint16(body[9:11], 0xffff)
	if err := service.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatalf("select dungeon with `[required item]`: %v", err)
	}
	inventory, _, loadErr := repositories.Inventory.Load(context.Background(), "99")
	if loadErr != nil {
		t.Fatalf("load inventory: %v", loadErr)
	}
	if len(inventory.Slots) != 0 {
		t.Fatalf("default mode mutated the ticket inventory: %+v", inventory.Slots)
	}
	return append([]byte(nil), connection.write.Bytes()...)
}

// 入场消耗：默认档一行都不执行；vendor 档声明了 `[required item]` 的 DGN 必须真的扣，
// 不足必须拒绝（且不留下半成品）。
func TestConsumeDungeonEntryItemsWithTournamentContract(t *testing.T) {
	ctx := context.Background()
	service, session, _ := newBackToVillageRuntime(t)
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("fixture has no repository group")
	}
	dungeon := worldmap.Dungeon{
		ID: 120,
		Metadata: worldmap.DungeonMetadata{
			Flags: map[string]bool{"tournament dungeon": true},
			RequiredItems: []worldmap.DungeonRequiredItem{
				{ItemID: 4726, Count: 1, ConsumeOnEntry: true},
			},
		},
	}

	// 默认档：连背包都不读、不写库。
	if err := service.consumeDungeonEntryItemsWithTournament(ctx, dungeon, session); err != nil {
		t.Fatalf("default mode consume: %v", err)
	}
	inventory, _, err := repositories.Inventory.Load(ctx, "99")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	if len(inventory.Slots) != 0 {
		t.Fatalf("default mode mutated the inventory: %+v", inventory.Slots)
	}

	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	// 证物不足 ⇒ 明确拒绝，且不静默扣 0。
	err = service.consumeDungeonEntryItemsWithTournament(ctx, dungeon, session)
	if !errors.Is(err, dnfdungeon.ErrEntryItemConsumeInsufficient) {
		t.Fatalf("insufficient err = %v want ErrEntryItemConsumeInsufficient", err)
	}
	inventory, _, err = repositories.Inventory.Load(ctx, "99")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	if len(inventory.Slots) != 0 {
		t.Fatalf("a rejected entry mutated the inventory: %+v", inventory.Slots)
	}

	// 发一张 4726 的证物 ⇒ 入场会真的把它扣掉（证物必须消失）。
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "99",
		Slots: map[string]dnfrepo.ItemStack{
			"0:0": {ItemID: 4726, Count: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.consumeDungeonEntryItemsWithTournament(ctx, dungeon, session); err != nil {
		t.Fatalf("consume with a held ticket: %v", err)
	}
	inventory, _, err = repositories.Inventory.Load(ctx, "99")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	if _, stillThere := inventory.Slots["0:0"]; stillThere {
		t.Fatalf("the entry ticket survived the entry transaction: %+v", inventory.Slots)
	}
	// 再进一次 ⇒ 又不足（证物已经消耗掉，不会凭空回来）。
	if err = service.consumeDungeonEntryItemsWithTournament(ctx, dungeon, session); !errors.Is(err, dnfdungeon.ErrEntryItemConsumeInsufficient) {
		t.Fatalf("second entry err = %v want insufficient", err)
	}
}

// 没有 `[required item]` 的副本（绝大多数）：vendor 档同样是完整 no-op。
func TestConsumeDungeonEntryItemsWithoutRequirementsIsNoOp(t *testing.T) {
	ctx := context.Background()
	service, session, _ := newBackToVillageRuntime(t)
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	dungeon := worldmap.Dungeon{
		ID: 700,
		Metadata: worldmap.DungeonMetadata{
			Flags: map[string]bool{"tournament dungeon": false},
		},
	}
	if err := service.consumeDungeonEntryItemsWithTournament(ctx, dungeon, session); err != nil {
		t.Fatalf("no-requirement consume: %v", err)
	}
	repositories, _ := service.repositoryGroup()
	inventory, _, err := repositories.Inventory.Load(ctx, "99")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	if len(inventory.Slots) != 0 {
		t.Fatalf("a dungeon without `[required item]` mutated the inventory: %+v", inventory.Slots)
	}
}

// 出本门：锦标赛终止态 + 奖励态已下发 ⇒ 完成局的 op42 必须放行；
// 默认档（非锦标赛）必须与改动前一致（仍要求普通结算屏障）。
func TestTournamentResultExitReadyContract(t *testing.T) {
	_, session, runtime := newBackToVillageRuntime(t)

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()

	// 默认档：锦标赛判据恒 false，普通判据仍未被满足。
	if currentDungeonTournamentResultExitReady(runtime, session.selectedCharacterID) {
		t.Fatal("a non-tournament run must never take the tournament exit gate")
	}
	if completedDungeonSettlementResultReady(runtime, session.selectedCharacterID) {
		t.Fatal("the ordinary settlement barrier must stay closed before op31/op46/op123")
	}

	// run 已完成 + 锦标赛终止 + 奖励态在位 ⇒ 放行。
	clearTournamentRoomForTest(t, runtime)
	if err := runtime.Session.Complete(); err != nil {
		t.Fatal(err)
	}
	runtime.Tournament = championTournamentRuntimeForTest(t, runtime.Session.Snapshot().Scene.Map.Map.ID)
	runtime.IgnoreDefaultDungeonClear = true
	runtime.tournamentReward = tournamentRewardStateForTest(t, true)
	if !currentDungeonTournamentResultExitReady(runtime, session.selectedCharacterID) {
		t.Fatal("a terminated tournament with a delivered reward state must open the exit gate")
	}
	if !completedDungeonSettlementResultReady(runtime, session.selectedCharacterID) {
		t.Fatal("the tournament exit must also satisfy the result-ready gate")
	}
	if !completedDungeonSettlementExitReady(runtime, session.selectedCharacterID) {
		t.Fatal("the tournament exit must satisfy the overall exit gate")
	}

	// 奖励态还没发出去（389 未成功）⇒ 门必须关着。
	runtime.tournamentReward = nil
	if currentDungeonTournamentResultExitReady(runtime, session.selectedCharacterID) {
		t.Fatal("the exit gate must stay closed until the reward state was delivered")
	}
	// 竞态保护：到期回调不得为**已被替换**的奖励态落库。
	replacement := tournamentRewardStateForTest(t, false)
	runtime.tournamentReward = replacement
	if deferred, reason := shouldDeferTournamentRewardAutoSelect(session, runtime, tournamentRewardStateForTest(t, false)); !deferred ||
		reason != "tournament_reward_state_was_replaced" {
		t.Fatalf("stale reward state guard = %t/%q", deferred, reason)
	}
	// 非 owner 角色 ⇒ 不因为锦标赛而放行。
	other := &runtimeDungeonState{Tournament: runtime.Tournament, tournamentReward: replacement, Session: runtime.Session}
	if currentDungeonTournamentResultExitReady(other, session.selectedCharacterID) {
		t.Fatal("a foreign runtime must not open the exit gate")
	}
}
