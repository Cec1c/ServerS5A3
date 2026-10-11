package dnfbridge

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	dnfdrops "longheng.io/server/internal/modules/dnf/drop"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件钉住「深渊只掉史诗」（操作者要求）的稀有度闸门：
// `dungeon_hell_party_reward_rarity.go` + `dungeon_hell_party_reward.go` 的投骰循环。
//
// 判据分五组：
//
//	1. 开关语义（默认档 = epic；唯一回退字面量 = vendor；显式稀有度列表；未识别 ⇒ 默认）；
//	2. **只掉史诗**：默认档下一件 r0/r1/r2/r3 都产不出来，而同一夹具的 vendor 档会产出
//	   上千件 —— 判别力是断言的一部分（否则"全都是 r4"可能只是因为夹具里没有别的档）；
//	3. **概率那一抽一个随机数都没动**：同一种子、同一夹具，两档的
//	   `rolls_performed` / `rolls_missed` / `rarity_rolls` / `rarity_drawn` / LCG 终态
//	   逐位相等，只有 `rarity_selected` 不同；
//	4. **绝不降级**：rarity 4 在波段里没有候选时，这一抽不掉东西
//	   （`rolls_rarity_bucket_empty`），而不是按厂商 `:246-250` 回落普通装备；
//	   vendor 档在**同一夹具同一种子**上恰好会掉出普通装备 ⇒ 这条对照同时是判别力；
//	5. 真档（`DNFBRIDGE_REAL_PVF_SMOKE` 门控）：副本 310 / 波段 [87,97] 的 248 件
//	   rarity 4 候选就是全部可能产出，且默认档确实落在它里面。

// hellPartyRewardRarityTestRowA 是真档 A 档（hellDifficulty = 1）的
// `[basis of rarity dicision]` 行：r0 0% / r1 59.9% / r2 39.4% / r3 0% / r4 0.7% / r5 0%。
// 即"厂商口径下 99.3% 的命中都掉低稀有度装备"。
const hellPartyRewardRarityTestRowA = "0 599000 993000 993000 1000000 1000001 1000001"

// hellPartyRewardRarityTestRowEpic 把任何 `lcg.Next(1000000)+1` 都压进 rarity 4
// （r0..r3 阈值 0 ⇒ 不可达；r4 阈值 1000000 ⇒ 全覆盖）：用来制造
// "闸门开着、但 rarity 4 在波段里没有候选"这一条路径。
const hellPartyRewardRarityTestRowEpic = "0 0 0 0 1000000 1000001 1000001"

// hellPartyRewardRarityTestHellText 造 hell 概率表：五档 rate 全给 rate，稀有度两行
// 都给 thresholds（与 `hellPartyRewardTestHellDropInfoTextFor` 同形，但阈值可指定）。
func hellPartyRewardRarityTestHellText(rate int64, thresholds string) string {
	row := "45 200 " + strings.TrimSpace(strings.Repeat(strconv.FormatInt(rate, 10)+" ", 5))
	return "[drop prob count]\n2\n" +
		"[dungeon difficulty drop prob]\n" +
		"1 44 0 0 0 0 0\n" + row + "\n" +
		"[basis of rarity dicision]\n" +
		"2 " + thresholds + " " + thresholds + "\n" +
		"[item drop ref table]\n" +
		strconv.FormatInt(hellPartyRewardTestMinimumLevel, 10) + " 1 1 " +
		strconv.FormatInt(hellPartyRewardTestBasisLevel, 10) + " 1 1\n"
}

// hellPartyRewardRarityBandItems 是本文件合成夹具用的同一批装备。
// 等级**互不相同**（`hellPartyRewardTestEquSource` 的 `.equ` 路径 = `test/g<grade>.equ`，
// 同等级会互相覆盖），全部落在厂商闭区间 `[10,97]` 内，覆盖 rarity 1/2/3/4。
func hellPartyRewardRarityBandItems() []struct {
	ID     int64
	Grade  int64
	Rarity int64
	Rate   int64
} {
	return []struct {
		ID     int64
		Grade  int64
		Rarity int64
		Rate   int64
	}{
		{ID: 9611, Grade: 90, Rarity: 1, Rate: 100},
		{ID: 9612, Grade: 92, Rarity: 2, Rate: 100},
		{ID: 9613, Grade: 94, Rarity: 3, Rate: 100},
		{ID: 9614, Grade: 95, Rarity: 4, Rate: 100},
		{ID: 9615, Grade: 96, Rarity: 4, Rate: 250},
	}
}

// hellPartyRewardRarityRequestForTest 造一次**修复档**投骰的输入（hell 表 + `.equ` 池），
// 副本难度 1（真档那一行 ⇒ rate 875 / chance 875）、深渊档位 A（稀有度行 1）。
func hellPartyRewardRarityRequestForTest(
	t *testing.T,
	source bridgePVFSource,
	rolls int64,
) hellPartyRewardRequest {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatalf("build synthetic drop catalog: %v", err)
	}
	monsterCatalog := &pvfDungeonMonsterCatalog{source: source}
	rules, err := currentDungeonMonsterDropRules(monsterCatalog)
	if err != nil {
		t.Fatalf("synthetic %s is unusable: %v", dnfdrops.MonsterRulePath, err)
	}
	return hellPartyRewardRequest{
		MonsterCode:           65747,
		DungeonMinimumLevel:   hellPartyRewardTestMinimumLevel,
		DungeonBasisLevel:     hellPartyRewardTestBasisLevel,
		DungeonDifficulty:     1,
		AbyssPartyDifficulty:  1,
		RewardRollCount:       rolls,
		dungeonMonsterCatalog: monsterCatalog,
		dropCatalog:           catalog,
		rules:                 rules,
		rng:                   newCurrentDungeonDropLCG(20261001),
	}
}

// hellPartyRewardRarityRunForTest 用**给定开关取值**从同一种子跑一次投骰，
// 返回结果与跑完之后的 LCG 终态（终态是"抽了几个随机数、什么顺序"的指纹）。
func hellPartyRewardRarityRunForTest(
	t *testing.T,
	request hellPartyRewardRequest,
	seed uint32,
	switchValue string,
) (hellPartyRewardResult, uint32) {
	t.Helper()
	t.Setenv(hellPartyRewardRarityEnv, switchValue)
	request.rng = newCurrentDungeonDropLCG(seed)
	result, err := hellPartyRewardRolls(request)
	if err != nil {
		t.Fatalf("roll (rarity switch=%q): %v", switchValue, err)
	}
	return result, request.rng.Seed()
}

// hellPartyRewardRarityBucketIDs 取本波段某个 rarity 桶的全部候选 itemID
// （直接来自 `.equ` 池，与投骰实现消费的是同一份候选）。
func hellPartyRewardRarityBucketIDs(
	t *testing.T,
	request hellPartyRewardRequest,
	rarity int64,
) map[uint32]bool {
	t.Helper()
	pool, err := request.dropCatalog.hellPartyRewardEquipmentPool()
	if err != nil {
		t.Fatalf("build .equ pool: %v", err)
	}
	gradeMin, gradeMax, ok := hellPartyRewardGradeRangeForLevels(
		request.DungeonMinimumLevel, request.DungeonBasisLevel)
	if !ok {
		t.Fatalf("band unusable: minimum=%d basis=%d",
			request.DungeonMinimumLevel, request.DungeonBasisLevel)
	}
	ids := make(map[uint32]bool)
	for _, candidate := range pool.candidates(gradeMin, gradeMax, rarity) {
		ids[candidate.ItemID] = true
	}
	return ids
}

// hellPartyRewardRarityRequireShare 钉住一个占比落在窗口里（固定种子 ⇒ 确定性，
// 窗口只用来挡住"读错了表"这类量级错误）。
func hellPartyRewardRarityRequireShare(
	t *testing.T,
	label string,
	got, total int,
	want, tolerance float64,
) {
	t.Helper()
	if total <= 0 {
		t.Fatalf("%s: no sample (total=%d)", label, total)
	}
	share := float64(got) / float64(total)
	if share < want-tolerance || share > want+tolerance {
		t.Fatalf("%s share=%.4f (%d/%d) want %.4f±%.4f", label, share, got, total, want, tolerance)
	}
	t.Logf("%s share=%.4f (%d/%d) want %.4f±%.4f", label, share, got, total, want, tolerance)
}

// TestHellPartyRewardRaritySwitchTiers 钉住开关的取值表：
//
//	epic（默认）= 只允许 rarity 4；vendor = 全集（= 今天）；`4` / `4,3` = 显式列表；
//	其余一切（空串、拼错、越界、空 token）⇒ 默认档；原始取值照进 `rarity_switch=`。
func TestHellPartyRewardRaritySwitchTiers(t *testing.T) {
	for _, test := range []struct {
		value              string
		wantMode           hellPartyRewardRarityMode
		wantAllowed        string
		wantAllows         []int64
		wantDenies         []int64
		wantCommonFallback bool
	}{
		{
			value: "", wantMode: hellPartyRewardRarityEpicMode, wantAllowed: "4",
			wantAllows: []int64{4}, wantDenies: []int64{0, 1, 2, 3, 5}, wantCommonFallback: false,
		},
		{
			value: "   ", wantMode: hellPartyRewardRarityEpicMode, wantAllowed: "4",
			wantAllows: []int64{4}, wantDenies: []int64{0, 1, 2, 3, 5}, wantCommonFallback: false,
		},
		{
			value: "epic", wantMode: hellPartyRewardRarityEpicMode, wantAllowed: "4",
			wantAllows: []int64{4}, wantDenies: []int64{0, 1, 2, 3, 5}, wantCommonFallback: false,
		},
		{
			value: " EPIC ", wantMode: hellPartyRewardRarityEpicMode, wantAllowed: "4",
			wantAllows: []int64{4}, wantDenies: []int64{0, 1, 2, 3, 5}, wantCommonFallback: false,
		},
		{
			value: "vendor", wantMode: hellPartyRewardRarityVendorMode, wantAllowed: "0,1,2,3,4,5",
			wantAllows: []int64{0, 1, 2, 3, 4, 5}, wantCommonFallback: true,
		},
		{
			value: " VeNdOr ", wantMode: hellPartyRewardRarityVendorMode, wantAllowed: "0,1,2,3,4,5",
			wantAllows: []int64{0, 1, 2, 3, 4, 5}, wantCommonFallback: true,
		},
		{
			value: "4", wantMode: hellPartyRewardRarityListMode, wantAllowed: "4",
			wantAllows: []int64{4}, wantDenies: []int64{0, 1, 2, 3, 5}, wantCommonFallback: false,
		},
		{
			value: "4,3", wantMode: hellPartyRewardRarityListMode, wantAllowed: "3,4",
			wantAllows: []int64{3, 4}, wantDenies: []int64{0, 1, 2, 5}, wantCommonFallback: false,
		},
		{
			value: " 4 , 4 , 3 ", wantMode: hellPartyRewardRarityListMode, wantAllowed: "3,4",
			wantAllows: []int64{3, 4}, wantDenies: []int64{0, 1, 2, 5}, wantCommonFallback: false,
		},
		{
			value: "0,2", wantMode: hellPartyRewardRarityListMode, wantAllowed: "0,2",
			wantAllows: []int64{0, 2}, wantDenies: []int64{1, 3, 4, 5}, wantCommonFallback: true,
		},
		{
			value: "junk", wantMode: hellPartyRewardRarityEpicMode, wantAllowed: "4",
			wantAllows: []int64{4}, wantDenies: []int64{0, 1, 2, 3, 5}, wantCommonFallback: false,
		},
		{
			value: "vendro", wantMode: hellPartyRewardRarityEpicMode, wantAllowed: "4",
			wantAllows: []int64{4}, wantDenies: []int64{0, 1, 2, 3, 5}, wantCommonFallback: false,
		},
		{
			value: "9", wantMode: hellPartyRewardRarityEpicMode, wantAllowed: "4",
			wantAllows: []int64{4}, wantDenies: []int64{0, 1, 2, 3, 5}, wantCommonFallback: false,
		},
		{
			value: "-1", wantMode: hellPartyRewardRarityEpicMode, wantAllowed: "4",
			wantAllows: []int64{4}, wantDenies: []int64{0, 1, 2, 3, 5}, wantCommonFallback: false,
		},
		{
			value: "4,x", wantMode: hellPartyRewardRarityEpicMode, wantAllowed: "4",
			wantAllows: []int64{4}, wantDenies: []int64{0, 1, 2, 3, 5}, wantCommonFallback: false,
		},
		{
			value: "4,", wantMode: hellPartyRewardRarityEpicMode, wantAllowed: "4",
			wantAllows: []int64{4}, wantDenies: []int64{0, 1, 2, 3, 5}, wantCommonFallback: false,
		},
	} {
		plan := readHellPartyRewardRarityPlan(test.value)
		if plan.Mode != test.wantMode || plan.allowedList() != test.wantAllowed ||
			plan.modeName() != test.wantMode.modeName() {
			t.Fatalf("readHellPartyRewardRarityPlan(%q)=%+v (mode=%s allowed=%s) want mode=%s allowed=%s",
				test.value, plan, plan.modeName(), plan.allowedList(),
				test.wantMode.modeName(), test.wantAllowed)
		}
		for _, rarity := range test.wantAllows {
			if !plan.allows(rarity) {
				t.Fatalf("value=%q must allow rarity %d (allowed=%s)", test.value, rarity, plan.allowedList())
			}
		}
		for _, rarity := range test.wantDenies {
			if plan.allows(rarity) {
				t.Fatalf("value=%q must deny rarity %d (allowed=%s)", test.value, rarity, plan.allowedList())
			}
		}
		if plan.allowsCommonFallback() != test.wantCommonFallback {
			t.Fatalf("value=%q common fallback=%t want %t（厂商 rarity 0 回落只有 rarity 0 本身被允许时才留）",
				test.value, plan.allowsCommonFallback(), test.wantCommonFallback)
		}
		// 原始取值必须原样进事件（空/未设置 = `-`），拼错才看得见。
		wantSwitch := strings.TrimSpace(test.value)
		if wantSwitch == "" {
			wantSwitch = hellPartyRewardRarityUnsetText
		}
		if plan.switchText() != wantSwitch {
			t.Fatalf("value=%q rarity_switch=%q want %q", test.value, plan.switchText(), wantSwitch)
		}
	}

	// 默认档就是读**未设置**的环境变量（消费点自读），不是"碰巧的零值"。
	t.Setenv(hellPartyRewardRarityEnv, "")
	if env := hellPartyRewardRarityPlanFromEnv(); env.Mode != hellPartyRewardRarityEpicMode ||
		env.allowedList() != "4" || env.switchText() != hellPartyRewardRarityUnsetText {
		t.Fatalf("unset env ⇒ %+v want the epic default", env)
	}
	t.Setenv(hellPartyRewardRarityEnv, "vendor")
	if env := hellPartyRewardRarityPlanFromEnv(); env.Mode != hellPartyRewardRarityVendorMode {
		t.Fatalf("vendor env ⇒ %+v", env)
	}

	// 开关名与两个档位字面量是线上可判据（操作者据此排障），钉死。
	if hellPartyRewardRarityEnv != "DNFBRIDGE_HELL_PARTY_REWARD_RARITY" {
		t.Fatalf("rarity switch env=%q", hellPartyRewardRarityEnv)
	}
	if hellPartyRewardRarityEpicValue != "epic" || hellPartyRewardRarityVendorValue != "vendor" {
		t.Fatalf("rarity literals=%q/%q", hellPartyRewardRarityEpicValue, hellPartyRewardRarityVendorValue)
	}
	if hellPartyRewardRarityEpic != 4 || hellPartyRewardRarityBuckets != 6 {
		t.Fatalf("epic rarity=%d buckets=%d want 4/6", hellPartyRewardRarityEpic, hellPartyRewardRarityBuckets)
	}
	if hellPartyRewardRarityAllList() != "0,1,2,3,4,5" {
		t.Fatalf("all list=%q", hellPartyRewardRarityAllList())
	}
}

// TestHellPartyRewardEpicTierDropsOnlyRarity4OverALongRun 是**操作者要求**的主用例：
// 默认档（未设开关）下，4096 次投骰（≈3580 次命中、厂商口径下 ≈2140 件 r1 + ≈1410 件 r2）
// 里**一件低稀有度都产不出来**，落地的每一件都在 rarity 4 桶里。
//
// ⚠ 落地**件数**这一栏从 2026-09-29 第 9 条起由史诗窗口决定（默认 16000/1e6 = 1.6%）：
// 命中不再等于落地。本用例因此同时钉住两件事：
//
//	① 默认档（窗口生效）：命中里只有约 1.6% 落地，**且落地的全部是 r4**；
//	② 回退字面量 `DNFBRIDGE_ABYSS_EPIC_WINDOW=legacy`（闸门关掉）：**每一次命中都落地**
//	   —— 上一版的那条不变量的原文照旧成立（没有弱化，只是搬到了它该在的档位上）。
//
// 判别力是断言的一部分：同一夹具、同一种子的 vendor 档必须产出上千件 r1/r2 ——
// 否则"全都是 r4"可能只是因为夹具里根本没有低稀有度物品。
func TestHellPartyRewardEpicTierDropsOnlyRarity4OverALongRun(t *testing.T) {
	const rolls = 4096
	// 默认档必须是"未设置"读出来的**内置默认**，不是外层环境碰巧留下的值。
	t.Setenv(abyssEpicWindowEnv, "")
	source := hellPartyRewardTestEquSource(hellPartyRewardRarityBandItems())
	request := hellPartyRewardRarityRequestForTest(t, source, rolls)

	epic, epicSeed := hellPartyRewardRarityRunForTest(t, request, 20261001, "")
	if epic.RarityMode != hellPartyRewardRarityEpicValue || epic.RarityAllowed != "4" {
		t.Fatalf("default tier rarity_mode=%q allowed=%q want epic/4", epic.RarityMode, epic.RarityAllowed)
	}
	if epic.Degraded != "" {
		t.Fatalf("synthetic fixed tier must not degrade: degraded=%q", epic.Degraded)
	}
	if !epic.EpicWindowActive || epic.EpicWindow != abyssEpicWindowDefault ||
		epic.EpicWindowSource != abyssEpicWindowSourceDefault ||
		epic.EpicWindowSwitch != abyssEpicWindowUnsetText {
		t.Fatalf("default tier epic window=%d source=%q switch=%q active=%t want %d/%s/%s/true",
			epic.EpicWindow, epic.EpicWindowSource, epic.EpicWindowSwitch, epic.EpicWindowActive,
			abyssEpicWindowDefault, abyssEpicWindowSourceDefault, abyssEpicWindowUnsetText)
	}
	if epic.RarityRolls < 3000 || len(epic.Selected) == 0 {
		t.Fatalf("long run must produce hits and epics: rarities=%d selected=%d", epic.RarityRolls, len(epic.Selected))
	}
	// 厂商的稀有度抽照抽：抽到的分布里必须**真的有** r1/r2（闸门关掉的那些）。
	hellPartyRewardRarityRequireShare(t, "epic rarity_drawn r1", epic.RarityDrawn[1], epic.RarityRolls, 0.599, 0.03)
	hellPartyRewardRarityRequireShare(t, "epic rarity_drawn r2", epic.RarityDrawn[2], epic.RarityRolls, 0.394, 0.03)
	hellPartyRewardRarityRequireShare(t, "epic rarity_drawn r4", epic.RarityDrawn[4], epic.RarityRolls, 0.007, 0.004)

	// 1) 落地的每一件都在 rarity 4 桶里，且不在任何低稀有度桶里。
	rarity4 := hellPartyRewardRarityBucketIDs(t, request, 4)
	lower := make(map[uint32]bool)
	for _, rarity := range []int64{0, 1, 2, 3} {
		for id := range hellPartyRewardRarityBucketIDs(t, request, rarity) {
			lower[id] = true
		}
	}
	if len(rarity4) == 0 || len(lower) == 0 {
		t.Fatalf("fixture must cover both rarity 4 (%d) and lower rarities (%d)", len(rarity4), len(lower))
	}
	for _, item := range epic.Selected {
		if lower[item.ItemID] {
			t.Fatalf("epic tier produced a non-epic item %d", item.ItemID)
		}
		if !rarity4[item.ItemID] {
			t.Fatalf("epic tier produced item %d which is not in the rarity 4 bucket", item.ItemID)
		}
	}
	// 2) 按档直方图：只有 r4 非零。
	for rarity := 0; rarity < hellPartyRewardRarityBuckets; rarity++ {
		if rarity == int(hellPartyRewardRarityEpic) {
			continue
		}
		if epic.RaritySelected[rarity] != 0 {
			t.Fatalf("epic tier selected rarity %d x%d (rarity_selected=%s)",
				rarity, epic.RaritySelected[rarity],
				hellPartyRewardRarityHistogram(epic.RaritySelected))
		}
	}
	if epic.RaritySelected[hellPartyRewardRarityIndex(hellPartyRewardRarityEpic)] != len(epic.Selected) {
		t.Fatalf("rarity_selected=%s disagrees with selected=%d",
			hellPartyRewardRarityHistogram(epic.RaritySelected), len(epic.Selected))
	}
	// 3) 命中次数守恒：命中 = 落地 + 桶空 + **被史诗窗口挡下**。默认档下"被稀有度闸门
	//    事后过滤"恒为 0（闸门生效在抽取之前 ⇒ 抽出来的那一件一定在允许档里），
	//    旧断言（"必须有被过滤的命中"）在回退字面量 `post_filter` 那一档，
	//    见 `TestHellPartyRewardRarityOrderRollbackLiteralReproducesTheDeployedFilter`。
	if epic.RarityFiltered+epic.RarityBucketEmpty+epic.EpicGated+len(epic.Selected) != epic.RarityRolls {
		t.Fatalf("hits=%d split into filtered=%d bucket_empty=%d epic_gated=%d selected=%d",
			epic.RarityRolls, epic.RarityFiltered, epic.RarityBucketEmpty, epic.EpicGated, len(epic.Selected))
	}
	if epic.RarityFiltered != 0 {
		t.Fatalf("the default tier must not filter after the draw: filtered=%d", epic.RarityFiltered)
	}
	if epic.RarityOrder != hellPartyRewardRarityOrderWithinValue {
		t.Fatalf("rarity_order=%q want %q", epic.RarityOrder, hellPartyRewardRarityOrderWithinValue)
	}
	// 落地的件数 = 命中 − 桶空 − 被窗口挡下的那些；且**窗口真的挡掉了绝大多数命中**
	// （默认 1.6% ⇒ 不挡才是回归）。
	if len(epic.Selected) != epic.RarityRolls-epic.RarityBucketEmpty-epic.EpicGated {
		t.Fatalf("selected=%d want hits(%d)-bucket_empty(%d)-epic_gated(%d)",
			len(epic.Selected), epic.RarityRolls, epic.RarityBucketEmpty, epic.EpicGated)
	}
	if epic.EpicGated == 0 {
		t.Fatal("the default epic window must gate most hits; it gated none")
	}
	if epic.NoDrop != epic.RarityFiltered+epic.RarityBucketEmpty+epic.EpicGated {
		t.Fatalf("no-drop=%d want filtered+bucket_empty+epic_gated=%d+%d+%d",
			epic.NoDrop, epic.RarityFiltered, epic.RarityBucketEmpty, epic.EpicGated)
	}
	// 4) 本波段六个桶的候选数（事件 `rarity_candidates=`）确实含低稀有度。
	if !epic.RarityCandidatesKnown || epic.RarityCandidates[4] == 0 || epic.RarityCandidates[1] == 0 {
		t.Fatalf("rarity_candidates=%s known=%t want the band histogram",
			hellPartyRewardRarityHistogram(epic.RarityCandidates), epic.RarityCandidatesKnown)
	}

	// 判别力：vendor 档在同一夹具、同一种子上必然产出上千件低稀有度装备。
	// 旧断言（"闸门必须删掉物品"= 抽完再过滤）属于回退字面量 `post_filter` 那一档：
	// 默认档现在是"根本不会抽到非史诗"，所以两边落地件数相等是**正确**的，
	// 见 `TestHellPartyRewardRarityOrderRollbackLiteralReproducesTheDeployedFilter`。
	vendor, vendorSeed := hellPartyRewardRarityRunForTest(t, request, 20261001, hellPartyRewardRarityVendorValue)
	lowerGranted := 0
	for rarity := 0; rarity < hellPartyRewardRarityBuckets; rarity++ {
		if rarity == int(hellPartyRewardRarityEpic) {
			continue
		}
		lowerGranted += vendor.RaritySelected[rarity]
	}
	if lowerGranted < 1000 {
		t.Fatalf("vendor tier granted %d lower-rarity items in %d rolls; the epic-only assertion would be vacuous",
			lowerGranted, rolls)
	}
	// 落地**比例**必须就是窗口本身（默认 1.6%）—— 这是"操作者口径写成 4–5 间 1 件"的
	// 直接读数（每间 = 2 次结算 × 命中 × 窗口）。
	hellPartyRewardRarityRequireShare(t, "default tier epic landing rate",
		len(epic.Selected), epic.RarityRolls,
		float64(abyssEpicWindowDefault)/float64(abyssEpicWindowBound), 0.004)
	// 默认档（窗口生效）**必须**比 vendor 少：这是操作者第 9 条要求的可见差别。
	if len(epic.Selected) >= len(vendor.Selected) {
		t.Fatalf("the default epic window must land fewer items than the vendor tier: epic=%d vendor=%d",
			len(epic.Selected), len(vendor.Selected))
	}
	// 窗口回退字面量 `legacy`：**每一次命中都落地**（上一版那一条不变量的原文），
	// 因此落地件数 ≥ vendor。
	t.Setenv(abyssEpicWindowEnv, abyssEpicWindowLegacyValue)
	ungated, ungatedSeed := hellPartyRewardRarityRunForTest(t, request, 20261001, "")
	if ungated.EpicWindowActive || ungated.EpicGated != 0 {
		t.Fatalf("the rollback literal must disable the window: active=%t gated=%d",
			ungated.EpicWindowActive, ungated.EpicGated)
	}
	if len(ungated.Selected) != ungated.RarityRolls-ungated.RarityBucketEmpty {
		t.Fatalf("the rollback literal must pay every hit: hits=%d selected=%d bucket_empty=%d",
			ungated.RarityRolls, len(ungated.Selected), ungated.RarityBucketEmpty)
	}
	if len(ungated.Selected) < len(vendor.Selected) {
		t.Fatalf("the rollback literal must not pay fewer items than vendor: vendor=%d legacy=%d",
			len(vendor.Selected), len(ungated.Selected))
	}
	if ungatedSeed != epicSeed {
		t.Fatalf("the window must not touch the random stream: default seed=%d rollback seed=%d",
			epicSeed, ungatedSeed)
	}
	t.Setenv(abyssEpicWindowEnv, "")
	t.Setenv(hellPartyRewardRarityOrderEnv, hellPartyRewardRarityOrderPostFilterValue)
	deployed, _ := hellPartyRewardRarityRunForTest(t, request, 20261001, "")
	t.Setenv(hellPartyRewardRarityOrderEnv, "")
	if len(deployed.Selected) >= len(vendor.Selected) {
		t.Fatalf("the rollback tier must remove items: vendor=%d post_filter=%d",
			len(vendor.Selected), len(deployed.Selected))
	}
	t.Logf("epic tier: hits=%d filtered=%d bucket_empty=%d epic_gated=%d epics=%d (window=%d, lcg seed %d->%d) / window rollback: selected=%d (seed %d) / vendor tier: selected=%d lower=%d (lcg seed %d->%d)",
		epic.RarityRolls, epic.RarityFiltered, epic.RarityBucketEmpty, epic.EpicGated, len(epic.Selected),
		epic.EpicWindow, 20261001, epicSeed, len(ungated.Selected), ungatedSeed,
		len(vendor.Selected), lowerGranted, 20261001, vendorSeed)
}

// TestHellPartyRewardRarityGateKeepsTheProbabilityStreamIdentical 是**A/B 性质**的钉子：
// 同一种子、同一夹具下，`epic`（默认）、`4`（等价显式列表）与 `vendor` 三档
//
//	rolls_performed / rolls_missed / rarity_rolls / rarity_drawn / LCG 终态
//
// **逐位相等**（= 概率那一抽与稀有度那一抽的消费次数和顺序完全没动），
// 唯一不同的是 `rarity_selected`（掉什么）。
func TestHellPartyRewardRarityGateKeepsTheProbabilityStreamIdentical(t *testing.T) {
	const rolls = 2048
	// 三档都跑**出厂默认窗口**（本用例的断言就是"出厂默认下三档的随机流逐位相同"）。
	t.Setenv(abyssEpicWindowEnv, "")
	source := hellPartyRewardTestEquSource(hellPartyRewardRarityBandItems())
	request := hellPartyRewardRarityRequestForTest(t, source, rolls)

	epic, epicSeed := hellPartyRewardRarityRunForTest(t, request, 20261002, "")
	explicit, explicitSeed := hellPartyRewardRarityRunForTest(t, request, 20261002, "4")
	vendor, vendorSeed := hellPartyRewardRarityRunForTest(t, request, 20261002, hellPartyRewardRarityVendorValue)
	otherSeedRun, otherSeed := hellPartyRewardRarityRunForTest(t, request, 20261003, "")

	if epicSeed == otherSeed {
		t.Fatal("two different seeds ended in the same LCG state; the seed is not reaching the roll")
	}
	for _, pair := range []struct {
		name      string
		left      hellPartyRewardResult
		right     hellPartyRewardResult
		leftSeed  uint32
		rightSeed uint32
	}{
		{name: "epic vs vendor", left: epic, right: vendor, leftSeed: epicSeed, rightSeed: vendorSeed},
		{name: "list-4 vs vendor", left: explicit, right: vendor, leftSeed: explicitSeed, rightSeed: vendorSeed},
		{name: "epic vs list-4", left: epic, right: explicit, leftSeed: epicSeed, rightSeed: explicitSeed},
	} {
		if pair.leftSeed != pair.rightSeed {
			t.Fatalf("%s consumed different random streams: seeds %d vs %d",
				pair.name, pair.leftSeed, pair.rightSeed)
		}
		if pair.left.Rolls != pair.right.Rolls || pair.left.Misses != pair.right.Misses {
			t.Fatalf("%s changed the probability rolls: rolls %d/%d misses %d/%d",
				pair.name, pair.left.Rolls, pair.right.Rolls, pair.left.Misses, pair.right.Misses)
		}
		if pair.left.RarityRolls != pair.right.RarityRolls {
			t.Fatalf("%s changed the rarity draws: %d vs %d",
				pair.name, pair.left.RarityRolls, pair.right.RarityRolls)
		}
		if pair.left.RarityDrawn != pair.right.RarityDrawn {
			t.Fatalf("%s drew a different vendor rarity distribution: %s vs %s", pair.name,
				hellPartyRewardRarityHistogram(pair.left.RarityDrawn),
				hellPartyRewardRarityHistogram(pair.right.RarityDrawn))
		}
		if pair.left.Rate != pair.right.Rate || pair.left.Chance != pair.right.Chance ||
			pair.left.Pool != pair.right.Pool {
			t.Fatalf("%s changed rate/chance/pool: %+v vs %+v", pair.name, pair.left, pair.right)
		}
		t.Logf("%s: identical stream (seed %d, rolls %d, misses %d, rarity draws %d, drawn %s)",
			pair.name, pair.leftSeed, pair.left.Rolls, pair.left.Misses, pair.left.RarityRolls,
			hellPartyRewardRarityHistogram(pair.left.RarityDrawn))
	}
	// 显式 `4` 与默认 `epic` 是同一个口径（落地物品逐件相同）。
	if len(epic.Selected) != len(explicit.Selected) {
		t.Fatalf("epic selected=%d but list-4 selected=%d", len(epic.Selected), len(explicit.Selected))
	}
	for index := range epic.Selected {
		if epic.Selected[index] != explicit.Selected[index] {
			t.Fatalf("roll#%d epic=%+v list-4=%+v", index+1, epic.Selected[index], explicit.Selected[index])
		}
	}
	// 变的只有"掉什么"：**回退字面量 `legacy`（窗口关掉）下每一次命中都产出允许档物品**，
	// 所以落地件数 ≥ vendor（vendor 会把"桶空且回落也空"的命中丢掉）。
	// 旧断言（"闸门让落地件数变少"= 抽完再过滤）在回退档
	// `TestHellPartyRewardRarityOrderRollbackLiteralReproducesTheDeployedFilter`。
	//
	// ⚠ 默认档（窗口 16000/1e6）下落地件数**故意**比 vendor 少（操作者第 9 条要的
	// "4–5 间 1 件"），所以这里先显式把窗口关掉，再单独断言"默认档更少"。
	t.Setenv(abyssEpicWindowEnv, abyssEpicWindowLegacyValue)
	ungated, ungatedSeed := hellPartyRewardRarityRunForTest(t, request, 20261002, "")
	if ungatedSeed != epicSeed {
		t.Fatalf("the epic window must not touch the random stream: default=%d rollback=%d",
			epicSeed, ungatedSeed)
	}
	if len(ungated.Selected) < len(vendor.Selected) {
		t.Fatalf("with the window rolled back the default tier must not pay fewer items than vendor: epic=%d vendor=%d",
			len(ungated.Selected), len(vendor.Selected))
	}
	if len(epic.Selected) >= len(vendor.Selected) {
		t.Fatalf("the shipped epic window must land fewer items than vendor: epic=%d vendor=%d",
			len(epic.Selected), len(vendor.Selected))
	}
	t.Setenv(abyssEpicWindowEnv, "")
	if epic.RarityFiltered != 0 {
		t.Fatalf("the default tier filtered %d hits after the draw", epic.RarityFiltered)
	}
	if otherSeedRun.RarityDrawn == epic.RarityDrawn {
		t.Fatal("a different seed drew the same rarity histogram; the assertion is not seed-sensitive")
	}
}

// TestHellPartyRewardVendorTierReproducesTodaysRarityMix 钉住**回退档**：`vendor` 必须
// 逐字复现今天的稀有度分布（A 档 r1 59.9% / r2 39.4% / r4 0.7%），
// 而不是"至少不全是 r4"这种松判据。
func TestHellPartyRewardVendorTierReproducesTodaysRarityMix(t *testing.T) {
	const rolls = 4096
	source := hellPartyRewardTestEquSource(hellPartyRewardRarityBandItems())
	request := hellPartyRewardRarityRequestForTest(t, source, rolls)

	vendor, _ := hellPartyRewardRarityRunForTest(t, request, 20261004, hellPartyRewardRarityVendorValue)
	if vendor.RarityMode != hellPartyRewardRarityVendorValue ||
		vendor.RarityAllowed != hellPartyRewardRarityAllList() {
		t.Fatalf("vendor tier rarity_mode=%q allowed=%q", vendor.RarityMode, vendor.RarityAllowed)
	}
	if vendor.RarityFiltered != 0 {
		t.Fatalf("vendor tier must never filter: filtered=%d", vendor.RarityFiltered)
	}
	if vendor.RarityRolls < 3000 {
		t.Fatalf("hits=%d want ~%d (chance 875/1001 x %d)", vendor.RarityRolls, rolls*875/1001, rolls)
	}
	// 抽到的分布与落地的分布都必须就是今天那张表。
	for _, want := range []struct {
		rarity    int
		share     float64
		tolerance float64
	}{
		{rarity: 1, share: 0.599, tolerance: 0.03},
		{rarity: 2, share: 0.394, tolerance: 0.03},
		{rarity: 4, share: 0.007, tolerance: 0.004},
	} {
		hellPartyRewardRarityRequireShare(t, "vendor rarity_drawn r"+strconv.Itoa(want.rarity),
			vendor.RarityDrawn[want.rarity], vendor.RarityRolls, want.share, want.tolerance)
		hellPartyRewardRarityRequireShare(t, "vendor rarity_selected r"+strconv.Itoa(want.rarity),
			vendor.RaritySelected[want.rarity], vendor.RarityRolls, want.share, want.tolerance)
	}
	if vendor.RaritySelected[1] == 0 || vendor.RaritySelected[2] == 0 || vendor.RaritySelected[4] == 0 {
		t.Fatalf("vendor tier must grant all three A-tier rarities: %s",
			hellPartyRewardRarityHistogram(vendor.RaritySelected))
	}
	if len(vendor.Selected) != vendor.RarityRolls-vendor.RarityBucketEmpty {
		t.Fatalf("vendor selected=%d hits=%d bucket_empty=%d",
			len(vendor.Selected), vendor.RarityRolls, vendor.RarityBucketEmpty)
	}
	t.Logf("vendor tier: hits=%d selected=%d drawn=%s selected_by_rarity=%s",
		vendor.RarityRolls, len(vendor.Selected),
		hellPartyRewardRarityHistogram(vendor.RarityDrawn),
		hellPartyRewardRarityHistogram(vendor.RaritySelected))
}

// TestHellPartyRewardEpicTierNeverDowngradesWhenRarity4HasNoCandidate 钉住第 3 条要求：
// rarity 4 在波段里没有候选时，默认档**不掉东西**（`rolls_rarity_bucket_empty`），
// 而**不是**按厂商 `HellMonsterDropConfig.cs:246-250` 回落 rarity 0。
//
// 判别力：同一夹具、同一种子的 vendor 档恰好会走那条回落，把普通/高级装备掉出来 ——
// 那正是操作者要去掉的东西。
func TestHellPartyRewardEpicTierNeverDowngradesWhenRarity4HasNoCandidate(t *testing.T) {
	items := []struct {
		ID     int64
		Grade  int64
		Rarity int64
		Rate   int64
	}{
		{ID: 9621, Grade: 90, Rarity: 0, Rate: 100},
		{ID: 9622, Grade: 91, Rarity: 1, Rate: 100},
	}
	source := hellPartyRewardTestEquSource(items)
	// 稀有度行把每一抽都压进 rarity 4（闸门开着），但波段里只有 rarity 0/1 的件。
	source[hellPartyRewardDropInfoPath] = hellPartyRewardRarityTestHellText(1000, hellPartyRewardRarityTestRowEpic)
	request := hellPartyRewardRarityRequestForTest(t, source, hellPartyRewardTestTierA)

	epic, epicSeed := hellPartyRewardRarityRunForTest(t, request, 20261005, "")
	if !epic.RarityCandidatesKnown || epic.RarityCandidates[4] != 0 ||
		epic.RarityCandidates[0] == 0 || epic.RarityCandidates[1] == 0 {
		t.Fatalf("fixture must have candidates in r0/r1 and none in r4: known=%t candidates=%s",
			epic.RarityCandidatesKnown, hellPartyRewardRarityHistogram(epic.RarityCandidates))
	}
	if epic.RarityRolls != int(hellPartyRewardTestTierA) {
		t.Fatalf("hits=%d want %d (chance 1000/1001)", epic.RarityRolls, hellPartyRewardTestTierA)
	}
	if epic.RarityDrawn[4] != epic.RarityRolls || epic.RarityFiltered != 0 {
		t.Fatalf("fixture must draw rarity 4 every hit: drawn=%s filtered=%d",
			hellPartyRewardRarityHistogram(epic.RarityDrawn), epic.RarityFiltered)
	}
	if len(epic.Selected) != 0 {
		t.Fatalf("epic tier granted %+v with no rarity 4 candidate; it must not downgrade", epic.Selected)
	}
	if epic.RarityBucketEmpty != epic.RarityRolls {
		t.Fatalf("bucket_empty=%d want every hit (%d)", epic.RarityBucketEmpty, epic.RarityRolls)
	}
	if epic.RaritySelected[0] != 0 || epic.RaritySelected[1] != 0 {
		t.Fatalf("epic tier downgraded to a common/advanced item: rarity_selected=%s",
			hellPartyRewardRarityHistogram(epic.RaritySelected))
	}

	// 判别力：vendor 档在同一输入上**必须**走厂商的 rarity 0 回落。
	vendor, vendorSeed := hellPartyRewardRarityRunForTest(t, request, 20261005, hellPartyRewardRarityVendorValue)
	if len(vendor.Selected) != int(hellPartyRewardTestTierA) {
		t.Fatalf("vendor tier must fall back to rarity 0 and grant %d items, got %+v",
			hellPartyRewardTestTierA, vendor.Selected)
	}
	if vendor.RaritySelected[0] == 0 || vendor.RaritySelected[1] != 0 {
		t.Fatalf("vendor fallback histogram=%s want the rarity 0 bucket only",
			hellPartyRewardRarityHistogram(vendor.RaritySelected))
	}
	for _, item := range vendor.Selected {
		if item.ItemID != 9621 {
			t.Fatalf("vendor fallback item=%+v want the rarity 0 fixture item 9621", item)
		}
	}

	// 显式列表把 rarity 0 也允许时，厂商那条回落是**允许**的（0 本身在集合里）⇒ 与 vendor 同效。
	relaxed, relaxedSeed := hellPartyRewardRarityRunForTest(t, request, 20261005, "0,4")
	if len(relaxed.Selected) != len(vendor.Selected) {
		t.Fatalf("list `0,4` selected=%d want the vendor fallback %d", len(relaxed.Selected), len(vendor.Selected))
	}
	if relaxedSeed != vendorSeed {
		t.Fatalf("list `0,4` seed=%d want vendor %d (same draws, only the gate differs)",
			relaxedSeed, vendorSeed)
	}
	t.Logf("empty rarity 4 bucket: epic hits=%d selected=%d bucket_empty=%d (seed %d) / vendor selected=%d via rarity 0 fallback (seed %d)",
		epic.RarityRolls, len(epic.Selected), epic.RarityBucketEmpty, epicSeed, len(vendor.Selected), vendorSeed)
}

// TestHellPartyRewardRollEventKeepsEveryLegacyFieldAndAddsTheRarityLedger 钉住事件字段表：
// **既有字段一个不改**（缺一个就算回归），新增的稀有度台账键齐全、取值自洽，
// 且真实打出来的那一行能让人一眼看出"这一次深渊只可能掉史诗"。
func TestHellPartyRewardRollEventKeepsEveryLegacyFieldAndAddsTheRarityLedger(t *testing.T) {
	service, session, _, runtime, scene, _ := prepareHellPartyRewardRuntime(t)

	const rolls = 1024
	t.Setenv(abyssEpicWindowEnv, "")
	source := hellPartyRewardTestEquSource(hellPartyRewardRarityBandItems())
	request := hellPartyRewardRarityRequestForTest(t, source, rolls)
	result, _ := hellPartyRewardRarityRunForTest(t, request, 20261006, "")

	_, last := hellPartyRewardActorKeys(t, runtime)
	objectKey := last[0]
	fields := hellPartyRewardRollFields(runtime, scene, objectKey, request, result, len(result.Selected), "test")
	pairs := hellPartyRewardEventPairs(t, hellPartyRewardRollEvent, fields)
	hellPartyRewardEventRequireKeys(t, hellPartyRewardRollEvent, pairs,
		// 既有字段（一个都不许少）。
		"dungeon_id", "maze_index", "room", "map_id", "monster_code", "monster_object_key",
		"hell_party_difficulty", "tier", "group_id", "reward_roll_count", "rolls_requested",
		"rolls_performed", "rolls_missed", "rolls_passed_no_item", "drop_rate", "rate_source",
		"chance", "chance_denominator", "dungeon_minimum_level", "dungeon_basis_level",
		"dungeon_difficulty", "pool", "candidates", "grade_min", "grade_max",
		"selected_items", "selected_count", "granted", "degraded", "switch", "source",
		// 新增的稀有度台账。
		"rarity_mode", "rarity_allowed", "rarity_switch", "rarity_rolls", "rarity_drawn",
		"rarity_selected", "rarity_candidates", "rolls_rarity_filtered", "rolls_rarity_bucket_empty",
		// 新增的史诗落地概率台账（操作者 2026-09-29 第 9 条）。
		"epic_window", "epic_window_denominator", "epic_window_switch", "epic_window_source",
		"epic_window_active", "rolls_epic_window", "epic_expectation_ppm")

	if pairs["rarity_mode"] != hellPartyRewardRarityEpicValue || pairs["rarity_allowed"] != "4" ||
		pairs["rarity_switch"] != hellPartyRewardRarityUnsetText {
		t.Fatalf("rarity switch fields=%q/%q/%q want epic/4/-",
			pairs["rarity_mode"], pairs["rarity_allowed"], pairs["rarity_switch"])
	}
	if pairs["rarity_rolls"] != pairs["rolls_performed"] && pairs["rarity_rolls"] == "0" {
		t.Fatalf("rarity_rolls=%q with rolls_performed=%q", pairs["rarity_rolls"], pairs["rolls_performed"])
	}
	if pairs["pool"] != hellPartyRewardPoolEquValue || pairs["rate_source"] != hellPartyRewardDropInfoPath {
		t.Fatalf("pool/rate_source=%q/%q", pairs["pool"], pairs["rate_source"])
	}
	if pairs["rarity_selected"] != "r0:0,r1:0,r2:0,r3:0,r4:"+pairs["selected_count"]+",r5:0" {
		t.Fatalf("rarity_selected=%q selected_count=%q want only r4", pairs["rarity_selected"], pairs["selected_count"])
	}
	if pairs["rarity_candidates"] == hellPartyRewardRarityUnsetText {
		t.Fatalf("fixed tier must report rarity_candidates, got %q", pairs["rarity_candidates"])
	}
	// 不变量：命中的四种去向加起来 = 命中次数；没掉东西的三种原因加起来 = rolls_passed_no_item。
	hits, err := strconv.Atoi(pairs["rarity_rolls"])
	if err != nil {
		t.Fatalf("rarity_rolls=%q is not an integer", pairs["rarity_rolls"])
	}
	filtered, errFiltered := strconv.Atoi(pairs["rolls_rarity_filtered"])
	bucketEmpty, errBucketEmpty := strconv.Atoi(pairs["rolls_rarity_bucket_empty"])
	epicGated, errEpicGated := strconv.Atoi(pairs["rolls_epic_window"])
	noItem, errNoItem := strconv.Atoi(pairs["rolls_passed_no_item"])
	selected, errSelected := strconv.Atoi(pairs["selected_count"])
	if errFiltered != nil || errBucketEmpty != nil || errEpicGated != nil || errNoItem != nil || errSelected != nil {
		t.Fatalf("counters are not integers: %v", pairs)
	}
	if filtered+bucketEmpty+epicGated != noItem {
		t.Fatalf("filtered=%d bucket_empty=%d epic_window=%d want rolls_passed_no_item=%d",
			filtered, bucketEmpty, epicGated, noItem)
	}
	if filtered+bucketEmpty+epicGated+selected != hits {
		t.Fatalf("filtered=%d bucket_empty=%d epic_window=%d selected=%d want hits=%d",
			filtered, bucketEmpty, epicGated, selected, hits)
	}
	// 默认档（在允许集合内抽）：**不**事后过滤任何命中；落地概率由史诗窗口决定。
	// 旧断言（"默认档必须过滤掉非史诗命中"）属于回退字面量 `post_filter` 那一档，
	// 见 `TestHellPartyRewardRarityOrderRollbackLiteralReproducesTheDeployedFilter`。
	if filtered != 0 {
		t.Fatalf("the default tier must not filter after the draw: filtered=%d %v", filtered, pairs)
	}
	if pairs["rarity_order"] != hellPartyRewardRarityOrderWithinValue {
		t.Fatalf("rarity_order=%q want %q", pairs["rarity_order"], hellPartyRewardRarityOrderWithinValue)
	}
	// 史诗窗口：默认档必须**生效**、必须挡下**绝大多数**命中，且窗口读数自洽。
	if pairs["epic_window"] != strconv.FormatInt(abyssEpicWindowDefault, 10) ||
		pairs["epic_window_denominator"] != strconv.FormatInt(abyssEpicWindowBound, 10) ||
		pairs["epic_window_switch"] != abyssEpicWindowUnsetText ||
		pairs["epic_window_source"] != abyssEpicWindowSourceDefault ||
		pairs["epic_window_active"] != "true" {
		t.Fatalf("epic window fields = %q/%q/%q/%q/%q want %d/%d/%s/%s/true",
			pairs["epic_window"], pairs["epic_window_denominator"], pairs["epic_window_switch"],
			pairs["epic_window_source"], pairs["epic_window_active"],
			abyssEpicWindowDefault, abyssEpicWindowBound,
			abyssEpicWindowUnsetText, abyssEpicWindowSourceDefault)
	}
	if epicGated == 0 {
		t.Fatalf("the shipped window must gate hits: hits=%d selected=%d gated=%d bucket_empty=%d window=%q active=%q",
			hits, selected, epicGated, bucketEmpty, pairs["epic_window"], pairs["epic_window_active"])
	}
	if selected == 0 || bucketEmpty != 0 {
		t.Fatalf("the default tier must still pay some hits (and never report an empty r4 bucket here): selected=%d bucket_empty=%d",
			selected, bucketEmpty)
	}
	// 这一次结算的期望件数 = 抽数 × 命中率 × 窗口（ppm），与算式逐位对上。
	wantExpectation := abyssEpicHitsPerSettlementPPM(rolls, int64(result.Chance)) *
		abyssEpicWindowDefault / abyssEpicWindowBound
	if pairs["epic_expectation_ppm"] != strconv.FormatInt(wantExpectation, 10) {
		t.Fatalf("epic_expectation_ppm=%q want %d", pairs["epic_expectation_ppm"], wantExpectation)
	}

	// 端到端：走真 packetLogger，逐字检查线上那一行（操作者按这一行验收）。
	logger, logPath := hellPartyRewardPoolReadyPacketLogger(t)
	service.packetLog = logger
	service.logGameEvent(session, hellPartyRewardRollEvent, fields...)
	text := closeHellPartyRewardPoolReadyPacketLogger(t, logger, logPath)
	line := hellPartyRewardEventLine(t, text, hellPartyRewardRollEvent)
	for _, want := range []string{
		"rarity_mode=epic",
		"rarity_allowed=4",
		"rarity_switch=-",
		"rarity_selected=r0:0,r1:0,r2:0,r3:0,r4:" + pairs["selected_count"] + ",r5:0",
		"rarity_drawn=r0:0,r1:",
		"rarity_candidates=r0:",
		"rolls_rarity_filtered=",
		"rolls_rarity_bucket_empty=0",
		"epic_window=" + strconv.FormatInt(abyssEpicWindowDefault, 10),
		"epic_window_denominator=" + strconv.FormatInt(abyssEpicWindowBound, 10),
		"epic_window_switch=-",
		"epic_window_source=" + abyssEpicWindowSourceDefault,
		"epic_window_active=true",
		"rolls_epic_window=",
		"epic_expectation_ppm=",
		"pool=equ",
		"rate_source=" + hellPartyRewardDropInfoPath,
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("rendered roll line is missing %q:\n%s", want, line)
		}
	}
	t.Logf("EVENT kind=%s %s", hellPartyRewardRollEvent, line)
}

// hellPartyRewardRarityOfCandidate 在真档波段里反查一件装备的 rarity
// （六个桶里找 itemID），`found == false` = 它不在本波段/没过厂商门槛。
func hellPartyRewardRarityOfCandidate(
	t *testing.T,
	request hellPartyRewardRequest,
	itemID uint32,
) (int64, bool) {
	t.Helper()
	for rarity := int64(0); rarity <= hellPartyRewardRarityMax; rarity++ {
		if hellPartyRewardRarityBucketIDs(t, request, rarity)[itemID] {
			return rarity, true
		}
	}
	return 0, false
}

// TestHellPartyRewardPoolReadyRecordCarriesTheRaritySwitch 钉住**启动期**就能 grep 到
// 稀有度闸门的状态（操作者不必等第一次深渊击杀）：激活记录
// `dnf-dungeon-hell-party-reward-pool-ready` 新增三个字段，取值与投骰事件同一口径
// （`rarity_allowed` = **生效**集合；回退/降级档下闸门不参与 ⇒ 退回全集）。
func TestHellPartyRewardPoolReadyRecordCarriesTheRaritySwitch(t *testing.T) {
	for _, test := range []struct {
		name            string
		raritySwitch    string
		poolSwitch      string
		mutate          func(source bridgePVFSource)
		wantMode        string
		wantAllowed     string
		wantSwitchValue string
		wantReason      string
	}{
		{
			name: "default_is_epic_only", raritySwitch: "", poolSwitch: hellPartyRewardPoolEquValue,
			wantMode: "epic", wantAllowed: "4", wantSwitchValue: "-", wantReason: "warmed",
		},
		{
			name: "vendor_rollback", raritySwitch: hellPartyRewardRarityVendorValue, poolSwitch: hellPartyRewardPoolEquValue,
			wantMode: "vendor", wantAllowed: "0,1,2,3,4,5", wantSwitchValue: "vendor", wantReason: "warmed",
		},
		{
			name: "explicit_list", raritySwitch: "4,3", poolSwitch: hellPartyRewardPoolEquValue,
			wantMode: "list", wantAllowed: "3,4", wantSwitchValue: "4,3", wantReason: "warmed",
		},
		{
			name: "itemdictionary_pool_keeps_the_gate_inert", raritySwitch: "", poolSwitch: hellPartyRewardPoolItemDictionaryValue,
			wantMode: "epic", wantAllowed: "0,1,2,3,4,5", wantSwitchValue: "-",
			wantReason: "rollback_tier_keeps_itemdictionary_pool",
		},
		{
			name: "degraded_keeps_the_gate_inert", raritySwitch: "", poolSwitch: hellPartyRewardPoolEquValue,
			mutate:   func(source bridgePVFSource) { source[dungeonDropEquipmentList] = "" },
			wantMode: "epic", wantAllowed: "0,1,2,3,4,5", wantSwitchValue: "-", wantReason: "pool_build_failed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			monsterCatalog := hellPartyRewardReadyTestSyntheticCatalog(t)
			if test.mutate != nil {
				test.mutate(monsterCatalog.source.(bridgePVFSource))
			}
			t.Setenv(hellPartyRewardPoolEnv, test.poolSwitch)
			t.Setenv(hellPartyRewardRarityEnv, test.raritySwitch)

			ready := hellPartyRewardPoolReadinessFor(monsterCatalog)
			pairs := hellPartyRewardEventPairs(t, hellPartyRewardPoolReadyEvent, ready.fields())
			hellPartyRewardEventRequireKeys(t, hellPartyRewardPoolReadyEvent, pairs,
				"rarity_mode", "rarity_allowed", "rarity_switch")
			if ready.Reason != test.wantReason {
				t.Fatalf("reason=%q want %q", ready.Reason, test.wantReason)
			}
			if ready.RarityMode != test.wantMode || pairs["rarity_mode"] != test.wantMode ||
				ready.RarityAllowed != test.wantAllowed || pairs["rarity_allowed"] != test.wantAllowed ||
				ready.RaritySwitch != test.wantSwitchValue || pairs["rarity_switch"] != test.wantSwitchValue {
				t.Fatalf("readiness rarity fields=%q/%q/%q pairs=%q/%q/%q want %q/%q/%q",
					ready.RarityMode, ready.RarityAllowed, ready.RaritySwitch,
					pairs["rarity_mode"], pairs["rarity_allowed"], pairs["rarity_switch"],
					test.wantMode, test.wantAllowed, test.wantSwitchValue)
			}
			// 生产调用点打的就是这份表（`hellPartyRewardPoolWarmUpFields`）。
			assertHellPartyRewardPoolWarmUpFieldsMatch(t, monsterCatalog, ready)
			t.Logf("EVENT kind=%s %s", hellPartyRewardPoolReadyEvent, hellPartyRewardPoolReadyRenderedLine(ready))
		})
	}

	// 没有怪物目录也要带上开关读数（那是最需要排障的一种启动状态）。
	ready := hellPartyRewardPoolReadinessFor(nil)
	pairs := hellPartyRewardEventPairs(t, hellPartyRewardPoolReadyEvent, ready.fields())
	if pairs["rarity_mode"] != "epic" || pairs["rarity_allowed"] != hellPartyRewardRarityAllList() {
		t.Fatalf("no-monster-catalog readiness rarity=%q/%q", pairs["rarity_mode"], pairs["rarity_allowed"])
	}
}

// TestRealPVFHellPartyRewardObservedLiveDropsAreNotEpic 把操作者的现场投诉钉成事实：
// 线上 2026-09-28T12:30:47Z 那次深渊（dungeon 310 / room `3:0` / map 60083 /
// `hell_party_difficulty=1 tier=A` / `dungeon_difficulty=0` ⇒ chance 335、
// `rolls_performed=8 rolls_missed=2`、`granted=6`）掉的 6 件装备，
// 在**真档**里逐件都不是 rarity 4 —— 所以"深渊掉的全是史诗"并不成立，
// 正是操作者说的"这些可以去掉"。
//
// 运行：`DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf> go test -count=1 -v -run RealPVFHellPartyRewardObservedLiveDrops ./internal/services/dnfbridge`
func TestRealPVFHellPartyRewardObservedLiveDropsAreNotEpic(t *testing.T) {
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
	catalog, err := monsterCatalog.DropCatalog()
	if err != nil {
		t.Fatalf("real drop catalog is unusable: %v", err)
	}
	rules, err := currentDungeonMonsterDropRules(monsterCatalog)
	if err != nil {
		t.Fatalf("real %s is unusable: %v", dnfdrops.MonsterRulePath, err)
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
		MonsterCode:           1052,
		DungeonMinimumLevel:   dungeon.Metadata.MinimumRequiredLevel.Value,
		DungeonBasisLevel:     dungeon.Metadata.BasisLevel.Value,
		DungeonDifficulty:     0,
		AbyssPartyDifficulty:  1,
		RewardRollCount:       hellPartyRewardTestTierA,
		dungeonMonsterCatalog: monsterCatalog,
		dropCatalog:           catalog,
		rules:                 rules,
		rng:                   newCurrentDungeonDropLCG(20260928),
	}
	// 线上 `selected_items=` 那 6 件，逐字抄录。
	observed := []uint32{114030031, 102020477, 102040542, 101020668, 100312431, 100130515}
	for _, itemID := range observed {
		rarity, found := hellPartyRewardRarityOfCandidate(t, request, itemID)
		if !found {
			t.Fatalf("observed live drop %d is not in the real band [%d,%d] .equ pool",
				itemID, request.DungeonMinimumLevel, request.DungeonBasisLevel)
		}
		if rarity == hellPartyRewardRarityEpic {
			t.Fatalf("observed live drop %d is rarity %d (epic); the operator's complaint would be unfounded",
				itemID, rarity)
		}
		t.Logf("observed live drop %d rarity=%d（新默认档会把它挡下）", itemID, rarity)
	}
}

// TestRealPVFHellPartyRewardEpicTierDropsOnlyTheRealRarity4Pool 是真档用例
// （`DNFBRIDGE_REAL_PVF_SMOKE` 门控，与 `TestRealPVFHellPartyRewardRollsUseTheFixedTierAndNeverDegrade`
// 同一写法）：线上那份 Script.pvf 上，副本 310 / 波段 [87,97] / 难度 1 的深渊奖励
// 在**默认档**下
//   - `rarity_mode=epic`、`pool=equ`、无降级；
//   - 24576 次投骰（≈21500 次命中）里落地的每一件都出自真档的 rarity 4 桶（248 件），
//     且**没有任何一件**出自 r0/r1/r2/r3 桶；
//   - 判别力：同一波段、同一种子的 vendor 档会落地两万件低稀有度装备；
//   - A/B：两档的 `rarity_drawn`、`rolls_missed` 与 LCG 终态逐位相等。
//
// 运行：`DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf> go test -count=1 -v -run RealPVFHellPartyRewardEpicTier ./internal/services/dnfbridge`
func TestRealPVFHellPartyRewardEpicTierDropsOnlyTheRealRarity4Pool(t *testing.T) {
	t.Setenv(abyssEpicWindowEnv, "")
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
	catalog, err := monsterCatalog.DropCatalog()
	if err != nil {
		t.Fatalf("real drop catalog is unusable: %v", err)
	}
	rules, err := currentDungeonMonsterDropRules(monsterCatalog)
	if err != nil {
		t.Fatalf("real %s is unusable: %v", dnfdrops.MonsterRulePath, err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatalf("load real worldmap: %v", err)
	}
	dungeon, ok := table.FindDungeon(310)
	if !ok {
		t.Fatal("real dungeon 310 not indexed")
	}
	minimumLevel := dungeon.Metadata.MinimumRequiredLevel.Value
	basisLevel := dungeon.Metadata.BasisLevel.Value
	gradeMin, gradeMax, ok := hellPartyRewardGradeRangeForLevels(minimumLevel, basisLevel)
	if !ok {
		t.Fatalf("real dungeon 310 levels are unusable: minimum=%d basis=%d", minimumLevel, basisLevel)
	}
	pool, err := catalog.hellPartyRewardEquipmentPool()
	if err != nil {
		t.Fatalf("build real .equ pool: %v", err)
	}
	counts := pool.rarityCandidateCounts(gradeMin, gradeMax)
	t.Logf("real band [%d,%d] rarity_candidates=%s", gradeMin, gradeMax, hellPartyRewardRarityHistogram(counts))
	// 判别力前提：低稀有度桶在真档波段里是**有货**的（否则"只掉史诗"没有意义）。
	for _, rarity := range []int{0, 1, 2, 3} {
		if counts[rarity] == 0 {
			t.Fatalf("real band [%d,%d] rarity=%d has no candidate; the epic-only assertion would be vacuous",
				gradeMin, gradeMax, rarity)
		}
	}
	if counts[4] < 200 {
		t.Fatalf("real band [%d,%d] rarity=4 candidates=%d want >= 200", gradeMin, gradeMax, counts[4])
	}

	const (
		rolls = int64(24576)
		seed  = uint32(20261007)
	)
	request := hellPartyRewardRequest{
		MonsterCode:           65747,
		DungeonMinimumLevel:   minimumLevel,
		DungeonBasisLevel:     basisLevel,
		DungeonDifficulty:     1, // 现场冻结口径：hell 表第二列 = 875
		AbyssPartyDifficulty:  1,
		RewardRollCount:       rolls,
		dungeonMonsterCatalog: monsterCatalog,
		dropCatalog:           catalog,
		rules:                 rules,
		rng:                   newCurrentDungeonDropLCG(seed),
	}

	epic, epicSeed := hellPartyRewardRarityRunForTest(t, request, seed, "")
	if epic.Degraded != "" {
		t.Fatalf("real PVF with a built pool must not degrade: degraded=%q", epic.Degraded)
	}
	if epic.RarityMode != hellPartyRewardRarityEpicValue || epic.RarityAllowed != "4" {
		t.Fatalf("real default tier rarity_mode=%q allowed=%q want epic/4", epic.RarityMode, epic.RarityAllowed)
	}
	if epic.Rate != 875 || epic.Chance != 875 {
		t.Fatalf("real rate=%d chance=%d want 875 (dungeon difficulty 1)", epic.Rate, epic.Chance)
	}
	if epic.GradeMin != gradeMin || epic.GradeMax != gradeMax {
		t.Fatalf("real band=%d..%d want %d..%d", epic.GradeMin, epic.GradeMax, gradeMin, gradeMax)
	}
	if epic.RarityRolls < 20000 || len(epic.Selected) == 0 {
		t.Fatalf("real long run: hits=%d selected=%d want ~21500 hits and >0 epics",
			epic.RarityRolls, len(epic.Selected))
	}
	// 真档上的**次序**判据（`_dist/abyss-epic-roll-order.md`）：
	// 默认档在允许集合内取件，实际落地还受已部署的史诗窗口限制；
	// 回退字面量 `post_filter` 复现线上"几乎全被过滤"的形状，且两档的随机流逐位相同。
	t.Setenv(hellPartyRewardRarityOrderEnv, hellPartyRewardRarityOrderPostFilterValue)
	deployed, deployedSeed := hellPartyRewardRarityRunForTest(t, request, seed, "")
	t.Setenv(hellPartyRewardRarityOrderEnv, "")
	t.Logf("real abyss: within hits=%d selected=%d filtered=%d seed=%d",
		epic.RarityRolls, len(epic.Selected), epic.RarityFiltered, epicSeed)
	t.Logf("real abyss: post_filter hits=%d selected=%d filtered=%d seed=%d",
		deployed.RarityRolls, len(deployed.Selected), deployed.RarityFiltered, deployedSeed)
	if deployedSeed != epicSeed {
		t.Fatalf("the order tiers consumed different streams: %d vs %d", deployedSeed, epicSeed)
	}
	if epic.RarityFiltered != 0 || deployed.RarityFiltered == 0 {
		t.Fatalf("real tiers: within filtered=%d (want 0), post_filter filtered=%d (want >0)",
			epic.RarityFiltered, deployed.RarityFiltered)
	}
	if !epic.EpicWindowActive || epic.EpicWindow != abyssEpicWindowDefault || epic.EpicGated == 0 {
		t.Fatalf("real default window active=%v window=%d gated=%d", epic.EpicWindowActive, epic.EpicWindow, epic.EpicGated)
	}
	if len(epic.Selected)+epic.RarityBucketEmpty+epic.EpicGated != epic.RarityRolls || epic.NoDrop != epic.RarityBucketEmpty+epic.EpicGated {
		t.Fatalf("real within hit accounting: hits=%d selected=%d bucket_empty=%d epic_gated=%d no_drop=%d",
			epic.RarityRolls, len(epic.Selected), epic.RarityBucketEmpty, epic.EpicGated, epic.NoDrop)
	}

	rarity4 := hellPartyRewardRarityBucketIDs(t, request, 4)
	if len(rarity4) != counts[4] {
		t.Fatalf("rarity 4 id set=%d want %d", len(rarity4), counts[4])
	}
	lower := make(map[uint32]bool)
	for _, rarity := range []int64{0, 1, 2, 3} {
		for id := range hellPartyRewardRarityBucketIDs(t, request, rarity) {
			lower[id] = true
		}
	}
	for _, item := range epic.Selected {
		if lower[item.ItemID] {
			t.Fatalf("real epic tier produced a non-epic item %d", item.ItemID)
		}
		if !rarity4[item.ItemID] {
			t.Fatalf("real epic tier produced item %d outside the real rarity 4 pool", item.ItemID)
		}
		definition, err := catalog.ResolveItem(item.ItemID)
		if err != nil {
			t.Fatalf("resolve %d: %v", item.ItemID, err)
		}
		if definition.Grade < gradeMin || definition.Grade > gradeMax {
			t.Fatalf("real epic item %d grade=%d outside [%d,%d]",
				item.ItemID, definition.Grade, gradeMin, gradeMax)
		}
	}
	for rarity := 0; rarity < hellPartyRewardRarityBuckets; rarity++ {
		if rarity == int(hellPartyRewardRarityEpic) {
			continue
		}
		if epic.RaritySelected[rarity] != 0 {
			t.Fatalf("real epic tier selected rarity %d x%d", rarity, epic.RaritySelected[rarity])
		}
	}
	// 抽到的稀有度遵循厂商表；实际落地率遵循已部署的史诗窗口。
	hellPartyRewardRarityRequireShare(t, "real epic rarity_drawn r4", epic.RarityDrawn[4], epic.RarityRolls, 0.007, 0.004)
	hellPartyRewardRarityRequireShare(t, "real epic granted epic rate", len(epic.Selected), epic.RarityRolls,
		float64(abyssEpicWindowDefault)/float64(abyssEpicWindowBound), 0.004)
	// 窗口关闭时保留旧的“每命中一件”断言，同时验证随机轨迹不变。
	t.Setenv(abyssEpicWindowEnv, abyssEpicWindowLegacyValue)
	ungated, ungatedSeed := hellPartyRewardRarityRunForTest(t, request, seed, "")
	if ungated.EpicWindowActive || ungated.EpicGated != 0 || len(ungated.Selected)+ungated.RarityBucketEmpty != ungated.RarityRolls || ungatedSeed != epicSeed {
		t.Fatalf("real window rollback: active=%v gated=%d hits=%d selected=%d empty=%d seed=%d want_seed=%d",
			ungated.EpicWindowActive, ungated.EpicGated, ungated.RarityRolls, len(ungated.Selected), ungated.RarityBucketEmpty, ungatedSeed, epicSeed)
	}
	t.Setenv(abyssEpicWindowEnv, "")

	// 判别力 + A/B：同一份真档、同一种子的 vendor 档会掉两万件低稀有度装备，
	// 而两档的随机数轨迹完全一致（只有落地物品不同）。
	vendor, vendorSeed := hellPartyRewardRarityRunForTest(t, request, seed, hellPartyRewardRarityVendorValue)
	lowerGranted := 0
	for rarity := 0; rarity < hellPartyRewardRarityBuckets; rarity++ {
		if rarity == int(hellPartyRewardRarityEpic) {
			continue
		}
		lowerGranted += vendor.RaritySelected[rarity]
	}
	if lowerGranted < 10000 {
		t.Fatalf("real vendor tier granted only %d lower-rarity items in %d rolls", lowerGranted, rolls)
	}
	if vendorSeed != epicSeed || vendor.RarityDrawn != epic.RarityDrawn || vendor.Misses != epic.Misses ||
		vendor.RarityRolls != epic.RarityRolls || vendor.Rolls != epic.Rolls {
		t.Fatalf("real tiers consumed different random streams: epic seed=%d rolls=%d misses=%d draws=%d vs vendor seed=%d rolls=%d misses=%d draws=%d",
			epicSeed, epic.Rolls, epic.Misses, epic.RarityRolls,
			vendorSeed, vendor.Rolls, vendor.Misses, vendor.RarityRolls)
	}
	// 每次结算（A 档 8 次投骰）的期望史诗件数：命中率 x 史诗窗口。
	perRoom := float64(hellPartyRewardTestTierA) * 875.0 / float64(hellPartyRewardDropChanceDenominator) * float64(abyssEpicWindowDefault) / float64(abyssEpicWindowBound)
	t.Logf("real epic tier: hits=%d filtered=%d bucket_empty=%d epics=%d; band=%d..%d candidates=%s; expected epics per tier-A room at difficulty 1 = %.4f",
		epic.RarityRolls, epic.RarityFiltered, epic.RarityBucketEmpty, len(epic.Selected),
		gradeMin, gradeMax, hellPartyRewardRarityHistogram(counts), perRoom)
}
