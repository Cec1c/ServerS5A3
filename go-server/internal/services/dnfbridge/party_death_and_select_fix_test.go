package dnfbridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件钉住运维 2026-09-29 反馈的**第 4、5 条**（组队）的根因修复。
//
// 两条的现场都取自 `packet_log.txt` 同一局（`party_id=1`，`conn_id=game-002698` char1
// 与 `conn_id=game-002700` char4，深渊 310，2026-09-29T05:52:50Z 创建共享实例，
// 05:58:28Z 击杀 Boss）。所有期望值都是线上**逐字节**抄下来的，不是构造的。

// ── 第 5 条：结算被队友的掉落收据挡掉 ⇒ 两边都假死 ──────────────────────────────

// partySettlementReceiptIncidentRuntime 用**线上那一局的真实字节**建一份运行时：
//
//	共享掉落账本（`party-instance-created … shared_room_and_drop_ledger`）里同时有
//	两个人的已消耗掉落，而背包只有 char1 自己那几条。
//
// 线上原始行（`grep -a`，行号见报告）：
//
//	239926 char4 `pickup-accepted drop_object_key=410 … item_id=3030 destination_slot=121`
//	240575 char4 `pickup-accepted drop_object_key=443 … item_id=3028 destination_slot=124`
//	240584 char1 `pickup-accepted drop_object_key=441 … item_id=3028 destination_slot=129`
//	240591 char1 `pickup-accepted drop_object_key=442 … item_id=3142 destination_slot=132`
//	241014 char1 `pickup-accepted drop_object_key=461 … item_id=3052 destination_slot=133`
//	241327 char1 `pickup-accepted drop_object_key=470 … item_id=3028 destination_slot=129`
//
// `dnf_inventory_items`（`collection_name='slots'`）逐字：
//
//	char1 `0:121 = 3330`、`0:124 = 3027`、`0:129 = 3028 (count 2)`、`0:132 = 3142`、`0:133 = 3052`
//	char4 `0:121 = 3030`、`0:124 = 3028`
//
// ⇒ char1 的收据里混进了 char4 的两条（410/443），它们**永远**不可能出现在 char1 的背包里
// （`0:121` 是 3330 不是 3030；`0:124` 是 3027 不是 3028）。
func partySettlementReceiptIncidentRuntime(
	t *testing.T,
) (*Service, *gameSession, *runtimeDungeonState, dnfrepo.InventoryRecord) {
	t.Helper()
	service, session, _, runtime, _, repositories := prepareAbyssEpicFragmentRuntime(
		t, bridgeAbyssEpicFragmentPVF(),
	)
	if session.selectedCharacterID != 99 {
		t.Fatalf("fixture character id=%d, the actor-key mapping below assumes 99", session.selectedCharacterID)
	}
	// 只改**归属**：把线上 char1/char4 的 actor key 映射到 fixture 的 99（= 自己）与 4（= 队友）。
	const selfActorObjectKey = uint16(99)
	const teammateActorObjectKey = uint16(4)
	owner := newRuntimeDungeonDropOwner()
	// 自己的四条（线上 char1）。441 与 470 同槽位 129、同物品 3028 ⇒ 合并成一条收据（count=2）。
	for _, drop := range []*runtimeDungeonDrop{
		{ObjectKey: 441, SceneSlot: 4, Item: dungeonDropItemDefinition{ItemID: 3028}, Amount: 1, DestinationSlot: 129},
		{ObjectKey: 442, SceneSlot: 5, Item: dungeonDropItemDefinition{ItemID: 3142}, Amount: 1, DestinationSlot: 132},
		{ObjectKey: 461, SceneSlot: 7, Item: dungeonDropItemDefinition{ItemID: 3052}, Amount: 1, DestinationSlot: 133},
		{ObjectKey: 470, SceneSlot: 9, Item: dungeonDropItemDefinition{ItemID: 3028}, Amount: 1, DestinationSlot: 129},
	} {
		drop.OwnerActorObjectKey = selfActorObjectKey
		drop.Status = runtimeDungeonDropConsumed
		owner.byObjectKey[drop.ObjectKey] = drop
	}
	// 队友的两条（线上 char4）。它们才是"整局结算计划被挡掉"的触发者。
	for _, drop := range []*runtimeDungeonDrop{
		{ObjectKey: 410, SceneSlot: 1, Item: dungeonDropItemDefinition{ItemID: 3030}, Amount: 1, DestinationSlot: 121},
		{ObjectKey: 443, SceneSlot: 6, Item: dungeonDropItemDefinition{ItemID: 3028}, Amount: 1, DestinationSlot: 124},
	} {
		drop.OwnerActorObjectKey = teammateActorObjectKey
		drop.Status = runtimeDungeonDropConsumed
		owner.byObjectKey[drop.ObjectKey] = drop
	}
	runtime.DropOwner = owner
	inventory := dnfrepo.InventoryRecord{
		CharacterID: "99",
		Slots: map[string]dnfrepo.ItemStack{
			currentDungeonPickupMainSlotKey(121): {ItemID: 3330, Count: 10002},
			currentDungeonPickupMainSlotKey(124): {ItemID: 3027, Count: 1},
			currentDungeonPickupMainSlotKey(129): {ItemID: 3028, Count: 2},
			currentDungeonPickupMainSlotKey(132): {ItemID: 3142, Count: 1},
			currentDungeonPickupMainSlotKey(133): {ItemID: 3052, Count: 1},
		},
	}
	if err := repositories.Inventory.Save(context.Background(), inventory); err != nil {
		t.Fatal(err)
	}
	return service, session, runtime, inventory
}

// TestPartySettlementExcludesTeammatesConsumedDrops 是第 5 条（**假死 / 无法结算**）的
// 判别力用例：
//
//	默认档 ⇒ 结算收据**只剩自己那三条**（129/132/133），不报错 ⇒ op34/op37/op35 能发出去；
//	`legacy` 档 ⇒ 逐字节复现线上那条 `settlement-plan-blocked … persisted state is invalid`。
//
// 证伪那半：把**自己**的收据弄坏（槽位在、但物品不是自己的那件）⇒ 默认档必须**继续失败**，
// 证明这个过滤不是"放过一切"。
func TestPartySettlementExcludesTeammatesConsumedDrops(t *testing.T) {
	logger, logPath := testPassiveObjectDropPacketLogger(t)
	service, session, runtime, inventory := partySettlementReceiptIncidentRuntime(t)
	service.packetLog = logger

	// ── 回退档：逐字节复现线上失败（这是"对照组"，证明这套 fixture 真的有判别力）──
	t.Setenv(partySettlementReceiptOwnerEnv, partySettlementReceiptOwnerLegacyValue)
	if _, err := service.currentDungeonCommittedDropReceiptsLocked(
		session, runtime, inventory,
	); !errors.Is(err, errCurrentDungeonSettlementStateInvalid) {
		t.Fatalf("legacy tier must reproduce the deployed failure, got err=%v", err)
	}

	// ── 默认档（修复）：只统计属于本角色的已消耗掉落 ──
	t.Setenv(partySettlementReceiptOwnerEnv, "")
	drops, err := service.currentDungeonCommittedDropReceiptsLocked(session, runtime, inventory)
	if err != nil {
		t.Fatalf("the shipped tier must settle through a teammate's receipts: %v", err)
	}
	if len(drops) != 3 {
		t.Fatalf("receipts=%+v want exactly the three own slots 129/132/133", drops)
	}
	// `ObjectKey` 在这一层是**目的槽位**（见 `currentDungeonCommittedDropReceiptsFiltered`
	// 的 `bySlot[drop.DestinationSlot] = currentDungeonClearRewardDrop{ObjectKey: drop.DestinationSlot …}`），
	// 129 那一条由 441 与 470 合并而来、`StackCount` 必须是合并后的 2。
	wantBySlot := map[uint16]struct {
		item  uint32
		count uint32
	}{
		129: {item: 3028, count: 2},
		132: {item: 3142, count: 1},
		133: {item: 3052, count: 1},
	}
	if len(drops) != len(wantBySlot) {
		t.Fatalf("receipt count=%d want %d", len(drops), len(wantBySlot))
	}
	for _, drop := range drops {
		want, ok := wantBySlot[drop.ObjectKey]
		if !ok || drop.TemplateID != want.item || drop.StackCount != want.count {
			t.Fatalf("receipt=%+v not in %+v", drop, wantBySlot)
		}
		delete(wantBySlot, drop.ObjectKey)
	}
	if len(wantBySlot) != 0 {
		t.Fatalf("missing receipts for slots %+v", wantBySlot)
	}

	// ── 证伪：**自己**的收据坏掉时必须继续 fail-closed（过滤不是"放过一切"）──
	selfBroken := newRuntimeDungeonDropOwner()
	for key, drop := range runtime.DropOwner.byObjectKey {
		clone := *drop
		selfBroken.byObjectKey[key] = &clone
	}
	// 把 442 的槽位改成 121 —— 那里放的是 3330，不是 3142。
	selfBroken.byObjectKey[442].DestinationSlot = 121
	brokenRuntime := *runtime
	brokenRuntime.DropOwner = selfBroken
	if _, err := service.currentDungeonCommittedDropReceiptsLocked(
		session, &brokenRuntime, inventory,
	); !errors.Is(err, errCurrentDungeonSettlementStateInvalid) {
		t.Fatalf("a corrupt OWN receipt must still fail closed, got err=%v", err)
	}
	// 目的槽位 0 的**自己**那条同样必须继续失败。
	zeroSlot := newRuntimeDungeonDropOwner()
	for key, drop := range runtime.DropOwner.byObjectKey {
		clone := *drop
		zeroSlot.byObjectKey[key] = &clone
	}
	zeroSlot.byObjectKey[461].DestinationSlot = 0
	zeroRuntime := *runtime
	zeroRuntime.DropOwner = zeroSlot
	if _, err := service.currentDungeonCommittedDropReceiptsLocked(
		session, &zeroRuntime, inventory,
	); !errors.Is(err, errCurrentDungeonSettlementStateInvalid) {
		t.Fatalf("a zero destination slot on an OWN receipt must still fail closed, got err=%v", err)
	}

	logged := closePassiveObjectDropPacketLogger(t, logger, logPath)
	if !strings.Contains(logged, "game-dungeon-settlement-drop-receipt-foreign-skip") {
		t.Fatalf("the foreign-receipt skip must be grep-able:\n%s", logged)
	}
	for _, field := range []string{
		"drop_object_key=410", "drop_object_key=443",
		"owner_actor_object_key=4", "self_actor_object_key=99",
		"destination_slot=121", "destination_slot=124",
		"decision=settlement_plan_excludes_a_teammates_consumed_drop",
		"switch=own", "env=" + partySettlementReceiptOwnerEnv,
	} {
		if !strings.Contains(logged, field) {
			t.Fatalf("the foreign-receipt event is missing %q:\n%s", field, logged)
		}
	}
	if strings.Contains(logged, "switch=legacy") {
		t.Fatal("the legacy tier must not write a foreign-receipt event")
	}
}

// TestPartySettlementReceiptOwnerModeReadsOnlyTheLegacyLiteral 钉住开关语义：
// 只有字面量 `legacy` 回退，其余一律是修复档（线上是 `default-in-force`）。
func TestPartySettlementReceiptOwnerModeReadsOnlyTheLegacyLiteral(t *testing.T) {
	for _, value := range []string{"", " ", "own", "party", "0", "off", "exempt", "LEGACYX"} {
		if got := readPartySettlementReceiptOwnerMode(value); got.filtersForeignOwned() != true {
			t.Fatalf("value=%q must stay in the shipped (filtering) tier, got %s", value, got)
		}
	}
	if got := readPartySettlementReceiptOwnerMode(" legacy "); got.filtersForeignOwned() {
		t.Fatal("the legacy literal must disable the ownership filter")
	}
	if got := readPartySettlementReceiptOwnerMode("Legacy"); got.filtersForeignOwned() {
		t.Fatal("the legacy literal is case-insensitive")
	}
}

// ── 第 4 条：队员拾取偶发失败（私账本指针被覆盖丢掉）─────────────────────────────

// TestPartySharedLedgerAdoptMergesInsteadOfOrphaningTheSessionLedger 是第 4 条的判别力用例。
//
// 线上那一行（全日志唯一一条本次现场）：
//
//	240149: `kind=game-dungeon-pickup-blocked conn_id=game-002698 … room=1:0 map_id=36752
//	        drop_object_key=422 picker_actor_object_key=1 reason=runtime_drop_owner_rejected
//	        error=dnf dungeon drop object was not found: object_key=422
//	        t=2026-09-29T05:53:10.347854913Z`
//
// 成因：char4 在 05:52:5x 先拾取 ⇒ 把**自己那份**账本升格成实例共享账本；
// char1 在 05:53:10 才第一次拾取 ⇒ `partySharedDungeonDropOwnerLocked` 先把
// `runtime.DropOwner` 覆盖成共享账本，**char1 自己那份（含 422）当场丢失**。
//
// 本用例按同一顺序复现：先"队友发布共享账本"，再让本会话去取用。
func TestPartySharedLedgerAdoptMergesInsteadOfOrphaningTheSessionLedger(t *testing.T) {
	service, _, follower, _, followerRuntime, scene := partyDungeonInstanceFixture(t, 7, 4242)
	if instance := service.adoptPartyDungeonInstance(follower, followerRuntime, scene); instance == nil {
		t.Fatal("the follower did not join a party dungeon instance")
	}
	if followerRuntime.partyInstance == nil {
		t.Fatal("the follower runtime has no party instance")
	}
	roomKey := runtimeDungeonRoomKeyFromScene(scene)

	// 本会话（队员）自己房里已经登记过一条掉落：object_key=422（线上那一条）。
	private := newRuntimeDungeonDropOwner()
	private.byObjectKey[422] = &runtimeDungeonDrop{
		ObjectKey:           422,
		SceneSlot:           2,
		Item:                dungeonDropItemDefinition{ItemID: 3028},
		Amount:              1,
		OwnerActorObjectKey: follower.selectedCharacterID,
		Room:                roomKey,
		Status:              runtimeDungeonDropAvailable,
	}

	// 队友先拾取 ⇒ 共享账本由**队友那份**升格而来（线上 char4 的 05:52:5x）。
	shared := newRuntimeDungeonDropOwner()
	shared.byObjectKey[410] = &runtimeDungeonDrop{
		ObjectKey:           410,
		SceneSlot:           1,
		Item:                dungeonDropItemDefinition{ItemID: 3030},
		Amount:              1,
		OwnerActorObjectKey: 1001,
		Room:                roomKey,
		Status:              runtimeDungeonDropConsumed,
	}
	shared.byObjectKey[411] = &runtimeDungeonDrop{ObjectKey: 411, SceneSlot: 2, Amount: 1, Room: roomKey}
	followerRuntime.partyInstance.dropOwner = shared
	followerRuntime.DropOwner = private

	// ── 回退档：逐字节复现线上那条 not found ──
	t.Setenv(partyDropLedgerAdoptEnv, partyDropLedgerAdoptLegacyValue)
	instance := followerRuntime.lockPartyDungeonInstance()
	got := partySharedDungeonDropOwnerLocked(followerRuntime)
	unlockPartyDungeonInstance(instance)
	if got != shared || followerRuntime.DropOwner != shared {
		t.Fatalf("legacy tier must replace the pointer: got=%p owner=%p want=%p", got, followerRuntime.DropOwner, shared)
	}
	if _, err := followerRuntime.DropOwner.owned(422, roomKey, follower.selectedCharacterID); !errors.Is(
		err, errDungeonDropNotFound,
	) {
		t.Fatalf("legacy tier must lose object_key=422 like the field log did, got err=%v", err)
	}

	// ── 默认档（修复）：并进来，422 必须还在 ──
	followerRuntime.DropOwner = private
	followerRuntime.partyInstance.dropOwner = shared
	t.Setenv(partyDropLedgerAdoptEnv, "")
	instance = followerRuntime.lockPartyDungeonInstance()
	got = partySharedDungeonDropOwnerLocked(followerRuntime)
	unlockPartyDungeonInstance(instance)
	if got != shared {
		t.Fatalf("the shared ledger must stay the instance ledger: got=%p want=%p", got, shared)
	}
	drop, err := followerRuntime.DropOwner.owned(422, roomKey, follower.selectedCharacterID)
	if err != nil || drop == nil || drop.ObjectKey != 422 {
		t.Fatalf("the session's own drop must survive the adopt: drop=%+v err=%v", drop, err)
	}
	// 队友那条也还在，且是**同一个对象**（不复制 ⇒ 不重复入账）。
	if followerRuntime.DropOwner.byObjectKey[410] != shared.byObjectKey[410] {
		t.Fatal("the merge must keep the same drop pointer, not a copy")
	}
	// 同键以共享账本为准（不覆盖已有条目）。
	if followerRuntime.DropOwner.byObjectKey[411] != shared.byObjectKey[411] {
		t.Fatal("an existing shared entry must win over the session entry")
	}
	// 并发写保护：合并之后 `runtime.DropOwner` 就是实例那一份。
	if followerRuntime.DropOwner != shared {
		t.Fatal("after the adopt the session must write into the instance ledger")
	}
}

// TestPartyDropLedgerAdoptModeReadsOnlyTheLegacyLiteral 钉住开关语义。
func TestPartyDropLedgerAdoptModeReadsOnlyTheLegacyLiteral(t *testing.T) {
	for _, value := range []string{"", " ", "merge", "0", "off", "independent", "LEGACYX"} {
		if !readPartyDropLedgerAdoptMode(value).merges() {
			t.Fatalf("value=%q must stay in the shipped (merging) tier", value)
		}
	}
	for _, value := range []string{"legacy", " Legacy "} {
		if readPartyDropLedgerAdoptMode(value).merges() {
			t.Fatalf("value=%q must roll back to replacing the pointer", value)
		}
	}
}

// ── 第 5 条：过图不能抹掉已成立的死亡回城倒计时（"队长无法复活"）──────────────────

// TestPartyDeathReturnSurvivesRoomTransition 是"队长无法复活"的判别力用例。
//
// 线上逐字：
//
//	05:55:45.971 `kind=game-dungeon-character-death-op32-send char_id=4 … timer_delay_ms=10000`
//	05:55:49.396 `kind=game-dungeon-character-death-timer-cancelled char_id=4 previous_generation=5
//	              current_generation=6 timer_name=dnf-dungeon-death:game-002700:run:1
//	              queue_item_cancelled=true source=dungeon_room_transition_committed`
//	05:58:38.563 `kind=game-dungeon-use-coin-blocked conn_id=game-002700 char_id=4
//	              target_object_key=4 expected_object_key=4 run_token=1
//	              death_timer_waiting=false reason=dungeon_run_not_active`
//
// `handleCurrentDungeonUseCoin` 的第一道闸门就是 `deathReturnWaiting`
// （`dungeon_death_return.go:405,417`），所以 05:55:49 之后这名角色在本局里**再也复活不了**。
func TestPartyDeathReturnSurvivesRoomTransition(t *testing.T) {
	service, leader, follower, leaderRuntime, followerRuntime, scene := partyDungeonInstanceFixture(t, 7, 4242)
	if instance := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene); instance == nil {
		t.Fatal("the leader did not publish a party dungeon instance")
	}
	if instance := service.adoptPartyDungeonInstance(follower, followerRuntime, scene); instance == nil {
		t.Fatal("the follower did not join the party dungeon instance")
	}
	instance := leaderRuntime.partyInstance
	if instance == nil {
		t.Fatal("the leader runtime has no party instance")
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	service.gameplayTimers = queue
	leaderRuntime.lifecycleToken = 1

	arm := func() {
		leaderRuntime.deathReturnWaiting = true
		leaderRuntime.deathReturnTimerName = "dnf-dungeon-death:test:run:1"
		leaderRuntime.deathReturnGeneration = 5
		leaderRuntime.deathReturnDueAt = queue.now.Add(currentDungeonDeathReturnDelay)
		if err := queue.ScheduleAfter(
			leaderRuntime.deathReturnTimerName,
			currentDungeonDeathReturnDelay,
			func(due time.Time) {},
		); err != nil {
			t.Fatal(err)
		}
	}
	// 过图提交处那两行（`dungeon_navigation_runtime.go` 的 `dungeon_room_transition_committed`）。
	commitRoomTransition := func() {
		if !keepDungeonDeathReturnAcrossRoomTransition(leaderRuntime) {
			service.cancelCurrentDungeonDeathReturnLocked(leader, leaderRuntime, "dungeon_room_transition_committed")
		}
	}

	// ── 修复档：过图之后倒计时还在（复活窗口仍可用）──
	t.Setenv(partyDeathReturnRoomTransitionEnv, "")
	arm()
	commitRoomTransition()
	if !leaderRuntime.deathReturnWaiting {
		t.Fatal("the shipped tier must keep the death-return countdown across a room transition")
	}
	if _, _, active := queue.counts(); active != 1 {
		t.Fatalf("the death timer must still be armed: active=%d", active)
	}

	// ── 回退档：逐字节复现线上（过图即取消 ⇒ 再也复活不了）──
	leaderRuntime.deathReturnWaiting = false
	leaderRuntime.deathReturnTimerName = ""
	t.Setenv(partyDeathReturnRoomTransitionEnv, partyDeathReturnRoomTransitionLegacyValue)
	arm()
	commitRoomTransition()
	if leaderRuntime.deathReturnWaiting {
		t.Fatal("the legacy tier must reproduce the deployed cancel")
	}
	if _, _, active := queue.counts(); active != 0 {
		t.Fatalf("the legacy tier must cancel the queue item: active=%d", active)
	}

	// ── 窄口径：没有死亡在等 / 不是队伍副本时，两档都必须走原逻辑 ──
	t.Setenv(partyDeathReturnRoomTransitionEnv, "")
	leaderRuntime.deathReturnWaiting = false
	leaderRuntime.deathReturnTimerName = ""
	if keepDungeonDeathReturnAcrossRoomTransition(leaderRuntime) {
		t.Fatal("without an armed death countdown the room transition must keep cancelling")
	}
	leaderRuntime.deathReturnWaiting = true
	leaderRuntime.partyInstance = nil
	if keepDungeonDeathReturnAcrossRoomTransition(leaderRuntime) {
		t.Fatal("a solo run must keep the deployed behaviour byte for byte")
	}
	leaderRuntime.partyInstance = instance
	if !keepDungeonDeathReturnAcrossRoomTransition(leaderRuntime) {
		t.Fatal("a party run with an armed countdown must keep it")
	}
}

// TestPartyDeathReturnRoomTransitionModeReadsOnlyTheLegacyLiteral 钉住开关语义。
func TestPartyDeathReturnRoomTransitionModeReadsOnlyTheLegacyLiteral(t *testing.T) {
	for _, value := range []string{"", " ", "keep", "0", "on", "party", "LEGACYX"} {
		if !readPartyDeathReturnRoomTransitionMode(value).keeps() {
			t.Fatalf("value=%q must stay in the shipped (keep) tier", value)
		}
	}
	for _, value := range []string{"legacy", " Legacy "} {
		if readPartyDeathReturnRoomTransitionMode(value).keeps() {
			t.Fatalf("value=%q must roll back to cancelling on the room transition", value)
		}
	}
}
