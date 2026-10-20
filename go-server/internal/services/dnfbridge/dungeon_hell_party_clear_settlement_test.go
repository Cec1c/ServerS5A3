package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件钉住两个操作者缺陷的修复：
//
//	(a)「开启挑战深渊派对时副本无法正常结算通关，没有翻牌，右上角也没返回城镇的结算」
//	    → **在迷宫 Boss 房**（与普通最终房间同一个判据 `currentDungeonOrdinaryFinalRoomReady`）
//	      跑既有最终房间结算链（`dungeon_hell_party_clear_settlement.go`）。
//	    上一轮"清完深渊房间即结算"是回归：本节的主用例（1/2）就是钉住"深渊房间清空**不得**
//	    结算、必须走到 Boss 房"，回退档（3）钉住那版提前结算逐字节可复现。
//	(b)「双人组队正常通关后翻牌奖励双方都看不到对方翻的牌」
//	    → 翻牌那一刻把队友槽位投影给同一队伍副本实例内的其它成员
//	      （`dungeon_party_card_flip_projection.go`）。
//	(c)「组队挑战深渊：打死柱子 boss 后卡白怪，没有出现 NPC」
//	    → 波次怪退役的权威 op38 投影给队友（`dungeon_hell_party_wave_peer_notify.go`）。
//
// 断言一律打在**发出的包**上（`splitGameServerUpperPacket` 解析真实下行），
// 不以运行时标志代替。

// bridgeHellPartyClearSettlementPVF 把波次 fixture 的迷宫改成 2×1：
// 起点 0:0（`[seal door pos]` 把它换成深渊房图 60083）与 Boss 房 1:0
// —— 也就是现场 dungeon 310 的形状：**深渊房间不是 Boss 房**（`scene.Boss=false`），
// 真实通关判据落在 1:0 那一间。
func bridgeHellPartyClearSettlementPVF() bridgePVFSource {
	source := bridgeHellPartyPVF()
	source["dungeon/hell.dgn"] = "[name]\n`Synthetic Hell Dungeon`\n" +
		"[minimum required level]\n10\n" +
		"[basis level]\n" + itoaTest(testHellPartyBasisLevel) + "\n" +
		"[hell dungeon]\n1\n" +
		"[hell party required item count]\n28\n" +
		"[limit party count]\n1\n" +
		"[maze info]\n" +
		"[size]\n2 1\n" +
		"[greed]\n`A`\n" +
		"[map specification]\n`map` 0 0 " + itoaTest(testHellPartyPlainMapID) + "\n" +
		"`map` 1 0 " + itoaTest(testHellPartyPlainMapID) + "\n" +
		"[start map]\n0 0\n" +
		"[boss map]\n1 0\n" +
		"[seal door map index]\n" + itoaTest(testHellPartyHellMapID) + "\n" +
		"[seal door pos]\n0 0\n" +
		"[seal door appear rate]\n10\n"
	return source
}

// prepareHellPartyClearSettlementRuntime 走真实进图链路（真实 op29 房间提交），
// 玩家就在深渊房间里，波次隐藏模板行落在房间 actor 表上。
func prepareHellPartyClearSettlementRuntime(
	t *testing.T,
) (*Service, *gameSession, *bufferConn, *runtimeDungeonState, worldmap.DungeonRoomScene) {
	t.Helper()
	service, session, conn, runtime, scene := prepareHellPartyDungeonRuntimeForEntryTest(
		t,
		bridgeHellPartyClearSettlementPVF(),
	)
	if !runtime.HellRoomSet || runtime.HellRoom.MapID != testHellPartyHellMapID {
		t.Fatalf("hell room not frozen: set=%t room=%+v", runtime.HellRoomSet, runtime.HellRoom)
	}
	if scene.Map.Map.ID != testHellPartyHellMapID || scene.Coordinate != (worldmap.RoomCoordinate{X: 0, Y: 0}) {
		t.Fatalf("start scene map=%d room=%s want %d/0:0", scene.Map.Map.ID, scene.Coordinate, testHellPartyHellMapID)
	}
	if scene.Boss {
		t.Fatal("the hell room must not be the maze boss room for this fixture")
	}
	if count, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatalf("announce hell party actors: %v count=%d", err, count)
	}
	return service, session, conn, runtime, scene
}

// hellPartyRoomMonsterKeys 返回房图自带（`[monster]` 行）的阻挡怪 object key。
func hellPartyRoomMonsterKeys(t *testing.T, runtime *runtimeDungeonState) []uint32 {
	t.Helper()
	keys := make([]uint32, 0, 6)
	for _, monster := range runtime.Room.Snapshot().Monsters {
		keys = append(keys, monster.ObjectKey)
	}
	if len(keys) != 6 {
		t.Fatalf("fixture room monsters=%v want 6", keys)
	}
	return keys
}

// currentRoomMonsterKeys 返回**当前房间**（移动之后的 Boss 房）的阻挡怪 object key。
func currentRoomMonsterKeys(t *testing.T, runtime *runtimeDungeonState) []uint32 {
	t.Helper()
	snapshot := runtime.Room.Snapshot()
	keys := make([]uint32, 0, len(snapshot.Monsters))
	for _, monster := range snapshot.Monsters {
		keys = append(keys, monster.ObjectKey)
	}
	if len(keys) == 0 {
		t.Fatalf("current room %s has no blocking monsters", snapshot.Coordinate)
	}
	return keys
}

// waveHellPartyActorKeys 返回两条波次的扩展 actor key（组 125 一只、组 211 三只）。
func waveHellPartyActorKeys(t *testing.T, runtime *runtimeDungeonState) []uint32 {
	t.Helper()
	first, last := hellPartyRewardActorKeys(t, runtime)
	keys := make([]uint32, 0, len(first)+len(last))
	keys = append(keys, first...)
	keys = append(keys, last...)
	return keys
}

// killDungeonActors 通过**真实 op39 处理器**把给定 object key 逐个打死。
func killDungeonActors(t *testing.T, service *Service, session *gameSession, keys ...uint32) {
	t.Helper()
	for _, key := range keys {
		if err := service.handleDungeonMonsterDeath(session, hellPartyRewardDeathBody(key)); err != nil {
			t.Fatalf("op39 object_key=%d: %v", key, err)
		}
	}
}

// moveDungeonSessionToRoomForTest 用**真实 op45 处理器**把会话走到目标房间
// （body 只填 NextX/NextY；门规则本轮只做证据收集，不据此放行）。
func moveDungeonSessionToRoomForTest(
	t *testing.T,
	service *Service,
	session *gameSession,
	x, y int64,
) worldmap.DungeonRoomScene {
	t.Helper()
	body := make([]byte, dungeoncmd.MoveMapRequestSize)
	body[0] = byte(x)
	body[1] = byte(y)
	if err := service.handleDungeonMoveMap(session, body); err != nil {
		t.Fatalf("op45 move to %d:%d: %v", x, y, err)
	}
	scene, ok := session.dungeon.runtime.Session.Scene()
	if !ok {
		t.Fatal("dungeon session scene is missing after the move")
	}
	if scene.Coordinate != (worldmap.RoomCoordinate{X: x, Y: y}) {
		t.Fatalf("move landed in room=%s want %d:%d", scene.Coordinate, x, y)
	}
	return scene
}

// currentHellPartyClearStateForTest 读**当前**会话场景（`scene` 是值类型快照，
// 每次都必须从会话重取，否则会读到过期的清房位）。
func currentHellPartyClearStateForTest(t *testing.T, runtime *runtimeDungeonState) dungeonHellPartyClearState {
	t.Helper()
	scene, ok := runtime.Session.Scene()
	if !ok {
		t.Fatal("dungeon session scene is missing")
	}
	return currentDungeonHellPartyClearState(runtime, scene)
}

// currentHellPartyDecisionForTest 用给定档位跑一次纯判定。
func currentHellPartyDecisionForTest(
	t *testing.T,
	runtime *runtimeDungeonState,
	mode dungeonHellPartyClearSettlementMode,
) (bool, string) {
	t.Helper()
	return currentDungeonHellPartyClearDecision(currentHellPartyClearStateForTest(t, runtime), mode)
}

// emittedPacketIDs 列出下行字节流里的 msg id 序列（断言"发了什么、按什么顺序"）。
func emittedPacketIDs(t *testing.T, data []byte) []uint16 {
	t.Helper()
	rest := data
	ids := make([]uint16, 0, 8)
	for len(rest) != 0 {
		var packet dnfproto.ChannelPacket
		packet, rest = splitGameServerUpperPacket(t, rest)
		ids = append(ids, packet.Header.MsgID)
	}
	return ids
}

// countPacketsWithID 数出某个 msg id 的条数。
func countPacketsWithID(t *testing.T, data []byte, msgID uint16) int {
	t.Helper()
	count := 0
	for _, id := range emittedPacketIDs(t, data) {
		if id == msgID {
			count++
		}
	}
	return count
}

// settlementEntryPackets 从下行字节流里数出 msg 31（结算入口）的条数并校验其形状。
func settlementEntryPackets(t *testing.T, data []byte) int {
	t.Helper()
	rest := data
	count := 0
	for len(rest) != 0 {
		var packet dnfproto.ChannelPacket
		packet, rest = splitGameServerUpperPacket(t, rest)
		if packet.Header.MsgID != currentDungeonSettlementEntryMsgID {
			continue
		}
		count++
		if packet.Header.Classification != 0 || !bytes.Equal(packet.Body, []byte{0}) {
			t.Fatalf("settlement entry shape header=%+v body=%x", packet.Header, packet.Body)
		}
	}
	return count
}

// killEntireHellRoomAndWaves 清掉深渊房间的房图怪与全部波次怪（= 操作者说的"打死柱子"）。
func killEntireHellRoomAndWaves(t *testing.T, service *Service, session *gameSession, runtime *runtimeDungeonState) {
	t.Helper()
	killDungeonActors(t, service, session, hellPartyRoomMonsterKeys(t, runtime)...)
	killDungeonActors(t, service, session, waveHellPartyActorKeys(t, runtime)...)
}

// TestHellPartyRoomClearDoesNotSettleAndTheRunContinuesToTheBossRoom 是回归 (a) 的主用例：
// 清光深渊房间（房图怪 + 柱子/波次怪）之后**不得**出现 op115/op31，本局必须还在进行中；
// 只有走到迷宫 Boss 房（1:0）并清掉 Boss 房的阻挡怪，才出现结算入口。
func TestHellPartyRoomClearDoesNotSettleAndTheRunContinuesToTheBossRoom(t *testing.T) {
	service, session, conn, runtime, _ := prepareHellPartyClearSettlementRuntime(t)
	roomMonsters := hellPartyRoomMonsterKeys(t, runtime)
	waves := waveHellPartyActorKeys(t, runtime)

	// ① 房图怪全清：房间被权威判清，但这不是 Boss 房 ⇒ 一个结算字节都不许发。
	conn.write.Reset()
	killDungeonActors(t, service, session, roomMonsters...)
	if ids := emittedPacketIDs(t, conn.write.Bytes()); len(ids) != len(roomMonsters) {
		t.Fatalf("hell room blocking deaths=%v want %d op38 only", ids, len(roomMonsters))
	}
	if count := settlementEntryPackets(t, conn.write.Bytes()); count != 0 {
		t.Fatalf("the hell room must not settle while it is not the boss room: op31 count=%d", count)
	}

	// ② 波次怪（柱子）全灭 —— 上一轮就是在这里提前结算的，本轮必须**什么都不发**。
	conn.write.Reset()
	killDungeonActors(t, service, session, waves...)
	data := conn.write.Bytes()
	ids := emittedPacketIDs(t, data)
	if len(ids) != len(waves) {
		t.Fatalf("hell room clear packet order=%v want %d × op38 (no settlement) bytes=%x",
			ids, len(waves), data)
	}
	for _, id := range ids {
		if id != uint16(dnfenum.CmdPacketNotifyDieMonster) {
			t.Fatalf("hell room clear emitted %d, want only op38: %v", id, ids)
		}
	}
	if count := countPacketsWithID(t, data, uint16(dnfenum.CmdPacketNotifyBossDieCheck)); count != 0 {
		t.Fatalf("hell room clear must not emit op115: count=%d", count)
	}
	if count := settlementEntryPackets(t, data); count != 0 {
		t.Fatalf("hell room clear must not emit op31: count=%d bytes=%x", count, data)
	}
	if snapshot := runtime.Session.Snapshot(); snapshot.Run.Status != worldmap.DungeonRunActive {
		t.Fatalf("the run must continue after the hell room clear: status=%s", snapshot.Run.Status)
	}
	if runtime.settlementEntrySent || runtime.ordinaryFinalRoomClearAccepted || runtime.bossDieCheckAccepted {
		t.Fatalf("the hell room clear committed the run: entry=%t ordinary=%t boss=%t",
			runtime.settlementEntrySent, runtime.ordinaryFinalRoomClearAccepted, runtime.bossDieCheckAccepted)
	}
	// 纯判定：深渊房间 + 房间判清 + 波次全灭，默认档仍然是"不结算"。
	if settle, reason := currentHellPartyDecisionForTest(t, runtime, dungeonHellPartyClearSettlementBossRoom); settle ||
		reason != "not_the_maze_boss_room" {
		t.Fatalf("hell room decision settle=%t reason=%s want false/not_the_maze_boss_room", settle, reason)
	}

	// ③ 走到 Boss 房 1:0：地图还是普通图，`scene.Boss=true`，波次行不在这间房。
	conn.write.Reset()
	moved := moveDungeonSessionToRoomForTest(t, service, session, 1, 0)
	if !moved.Boss {
		t.Fatalf("the boss room must carry scene.Boss: %+v", moved)
	}
	if state := currentHellPartyClearStateForTest(t, runtime); state.IsHellRoom || !state.BossRoom || !state.AbyssRun {
		t.Fatalf("boss room state=%+v want hell=false boss=true abyss_run=true", state)
	}
	if count := settlementEntryPackets(t, conn.write.Bytes()); count != 0 {
		t.Fatalf("entering the boss room must not settle by itself: op31 count=%d", count)
	}

	// ④ 清掉 Boss 房：结算必须在这里发生。
	conn.write.Reset()
	killDungeonActors(t, service, session, currentRoomMonsterKeys(t, runtime)...)
	clearBytes := conn.write.Bytes()
	if count := settlementEntryPackets(t, clearBytes); count != 1 {
		t.Fatalf("the boss room must settle exactly once: op31 count=%d bytes=%x", count, clearBytes)
	}
	clearIDs := emittedPacketIDs(t, clearBytes)
	if len(clearIDs) < 3 || clearIDs[len(clearIDs)-2] != uint16(dnfenum.CmdPacketNotifyBossDieCheck) ||
		clearIDs[len(clearIDs)-1] != currentDungeonSettlementEntryMsgID {
		t.Fatalf("boss room settlement order=%v want … %d %d", clearIDs,
			dnfenum.CmdPacketNotifyBossDieCheck, currentDungeonSettlementEntryMsgID)
	}
	if snapshot := runtime.Session.Snapshot(); snapshot.Run.Status != worldmap.DungeonRunCompleted ||
		!runtime.settlementEntrySent || snapshot.Run.Current != (worldmap.RoomCoordinate{X: 1, Y: 0}) {
		t.Fatalf("boss room clear did not commit the run: status=%s room=%s entry=%t",
			snapshot.Run.Status, snapshot.Run.Current, runtime.settlementEntrySent)
	}
}

// TestAbyssRunSettlesExactlyOnceAtTheBossRoom 钉住"整局只有一次结算，且就在 Boss 房"：
// 深渊房清空 0 次、Boss 房清空恰好 1 次（补刀也不得出现第二条 op31）。
func TestAbyssRunSettlesExactlyOnceAtTheBossRoom(t *testing.T) {
	service, session, conn, runtime, _ := prepareHellPartyClearSettlementRuntime(t)
	collected := make([]byte, 0, 4096)

	conn.write.Reset()
	killEntireHellRoomAndWaves(t, service, session, runtime)
	collected = append(collected, conn.write.Bytes()...)
	if count := settlementEntryPackets(t, conn.write.Bytes()); count != 0 {
		t.Fatalf("the hell room must never settle: op31 count=%d", count)
	}

	conn.write.Reset()
	moveDungeonSessionToRoomForTest(t, service, session, 1, 0)
	collected = append(collected, conn.write.Bytes()...)
	bossKeys := currentRoomMonsterKeys(t, runtime)
	conn.write.Reset()
	killDungeonActors(t, service, session, bossKeys...)
	collected = append(collected, conn.write.Bytes()...)

	if count := settlementEntryPackets(t, collected); count != 1 {
		t.Fatalf("a full abyss run must settle exactly once: op31 count=%d", count)
	}
	if count := countPacketsWithID(t, collected, uint16(dnfenum.CmdPacketNotifyBossDieCheck)); count != 1 {
		t.Fatalf("a full abyss run must emit op115 exactly once: count=%d", count)
	}
	// 结算之后再补一刀（重复 op39 / 队友广播的补刀）不得再发第二条 op31。
	conn.write.Reset()
	killDungeonActors(t, service, session, bossKeys[0])
	if count := settlementEntryPackets(t, conn.write.Bytes()); count != 0 {
		t.Fatalf("a second settlement entry was sent after the clear: %x", conn.write.Bytes())
	}
	if snapshot := runtime.Session.Snapshot(); snapshot.Run.Status != worldmap.DungeonRunCompleted {
		t.Fatalf("run status=%s want %s", snapshot.Run.Status, worldmap.DungeonRunCompleted)
	}
}

// TestHellPartyClearSettlementLegacyReproducesTheDeployedTooEarlySettlement 是回退档的
// 逐字节钉子：`legacy` = **上一轮上线那版**（深渊房间清空即结算）。默认档那串下行是它的
// 真前缀，回退档多出来的部分必须恰好是"结算尾部"（op115 + op31）—— 即回退档没有改变任何
// 一只怪的死亡下行，只是把当时那条提前结算补回来。
func TestHellPartyClearSettlementLegacyReproducesTheDeployedTooEarlySettlement(t *testing.T) {
	run := func(t *testing.T, mode string) ([]byte, worldmap.DungeonRunStatus, bool) {
		t.Helper()
		t.Setenv(dungeonHellPartyClearSettlementEnv, mode)
		service, session, conn, runtime, _ := prepareHellPartyClearSettlementRuntime(t)
		killDungeonActors(t, service, session, hellPartyRoomMonsterKeys(t, runtime)...)
		conn.write.Reset()
		killDungeonActors(t, service, session, waveHellPartyActorKeys(t, runtime)...)
		return append([]byte(nil), conn.write.Bytes()...),
			runtime.Session.Snapshot().Run.Status, runtime.settlementEntrySent
	}
	legacyBytes, legacyStatus, legacyEntry := run(t, dungeonHellPartyClearSettlementLegacyValue)
	fixedBytes, fixedStatus, fixedEntry := run(t, "vendor")

	if fixedStatus != worldmap.DungeonRunActive || fixedEntry {
		t.Fatalf("the fixed mode must leave the run active after the hell room clear: status=%s entry=%t",
			fixedStatus, fixedEntry)
	}
	if legacyStatus != worldmap.DungeonRunCompleted || !legacyEntry {
		t.Fatalf("the legacy mode must reproduce the deployed early settlement: status=%s entry=%t",
			legacyStatus, legacyEntry)
	}
	if !bytes.HasPrefix(legacyBytes, fixedBytes) {
		t.Fatalf("the legacy stream must extend the fixed stream byte for byte:\nfixed =%x\nlegacy=%x",
			fixedBytes, legacyBytes)
	}
	tail := legacyBytes[len(fixedBytes):]
	tailIDs := emittedPacketIDs(t, tail)
	if len(tailIDs) != 2 || tailIDs[0] != uint16(dnfenum.CmdPacketNotifyBossDieCheck) ||
		tailIDs[1] != currentDungeonSettlementEntryMsgID {
		t.Fatalf("the legacy rollback must add exactly the settlement tail: ids=%v bytes=%x", tailIDs, tail)
	}
	if count := settlementEntryPackets(t, legacyBytes); count != 1 {
		t.Fatalf("legacy op31 count=%d want exactly 1", count)
	}
	// 提前结算那一条必须落在深渊房间（room=0:0 / map=60083），这是"线上那版"的签名。
	if settle, reason := currentHellPartyDecisionForTest(t, mustPrepareLegacyRuntimeForDecision(t), dungeonHellPartyClearSettlementHellRoom); !settle ||
		reason != "deployed_all_hell_party_wave_monsters_dead_and_room_cleared" {
		t.Fatalf("legacy decision settle=%t reason=%s", settle, reason)
	}
}

// mustPrepareLegacyRuntimeForDecision 造一份"深渊房间已清空"的运行时（legacy 判定用）。
func mustPrepareLegacyRuntimeForDecision(t *testing.T) *runtimeDungeonState {
	t.Helper()
	t.Setenv(dungeonHellPartyClearSettlementEnv, dungeonHellPartyClearSettlementLegacyValue)
	service, session, _, runtime, _ := prepareHellPartyClearSettlementRuntime(t)
	killEntireHellRoomAndWaves(t, service, session, runtime)
	return runtime
}

// TestHellPartyClearSettlementOffEmitsNothingExtra 钉住 `off` 档（更早的历史行为）：
// 深渊房间一个结算字节都不多（只有每只怪各自的 op38），完成位一位都不置。
func TestHellPartyClearSettlementOffEmitsNothingExtra(t *testing.T) {
	t.Setenv(dungeonHellPartyClearSettlementEnv, dungeonHellPartyClearSettlementOffValue)
	service, session, conn, runtime, _ := prepareHellPartyClearSettlementRuntime(t)
	killDungeonActors(t, service, session, hellPartyRoomMonsterKeys(t, runtime)...)
	waves := waveHellPartyActorKeys(t, runtime)

	conn.write.Reset()
	killDungeonActors(t, service, session, waves...)
	data := append([]byte(nil), conn.write.Bytes()...)
	if count := settlementEntryPackets(t, data); count != 0 {
		t.Fatalf("off mode must not settle the abyss room: op31 count=%d", count)
	}
	ids := emittedPacketIDs(t, data)
	if len(ids) != len(waves) {
		t.Fatalf("off mode deaths=%v want %d op38 (bytes=%x)", ids, len(waves), data)
	}
	for _, id := range ids {
		if id != uint16(dnfenum.CmdPacketNotifyDieMonster) {
			t.Fatalf("off mode emitted an unexpected message %d (ids=%v)", id, ids)
		}
	}
	if runtime.settlementEntrySent || runtime.bossDieCheckAccepted || runtime.ordinaryFinalRoomClearAccepted {
		t.Fatalf("off mode mutated the completion state: entry=%t boss=%t ordinary=%t",
			runtime.settlementEntrySent, runtime.bossDieCheckAccepted, runtime.ordinaryFinalRoomClearAccepted)
	}
}

// TestHellPartyClearIsIndependentOfTheRewardHooks 钉住"奖励路径既不能挡住也不能重复结算"：
// 奖励投骰/必掉碎片全部回退时，Boss 房的结算照旧发生，且恰好一条 op31。
func TestHellPartyClearIsIndependentOfTheRewardHooks(t *testing.T) {
	for _, test := range []struct {
		name string
		env  map[string]string
	}{
		{name: "default_reward_switches"},
		{
			name: "reward_roll_legacy_fragment_off",
			env: map[string]string{
				hellPartyRewardRollEnv:            hellPartyRewardRollLegacyValue,
				abyssEpicFragmentEnv:              abyssEpicFragmentValueOff,
				dungeonPartyCardFlipProjectionEnv: dungeonPartyCardFlipProjectionLegacyValue,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			service, session, conn, runtime, _ := prepareHellPartyClearSettlementRuntime(t)
			killEntireHellRoomAndWaves(t, service, session, runtime)
			moveDungeonSessionToRoomForTest(t, service, session, 1, 0)
			conn.write.Reset()
			killDungeonActors(t, service, session, currentRoomMonsterKeys(t, runtime)...)
			if count := settlementEntryPackets(t, conn.write.Bytes()); count != 1 {
				t.Fatalf("the boss room clear must not depend on the reward hooks: op31 count=%d", count)
			}
		})
	}
}

// TestHellPartyClearSettlementSwitchOnlyKnownLiteralsRollBack 钉住开关档位：
// 只有字面量 `legacy`（提前结算）与 `off`（不结算）才改变行为，其它任何取值
// （含拼错、大小写、空白）都取默认修复档。
func TestHellPartyClearSettlementSwitchOnlyKnownLiteralsRollBack(t *testing.T) {
	for _, test := range []struct {
		value string
		want  dungeonHellPartyClearSettlementMode
	}{
		{value: "", want: dungeonHellPartyClearSettlementBossRoom},
		{value: "vendor", want: dungeonHellPartyClearSettlementBossRoom},
		{value: "VENDOR", want: dungeonHellPartyClearSettlementBossRoom},
		{value: "boss", want: dungeonHellPartyClearSettlementBossRoom},
		{value: "legacyy", want: dungeonHellPartyClearSettlementBossRoom},
		{value: "offf", want: dungeonHellPartyClearSettlementBossRoom},
		{value: "Legacy", want: dungeonHellPartyClearSettlementHellRoom},
		{value: " legacy ", want: dungeonHellPartyClearSettlementHellRoom},
		{value: "OFF", want: dungeonHellPartyClearSettlementOff},
		{value: " off ", want: dungeonHellPartyClearSettlementOff},
	} {
		got := readDungeonHellPartyClearSettlementMode(test.value)
		if got != test.want {
			t.Fatalf("read(%q)=%v want %v", test.value, got, test.want)
		}
		if got.settles() != (test.want != dungeonHellPartyClearSettlementOff) {
			t.Fatalf("settles() mismatch for %q", test.value)
		}
		if got.bossRoomOnly() != (test.want == dungeonHellPartyClearSettlementBossRoom) {
			t.Fatalf("bossRoomOnly() mismatch for %q", test.value)
		}
	}
}

// TestHellPartyClearDecisionRequiresTheOrdinaryBossRoomCriterion 逐条钉住纯判定：
// 默认档 = 普通最终房间那条判据（保留 `scene.Boss` + `scene.Coordinate == BossCoordinate`
// + `scene.Cleared`），深渊房间无论清得多干净都不结算。
func TestHellPartyClearDecisionRequiresTheOrdinaryBossRoomCriterion(t *testing.T) {
	bossCoordinate := worldmap.RoomCoordinate{X: 1, Y: 0}
	hellRoomCleared := dungeonHellPartyClearState{
		AbyssRun: true, IsHellRoom: true, Room: "0:0", RoomMapID: testHellPartyHellMapID,
		HellRoomMapID: testHellPartyHellMapID, RoomCleared: true,
		WaveTotal: 4, WaveRemaining: 0, BlockingRemaining: 0,
	}
	if settle, reason := currentDungeonHellPartyClearDecision(hellRoomCleared, dungeonHellPartyClearSettlementBossRoom); settle ||
		reason != "not_the_maze_boss_room" {
		t.Fatalf("cleared hell room settle=%t reason=%s want false/not_the_maze_boss_room", settle, reason)
	}
	if settle, reason := currentDungeonHellPartyClearDecision(hellRoomCleared, dungeonHellPartyClearSettlementHellRoom); !settle ||
		reason != "deployed_all_hell_party_wave_monsters_dead_and_room_cleared" {
		t.Fatalf("legacy cleared hell room settle=%t reason=%s want true/deployed reason", settle, reason)
	}
	if settle, reason := currentDungeonHellPartyClearDecision(hellRoomCleared, dungeonHellPartyClearSettlementOff); settle ||
		reason != "hell_party_clear_settlement_disabled" {
		t.Fatalf("off settled=%t reason=%s want false/disabled", settle, reason)
	}

	bossRoom := func(mutate func(*dungeonHellPartyClearState)) dungeonHellPartyClearState {
		state := dungeonHellPartyClearState{
			AbyssRun: true, Room: bossCoordinate.String(), RoomMapID: testHellPartyPlainMapID,
			HellRoomMapID: testHellPartyHellMapID, BossRoom: true, BossCoordinateMatched: true,
			RoomCleared: true, BossRoomReady: true, BossRoomSource: "86jp_prepare_dungeon_clear_boss_room_after_authoritative_op39",
		}
		if mutate != nil {
			mutate(&state)
		}
		return state
	}
	if settle, reason := currentDungeonHellPartyClearDecision(bossRoom(nil), dungeonHellPartyClearSettlementBossRoom); !settle ||
		reason != "86jp_prepare_dungeon_clear_boss_room_after_authoritative_op39" {
		t.Fatalf("ready boss room settle=%t reason=%s want true/ordinary source", settle, reason)
	}
	if settle, reason := currentDungeonHellPartyClearDecision(bossRoom(func(state *dungeonHellPartyClearState) {
		state.BossCoordinateMatched = false
	}), dungeonHellPartyClearSettlementBossRoom); settle || reason != "boss_coordinate_mismatch" {
		t.Fatalf("boss coordinate mismatch settle=%t reason=%s", settle, reason)
	}
	if settle, reason := currentDungeonHellPartyClearDecision(bossRoom(func(state *dungeonHellPartyClearState) {
		state.RoomCleared = false
	}), dungeonHellPartyClearSettlementBossRoom); settle || reason != "boss_room_not_authoritatively_cleared" {
		t.Fatalf("uncleared boss room settle=%t reason=%s", settle, reason)
	}
	if settle, reason := currentDungeonHellPartyClearDecision(bossRoom(func(state *dungeonHellPartyClearState) {
		state.BossRoomReady = false
		state.BossRoomSource = ""
	}), dungeonHellPartyClearSettlementBossRoom); settle || reason != "ordinary_final_room_criterion_not_ready" {
		t.Fatalf("boss room without the ordinary criterion settle=%t reason=%s", settle, reason)
	}
	if settle, reason := currentDungeonHellPartyClearDecision(dungeonHellPartyClearState{}, dungeonHellPartyClearSettlementBossRoom); settle ||
		reason != "not_a_hell_party_run" {
		t.Fatalf("non abyss run settle=%t reason=%s", settle, reason)
	}
	// room_type 必须能一眼区分"判定发生在哪种房间"。
	for _, test := range []struct {
		state dungeonHellPartyClearState
		want  string
	}{
		{state: hellRoomCleared, want: "hell_party_room"},
		{state: bossRoom(nil), want: "boss_room"},
		{state: bossRoom(func(state *dungeonHellPartyClearState) { state.IsHellRoom = true }), want: "hell_party_room_and_boss_room"},
		{state: dungeonHellPartyClearState{}, want: "ordinary_room"},
	} {
		if got := test.state.roomType(); got != test.want {
			t.Fatalf("roomType(%+v)=%s want %s", test.state, got, test.want)
		}
	}
}

// TestNonHellPartyRoomClearIsByteIdenticalWithTheSwitchOnAndOff 钉住"普通（非深渊局）
// 逐字节不变"：同一串死亡事件在三个档位下下行的字节必须完全一致，且永不结算。
func TestNonHellPartyRoomClearIsByteIdenticalWithTheSwitchOnAndOff(t *testing.T) {
	run := func(t *testing.T, mode string) []byte {
		t.Setenv(dungeonHellPartyClearSettlementEnv, mode)
		service, session, conn, runtime, _ := prepareHellPartyClearSettlementRuntime(t)
		// 关掉本局的深渊房间标记 ⇒ 这就是一间普通房间（波次 actor 仍在册，
		// 退役路径照走，但本文件必须完全不介入）。
		runtime.HellRoomSet = false
		keys := hellPartyRoomMonsterKeys(t, runtime)
		keys = append(keys, waveHellPartyActorKeys(t, runtime)...)
		conn.write.Reset()
		killDungeonActors(t, service, session, keys...)
		return append([]byte(nil), conn.write.Bytes()...)
	}
	fixed := run(t, "vendor")
	legacy := run(t, dungeonHellPartyClearSettlementLegacyValue)
	off := run(t, dungeonHellPartyClearSettlementOffValue)
	if !bytes.Equal(fixed, legacy) || !bytes.Equal(fixed, off) {
		t.Fatalf("a non-abyss room changed with the switch:\nfixed =%x\nlegacy=%x\noff   =%x", fixed, legacy, off)
	}
	if count := settlementEntryPackets(t, fixed); count != 0 {
		t.Fatalf("a non-abyss room must never settle from this file: count=%d", count)
	}
}

// partyHellPartyWaveFixture 造一对同队会话，共享**同一个深渊房间运行时**：
// 队长（char 99）与队友（char 43）落在同一个队伍副本实例、同一份房间对象上。
func partyHellPartyWaveFixture(
	t *testing.T,
) (*Service, *gameSession, *gameSession, *bufferConn, *runtimeDungeonState, worldmap.DungeonRoomScene) {
	t.Helper()
	service, leader, _, leaderRuntime, scene := prepareHellPartyClearSettlementRuntime(t)
	state := alignedcmd.PartyState{
		PartyID:    3,
		UserID:     1001,
		MaxMembers: 4,
		UserState:  1,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 1001, UserState: 1},
			{UserID: 1002, UserState: 1},
		},
	}
	storeRuntimePartyState(leader, state)
	followerConn := &bufferConn{}
	follower := &gameSession{conn: followerConn, connID: "hell-party-wave-peer-test", selectedCharacterID: 43}
	storeRuntimePartyState(follower, state)
	followerRuntime := &runtimeDungeonState{
		Dungeon:       leaderRuntime.Dungeon,
		MazeIndex:     leaderRuntime.MazeIndex,
		Seed:          leaderRuntime.Seed,
		Room:          leaderRuntime.Room,
		NextObjectKey: leaderRuntime.NextObjectKey,
		Rooms:         make(map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit),
	}
	if instance := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene); instance == nil {
		t.Fatal("party dungeon instance was not published")
	}
	if instance := service.adoptPartyDungeonInstance(follower, followerRuntime, scene); instance == nil {
		t.Fatal("the second member did not join the party dungeon instance")
	}
	return service, leader, follower, followerConn, leaderRuntime, scene
}

// TestHellPartyWaveRetirementIsProjectedToThePartyMember 是缺陷 (c) 的主用例：
// 队长打死一只波次怪（柱子/深渊怪）后，队友必须收到同一份**不带掉落行**的 op38
// （现场：队友那侧什么都没有 ⇒ 屏幕上一只服务端已经判死的白怪）。
func TestHellPartyWaveRetirementIsProjectedToThePartyMember(t *testing.T) {
	service, leader, _, followerConn, runtime, _ := partyHellPartyWaveFixture(t)
	waves := waveHellPartyActorKeys(t, runtime)
	leaderConn := leader.conn.(*bufferConn)
	leaderConn.write.Reset()
	followerConn.write.Reset()

	killDungeonActors(t, service, leader, waves[0])

	leaderIDs := emittedPacketIDs(t, leaderConn.write.Bytes())
	if len(leaderIDs) != 1 || leaderIDs[0] != uint16(dnfenum.CmdPacketNotifyDieMonster) {
		t.Fatalf("the killer's own stream changed: ids=%v", leaderIDs)
	}
	followerIDs := emittedPacketIDs(t, followerConn.write.Bytes())
	if len(followerIDs) != 1 || followerIDs[0] != uint16(dnfenum.CmdPacketNotifyDieMonster) {
		t.Fatalf("the partner must receive the wave retirement notification: ids=%v bytes=%x",
			followerIDs, followerConn.write.Bytes())
	}
	// 队友那一份必须是同一形状（7 字节零掉落体、目标就是那只波次怪），不能带掉落行
	// （重复下发会在队友客户端复制地面物品）。
	peerPacket, peerRest := splitGameServerUpperPacket(t, followerConn.write.Bytes())
	if len(peerRest) != 0 || peerPacket.Header.Classification != 0 {
		t.Fatalf("peer notification header=%+v rest=%x", peerPacket.Header, peerRest)
	}
	wantPeerBody, err := buildCurrentDungeonDeathNotificationBodyWithDrops(
		waves[0], currentDungeonDeathResponseMonster, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(peerPacket.Body, wantPeerBody) {
		t.Fatalf("peer notification body=%x want the 7-byte zero-drop op38 %x for object_key=%d",
			peerPacket.Body, wantPeerBody, waves[0])
	}
	if len(peerPacket.Body) != currentDungeonZeroDropDeathBodySize {
		t.Fatalf("peer notification body length=%d want %d (no drop rows)",
			len(peerPacket.Body), currentDungeonZeroDropDeathBodySize)
	}
	if count := countPacketsWithID(t, followerConn.write.Bytes(), uint16(dnfenum.CmdPacketNotifyDieMonster)); count != 1 {
		t.Fatalf("peer notification count=%d want exactly 1", count)
	}
}

// TestHellPartyWavePeerNotifyLegacySendsNothingToThePartner 钉住回退档：
// `legacy` 下队友一条包都收不到（逐字节等于修复前）。
func TestHellPartyWavePeerNotifyLegacySendsNothingToThePartner(t *testing.T) {
	t.Setenv(dungeonHellPartyWavePeerNotifyEnv, dungeonHellPartyWavePeerNotifyLegacyValue)
	service, leader, _, followerConn, runtime, _ := partyHellPartyWaveFixture(t)
	waves := waveHellPartyActorKeys(t, runtime)
	leaderConn := leader.conn.(*bufferConn)
	leaderConn.write.Reset()
	followerConn.write.Reset()

	killDungeonActors(t, service, leader, waves[0])

	if len(followerConn.write.Bytes()) != 0 {
		t.Fatalf("legacy mode must not project anything to the partner: %x", followerConn.write.Bytes())
	}
	if ids := emittedPacketIDs(t, leaderConn.write.Bytes()); len(ids) != 1 {
		t.Fatalf("legacy mode changed the killer's own stream: ids=%v", ids)
	}
}

// TestHellPartyWavePeerNotifySwitchOnlyLegacyRollsBack 钉住 (c) 的开关档位。
func TestHellPartyWavePeerNotifySwitchOnlyLegacyRollsBack(t *testing.T) {
	for _, test := range []struct {
		value string
		want  dungeonHellPartyWavePeerNotifyMode
	}{
		{value: "", want: dungeonHellPartyWavePeerNotifyOn},
		{value: "vendor", want: dungeonHellPartyWavePeerNotifyOn},
		{value: "on", want: dungeonHellPartyWavePeerNotifyOn},
		{value: "LEGACYx", want: dungeonHellPartyWavePeerNotifyOn},
		{value: "legacy", want: dungeonHellPartyWavePeerNotifyLegacy},
		{value: " legacy ", want: dungeonHellPartyWavePeerNotifyLegacy},
	} {
		got := readDungeonHellPartyWavePeerNotifyMode(test.value)
		if got != test.want {
			t.Fatalf("read(%q)=%v want %v", test.value, got, test.want)
		}
		if got.notifies() != (test.want == dungeonHellPartyWavePeerNotifyOn) {
			t.Fatalf("notifies() mismatch for %q", test.value)
		}
	}
}

// TestPartyCardFlipIsProjectedToTheOtherMember 是缺陷 (b) 的主用例：
// 队长翻免费行后，队友必须收到一条 op71 且其中**队长的槽位已翻开**；
// 队友随后翻付费行时，他自己的投影必须同时带上两人的槽位（不会把队长的牌抹掉）。
func TestPartyCardFlipIsProjectedToTheOtherMember(t *testing.T) {
	service, leader, follower, leaderRuntime, followerRuntime, scene := partyDungeonInstanceFixture(t, 21, 777)
	leader.dungeon.runtime = leaderRuntime
	follower.dungeon.runtime = followerRuntime
	if instance := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene); instance == nil {
		t.Fatal("party dungeon instance was not published")
	}
	if joined := service.adoptPartyDungeonInstance(follower, followerRuntime, scene); joined == nil {
		t.Fatal("second member did not join the party dungeon instance")
	}
	freezeDungeonCardStateForTest(t, leaderRuntime, "1001")
	freezeDungeonCardStateForTest(t, followerRuntime, "1002")
	for _, runtime := range []*runtimeDungeonState{leaderRuntime, followerRuntime} {
		runtime.settlementCardLayoutSent = true
		runtime.settlementCardRightStateSent = true
		runtime.settlementClearRewardSent = true
		runtime.advanceSettlementPhase(currentDungeonSettlementPhaseCardsRevealed)
	}
	leaderConn := leader.conn.(*bufferConn)
	followerConn := follower.conn.(*bufferConn)
	leaderConn.write.Reset()
	followerConn.write.Reset()

	// 队长（槽位 0）翻免费行。
	leader.dungeon.mu.Lock()
	err := service.selectCurrentDungeonCardLocked(leader, leaderRuntime, dungeonCardSideFree, 0, "test_leader_free_flip")
	leader.dungeon.mu.Unlock()
	if err != nil {
		t.Fatalf("leader free flip: %v", err)
	}

	leaderAck, rest := splitGameServerUpperPacket(t, leaderConn.write.Bytes())
	if leaderAck.Header.MsgID != uint16(dnfenum.CmdPacketSelectCard) ||
		leaderAck.Header.Classification != currentDungeonCardResponseClass || len(rest) != 0 {
		t.Fatalf("leader ack header=%+v rest=%x", leaderAck.Header, rest)
	}
	assertOp71SlotFlipped(t, leaderAck.Body, 0, dungeonCardSideFree, 0, "leader's own ack")

	peerAck, peerRest := splitGameServerUpperPacket(t, followerConn.write.Bytes())
	if peerAck.Header.MsgID != uint16(dnfenum.CmdPacketSelectCard) ||
		peerAck.Header.Classification != currentDungeonCardResponseClass || len(peerRest) != 0 {
		t.Fatalf("the partner must receive the flipped-row projection: header=%+v rest=%x", peerAck.Header, peerRest)
	}
	assertOp71SlotFlipped(t, peerAck.Body, 0, dungeonCardSideFree, 0, "partner projection of the leader's flip")

	// 队友（槽位 1）翻付费行：他自己的投影必须同时带两个槽位。
	followerConn.write.Reset()
	leaderConn.write.Reset()
	follower.dungeon.mu.Lock()
	err = service.selectCurrentDungeonCardLocked(follower, followerRuntime, dungeonCardSidePaid, 1, "test_follower_paid_flip")
	follower.dungeon.mu.Unlock()
	if err != nil {
		t.Fatalf("follower paid flip: %v", err)
	}
	followerAck, _ := splitGameServerUpperPacket(t, followerConn.write.Bytes())
	assertOp71SlotFlipped(t, followerAck.Body, 1, dungeonCardSidePaid, 1, "follower's own ack")
	assertOp71SlotFlipped(t, followerAck.Body, 0, dungeonCardSideFree, 0, "partner slot kept in the follower's ack")
	leaderPeerAck, _ := splitGameServerUpperPacket(t, leaderConn.write.Bytes())
	assertOp71SlotFlipped(t, leaderPeerAck.Body, 1, dungeonCardSidePaid, 1, "leader projection of the follower's flip")
	assertOp71SlotFlipped(t, leaderPeerAck.Body, 0, dungeonCardSideFree, 0, "leader's own slot kept in the peer projection")
}

// TestPartyCardFlipProjectionLegacyStillSendsNothingToThePartner 钉住回退档：
// `legacy` 下队友一条包都收不到，而请求方自己的 op71 照旧。
func TestPartyCardFlipProjectionLegacyStillSendsNothingToThePartner(t *testing.T) {
	t.Setenv(dungeonPartyCardFlipProjectionEnv, dungeonPartyCardFlipProjectionLegacyValue)
	service, leader, follower, leaderRuntime, followerRuntime, scene := partyDungeonInstanceFixture(t, 22, 778)
	leader.dungeon.runtime = leaderRuntime
	follower.dungeon.runtime = followerRuntime
	if service.adoptPartyDungeonInstance(leader, leaderRuntime, scene) == nil {
		t.Fatal("party dungeon instance was not published")
	}
	if service.adoptPartyDungeonInstance(follower, followerRuntime, scene) == nil {
		t.Fatal("second member did not join the party dungeon instance")
	}
	freezeDungeonCardStateForTest(t, leaderRuntime, "1001")
	leaderRuntime.settlementCardLayoutSent = true
	leaderRuntime.settlementClearRewardSent = true
	leaderRuntime.advanceSettlementPhase(currentDungeonSettlementPhaseCardsRevealed)
	leaderConn := leader.conn.(*bufferConn)
	followerConn := follower.conn.(*bufferConn)
	leaderConn.write.Reset()
	followerConn.write.Reset()

	leader.dungeon.mu.Lock()
	err := service.selectCurrentDungeonCardLocked(leader, leaderRuntime, dungeonCardSideFree, 0, "test_legacy_leader_free_flip")
	leader.dungeon.mu.Unlock()
	if err != nil {
		t.Fatalf("leader free flip: %v", err)
	}
	if _, rest := splitGameServerUpperPacket(t, leaderConn.write.Bytes()); len(rest) != 0 {
		t.Fatalf("legacy mode changed the local ack: rest=%x", rest)
	}
	if len(followerConn.write.Bytes()) != 0 {
		t.Fatalf("legacy mode must not project anything to the partner: %x", followerConn.write.Bytes())
	}
}

// assertOp71SlotFlipped 校验 op71 体里第 wireIndex 槽的指定行已由 expectedPartySlot 翻开。
// 修复前该值恒 0（= 收包者自己），所以"队友翻的牌"永远算在收包者头上；见
// dungeon_party_card_flip_row_model.go。
func assertOp71SlotFlipped(
	t *testing.T,
	body []byte,
	wireIndex int,
	side dungeonCardSide,
	expectedPartySlot byte,
	what string,
) {
	t.Helper()
	// 体：1 字节成功 + 8 槽 × (StateA, StateB, count, count×8B, terminal)
	if len(body) < 1 {
		t.Fatalf("%s: empty op71 body", what)
	}
	offset := 1
	for index := 0; index < dungeonCardWireSlotCount; index++ {
		if offset+3 > len(body) {
			t.Fatalf("%s: truncated op71 body=%x", what, body)
		}
		stateA, stateB, count := body[offset], body[offset+1], int(body[offset+2])
		offset += 3 + count*8 + 1
		if index != wireIndex {
			continue
		}
		state := stateA
		if side == dungeonCardSidePaid {
			state = stateB
		}
		// 行状态字节 = **翻牌人的队伍槽位**（厂商 `CardRewardCoordinator.cs:1073-1142`
		// `freeSelectors[selectedFree] = partySlot`）。本 fixture 里队长 = 槽位 0、
		// 队友 = 槽位 1，所以"这一行被翻开了"的判据是 state == 该角色的槽位，
		// 而**不是**恒 0（恒 0 正好是操作者「看不到对方的翻牌」的成因，
		// 见 dungeon_party_card_flip_row_model.go）。
		if state != expectedPartySlot {
			t.Fatalf(
				"%s: slot %d side %d state=%d want the flipper party slot %d (body=%x)",
				what, wireIndex, side, state, expectedPartySlot, body,
			)
		}
		return
	}
	t.Fatalf("%s: slot %d missing (body=%x)", what, wireIndex, body)
}

// TestSoloCardFlipAckIsByteIdenticalWithAndWithoutTheProjection 钉住单人（无队伍实例）：
// 修复档与回退档下，本机 op71 的字节必须逐字节一致。
func TestSoloCardFlipAckIsByteIdenticalWithAndWithoutTheProjection(t *testing.T) {
	runSolo := func(t *testing.T, mode string) []byte {
		t.Setenv(dungeonPartyCardFlipProjectionEnv, mode)
		service, session, conn, runtime, _ := prepareHellPartyClearSettlementRuntime(t)
		if runtime.partyInstance != nil {
			t.Fatal("the solo fixture must not have a party dungeon instance")
		}
		freezeDungeonCardStateForTest(t, runtime, "99")
		runtime.settlementCardLayoutSent = true
		runtime.settlementClearRewardSent = true
		runtime.advanceSettlementPhase(currentDungeonSettlementPhaseCardsRevealed)
		conn.write.Reset()
		session.dungeon.mu.Lock()
		err := service.selectCurrentDungeonCardLocked(session, runtime, dungeonCardSideFree, 0, "test_solo_free_flip")
		session.dungeon.mu.Unlock()
		if err != nil {
			t.Fatalf("solo free flip: %v", err)
		}
		return append([]byte(nil), conn.write.Bytes()...)
	}
	withFix := runSolo(t, "vendor")
	without := runSolo(t, dungeonPartyCardFlipProjectionLegacyValue)
	if !bytes.Equal(withFix, without) {
		t.Fatalf("solo card ack changed with the switch:\nvendor=%x\nlegacy=%x", withFix, without)
	}
	if len(withFix) == 0 {
		t.Fatal("the solo card ack must still be emitted")
	}
}

// freezeDungeonCardStateForTest 给运行时冻结一份最小可用的翻牌奖励计划。
func freezeDungeonCardStateForTest(t *testing.T, runtime *runtimeDungeonState, characterID string) {
	t.Helper()
	plan, err := newDungeonCardRewardPlan(
		dungeonCardPlanIdentity{
			CharacterID: characterID,
			DungeonID:   runtime.Dungeon.ID,
			MazeIndex:   runtime.MazeIndex,
			RunSeed:     runtime.Seed,
		},
		"hell_party_clear_settlement_test",
		dungeonCardRewardBundle{Gold: 17},
		dungeonCardRewardBundle{Gold: 19},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := newDungeonCardState(plan)
	if err != nil {
		t.Fatal(err)
	}
	runtime.settlementCardRewardState = state
}

// TestPartyCardFlipProjectionSwitchOnlyLegacyRollsBack 钉住 (b) 的开关档位。
func TestPartyCardFlipProjectionSwitchOnlyLegacyRollsBack(t *testing.T) {
	for _, test := range []struct {
		value string
		want  dungeonPartyCardFlipProjectionMode
	}{
		{value: "", want: dungeonPartyCardFlipProjectionVendor},
		{value: "vendor", want: dungeonPartyCardFlipProjectionVendor},
		{value: "LEGACYx", want: dungeonPartyCardFlipProjectionVendor},
		{value: "legacy", want: dungeonPartyCardFlipProjectionLegacy},
	} {
		if got := readDungeonPartyCardFlipProjectionMode(test.value); got != test.want {
			t.Fatalf("read(%q)=%v want %v", test.value, got, test.want)
		}
	}
}
