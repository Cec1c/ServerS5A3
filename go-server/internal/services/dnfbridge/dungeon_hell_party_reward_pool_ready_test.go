package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	dnfdrops "longheng.io/server/internal/modules/dnf/drop"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件钉住两句**运维判据事件**的字段表：
//
//	dnf-dungeon-hell-party-reward-pool-ready   —— 激活记录（预载之后"修复档到底生效没有"）
//	game-dungeon-hell-party-reward-roll        —— 投骰事件（这一次走了哪一档）
//
// 线上 2026-09-28 20:16:52（部署后第一条）的激活记录是
//
//	path=etc/itemdropinfo_monster_hell.etc pool=true false=false degraded=true reason=warmed
//	pool_items=7107 pool_buckets=98 pool_min_grade=1 pool_max_grade=96
//	scanned_equ_files=140862 build_ms=8626
//
// 一条**自相矛盾**的记录：池子明明建出来了（7107 件 / 98 桶 / 14 万次扫描 / 最高 grade 96），
// 却写着 `degraded=true`，而且布尔值 `false` 跑到了**键位**上。根因是键值对数组被**按下标**
// 改写（`fields[3] = true` 命中 `pool` 的值、`fields[4] = false` 命中 `built` 的键）。
//
// 因此这里的判据是**结构性的**（不靠人读日志，改了取池口径或字段顺序立刻失败）：
//
//  1. 字段数必须偶数；键位必须是非空字符串、不得是 `true`/`false` 这类**值**字面量、不得重复；
//  2. 值必须自洽：`pool_items > 0 ⇒ built && !degraded`；`degraded ⇒ !built && pool_items == 0`
//     （"池子可用"与"池子建不出来"两种状态不可能混淆）；
//  3. 投骰事件自己的 `pool`/`degraded` 必须与激活记录对得上；
//  4. 真档 PVF（`DNFBRIDGE_REAL_PVF_SMOKE` 门控）上同样成立，并打出操作者要认的那一行。

// hellPartyRewardEventPairs 把一份键值对表解析成 map，并把"错位"直接判死：
// 奇数个字段（最后一个值没有键）、键位不是字符串/为空、键位是 `true`/`false`（值顶掉了键）、
// 同一个键出现两次 —— 四种形状全部 t.Fatal。
func hellPartyRewardEventPairs(t *testing.T, kind string, fields []any) map[string]string {
	t.Helper()
	if len(fields) == 0 {
		t.Fatalf("%s emitted no fields", kind)
	}
	if len(fields)%2 != 0 {
		t.Fatalf("%s emitted %d fields (odd ⇒ the last value has no key): %v", kind, len(fields), fields)
	}
	pairs := make(map[string]string, len(fields)/2)
	for index := 0; index+1 < len(fields); index += 2 {
		key, ok := fields[index].(string)
		if !ok || key == "" {
			t.Fatalf("%s field #%d is not a usable key: %#v (value=%#v) ⇒ key/value misaligned",
				kind, index, fields[index], fields[index+1])
		}
		if key == "true" || key == "false" {
			t.Fatalf("%s field #%d has the boolean literal %q in the key position ⇒ key/value misaligned",
				kind, index, key)
		}
		if _, exists := pairs[key]; exists {
			t.Fatalf("%s field #%d repeats the key %q", kind, index, key)
		}
		pairs[key] = toHellPartyRewardEventValue(fields[index+1])
	}
	return pairs
}

func toHellPartyRewardEventValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

// hellPartyRewardEventRequireKeys 要求这些键全部存在（其余键允许增减）。
func hellPartyRewardEventRequireKeys(t *testing.T, kind string, pairs map[string]string, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if _, ok := pairs[key]; !ok {
			t.Fatalf("%s is missing the key %q (got %v)", kind, key, pairs)
		}
	}
}

// hellPartyRewardPoolReadyRenderedLine 把激活记录渲染成**线上那一行**的形状
// （`key=value` 空格分隔，不带 `EVENT kind=` 前缀与行尾时间戳）：
// 报告里给操作者认的那一行就是这个函数的输出。
func hellPartyRewardPoolReadyRenderedLine(ready hellPartyRewardPoolReadiness) string {
	fields := ready.fields()
	parts := make([]string, 0, len(fields)/2)
	for index := 0; index+1 < len(fields); index += 2 {
		parts = append(parts, fmt.Sprintf("%v=%v", fields[index], fields[index+1]))
	}
	return strings.Join(parts, " ")
}

// assertHellPartyRewardPoolReadyRecord 是激活记录的公共判据（用例与事件同源）：
// 字段表结构 + 关键字段齐全 + 取值自洽 + 与"池子台账"逐字相等。
func assertHellPartyRewardPoolReadyRecord(
	t *testing.T,
	ready hellPartyRewardPoolReadiness,
	wantPool string,
	wantBuilt bool,
	wantDegraded bool,
	wantReason string,
) map[string]string {
	t.Helper()
	if !ready.consistent() {
		t.Fatalf("readiness is self-contradictory: %+v (pool_items=%d built=%t degraded=%t)",
			ready, ready.PoolItems, ready.Built, ready.Degraded)
	}
	if ready.Pool != wantPool || ready.Built != wantBuilt ||
		ready.Degraded != wantDegraded || ready.Reason != wantReason {
		t.Fatalf("readiness=%+v want pool=%s built=%t degraded=%t reason=%s",
			ready, wantPool, wantBuilt, wantDegraded, wantReason)
	}
	if !wantBuilt && (ready.PoolItems != 0 || ready.PoolBuckets != 0 || ready.ScannedEquFiles != 0 ||
		ready.PoolMinGrade != 0 || ready.PoolMaxGrade != 0) {
		t.Fatalf("an unbuilt pool must report zeroed counters: %+v", ready)
	}
	if wantBuilt && (ready.PoolItems <= 0 || ready.PoolBuckets <= 0 || ready.ScannedEquFiles <= 0) {
		t.Fatalf("a built pool must report non-zero counters: %+v", ready)
	}
	pairs := hellPartyRewardEventPairs(t, hellPartyRewardPoolReadyEvent, ready.fields())
	hellPartyRewardEventRequireKeys(t, hellPartyRewardPoolReadyEvent, pairs,
		"path", "pool", "built", "degraded", "reason",
		"pool_items", "pool_buckets", "pool_min_grade", "pool_max_grade",
		"scanned_equ_files", "build_ms")
	if pairs["path"] != hellPartyRewardDropInfoPath {
		t.Fatalf("ready path=%q want %q", pairs["path"], hellPartyRewardDropInfoPath)
	}
	if pairs["pool"] != wantPool || pairs["built"] != strconv.FormatBool(wantBuilt) ||
		pairs["degraded"] != strconv.FormatBool(wantDegraded) || pairs["reason"] != wantReason {
		t.Fatalf("ready pairs=%v want pool=%s built=%t degraded=%t reason=%s",
			pairs, wantPool, wantBuilt, wantDegraded, wantReason)
	}
	// 自洽判据必须能从**打出来的字段**上复核，而不是只看结构体。
	items, err := strconv.Atoi(pairs["pool_items"])
	if err != nil {
		t.Fatalf("ready pool_items=%q is not an integer", pairs["pool_items"])
	}
	if items > 0 && pairs["degraded"] != "false" {
		t.Fatalf("pool_items=%d with degraded=%s is exactly the shipped contradiction", items, pairs["degraded"])
	}
	if pairs["degraded"] == "true" && (pairs["built"] != "false" || items != 0) {
		t.Fatalf("degraded=%s must imply built=false and pool_items=0 (got built=%s pool_items=%d)",
			pairs["degraded"], pairs["built"], items)
	}
	if items != ready.PoolItems {
		t.Fatalf("ready pool_items=%d want %d (the printed ledger must equal the build ledger)", items, ready.PoolItems)
	}
	return pairs
}

// assertHellPartyRewardPoolWarmUpFieldsMatch 钉住**生产调用点**（预载）打的就是这份表：
// `hellPartyRewardPoolWarmUpFields` 只是 `hellPartyRewardPoolReadinessFor(...).fields()`
// 的渲染，两者必须逐键相等 —— 否则事件可以在调用点上被偷偷改回错位的老写法，
// 而结构体那边的用例照样全绿。
func assertHellPartyRewardPoolWarmUpFieldsMatch(
	t *testing.T,
	monsterTable *pvfDungeonMonsterCatalog,
	ready hellPartyRewardPoolReadiness,
) {
	t.Helper()
	want := hellPartyRewardEventPairs(t, hellPartyRewardPoolReadyEvent, ready.fields())
	got := hellPartyRewardEventPairs(t, hellPartyRewardPoolReadyEvent+" (warm-up call site)",
		hellPartyRewardPoolWarmUpFields(monsterTable))
	if len(got) != len(want) {
		t.Fatalf("warm-up fields=%v want the readiness record %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("warm-up %s=%q want %q (fields=%v)", key, got[key], value, got)
		}
	}
}

// hellPartyRewardPoolReadyPacketLogger 与其它用例同形的临时包日志（`EVENT kind=…` 行）。
func hellPartyRewardPoolReadyPacketLogger(t *testing.T) (*packetLogger, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "hell-party-reward-ready-packet-log.txt")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	return logger, logPath
}

func closeHellPartyRewardPoolReadyPacketLogger(t *testing.T, logger *packetLogger, logPath string) string {
	t.Helper()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// hellPartyRewardEventLine 取一条事件行原文（含行尾 `t=` 时间戳）。
func hellPartyRewardEventLine(t *testing.T, text string, kind string) string {
	t.Helper()
	prefix := "EVENT kind=" + kind
	for _, line := range strings.Split(text, "\n") {
		if line == prefix || strings.HasPrefix(line, prefix+" ") {
			return line
		}
	}
	t.Fatalf("event %s not found in:\n%s", kind, text)
	return ""
}

// hellPartyRewardRollForTest 造一次"修复档/回退档/降级档"的投骰（不经 Service，
// 直接用一份合成 source 的 drop catalog + 怪物规则表）。
func hellPartyRewardRollForTest(
	t *testing.T,
	monsterCatalog *pvfDungeonMonsterCatalog,
) hellPartyRewardResult {
	t.Helper()
	catalog, err := monsterCatalog.DropCatalog()
	if err != nil {
		t.Fatalf("build drop catalog: %v", err)
	}
	rules, err := currentDungeonMonsterDropRules(monsterCatalog)
	if err != nil {
		t.Fatalf("build monster rules: %v", err)
	}
	request := hellPartyRewardRequest{
		MonsterCode:           65747,
		DungeonMinimumLevel:   hellPartyRewardTestMinimumLevel,
		DungeonBasisLevel:     hellPartyRewardTestBasisLevel,
		DungeonDifficulty:     1,
		AbyssPartyDifficulty:  1,
		RewardRollCount:       hellPartyRewardTestTierA,
		dungeonMonsterCatalog: monsterCatalog,
		dropCatalog:           catalog,
		rules:                 rules,
		rng:                   newCurrentDungeonDropLCG(20260928),
	}
	result, err := hellPartyRewardRolls(request)
	if err != nil {
		t.Fatalf("roll: %v", err)
	}
	return result
}

// hellPartyRewardReadyTestSyntheticCatalog 造一棵只含一件 `.equ` 的合成目录
// （与 `hellPartyRewardTestEquSource` 同一形状，另补普通怪表与 ItemDictionary 行，
// 让降级/回退档的取件路径也能跑通）。
func hellPartyRewardReadyTestSyntheticCatalog(t *testing.T) *pvfDungeonMonsterCatalog {
	t.Helper()
	items := []struct {
		ID     int64
		Grade  int64
		Rarity int64
		Rate   int64
	}{
		// 默认档（`rarity_allowed=4`）的候选必须存在，否则"池子有候选"这条接线断言
		// 在"在允许集合内抽"的口径下会变成 0（`_dist/abyss-epic-roll-order.md`）。
		{ID: 9401, Grade: hellPartyRewardTestBasisLevel, Rarity: 4, Rate: 100},
	}
	source := hellPartyRewardTestEquSource(items)
	source[dungeonDropEquipmentDictionaryPath] = "9402 0 70 0 10001 100 `legacy pool row`"
	return &pvfDungeonMonsterCatalog{source: source}
}

// TestHellPartyRewardPoolReadyRecordAndRollEventAgree 钉住**接线后的**两条事件：
// 池子建出来时激活记录必须自洽（`pool=equ built=true degraded=false` + 台账），
// 投骰事件自己的 `pool`/`degraded` 必须与它对得上，而且**真日志行**里不许再出现
// `pool=true` / `false=false` / `degraded=true` 这种错位。
func TestHellPartyRewardPoolReadyRecordAndRollEventAgree(t *testing.T) {
	service, session, _, runtime, scene, _ := prepareHellPartyRewardRuntime(t)
	t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolEquValue)
	// 本用例钉的是**池子/接线自洽**（`pool`/`degraded`/`candidates` 与激活记录一致），
	// 不是稀有度闸门：夹具 `bridgeHellPartyRewardPVF` 的唯一一件装备是 **rarity 0**，
	// 在出厂档 `epic`（`rarity_allowed=4`）下"允许集合内没有候选"是正确的读数
	// （`candidates=0`），会让这条"池子有候选"的断言失去意义。
	// 所以这里把闸门显式设成厂商全集档（与 round-1 对其它旧用例的处理同一惯例），
	// 出厂档的候选/分布语义由 `dungeon_hell_party_reward_rarity_order_test.go` 覆盖。
	t.Setenv(hellPartyRewardRarityEnv, hellPartyRewardRarityVendorValue)
	request := hellPartyRewardRequestForTest(t, service, runtime, scene, 1000)

	ready := hellPartyRewardPoolReadinessFor(service.dungeonMonsterTable)
	pairs := assertHellPartyRewardPoolReadyRecord(t, ready, hellPartyRewardPoolEquValue, true, false, "warmed")
	assertHellPartyRewardPoolWarmUpFieldsMatch(t, service.dungeonMonsterTable, ready)

	_, last := hellPartyRewardActorKeys(t, runtime)
	objectKey := last[0]
	result, err := hellPartyRewardRolls(request)
	if err != nil {
		t.Fatalf("roll: %v", err)
	}
	if result.Degraded != "" {
		t.Fatalf("pool built ⇒ the roll must not degrade: degraded=%q", result.Degraded)
	}
	if result.Pool != ready.Pool {
		t.Fatalf("roll pool=%q want the readiness pool %q", result.Pool, ready.Pool)
	}

	rollPairs := hellPartyRewardEventPairs(t, hellPartyRewardRollEvent,
		hellPartyRewardRollFields(runtime, scene, objectKey, request, result, len(result.Selected), "test"))
	hellPartyRewardEventRequireKeys(t, hellPartyRewardRollEvent, rollPairs,
		"pool", "rate_source", "candidates", "grade_min", "grade_max", "degraded",
		"drop_rate", "chance", "rolls_performed", "rolls_missed", "selected_count")
	if rollPairs["pool"] != pairs["pool"] {
		t.Fatalf("roll pool=%q want the readiness pool %q", rollPairs["pool"], pairs["pool"])
	}
	if (rollPairs["degraded"] != "") != (pairs["degraded"] == "true") {
		t.Fatalf("roll degraded=%q and readiness degraded=%s disagree", rollPairs["degraded"], pairs["degraded"])
	}
	if rollPairs["rate_source"] != hellPartyRewardDropInfoPath {
		t.Fatalf("roll rate_source=%q want %q", rollPairs["rate_source"], hellPartyRewardDropInfoPath)
	}
	if items, err := strconv.Atoi(rollPairs["candidates"]); err != nil || items <= 0 {
		t.Fatalf("roll candidates=%q want the built .equ pool to have candidates (mode=%s allowed=%s order=%s pool=%s grade=[%s,%s] degraded=%q rarity_candidates=%s)",
			rollPairs["candidates"], rollPairs["rarity_mode"], rollPairs["rarity_allowed"],
			rollPairs["rarity_order"], rollPairs["pool"], rollPairs["grade_min"], rollPairs["grade_max"],
			rollPairs["degraded"], rollPairs["rarity_candidates"])
	}

	// 端到端：走**真** packetLogger，逐字检查线上那一行。
	logger, logPath := hellPartyRewardPoolReadyPacketLogger(t)
	service.packetLog = logger
	service.logPacketEvent(hellPartyRewardPoolReadyEvent, ready.fields()...)
	service.logGameEvent(session, hellPartyRewardRollEvent,
		hellPartyRewardRollFields(runtime, scene, objectKey, request, result, len(result.Selected), "test")...)
	text := closeHellPartyRewardPoolReadyPacketLogger(t, logger, logPath)

	readyLine := hellPartyRewardEventLine(t, text, hellPartyRewardPoolReadyEvent)
	for _, want := range []string{
		"path=" + hellPartyRewardDropInfoPath,
		"pool=" + hellPartyRewardPoolEquValue,
		"built=true",
		"degraded=false",
		"reason=warmed",
		"pool_items=" + strconv.Itoa(ready.PoolItems),
		"pool_buckets=" + strconv.Itoa(ready.PoolBuckets),
		"pool_min_grade=" + strconv.FormatInt(ready.PoolMinGrade, 10),
		"pool_max_grade=" + strconv.FormatInt(ready.PoolMaxGrade, 10),
		"scanned_equ_files=" + strconv.Itoa(ready.ScannedEquFiles),
		"build_ms=",
	} {
		if !strings.Contains(readyLine, want) {
			t.Fatalf("ready line is missing %q:\n%s", want, readyLine)
		}
	}
	for _, bad := range []string{"pool=true", "false=false", "degraded=true"} {
		if strings.Contains(readyLine, bad) {
			t.Fatalf("ready line still carries the shipped misalignment %q:\n%s", bad, readyLine)
		}
	}
	rollLine := hellPartyRewardEventLine(t, text, hellPartyRewardRollEvent)
	for _, want := range []string{
		"pool=" + hellPartyRewardPoolEquValue,
		"rate_source=" + hellPartyRewardDropInfoPath,
		"degraded= ",
	} {
		if !strings.Contains(rollLine, want) {
			t.Fatalf("roll line is missing %q:\n%s", want, rollLine)
		}
	}
	if strings.Contains(rollLine, "false=false") {
		t.Fatalf("roll line carries a misaligned pair:\n%s", rollLine)
	}
	t.Logf("EVENT kind=%s %s", hellPartyRewardPoolReadyEvent, hellPartyRewardPoolReadyRenderedLine(ready))
}

// TestHellPartyRewardPoolReadyRecordDistinguishesTheThreeStates 钉住三种状态**两两可分**：
//   - 池子建不出来（清单空）⇒ `built=false degraded=true pool_items=0` + `error=`，
//     且投骰事件必须同时报降级（`degraded` 非空、取回今天的 ItemDictionary 池）；
//   - 操作者显式回退档 ⇒ `pool=itemdictionary built=false degraded=false`，且**一次扫描都不做**；
//   - 怪物目录都没有 ⇒ `degraded=true reason=no_monster_catalog pool_items=0`。
func TestHellPartyRewardPoolReadyRecordDistinguishesTheThreeStates(t *testing.T) {
	t.Run("pool cannot be built", func(t *testing.T) {
		monsterCatalog := hellPartyRewardReadyTestSyntheticCatalog(t)
		source := monsterCatalog.source.(bridgePVFSource)
		// 清单在（目录建得出来）但一件都过不了厂商门槛 ⇒ 池子建不出来。
		source[dungeonDropEquipmentList] = ""
		t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolEquValue)

		ready := hellPartyRewardPoolReadinessFor(monsterCatalog)
		pairs := assertHellPartyRewardPoolReadyRecord(t, ready, hellPartyRewardPoolEquValue, false, true, "pool_build_failed")
		assertHellPartyRewardPoolWarmUpFieldsMatch(t, monsterCatalog, ready)
		if ready.Error == "" {
			t.Fatalf("a genuine build failure must carry error=: %+v", ready)
		}
		if !strings.Contains(ready.Error, dungeonDropEquipmentList) {
			t.Fatalf("build failure error=%q must name %s", ready.Error, dungeonDropEquipmentList)
		}
		result := hellPartyRewardRollForTest(t, monsterCatalog)
		if result.Degraded == "" {
			t.Fatal("the roll must degrade when the pool cannot be built")
		}
		if result.Pool != hellPartyRewardPoolItemDictionaryValue {
			t.Fatalf("degraded roll pool=%q want today's %q", result.Pool, hellPartyRewardPoolItemDictionaryValue)
		}
		if (pairs["degraded"] == "true") != (result.Degraded != "") {
			t.Fatalf("readiness degraded=%s and roll degraded=%q disagree", pairs["degraded"], result.Degraded)
		}
	})

	t.Run("explicit rollback tier is not a degradation", func(t *testing.T) {
		monsterCatalog := hellPartyRewardReadyTestSyntheticCatalog(t)
		t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolItemDictionaryValue)

		ready := hellPartyRewardPoolReadinessFor(monsterCatalog)
		pairs := assertHellPartyRewardPoolReadyRecord(t, ready, hellPartyRewardPoolItemDictionaryValue, false, false,
			"rollback_tier_keeps_itemdictionary_pool")
		assertHellPartyRewardPoolWarmUpFieldsMatch(t, monsterCatalog, ready)
		if ready.ScannedEquFiles != 0 {
			t.Fatalf("the rollback tier must not scan the .equ tree: scanned=%d", ready.ScannedEquFiles)
		}
		if pairs["pool_items"] != "0" {
			t.Fatalf("the rollback tier has no pool: pool_items=%s", pairs["pool_items"])
		}
		result := hellPartyRewardRollForTest(t, monsterCatalog)
		if result.Degraded != "" {
			t.Fatalf("an operator-chosen rollback is not a degradation: degraded=%q", result.Degraded)
		}
		if result.Pool != hellPartyRewardPoolItemDictionaryValue {
			t.Fatalf("rollback roll pool=%q want %q", result.Pool, hellPartyRewardPoolItemDictionaryValue)
		}
		if (pairs["degraded"] == "true") != (result.Degraded != "") {
			t.Fatalf("readiness degraded=%s and roll degraded=%q disagree", pairs["degraded"], result.Degraded)
		}
	})

	t.Run("no monster catalog at all", func(t *testing.T) {
		t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolEquValue)
		ready := hellPartyRewardPoolReadinessFor(nil)
		pairs := assertHellPartyRewardPoolReadyRecord(t, ready, hellPartyRewardPoolEquValue, false, true, "no_monster_catalog")
		if pairs["pool_items"] != "0" || pairs["built"] != "false" {
			t.Fatalf("no monster catalog ⇒ no pool: %v", pairs)
		}
	})
}

// TestRealPVFHellPartyRewardPoolReadyRecordIsSelfConsistentAndMatchesTheRoll 是真档用例
// （`DNFBRIDGE_REAL_PVF_SMOKE` 门控，与 `TestRealPVFHellPartyRewardRewardTablesExist` 同一写法）：
// 线上那份 Script.pvf 上
//   - 激活记录自洽：`pool_items > 0`、`built=true`、`degraded=false`、字段表成对且键齐全；
//   - `.equ` 池规模与线上部署后那条记录同一量级（7107 件 / 98 桶 / max grade 96 / 140862 次扫描）；
//   - 同一次进程状态下副本 310 的投骰真的走修复档：`pool=equ`、`degraded` 为空。
//
// 运行：`DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf> go test -count=1 -v -run RealPVFHellPartyRewardPoolReady ./internal/services/dnfbridge`
func TestRealPVFHellPartyRewardPoolReadyRecordIsSelfConsistentAndMatchesTheRoll(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open real pvf: %v", err)
	}
	monsterCatalog, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatalf("load real monster catalog: %v", err)
	}
	t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolEquValue)

	ready := hellPartyRewardPoolReadinessFor(monsterCatalog)
	t.Logf("EVENT kind=%s %s", hellPartyRewardPoolReadyEvent, hellPartyRewardPoolReadyRenderedLine(ready))
	pairs := assertHellPartyRewardPoolReadyRecord(t, ready, hellPartyRewardPoolEquValue, true, false, "warmed")
	if ready.PoolItems < 7000 {
		t.Fatalf("real .equ pool items=%d want >= 7000", ready.PoolItems)
	}
	if ready.PoolMaxGrade < 91 {
		t.Fatalf("real .equ pool max grade=%d want >= 91（内容缺失的判断不成立）", ready.PoolMaxGrade)
	}
	if buildMS, err := strconv.ParseInt(pairs["build_ms"], 10, 64); err != nil || buildMS < 0 {
		t.Fatalf("ready build_ms=%q is not a duration in ms", pairs["build_ms"])
	}

	rules, err := currentDungeonMonsterDropRules(monsterCatalog)
	if err != nil {
		t.Fatalf("real %s is unusable: %v", dnfdrops.MonsterRulePath, err)
	}
	catalog, err := monsterCatalog.DropCatalog()
	if err != nil {
		t.Fatalf("real drop catalog is unusable: %v", err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatalf("load real worldmap: %v", err)
	}
	dungeon, ok := table.FindDungeon(310)
	if !ok {
		t.Fatal("real dungeon 310 not indexed")
	}
	request := hellPartyRewardRequest{
		MonsterCode:           65747,
		DungeonMinimumLevel:   dungeon.Metadata.MinimumRequiredLevel.Value,
		DungeonBasisLevel:     dungeon.Metadata.BasisLevel.Value,
		DungeonDifficulty:     1,
		AbyssPartyDifficulty:  1,
		RewardRollCount:       hellPartyRewardTestTierA,
		dungeonMonsterCatalog: monsterCatalog,
		dropCatalog:           catalog,
		rules:                 rules,
		rng:                   newCurrentDungeonDropLCG(20260928),
	}
	result, err := hellPartyRewardRolls(request)
	if err != nil {
		t.Fatalf("real wired roll: %v", err)
	}
	t.Logf("real wired roll: rate=%d rate_source=%s pool=%s candidates=%d grade=%d..%d selected=%d degraded=%q",
		result.Rate, result.RateSource, result.Pool, result.CandidateCount,
		result.GradeMin, result.GradeMax, len(result.Selected), result.Degraded)
	if result.Degraded != "" {
		t.Fatalf("real PVF with a built pool must not degrade: degraded=%q", result.Degraded)
	}
	if result.Pool != ready.Pool {
		t.Fatalf("roll pool=%q want the readiness pool %q", result.Pool, ready.Pool)
	}
	if result.RateSource != hellPartyRewardDropInfoPath {
		t.Fatalf("roll rate_source=%q want %q", result.RateSource, hellPartyRewardDropInfoPath)
	}
	if (ready.Degraded) != (result.Degraded != "") {
		t.Fatalf("readiness degraded=%t and roll degraded=%q disagree", ready.Degraded, result.Degraded)
	}
}
