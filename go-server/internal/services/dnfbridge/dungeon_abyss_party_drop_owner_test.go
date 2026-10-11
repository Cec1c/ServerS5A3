package dnfbridge

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件钉住 P0（2026-09-28T17:52 线上：**组队深渊两边都不掉碎片**）的两条修复。
//
// 现场（`_dist/abyss-round3.md` §8）：
//   - `game-dungeon-abyss-epic-fragment-skipped conn_id=game-000012 … object_key=434
//     group_id=214 final_wave_groups=212 is_final_wave=false reason=not_final_wave` ——
//     房间里最后一波的**最后一只**由**队员**击杀，而那名队员自己的波次表说最后一波是组 212
//     （房间真值是 {126,214}）⇒ 保底整支不发 ⇒ 掉落账本共享 ⇒ **全队都拿不到**。
//   - 同局的掉落行所有者是**击杀者**（`dungeon_death_response.go:91`），而拾取校验要求它等于
//     **拾取者**（`dungeon_drop_runtime.go` 的 `owned()`）⇒ 队员即使看到也捡不起来。

// newAbyssFinalWavePartyFixture 造一对同队会话，并把**击杀方会话**的冻结波次表换成
// 一张房间里根本不存在的表（复现"两人各自抽签"）。
func newAbyssFinalWavePartyFixture(
	t *testing.T,
) (*Service, *gameSession, *bufferConn, *runtimeDungeonState) {
	t.Helper()
	service, leader, _, leaderConn, _, runtime, _, _ := partyAbyssPillarFixture(t)
	runtime.HellPartyWaves = []dungeonHellPartyWave{{Order: 2, GroupID: abyssFinalWaveTestForeignGroup}}
	return service, leader, leaderConn, runtime
}

const abyssFinalWaveTestForeignGroup = int64(999999)

// TestAbyssFinalWaveIsRoomScopedSoTheWholePartyIsPaid 是 P0 的判别力用例：
// 同一份"击杀方会话的波次表是错的"的夹具下，默认档（房间口径）**照常发保底**，
// 回退档（会话口径）**一条都不发** —— 证明根因就是"最后一波按会话算"。
func TestAbyssFinalWaveIsRoomScopedSoTheWholePartyIsPaid(t *testing.T) {
	run := func(t *testing.T, mode string) (int, int, string) {
		t.Helper()
		t.Setenv(abyssEpicFragmentFinalWaveEnv, mode)
		logger, logPath := testPassiveObjectDropPacketLogger(t)
		service, leader, leaderConn, runtime := newAbyssFinalWavePartyFixture(t)
		service.packetLog = logger
		leaderConn.write.Reset()
		killDungeonActors(t, service, leader, waveHellPartyActorKeys(t, runtime)...)
		fragmentRows := 0
		for _, body := range abyssOp38Bodies(t, leaderConn.write.Bytes()) {
			_, fragments := abyssEpicFragmentSplit(t, abyssEpicFragmentDropRows(t, body))
			fragmentRows += len(fragments)
		}
		logged := closePassiveObjectDropPacketLogger(t, logger, logPath)
		return fragmentRows, countAbyssEvent(logged, "game-dungeon-abyss-epic-fragment-granted"),
			abyssFirstSkipReason(logged)
	}

	fragmentRows, grants, reason := run(t, "")
	if grants != 1 || fragmentRows == 0 {
		t.Fatalf("the room-scoped tier must pay exactly once for the room's final wave: grants=%d fragment_rows=%d reason=%s",
			grants, fragmentRows, reason)
	}

	fragmentRows, grants, reason = run(t, abyssEpicFragmentFinalWaveSessionValue)
	if grants != 0 || fragmentRows != 0 {
		t.Fatalf("the session-scoped rollback must reproduce the deployed P0 (no payout): grants=%d fragment_rows=%d",
			grants, fragmentRows)
	}
	if reason != "not_final_wave" {
		t.Fatalf("the rollback must fail with the deployed reason, got %q", reason)
	}
}

func countAbyssEvent(logged, kind string) int {
	return strings.Count(logged, "kind="+kind+" ")
}

func abyssFirstSkipReason(logged string) string {
	for _, line := range strings.Split(logged, "\n") {
		if !strings.Contains(line, "kind=game-dungeon-abyss-epic-fragment-skipped ") {
			continue
		}
		if index := strings.Index(line, "reason="); index >= 0 {
			rest := line[index+len("reason="):]
			if space := strings.IndexByte(rest, ' '); space >= 0 {
				return rest[:space]
			}
			return rest
		}
	}
	return ""
}

// TestAbyssFinalWaveFallsBackToTheSessionTableWhenTheRoomHasNoWaveRows 钉住两件事：
//
//	① 房间行里**一个波次行都没有**时，修复档**回落**到会话表（绝不因此判 `final_wave_unknown`）；
//	② 房间与会话两张表都存在时，修复档**以房间为准**并点名来源（`final_wave_source=`）。
func TestAbyssFinalWaveFallsBackToTheSessionTableWhenTheRoomHasNoWaveRows(t *testing.T) {
	// ① 房间读不到波次行：只有会话表可用。
	bare := &runtimeDungeonState{
		HellPartyWaves: []dungeonHellPartyWave{{Order: 2, GroupID: 214}},
	}
	wave, source, ok := abyssEpicFragmentFinalWaveFor(bare, readAbyssEpicFragmentFinalWaveMode(""))
	if !ok || wave.Order != 2 || !wave.containsGroup(214) {
		t.Fatalf("the fallback must use the session table: wave=%+v ok=%t", wave, ok)
	}
	if source != abyssEpicFragmentFinalWaveFromSession {
		t.Fatalf("the fallback must point at the session table, got %s", source)
	}

	// ② 两张表都在：以房间为准，且**与会话表不一致时也照样成立**（P0 的形状）。
	service, leader, leaderConn, runtime := newAbyssFinalWavePartyFixture(t)
	roomWave, roomSource, roomOK := abyssEpicFragmentFinalWaveFor(runtime, readAbyssEpicFragmentFinalWaveMode(""))
	if !roomOK || roomSource != abyssEpicFragmentFinalWaveFromRoom {
		t.Fatalf("the shipped tier must be room-scoped: wave=%+v ok=%t source=%s", roomWave, roomOK, roomSource)
	}
	if roomWave.containsGroup(abyssFinalWaveTestForeignGroup) {
		t.Fatal("the room-scoped wave must not contain the killer session's foreign group")
	}
	leaderConn.write.Reset()
	killDungeonActors(t, service, leader, waveHellPartyActorKeys(t, runtime)...)
	if len(leaderConn.write.Bytes()) == 0 {
		t.Fatal("the wave retirements must still be acknowledged")
	}
}

// TestAbyssPartyDropsAreSharedInsideTheInstance 钉住拾取闸门的修复：
// `PartyShared` 的掉落在同一实例内**任一成员**都能捡；关掉时逐字节等于今天（只认所有者）。
func TestAbyssPartyDropsAreSharedInsideTheInstance(t *testing.T) {
	room := runtimeDungeonRoomKey{X: 0, Y: 0, MapID: 60083}
	owner := newRuntimeDungeonDropOwner()
	drop := &runtimeDungeonDrop{
		ObjectKey:           700,
		SceneSlot:           4,
		Room:                room,
		OwnerActorObjectKey: currentSceneActorObjectKey(99),
		Status:              runtimeDungeonDropAvailable,
		PartyShared:         true,
	}
	owner.byObjectKey[700] = drop

	if _, err := owner.owned(700, room, currentSceneActorObjectKey(99)); err != nil {
		t.Fatalf("the killer must still pick their own drop: %v", err)
	}
	if _, err := owner.owned(700, room, currentSceneActorObjectKey(43)); err != nil {
		t.Fatalf("a party-shared drop must be pickable by the teammate: %v", err)
	}
	drop.PartyShared = false
	if _, err := owner.owned(700, room, currentSceneActorObjectKey(43)); !errors.Is(err, errDungeonDropActorMismatch) {
		t.Fatalf("the legacy tier must reject the teammate, got err=%v", err)
	}
}

// TestAbyssPartyDropOwnershipTiersMatchTheirContracts 钉住三档的账本契约：
//
//	independent（默认，操作者口径「各掉各的」）：每名成员各注册一份
//	    ⇒ 掉落总数 = 单份 × 成员数、有一半属于队友、`PartyShared` 恒 false
//	      （拾取仍是严格所有者校验 ⇒ 只能捡自己的）；
//	shared（第 4 次发版）：只有一份、`PartyShared=true`（谁先谁得）；
//	legacy（第 3 次发版）：只有一份、严格所有者校验。
//
// 杀者自己那一帧在三档下都必须**只带属于自己的那几行**（不能被"独立"改多/改没）。
func TestAbyssPartyDropOwnershipTiersMatchTheirContracts(t *testing.T) {
	peerKey := currentSceneActorObjectKey(43)

	run := func(t *testing.T, mode string) (shared int, total int, foreignOwner int, killerDrops int) {
		t.Helper()
		t.Setenv(abyssPartyDropOwnerEnv, mode)
		service, leader, _, leaderConn, _, runtime, _, _ := partyAbyssPillarFixture(t)
		leaderConn.write.Reset()
		killDungeonActors(t, service, leader, waveHellPartyActorKeys(t, runtime)...)
		for _, body := range abyssOp38Bodies(t, leaderConn.write.Bytes()) {
			killerDrops += abyssOp38DropRows(body)
		}
		if runtime.DropOwner == nil {
			t.Fatal("the party instance must keep a drop ledger")
		}
		for _, drop := range runtime.DropOwner.byObjectKey {
			total++
			if drop.PartyShared {
				shared++
			}
			if drop.OwnerActorObjectKey == peerKey {
				foreignOwner++
			}
		}
		return shared, total, foreignOwner, killerDrops
	}

	legacyShared, legacyTotal, legacyForeign, legacyKillerRows := run(t, abyssPartyDropOwnerLegacyValue)
	if legacyTotal == 0 || legacyShared != 0 || legacyForeign != 0 {
		t.Fatalf("the legacy tier must keep one owner-strict set: shared=%d foreign=%d total=%d",
			legacyShared, legacyForeign, legacyTotal)
	}
	sharedShared, sharedTotal, _, sharedKillerRows := run(t, abyssPartyDropOwnerSharedValue)
	if sharedTotal != legacyTotal || sharedShared != sharedTotal {
		t.Fatalf("the shared tier must keep one relaxed set: shared=%d total=%d want %d",
			sharedShared, sharedTotal, legacyTotal)
	}

	indepShared, indepTotal, indepForeign, indepKillerRows := run(t, "")
	if indepTotal != legacyTotal*2 || indepForeign != indepTotal/2 {
		t.Fatalf("independent drops must register one set per member: total=%d foreign=%d (one set=%d)",
			indepTotal, indepForeign, legacyTotal)
	}
	if indepShared != 0 {
		t.Fatalf("independent drops must keep the strict owner check: shared=%d", indepShared)
	}
	if indepKillerRows != legacyKillerRows {
		t.Fatalf("the killer's own frame must carry exactly its own set: rows=%d want %d",
			indepKillerRows, legacyKillerRows)
	}
	if sharedKillerRows != legacyKillerRows {
		t.Fatalf("the shared tier changed the killer's own rows: %d vs %d", sharedKillerRows, legacyKillerRows)
	}
}

// TestAbyssIndependentDropsArePickableOnlyByTheirOwner 是运维第 3 条投诉
// （「队员无法拾取 队长可以拾取」）的服务端判据：同一本共享账本上，
// **每个人只查得到/只捡得起属于自己的那一条**。
//
//	自己那条 ⇒ `owned()` 通过（= 队员的 op43 会被接受）；
//	别人那条 ⇒ `errDungeonDropActorMismatch`（= "只能捡自己的"，不是"找不到"）。
func TestAbyssIndependentDropsArePickableOnlyByTheirOwner(t *testing.T) {
	service, leader, _, leaderConn, followerConn, runtime, _, _ := partyAbyssPillarFixture(t)
	selfKey := currentSceneActorObjectKey(99)
	peerKey := currentSceneActorObjectKey(43)

	leaderConn.write.Reset()
	followerConn.write.Reset()
	killDungeonActors(t, service, leader, waveHellPartyActorKeys(t, runtime)...)

	_, lastGroup := hellPartyRewardActorKeys(t, runtime)
	finalWave := lastGroup[len(lastGroup)-1]
	var leaderBody, peerBody []byte
	for _, body := range abyssOp38Bodies(t, leaderConn.write.Bytes()) {
		if abyssOp38Key(body) == uint16(finalWave) {
			leaderBody = body
		}
	}
	for _, body := range abyssOp38Bodies(t, followerConn.write.Bytes()) {
		if abyssOp38Key(body) == uint16(finalWave) {
			peerBody = body
		}
	}
	if leaderBody == nil || peerBody == nil {
		t.Fatal("both members must receive their own final-wave frame")
	}
	leaderRows := abyssEpicFragmentDropRows(t, leaderBody)
	peerRows := abyssEpicFragmentDropRows(t, peerBody)
	if len(leaderRows) == 0 || len(leaderRows) != len(peerRows) {
		t.Fatalf("independent drops must give each member one set: %d vs %d", len(leaderRows), len(peerRows))
	}
	if runtime.DropOwner == nil {
		t.Fatal("the party instance must expose one shared ledger")
	}
	room := runtimeDungeonRoomKeyFromScene(mustSceneForTest(t, runtime))
	if _, err := runtime.DropOwner.owned(leaderRows[0].ObjectKey, room, selfKey); err != nil {
		t.Fatalf("the killer must be able to pick its own drop: %v", err)
	}
	if _, err := runtime.DropOwner.owned(peerRows[0].ObjectKey, room, peerKey); err != nil {
		t.Fatalf("the teammate must be able to pick its own drop: %v", err)
	}
	if _, err := runtime.DropOwner.owned(peerRows[0].ObjectKey, room, selfKey); !errors.Is(err, errDungeonDropActorMismatch) {
		t.Fatalf("the killer must not pick the teammate's drop, got err=%v", err)
	}
	if _, err := runtime.DropOwner.owned(leaderRows[0].ObjectKey, room, peerKey); !errors.Is(err, errDungeonDropActorMismatch) {
		t.Fatalf("the teammate must not pick the killer's drop, got err=%v", err)
	}
}

func mustSceneForTest(t *testing.T, runtime *runtimeDungeonState) worldmap.DungeonRoomScene {
	t.Helper()
	scene, ok := runtime.Session.Scene()
	if !ok {
		t.Fatal("dungeon scene is missing")
	}
	return scene
}

// TestAbyssMemberPicksItsOwnDropThroughTheSharedLedger 是运维第 3 条投诉的**端到端**判据
// （走真实 op43 入口 `handleCurrentDungeonPickup`）：
//
//	① 队员捡**自己的**那一条 ⇒ 收下（`game-dungeon-pickup-accepted`）；
//	② 队员捡**队长**那一条 ⇒ `runtime_drop_owner_rejected`（"只能捡自己的"）。
//
// 这条用例同时钉住"队伍实例共享账本"的接线：overlay F7 把它中立化（各人各持一份账本，
// = 第 4 次发版的线上状态）时，① 会变成 `errDungeonDropNotFound`。
func TestAbyssMemberPicksItsOwnDropThroughTheSharedLedger(t *testing.T) {
	logger, logPath := testPassiveObjectDropPacketLogger(t)
	service, leader, follower, leaderConn, followerConn, runtime, _, followerRuntime := partyAbyssPillarFixture(t)
	service.packetLog = logger
	leaderConn.write.Reset()
	followerConn.write.Reset()
	killDungeonActors(t, service, leader, waveHellPartyActorKeys(t, runtime)...)

	_, lastGroup := hellPartyRewardActorKeys(t, runtime)
	finalWave := lastGroup[len(lastGroup)-1]
	leaderRows := abyssFrameRowsForKey(t, leaderConn.write.Bytes(), uint16(finalWave))
	peerRows := abyssFrameRowsForKey(t, followerConn.write.Bytes(), uint16(finalWave))
	if len(leaderRows) == 0 || len(leaderRows) != len(peerRows) {
		t.Fatalf("each member must receive its own set: leader=%d peer=%d", len(leaderRows), len(peerRows))
	}

	// 把队员会话接成"真的在打这一局"的样子（fixture 只造了它的包通道）。
	followerRuntime.Session = runtime.Session
	follower.dungeon.runtime = followerRuntime

	if err := service.handleCurrentDungeonPickup(
		follower, currentDungeonPickupTestBody(peerRows[0].ObjectKey),
	); err != nil {
		t.Fatalf("member pickup of its own drop: %v", err)
	}
	if err := service.handleCurrentDungeonPickup(
		follower, currentDungeonPickupTestBody(leaderRows[0].ObjectKey),
	); err != nil {
		t.Fatalf("member pickup of the leader's drop: %v", err)
	}
	logged := closePassiveObjectDropPacketLogger(t, logger, logPath)
	// ① 自己那一条：**查得到** —— 允许停在后续步骤（fixture 的队员会话没有账号/背包行），
	//    但**绝不允许**是 `runtime_drop_owner_rejected`（= 找不到 / 归属不对）。
	//    第 4 次发版线上正是这一条：队员的 op43 全部回
	//    `… reason=runtime_drop_owner_rejected error=dnf dungeon drop object was not found`。
	ownKey := strconv.FormatUint(uint64(peerRows[0].ObjectKey), 10)
	for _, line := range strings.Split(logged, "\n") {
		if !strings.Contains(line, "kind=game-dungeon-pickup-blocked ") {
			continue
		}
		if strings.Contains(line, "drop_object_key="+ownKey+" ") &&
			strings.Contains(line, "reason=runtime_drop_owner_rejected") {
			t.Fatalf("the teammate's own drop was rejected by the ledger/owner check:\n%s", line)
		}
	}
	// ② 队长那一条：**只能捡自己的** ⇒ 明确按归属拒绝（不是"找不到"）。
	leaderKey := strconv.FormatUint(uint64(leaderRows[0].ObjectKey), 10)
	rejected := false
	for _, line := range strings.Split(logged, "\n") {
		if !strings.Contains(line, "kind=game-dungeon-pickup-blocked ") {
			continue
		}
		if strings.Contains(line, "drop_object_key="+leaderKey+" ") &&
			strings.Contains(line, "belongs to another actor") {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("the teammate must be refused on the leader's drop by ownership:\n%s", logged)
	}
	if strings.Contains(logged, "kind=game-dungeon-pickup-accepted ") &&
		!strings.Contains(logged, "epic_piece=true") {
		t.Fatalf("an accepted pickup here must be the account-ledger epic piece:\n%s", logged)
	}
}

// abyssFrameRowsForKey 取该 key 那一帧的掉落行。
func abyssFrameRowsForKey(t *testing.T, data []byte, key uint16) []abyssEpicFragmentDropRow {
	t.Helper()
	for _, body := range abyssOp38Bodies(t, data) {
		if abyssOp38Key(body) != key {
			continue
		}
		return abyssEpicFragmentDropRows(t, body)
	}
	return nil
}
