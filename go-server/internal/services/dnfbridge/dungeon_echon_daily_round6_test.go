package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"testing"
	"time"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfmonster "longheng.io/server/internal/modules/dnf/monster"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 文档段94 验证缺口（只补回归：不改产品代码、不新增协议、不动既有文件）：
//
// 既有真档回归 TestRealPVFDimensionDungeonSharedDailyCounterAndEkernCycleLimits 把 5097 的次数用
// consumeCurrentDungeonCycleClearCount 直接调用写入，因此它证明的是 owner 侧语义，不能证明
// 「客户端真的打完这一轮」时这条计数会不会落库。本文件补的就是这段接线：
//
//	prepare → freeze → commit（真实入口链；额度取自真档 v106 etc/dungeoncyclesystem.etc）
//	  → 真实死亡包（op39 销毁本房间 clear condition 目标）
//	  → handleCurrentDungeonDestroyObjectClearConditionDeathLocked
//	    → completeCurrentDungeonOrdinaryFinalRoomAfterDeathLocked
//	    → completeCurrentDungeonBossDieCheckLocked 内的 commitCurrentDungeonCycleClearCountLocked
//	  → 5097 当日计数 +1 落库
//
// 三条断言：① 死亡接线确实让 5097 计数落库；② 同一轮重复死亡不二次计数；③ 额度用尽后再次入场
// 在真实入口链上被拒，且计数与资产不变。
//
// 资源与 fixture 的分工（不混用、不编造）：
//   - 次数表与额度：真实 v106 Script.pvf（环境变量 DNFBRIDGE_REAL_PVF_SMOKE），经
//     s.initialEquipmentArchive 与线上同一条读取路径读入。本用例不判定额度口径（入场/通关之争
//     未证），只断言真档为 5097 声明了 3，然后按既有实现口径验证接线与幂等。
//   - 迷宫/地图/怪物：沿用既有 destroy-object fixture（bridgePVFSource 合成 1x1 boss 房间，
//     与 TestDestroyObjectClearConditionOp39CompletesBossRoomWithoutTruncatedOp38 同形）。
//     副本 id 是 5097，clear condition 目标用真档记录的目标 53764 / count 1
//     （仓内真档 survey 产物 cmd/scratch/pvfsurvey/out/q4_destroy_object_conditions.tsv 记录：
//     5097 的 count_match 目标为 53764，对象位于
//     ActionObject/Map/Legend/Ekern/System/BossChecker/BossChecker.obj）。
const doc94EkernClearConditionObjectID = uint32(53764)

// doc94Official249DeathBody 按当前档（official-24.9）已核的原生 writer 布局构造 op39：
// 74 字节前缀 + count*14 字节战斗项（dungeoncmd.Official249DieMonsterBaseSize /
// Official249DieMonsterEntrySize，见 dungeoncmd/request_official249.go 与
// TestOfficial249DeathUsesNativeLayoutAndPreservesRoomOwnership 的构造方式）。
// 旧 fixture 的 tutorialScopeVariableZeroCombatDeathBody 是 66 字节 **legacy variable** 布局，
// 在 official 档下会被 DecodeOfficial249DieMonsterRequest 以 "body too short: got 66 want >= 74"
// 拒绝 → 死亡根本进不到 clear 分支（这是夹具错误，不是产品缺口）。
// 目标对象放在 [0:4]，owner 用厂商识别 map-owned 被动对象的 0xFFFF（见 dungeon_combat_runtime.go:72-77），
// count=0（不打战斗项）。
func doc94Official249DeathBody(objectKey uint32) []byte {
	body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize)
	binary.LittleEndian.PutUint32(body[0:4], objectKey)
	binary.LittleEndian.PutUint16(body[4:6], ^uint16(0))
	return body
}

func doc94EkernBossRoomPVFSource() bridgePVFSource {
	return bridgePVFSource{
		worldmap.DefaultMapList: "74195 \x60legend/ekern_boss.map\x60\n",
		"map/legend/ekern_boss.map": "[map name]\n\x60Ekern BossChecker\x60\n" +
			"[dungeon]\n5097\n[type]\n\x60[boss]\x60\n" +
			"[passive object]\n53764 476 234 0\n",
		worldmap.DefaultDungeonList: "5097 \x60ekern_single.dgn\x60\n",
		"dungeon/ekern_single.dgn": "[name]\n\x60Ekern Single - Lost Temple\x60\n" +
			"[minimum required level]\n90\n[basis level]\n90\n[limit party count]\n1\n" +
			"[maze info]\n[size]\n1 1\n[greed]\n\x60A\x60\n" +
			"[map specification]\n\x60boss\x60 0 0 74195\n[start map]\n0 0\n[boss map]\n0 0\n" +
			"[clear condition]\n[destroy object]\n53764 1\n[/clear condition]\n",
		worldmap.DefaultWorldMapList: "45 \x60legend.wdm\x60\n",
		"worldmap/legend.wdm":        "[name]\n\x60魔界集结地\x60\n[dungeon]\n5097 -1\n[/dungeon]\n",
		dnfmonster.DefaultList:       "3001 \x60unused.gob\x60\n",
		"monster/unused.gob":         "[name]\n\x60Unused\x60\n[level]\n90\n[hp]\n1\n[exp]\n0\n",
	}
}

func TestDoc94EkernBossDeathCommitsClearCountOnceAndBlocksFourthEntry(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires published PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, resolver, monsters := loadBridgeDungeonStaticData(t, doc94EkernBossRoomPVFSource())
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "94",
		AccountID:   "doc94-account",
		Name:        "DOC94",
		Job:         "0",
		Level:       90,
		Stats: map[string]int64{
			"fatigue": 100, "town_id": 107, "area_id": 0,
			"pos_x": 250, "pos_y": 224, "direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "94", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	service := &Service{
		options: options{
			currentClientVersion: "official-24.9",
			accountID:            "doc94-account",
			gameUpperBodyCodec:   gameUpperBodyCodecPlain,
			pvfPath:              path,
		},
		worldMapTable:           table,
		worldMapResolver:        resolver,
		dungeonMonsterTable:     monsters,
		initialEquipmentArchive: archive,
		gameplayTimers:          queue,
		dungeonChoice:           func(int) (int, error) { return 0, nil },
		dungeonSeed:             func() (uint32, error) { return 1, nil },
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repos, true },
	}
	requirement, declared, err := service.currentDungeonCycleRequirementForDungeon(5097)
	if err != nil || !declared {
		t.Fatalf("real v106 etc/dungeoncyclesystem.etc must declare 5097: declared=%t err=%v", declared, err)
	}
	if requirement.DailyCount != 3 {
		t.Fatalf("real v106 5097 daily count=%d, want the published 3", requirement.DailyCount)
	}
	session := seedDungeonDailyCountCharacter(t, repos, "94", 94, "doc94-account")
	bindDungeonSelectorOriginForTestAt(t, service, session, 107, 0, 250, 224)

	storedCount := func() int64 {
		t.Helper()
		stored, found, err := repos.Character.Load(ctx, "94")
		if err != nil || !found {
			t.Fatalf("load character 94: err=%v found=%t", err, found)
		}
		used, err := dnfdungeon.DailyEntryUsed(stored, 5097, queue.now)
		if err != nil {
			t.Fatal(err)
		}
		return used
	}

	for round := int64(1); round <= requirement.DailyCount; round++ {
		// 真实入口链：prepare → freeze → commit（同一条链同时走完入场资产事务）。
		runtime, err := enterDungeonOnce(t, service, session, 5097)
		if err != nil {
			t.Fatalf("5097 entry %d rejected: %v", round, err)
		}
		if runtime == nil || runtime.Session == nil {
			t.Fatalf("5097 entry %d produced no runtime", round)
		}
		// 记录事实：入场本身不改这一格的计数（额度口径未证，见文件头）。
		if used := storedCount(); used != round-1 {
			t.Fatalf("5097 entry %d changed the clear count: used=%d want=%d", round, used, round-1)
		}
		scene, ok := runtime.Session.Scene()
		if !ok || !scene.Boss || !scene.Cleared || len(scene.PassiveObjects) != 1 {
			t.Fatalf("5097 round %d fixture scene=%+v found=%t", round, scene, ok)
		}
		// (1) 实际死亡接线（op39 销毁本房间 clear condition 目标）必须让 5097 计数落库。
		if err := service.handleDungeonMonsterDeath(
			session,
			doc94Official249DeathBody(doc94EkernClearConditionObjectID),
		); err != nil {
			t.Fatalf("5097 round %d authoritative death: %v", round, err)
		}
		if used := storedCount(); used != round {
			stored, _, _ := repos.Character.Load(ctx, "94")
			cycleRequirement, declaredNow, cycleErr := service.currentDungeonCycleRequirementForDungeon(runtime.Dungeon.ID)
			t.Logf("diagnostic: dungeon_id=%d maze=%d gameplay_now=%s stored_stats=%v requirement_declared=%t requirement=%+v err=%v "+
				"boss_die_accepted=%t final_room_accepted=%t clear_count_committed=%t settlement_sent=%t run_status=%v",
				runtime.Dungeon.ID, runtime.MazeIndex, service.gameplayNow().UTC().Format(time.RFC3339), stored.Stats,
				declaredNow, cycleRequirement, cycleErr,
				runtime.bossDieCheckAccepted, runtime.ordinaryFinalRoomClearAccepted,
				runtime.dungeonCycleClearCountCommitted, runtime.settlementEntrySent,
				runtime.Session.Snapshot().Run.Status)
			var roomSnapshot runtimeDungeonRoomSnapshot
			roomAvailable := false
			if runtime.Room != nil {
				roomSnapshot = runtime.Room.Snapshot()
				roomAvailable = true
			}
			t.Logf("diagnostic-gate: source=%q room_available=%t room_coord=%v room_map=%d scene_coord=%v scene_map=%d "+
				"boss_set=%t boss_coord=%v maze_index=%d mazes=%d conditions=%+v passive=%+v",
				currentDungeonDestroyObjectClearConditionSource(runtime, scene, doc94EkernClearConditionObjectID),
				roomAvailable, roomSnapshot.Coordinate, roomSnapshot.MapID, scene.Coordinate, scene.Map.Map.ID,
				runtime.BossSet, runtime.BossCoordinate, runtime.MazeIndex, len(runtime.Dungeon.Mazes),
				runtime.Dungeon.Mazes[runtime.MazeIndex].ClearConditions, scene.PassiveObjects)
			pendingLayer, nextLayerIndex, nextLayerMapID, layerErr := service.currentDungeonPendingLayer(runtime, scene)
			finalReady, finalSource := currentDungeonOrdinaryFinalRoomReady(runtime, scene)
			t.Logf("diagnostic-final: pending_layer=%t next_layer=%d next_map=%d layer_err=%v final_ready=%t final_source=%q "+
				"tutorial_dungeon=%t tutorial_scene=%t town_return_pending=%t town_return_op24=%t",
				pendingLayer, nextLayerIndex, nextLayerMapID, layerErr, finalReady, finalSource,
				isPVFTutorialDungeon(runtime), isPVFTutorialDungeonScene(runtime, scene),
				runtime.townReturnPending, runtime.townReturnOp24Sent)
			t.Fatalf("5097 round %d death did not commit the clear count: used=%d want=%d", round, used, round)
		}
		// (2) 同一轮重复死亡（客户端重传）不得二次计数。
		if err := service.handleDungeonMonsterDeath(
			session,
			doc94Official249DeathBody(doc94EkernClearConditionObjectID),
		); err != nil {
			t.Fatalf("5097 round %d repeated death: %v", round, err)
		}
		if used := storedCount(); used != round {
			t.Fatalf("5097 round %d repeated death double counted: used=%d want=%d", round, used, round)
		}
	}

	// (3) 额度用尽后，同一条真实入口链必须拒绝，且计数与资产都不变。
	before := snapshotDailyCountAssets(t, repos, "94")
	if _, err := enterDungeonOnce(t, service, session, 5097); !errors.Is(err, dnfdungeon.ErrDailyEntryLimit) {
		t.Fatalf("5097 accepted entry over the declared cycle limit: %v", err)
	}
	if used := storedCount(); used != requirement.DailyCount {
		t.Fatalf("rejected entry changed the clear count: used=%d want=%d", used, requirement.DailyCount)
	}
	requireSameAssets(t, before, snapshotDailyCountAssets(t, repos, "94"))
	t.Logf("doc94: real v106 5097 limit=%d; authoritative death committed rounds 1..%d once each; entry %d rejected",
		requirement.DailyCount, requirement.DailyCount, requirement.DailyCount+1)
}
