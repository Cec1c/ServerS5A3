package dnfbridge

import (
	"strconv"
	"strings"
	"testing"
)

// 本文件钉住操作者 2026-09-29 第 9 条（`_dist/运维实测-20260929-0500.md`）：
//
//	「深渊爆率问题 **要写成评价打4-5个深渊房间看到一个史诗装备才对**
//	  **碎片还是每次都要有** **难度1掉落7个碎片按难度增加掉率**」
//
// 判据分五组：
//
//	1. **算式**：窗口宽度与"多少间房一件史诗"的关系（真档那一行 rate = 335/875/1000/1400/1000，
//	   A 档 8 抽、每房间 2 次结算）—— 默认档必须落在 4–5 间之间，且**随难度递增**；
//	2. **窗口的语义**：`vendor` 别名与厂商自己的 r4 档**逐位重合**（穷举 1,000,000 个 roll 证明），
//	   默认档只否决"没落进顶端窗口"的那些抽；
//	3. **一个随机数都没动**：默认档 / 回退字面量 `legacy` / 关成 0，三档的
//	   `rolls`/`misses`/`rarity_rolls`/`rarity_drawn` 与 LCG 终态**逐位相同**；
//	4. **回退与范围**：`legacy`/`off` 逐字节回到今天（每一次命中都落地）；`post_filter`、
//	   `RARITY=vendor`、`POOL=itemdictionary` 三档下窗口**根本不参与**；
//	5. **碎片不受影响**（操作者「碎片还是每次都要有」）：端到端跑一次真实 op38 ——
//	   窗口关成 0 时**一件史诗都不掉**，而那一间房的 **7 个碎片一个不少、而且逐件相同**
//	   （碎片保底用的是同一条房间掉落 LCG，窗口不多抽也不少抽）。

// hellPartyRewardEpicWindowRunForTest 用给定窗口、给定种子跑一档（稀有度档与次序档由调用方设）。
func hellPartyRewardEpicWindowRunForTest(
	t *testing.T,
	request hellPartyRewardRequest,
	seed uint32,
	window string,
) hellPartyRewardResult {
	t.Helper()
	t.Setenv(abyssEpicWindowEnv, window)
	request.rng = newCurrentDungeonDropLCG(seed)
	result, err := hellPartyRewardRolls(request)
	if err != nil {
		t.Fatalf("roll (epic window=%q): %v", window, err)
	}
	return result
}

// TestAbyssEpicWindowSwitchOnlyAcceptsIntegersAndTheDocumentedLiterals 钉住档位解析：
//
//	未设置/空/拼错/越界 ⇒ **默认档**（16000，原始取值照进 `epic_window_switch=`）；
//	纯整数 0..1000000   ⇒ 直接用；
//	legacy/off/unlimited/all ⇒ 闸门关掉（= 每一次命中都落地 = 今天）；
//	vendor              ⇒ 厂商自己的 r4 档（7000）。
func TestAbyssEpicWindowSwitchOnlyAcceptsIntegersAndTheDocumentedLiterals(t *testing.T) {
	for _, test := range []struct {
		value      string
		wantWidth  int64
		wantSource string
		wantSwitch string
	}{
		{value: "", wantWidth: abyssEpicWindowDefault, wantSource: abyssEpicWindowSourceDefault, wantSwitch: "-"},
		{value: "   ", wantWidth: abyssEpicWindowDefault, wantSource: abyssEpicWindowSourceDefault, wantSwitch: "-"},
		{value: "16000", wantWidth: 16000, wantSource: abyssEpicWindowSourceEnv, wantSwitch: "16000"},
		{value: " 14284 ", wantWidth: 14284, wantSource: abyssEpicWindowSourceEnv, wantSwitch: "14284"},
		{value: "0", wantWidth: 0, wantSource: abyssEpicWindowSourceEnv, wantSwitch: "0"},
		{value: "1000000", wantWidth: abyssEpicWindowAlways, wantSource: abyssEpicWindowSourceEnv, wantSwitch: "1000000"},
		{value: "legacy", wantWidth: abyssEpicWindowAlways, wantSource: "alias:legacy", wantSwitch: "legacy"},
		{value: " OFF ", wantWidth: abyssEpicWindowAlways, wantSource: "alias:off", wantSwitch: "OFF"},
		{value: "unlimited", wantWidth: abyssEpicWindowAlways, wantSource: "alias:unlimited", wantSwitch: "unlimited"},
		{value: "vendor", wantWidth: abyssEpicWindowVendor, wantSource: "alias:vendor", wantSwitch: "vendor"},
		// 拼错 / 越界 / 非整数 ⇒ 默认档，原始取值照进事件（不许静默吞掉）。
		{value: "premium", wantWidth: abyssEpicWindowDefault, wantSource: abyssEpicWindowSourceDefault, wantSwitch: "premium"},
		{value: "-1", wantWidth: abyssEpicWindowDefault, wantSource: abyssEpicWindowSourceDefault, wantSwitch: "-1"},
		{value: "1000001", wantWidth: abyssEpicWindowDefault, wantSource: abyssEpicWindowSourceDefault, wantSwitch: "1000001"},
		{value: "16000.5", wantWidth: abyssEpicWindowDefault, wantSource: abyssEpicWindowSourceDefault, wantSwitch: "16000.5"},
	} {
		plan := readAbyssEpicWindowPlan(test.value)
		if plan.Width != test.wantWidth || plan.Source != test.wantSource ||
			plan.switchText() != test.wantSwitch {
			t.Fatalf("value=%q ⇒ width=%d source=%q switch=%q want %d/%q/%q",
				test.value, plan.Width, plan.Source, plan.switchText(),
				test.wantWidth, test.wantSource, test.wantSwitch)
		}
	}
	// 开关名与默认宽度是线上可判据（操作者据此排障/调档），钉死。
	if abyssEpicWindowEnv != "DNFBRIDGE_ABYSS_EPIC_WINDOW" {
		t.Fatalf("epic window env=%q", abyssEpicWindowEnv)
	}
	if abyssEpicWindowDefault != 16000 || abyssEpicWindowVendor != 7000 ||
		abyssEpicWindowBound != 1000000 {
		t.Fatalf("window constants=%d/%d/%d want 16000/7000/1000000",
			abyssEpicWindowDefault, abyssEpicWindowVendor, abyssEpicWindowBound)
	}
	// 默认档就是读**未设置**的环境变量（消费点自读），不是"碰巧的零值"。
	t.Setenv(abyssEpicWindowEnv, "")
	if env := abyssEpicWindowPlanFromEnv(); env.Width != abyssEpicWindowDefault ||
		env.Source != abyssEpicWindowSourceDefault || env.switchText() != abyssEpicWindowUnsetText {
		t.Fatalf("unset env ⇒ %+v want the default window", env)
	}
}

// TestAbyssEpicWindowVendorAliasIsExactlyTheVendorsOwnRarity4Window 是**语义**的穷举证明：
// `vendor` 别名（7000）必须与厂商 A 档阈值表自己的 r4 档**逐位重合** ——
// 对全部 1,000,000 个可能的 `lcg.Next(1000000)+1` 都成立，一个都不差。
//
// 这条同时证明"窗口这一维与稀有度那一抽是同一根轴"（不是另发明一个概率维度）：
// 窗口宽度就是"这一抽算多稀有"的顶端切片，`vendor` = 厂商原值，默认档 = 把顶端切片放大。
func TestAbyssEpicWindowVendorAliasIsExactlyTheVendorsOwnRarity4Window(t *testing.T) {
	info := parseHellPartyRewardDropInfo(
		hellPartyRewardRarityTestHellText(875, hellPartyRewardRarityTestRowA))
	vendorPlan := readAbyssEpicWindowPlan(abyssEpicWindowVendorValue)
	if vendorPlan.Width != abyssEpicWindowVendor {
		t.Fatalf("vendor alias width=%d want %d", vendorPlan.Width, abyssEpicWindowVendor)
	}
	mismatches := 0
	firstMismatch := int64(0)
	vendorEpics := 0
	for roll := int64(1); roll <= abyssEpicWindowBound; roll++ {
		drawn := info.rarityForRoll(1, roll)
		want := drawn == hellPartyRewardRarityEpic
		if want {
			vendorEpics++
		}
		if vendorPlan.permits(roll) != want {
			if mismatches == 0 {
				firstMismatch = roll
			}
			mismatches++
		}
	}
	if mismatches != 0 {
		t.Fatalf("the vendor alias is not the vendor's own rarity-4 window: %d mismatching rolls (first=%d)",
			mismatches, firstMismatch)
	}
	if vendorEpics != int(abyssEpicWindowVendor) {
		t.Fatalf("vendor A-tier rarity-4 window=%d rolls want %d", vendorEpics, abyssEpicWindowVendor)
	}
	// 默认档是 vendor 窗口的**超集**：厂商自己抽到的每一件史诗在默认档下**也**落地
	// （"放大顶端切片"不会把厂商的史诗弄丢）。
	defaultPlan := readAbyssEpicWindowPlan("")
	for roll := int64(1); roll <= abyssEpicWindowBound; roll++ {
		if info.rarityForRoll(1, roll) == hellPartyRewardRarityEpic && !defaultPlan.permits(roll) {
			t.Fatalf("the default window dropped a roll the vendor itself calls epic: roll=%d", roll)
		}
	}
	// `legacy`（= 今天）放行每一抽；`0` 一抽都不放行；取不到 roll（0）时 fail-open。
	legacyPlan := readAbyssEpicWindowPlan(abyssEpicWindowLegacyValue)
	if legacyPlan.gates() {
		t.Fatal("the legacy literal must switch the gate off")
	}
	for _, roll := range []int64{1, 7000, 993000, 993001, 999999, 1000000} {
		if !legacyPlan.permits(roll) {
			t.Fatalf("legacy must permit every roll: %d", roll)
		}
		if !defaultPlan.permits(0) {
			t.Fatal("a missing roll must fail-open")
		}
	}
	zeroPlan := readAbyssEpicWindowPlan("0")
	if !zeroPlan.gates() {
		t.Fatal("width 0 must be a real gate")
	}
	for _, roll := range []int64{1, 500000, 999999, 1000000} {
		if zeroPlan.permits(roll) {
			t.Fatalf("width 0 must never permit a roll: %d", roll)
		}
	}
	t.Logf("vendor window: %d/1000000 rolls are the vendor's own rarity-4 draw; default window=%d; legacy=%d",
		vendorEpics, defaultPlan.Width, legacyPlan.Width)
}

// TestAbyssEpicWindowDefaultHitsTheOperatorsFourToFiveRoomsPerEpic 是**算式**用例：
// 用真档那一行 `45 200 335 875 1000 1400 1000`、A 档 8 抽、每房间 2 次结算，
// 逐个副本难度算出"多少间房一件史诗"，并钉住：
//
//	① 难度 1（操作者实测所在的难度）必须落在 **4–5 间**之间；
//	② 边界宽度 17855 ⇒ 恰好 ≈4.00 间、14284 ⇒ 恰好 ≈5.00 间（带的两端就是这两个数）；
//	③ **随难度递增**（难度越高越容易出史诗 ⇒ 间数单调不增）；
//	④ 与"今天的 shipped 行为"（窗口 = 1000000）差一个数量级：今天 ≈ 0.07 间 1 件。
func TestAbyssEpicWindowDefaultHitsTheOperatorsFourToFiveRoomsPerEpic(t *testing.T) {
	info := parseHellPartyRewardDropInfo(hellPartyRewardTestHellDropInfoRealRatesText())
	const rolls = hellPartyRewardTestTierA
	widths := func(width int64) []float64 {
		rooms := make([]float64, 0, 5)
		for difficulty := int64(0); difficulty <= 4; difficulty++ {
			rate, found := info.rateForLevel(hellPartyRewardTestBasisLevel, difficulty)
			if !found {
				t.Fatalf("real rates text has no row for basis=%d difficulty=%d",
					hellPartyRewardTestBasisLevel, difficulty)
			}
			rooms = append(rooms, abyssEpicRoomsPerDrop(
				abyssEpicSettlementsPerRoom, rolls, rate, width))
		}
		return rooms
	}

	// 真档那一行本身（算式的输入）先钉住，免得"表读错了"把算术带偏。
	for difficulty, want := range []int64{335, 875, 1000, 1400, 1000} {
		if rate, _ := info.rateForLevel(hellPartyRewardTestBasisLevel, int64(difficulty)); rate != want {
			t.Fatalf("difficulty %d rate=%d want %d", difficulty, rate, want)
		}
	}
	// 期望命中次数（算式 ①②）：难度 1 每次结算 8 × 876/1001、每房间两次。
	if got := abyssEpicHitsPerSettlementPPM(rolls, 875); got != 7000999 {
		t.Fatalf("E[hits/settlement] at difficulty 1 = %d ppm want 7000999 (8 x 876/1001)", got)
	}
	if got := abyssEpicHitsPerSettlementPPM(rolls, 1000); got != 8000000 {
		t.Fatalf("E[hits/settlement] at chance 1000 = %d ppm want 8000000 (always hits)", got)
	}

	defaultRooms := widths(abyssEpicWindowDefault)
	if defaultRooms[1] < 4 || defaultRooms[1] > 5 {
		t.Fatalf("the shipped default must be 4-5 rooms per epic at difficulty 1, got %.3f", defaultRooms[1])
	}
	for difficulty := 1; difficulty <= 4; difficulty++ {
		if defaultRooms[difficulty] > defaultRooms[difficulty-1] {
			t.Fatalf("rooms per epic must not increase with difficulty: difficulty %d=%.3f > %d=%.3f",
				difficulty, defaultRooms[difficulty], difficulty-1, defaultRooms[difficulty-1])
		}
	}
	if defaultRooms[0] <= defaultRooms[1] {
		t.Fatalf("difficulty 0 must be rarer than difficulty 1: %.3f vs %.3f", defaultRooms[0], defaultRooms[1])
	}
	// 带的两端（操作者若想正好 4 间或正好 5 间，直接用这两个数）。
	lowerRooms := widths(14284)
	upperRooms := widths(17855)
	if lowerRooms[1] < 4.99 || lowerRooms[1] > 5.01 {
		t.Fatalf("width 14284 must be exactly 5.00 rooms at difficulty 1, got %.4f", lowerRooms[1])
	}
	if upperRooms[1] < 3.99 || upperRooms[1] > 4.01 {
		t.Fatalf("width 17855 must be exactly 4.00 rooms at difficulty 1, got %.4f", upperRooms[1])
	}
	// 今天的行为（窗口 = 1000000）：每间 ≈ 14 件 ⇒ 0.07 间 1 件。
	todayRooms := widths(abyssEpicWindowAlways)
	if todayRooms[1] < 0.07 || todayRooms[1] > 0.072 {
		t.Fatalf("the shipped-today window must be ~0.0714 rooms at difficulty 1, got %.4f", todayRooms[1])
	}
	// 厂商原味（7000）：10.2 间 1 件。
	vendorRooms := widths(abyssEpicWindowVendor)
	if vendorRooms[1] < 10 || vendorRooms[1] > 10.5 {
		t.Fatalf("the vendor window must be ~10.2 rooms at difficulty 1, got %.4f", vendorRooms[1])
	}
	// `0` = 一件都不掉（返回 0 表示不可达，不是 Inf）。
	if rooms := abyssEpicRoomsPerDrop(abyssEpicSettlementsPerRoom, rolls, 875, 0); rooms != 0 {
		t.Fatalf("width 0 must be unreachable, got %.4f", rooms)
	}
	t.Logf("rooms per epic (default window=%d): d0=%.2f d1=%.2f d2=%.2f d3=%.2f d4=%.2f | width 14284⇒%.2f, 17855⇒%.2f, vendor⇒%.2f, today⇒%.4f",
		abyssEpicWindowDefault,
		defaultRooms[0], defaultRooms[1], defaultRooms[2], defaultRooms[3], defaultRooms[4],
		lowerRooms[1], upperRooms[1], vendorRooms[1], todayRooms[1])
}

// TestAbyssEpicWindowKeepsTheRandomStreamIdenticalAndOnlyChangesWhatLands 是**不变量**用例：
// 默认档、回退字面量 `legacy`、关成 `0` 三档的
// `rolls`/`misses`/`rarity_rolls`/`rarity_drawn` 与 **LCG 终态逐位相同**，
// 只有"落地几件"不同 —— 否则这个房间之后的所有掉落（含碎片保底）都会被整体挪位。
func TestAbyssEpicWindowKeepsTheRandomStreamIdenticalAndOnlyChangesWhatLands(t *testing.T) {
	const rolls = 4096
	source := hellPartyRewardTestEquSource(hellPartyRewardRarityBandItems())
	request := hellPartyRewardRarityRequestForTest(t, source, rolls)
	const seed = uint32(20260929)

	t.Setenv(hellPartyRewardRarityOrderEnv, "")
	t.Setenv(hellPartyRewardRarityEnv, "")
	defaultTier := hellPartyRewardEpicWindowRunForTest(t, request, seed, "")
	legacyTier := hellPartyRewardEpicWindowRunForTest(t, request, seed, abyssEpicWindowLegacyValue)
	zeroTier := hellPartyRewardEpicWindowRunForTest(t, request, seed, "0")

	if defaultTier.Rolls != legacyTier.Rolls || defaultTier.Rolls != zeroTier.Rolls ||
		defaultTier.Misses != legacyTier.Misses || defaultTier.Misses != zeroTier.Misses ||
		defaultTier.RarityRolls != legacyTier.RarityRolls || defaultTier.RarityRolls != zeroTier.RarityRolls {
		t.Fatalf("the window changed the number of draws: rolls %d/%d/%d misses %d/%d/%d hits %d/%d/%d",
			defaultTier.Rolls, legacyTier.Rolls, zeroTier.Rolls,
			defaultTier.Misses, legacyTier.Misses, zeroTier.Misses,
			defaultTier.RarityRolls, legacyTier.RarityRolls, zeroTier.RarityRolls)
	}
	if defaultTier.RarityDrawn != legacyTier.RarityDrawn || defaultTier.RarityDrawn != zeroTier.RarityDrawn {
		t.Fatalf("the window changed the vendor rarity draw: %s / %s / %s",
			hellPartyRewardRarityHistogram(defaultTier.RarityDrawn),
			hellPartyRewardRarityHistogram(legacyTier.RarityDrawn),
			hellPartyRewardRarityHistogram(zeroTier.RarityDrawn))
	}
	if defaultTier.Chance != legacyTier.Chance || defaultTier.Rate != legacyTier.Rate {
		t.Fatalf("the window changed rate/chance: %d/%d vs %d/%d",
			defaultTier.Rate, defaultTier.Chance, legacyTier.Rate, legacyTier.Chance)
	}
	// 三档的落地件数必须有判别力：今天全落地 > 默认档 > 0。
	if len(legacyTier.Selected) != legacyTier.RarityRolls-legacyTier.RarityBucketEmpty {
		t.Fatalf("legacy must pay every hit: hits=%d selected=%d bucket_empty=%d",
			legacyTier.RarityRolls, len(legacyTier.Selected), legacyTier.RarityBucketEmpty)
	}
	if len(zeroTier.Selected) != 0 || zeroTier.EpicGated != zeroTier.RarityRolls {
		t.Fatalf("width 0 must gate every hit: selected=%d gated=%d hits=%d",
			len(zeroTier.Selected), zeroTier.EpicGated, zeroTier.RarityRolls)
	}
	if len(defaultTier.Selected) == 0 || len(defaultTier.Selected) >= len(legacyTier.Selected) {
		t.Fatalf("the default window must land some, but far fewer, items than legacy: default=%d legacy=%d",
			len(defaultTier.Selected), len(legacyTier.Selected))
	}
	// 落地比例 = 窗口本身（1.6%）。
	hellPartyRewardRarityRequireShare(t, "default window landing rate",
		len(defaultTier.Selected), defaultTier.RarityRolls,
		float64(abyssEpicWindowDefault)/float64(abyssEpicWindowBound), 0.004)
	// 落地的一定还是 r4（窗口只管"落不落地"，不管"掉哪一档"）。
	if defaultTier.RaritySelected[hellPartyRewardRarityIndex(hellPartyRewardRarityEpic)] != len(defaultTier.Selected) {
		t.Fatalf("the window must not change which rarity lands: rarity_selected=%s selected=%d",
			hellPartyRewardRarityHistogram(defaultTier.RaritySelected), len(defaultTier.Selected))
	}
	// 守恒：命中 = 落地 + 桶空 + 被窗口挡下；`epic_expectation_ppm` = 抽数 × 命中率 × 窗口。
	if defaultTier.RarityFiltered+defaultTier.RarityBucketEmpty+defaultTier.EpicGated+
		len(defaultTier.Selected) != defaultTier.RarityRolls {
		t.Fatalf("hits=%d split into filtered=%d bucket_empty=%d gated=%d selected=%d",
			defaultTier.RarityRolls, defaultTier.RarityFiltered, defaultTier.RarityBucketEmpty,
			defaultTier.EpicGated, len(defaultTier.Selected))
	}
	// `epic_window_active` 的口径 = "闸门装上了、而且真的在否决"：
	// 回退字面量（宽度 = 1000000）⇒ false；宽度 0（一抽都不放行）⇒ true。
	if !defaultTier.EpicWindowActive || legacyTier.EpicWindowActive || !zeroTier.EpicWindowActive {
		t.Fatalf("epic_window_active must be true for the default window and for width 0, false only for the rollback literal: %t/%t/%t",
			defaultTier.EpicWindowActive, legacyTier.EpicWindowActive, zeroTier.EpicWindowActive)
	}
	wantExpectation := abyssEpicHitsPerSettlementPPM(rolls, int64(defaultTier.Chance)) *
		abyssEpicWindowDefault / abyssEpicWindowBound
	if defaultTier.EpicExpectationPPM != wantExpectation {
		t.Fatalf("epic_expectation_ppm=%d want %d", defaultTier.EpicExpectationPPM, wantExpectation)
	}
	if legacyTier.EpicExpectationPPM != 0 || zeroTier.EpicExpectationPPM != 0 {
		t.Fatalf("the expectation readout must be 0 when nothing can land: legacy=%d zero=%d",
			legacyTier.EpicExpectationPPM, zeroTier.EpicExpectationPPM)
	}
	t.Logf("window tiers: legacy selected=%d gated=%d / default selected=%d gated=%d ppm=%d / zero selected=%d gated=%d (hits=%d, same lcg state)",
		len(legacyTier.Selected), legacyTier.EpicGated,
		len(defaultTier.Selected), defaultTier.EpicGated, defaultTier.EpicExpectationPPM,
		len(zeroTier.Selected), zeroTier.EpicGated, defaultTier.RarityRolls)
}

// TestAbyssEpicWindowOnlyVetoesTheEpicRarityInAWideAllowedSet 钉住"只否决史诗"：
// 允许集合 = `0,4` 时，并集里还有 rarity 0 的件 ⇒ 那些档**不受窗口影响**
// （照旧每一次命中都落地），只有抽取真的落在 rarity 4 桶上时才过窗口。
func TestAbyssEpicWindowOnlyVetoesTheEpicRarityInAWideAllowedSet(t *testing.T) {
	items := []struct {
		ID     int64
		Grade  int64
		Rarity int64
		Rate   int64
	}{
		{ID: 9631, Grade: 90, Rarity: 0, Rate: 100},
		{ID: 9632, Grade: 95, Rarity: 4, Rate: 100},
	}
	source := hellPartyRewardTestEquSource(items)
	// 稀有度行：任何 roll 都落在 rarity 0（这样"抽取"是否落在 r4 **完全**由并集加权决定）。
	source[hellPartyRewardDropInfoPath] = hellPartyRewardRarityTestHellText(1000, hellPartyRewardRarityTestRowA)
	request := hellPartyRewardRarityRequestForTest(t, source, 2048)
	const seed = uint32(20260930)

	t.Setenv(hellPartyRewardRarityOrderEnv, "")
	t.Setenv(hellPartyRewardRarityEnv, "0,4")
	wide := hellPartyRewardEpicWindowRunForTest(t, request, seed, "")
	if wide.RarityAllowed != "0,4" {
		t.Fatalf("this case needs the wide allowed set, got %q", wide.RarityAllowed)
	}
	if !wide.EpicWindowActive {
		t.Fatal("the window must be active in the wide (still restricting) set")
	}
	if wide.RarityBucketEmpty != 0 {
		t.Fatalf("the wide set has candidates in both buckets: bucket_empty=%d", wide.RarityBucketEmpty)
	}
	// 全部命中：一半左右落 r0（每次命中都落地），落 r4 的那些才被窗口筛。
	if wide.RaritySelected[0] == 0 {
		t.Fatalf("rarity 0 must land on every hit that drew it: %s",
			hellPartyRewardRarityHistogram(wide.RaritySelected))
	}
	// 全部命中都有去向：r0 落地 + r4 落地 + r4 被窗口挡下 = 命中次数（桶空必须为 0）。
	if wide.RaritySelected[0]+wide.RaritySelected[4]+wide.EpicGated != wide.RarityRolls {
		t.Fatalf("wide set: r0 landed=%d r4 landed=%d epic gated=%d want every hit (%d)",
			wide.RaritySelected[0], wide.RaritySelected[4], wide.EpicGated, wide.RarityRolls)
	}
	// 回退字面量：窗口关掉 ⇒ **抽到 r4 的那些全都落地**（这是"r4 一共抽到几次"的读数）。
	legacy := hellPartyRewardEpicWindowRunForTest(t, request, seed, abyssEpicWindowLegacyValue)
	if legacy.EpicGated != 0 || legacy.RaritySelected[4] == 0 {
		t.Fatalf("legacy must land the r4 hits too: gated=%d rarity_selected=%s",
			legacy.EpicGated, hellPartyRewardRarityHistogram(legacy.RaritySelected))
	}
	// rarity 0 那一档**完全不受窗口影响**（逐件相同）：窗口只管史诗。
	if wide.RaritySelected[0] != legacy.RaritySelected[0] {
		t.Fatalf("the window must not touch the non-epic rarities: default=%d legacy=%d",
			wide.RaritySelected[0], legacy.RaritySelected[0])
	}
	// 抽到 r4 的次数在两档下相同，只是默认档把其中绝大多数挡下了。
	if wide.RaritySelected[4]+wide.EpicGated != legacy.RaritySelected[4] {
		t.Fatalf("the window must not change how many r4 draws happen: default landed=%d + gated=%d vs legacy=%d",
			wide.RaritySelected[4], wide.EpicGated, legacy.RaritySelected[4])
	}
	if wide.RaritySelected[4] == 0 || wide.RaritySelected[4] >= legacy.RaritySelected[4] {
		t.Fatalf("the default window must land some, but far fewer, r4 draws: default=%d legacy=%d",
			wide.RaritySelected[4], legacy.RaritySelected[4])
	}
	t.Logf("wide set 0,4: hits=%d r0 landed=%d (identical in both tiers) r4 draws=%d → landed=%d / windowed=%d",
		wide.RarityRolls, wide.RaritySelected[0], legacy.RaritySelected[4],
		wide.RaritySelected[4], wide.EpicGated)
}

// TestAbyssEpicWindowIsInertOutsideTheWithinFixedTier 钉住**生效范围**：
// 窗口只在"修复档 ∧ 允许集合收窄 ∧ `within` 次序"下参与。其余三档
// （`post_filter` / `RARITY=vendor` / `POOL=itemdictionary`）下
// `epic_window_active=false`、`rolls_epic_window=0`，且结果与把窗口设成 `legacy` **逐字节相同**。
func TestAbyssEpicWindowIsInertOutsideTheWithinFixedTier(t *testing.T) {
	const rolls = 1024
	source := hellPartyRewardTestEquSource(hellPartyRewardRarityBandItems())
	source[dungeonDropEquipmentDictionaryPath] = "9103 1 70 0 10001 100 `itemdictionary only grade 70`"
	request := hellPartyRewardRarityRequestForTest(t, source, rolls)
	const seed = uint32(20261001)

	for _, test := range []struct {
		name         string
		order        string
		raritySwitch string
		poolSwitch   string
	}{
		{name: "post_filter order", order: hellPartyRewardRarityOrderPostFilterValue},
		{name: "vendor rarity set", order: "", raritySwitch: hellPartyRewardRarityVendorValue},
		{name: "itemdictionary pool", order: "", poolSwitch: hellPartyRewardPoolItemDictionaryValue},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(hellPartyRewardRarityOrderEnv, test.order)
			t.Setenv(hellPartyRewardRarityEnv, test.raritySwitch)
			t.Setenv(hellPartyRewardPoolEnv, test.poolSwitch)
			shipped := hellPartyRewardEpicWindowRunForTest(t, request, seed, "")
			if shipped.EpicWindowActive || shipped.EpicGated != 0 {
				t.Fatalf("%s: the window must be inert here: active=%t gated=%d %+v",
					test.name, shipped.EpicWindowActive, shipped.EpicGated, shipped)
			}
			// 窗口读数照旧带进事件（开关被写错时看得见），但**不影响任何结果**。
			if shipped.EpicWindow != abyssEpicWindowDefault ||
				shipped.EpicWindowSource != abyssEpicWindowSourceDefault {
				t.Fatalf("%s: the window readout must still be reported: %d/%q",
					test.name, shipped.EpicWindow, shipped.EpicWindowSource)
			}
			rolledBack := hellPartyRewardEpicWindowRunForTest(
				t, request, seed, abyssEpicWindowLegacyValue)
			if len(shipped.Selected) != len(rolledBack.Selected) ||
				shipped.RarityFiltered != rolledBack.RarityFiltered ||
				shipped.RarityBucketEmpty != rolledBack.RarityBucketEmpty ||
				shipped.NoDrop != rolledBack.NoDrop {
				t.Fatalf("%s: the window must be a no-op here: %+v vs %+v",
					test.name, shipped, rolledBack)
			}
			t.Setenv(hellPartyRewardPoolEnv, "")
		})
	}
}

// ── 端到端：窗口关成 0 ⇒ 一件史诗都不掉，而碎片一个不少、逐件相同 ─────────────

// abyssEpicWindowFixedTierPVF 在碎片夹具（`bridgeAbyssEpicFragmentPVF`）之上补两样东西，
// 让**修复档**真的生效、且并集里**真的有史诗候选**：
//
//  1. `etc/itemdropinfo_monster_hell.etc`：`bridgeHellPartyRewardPVF` 故意不放它
//     （⇒ 那一档是"降级成今天"，窗口不参与，所以既有的碎片用例全都不受影响）；
//  2. 把碎片成品装备之一的 `[creation rate]` 从 0 改成 100 ⇒ 它进 `.equ` 奖励池
//     （`EquipmentDropPoolProvider.cs:101` 的门槛），`[rarity] 4` ⇒ 并集非空。
func abyssEpicWindowFixedTierPVF() bridgePVFSource {
	source := bridgeAbyssEpicFragmentPVF()
	source[hellPartyRewardDropInfoPath] = hellPartyRewardTestHellDropInfoTextFor(
		hellPartyRewardTestFullRateChance)
	path := hellPartyRewardEquipmentListRoot + "/piece/450100.equ"
	source[path] = strings.Replace(source[path], "[creation rate]\n0\n", "[creation rate]\n100\n", 1)
	return source
}

// abyssEpicWindowRoomRun 打完一间深渊房的**两个波次组**（先打第 1 波那一组、再打最后一波
// 那一组），返回（概率那一支的装备行总数, 保底碎片行的 itemID 序列）。
//
// 房间掉落 LCG 的起点由用例显式钉死（`visit.DropRNG = seed`）⇒ 两次运行可以逐位对比。
func abyssEpicWindowRoomRun(
	t *testing.T,
	window string,
	seed uint32,
) (int, []uint32) {
	t.Helper()
	t.Setenv(abyssEpicWindowEnv, window)
	service, session, conn, runtime, scene, _ := prepareAbyssEpicFragmentRuntime(
		t, abyssEpicWindowFixedTierPVF())
	first, last := hellPartyRewardActorKeys(t, runtime)
	visit, err := runtime.currentDungeonRoomVisit(scene)
	if err != nil {
		t.Fatalf("room visit: %v", err)
	}
	visit.DropRNG = seed

	// 最后一波那一组的**最后一只**（房间的最后一次死亡）才是碎片保底点。
	finalKey := last[len(last)-1]
	deaths := make([]uint32, 0, len(first)+len(last))
	deaths = append(deaths, first...)
	deaths = append(deaths, last...)

	equipment := 0
	fragmentIDs := make([]uint32, 0, 8)
	for _, objectKey := range deaths {
		rows := abyssEpicFragmentDeath(t, service, session, conn, objectKey)
		for _, row := range rows {
			isFragment := false
			for _, piece := range abyssEpicFragmentTestPieces {
				if row.ItemID == piece {
					isFragment = true
					break
				}
			}
			if !isFragment {
				equipment++
				continue
			}
			if row.Amount != 1 {
				t.Fatalf("fragment row amount=%d want 1 (row=%+v)", row.Amount, row)
			}
			if objectKey != finalKey {
				t.Fatalf("a fragment row landed before the room's final wave: key=%d final=%d row=%+v",
					objectKey, finalKey, row)
			}
			fragmentIDs = append(fragmentIDs, row.ItemID)
		}
	}
	return equipment, fragmentIDs
}

// TestAbyssEpicWindowZeroStopsTheEpicsAndStillPaysTheFragmentsInEveryRoom 是操作者
// 「碎片还是每次都要有」+「4-5 个房间 1 件史诗」两句原话的**端到端**钉子：
//
//	窗口 = 0    ⇒ 概率那一支**一件史诗都发不出**，而那一间房的碎片**必须**照发；
//	窗口 = 默认 ⇒ 落地件数远少于"每一次命中都落地"；
//	窗口 = 今天 ⇒ 每一次命中都落地（回退档的语义）；
//	三档的**碎片 id 序列逐件相同** ⇒ 史诗窗口一个随机数都没动（碎片保底用的是同一条 LCG）。
func TestAbyssEpicWindowZeroStopsTheEpicsAndStillPaysTheFragmentsInEveryRoom(t *testing.T) {
	const seed = uint32(0x5eed1234)
	t.Setenv(hellPartyRewardRarityEnv, "")
	t.Setenv(hellPartyRewardRarityOrderEnv, "")
	t.Setenv(abyssEpicFragmentEnv, abyssEpicFragmentValueGuaranteed)

	zeroEquipment, zeroFragments := abyssEpicWindowRoomRun(t, "0", seed)
	if zeroEquipment != 0 {
		t.Fatalf("window 0 must not land a single epic, got %d equipment rows", zeroEquipment)
	}
	if len(zeroFragments) != abyssEpicFragmentTestDefaultAtDifficultyOne {
		t.Fatalf("碎片的保底必须完全不受史诗窗口影响: fragments=%d want %d (ids=%v)",
			len(zeroFragments), abyssEpicFragmentTestDefaultAtDifficultyOne, zeroFragments)
	}

	legacyEquipment, legacyFragments := abyssEpicWindowRoomRun(
		t, abyssEpicWindowLegacyValue, seed)
	if legacyEquipment == 0 {
		t.Fatal("the rollback literal must still pay every hit")
	}
	shippedEquipment, shippedFragments := abyssEpicWindowRoomRun(t, "", seed)
	if shippedEquipment >= legacyEquipment {
		t.Fatalf("the shipped window must land far fewer epics than the rollback literal: shipped=%d legacy=%d",
			shippedEquipment, legacyEquipment)
	}

	// 三个档位下的碎片逐件相同（这是"窗口不多抽也不少抽一个随机数"的端到端证据）。
	for index := range zeroFragments {
		if legacyFragments[index] != zeroFragments[index] || shippedFragments[index] != zeroFragments[index] {
			t.Fatalf("the epic window changed the fragment draw: zero=%v legacy=%v shipped=%v",
				zeroFragments, legacyFragments, shippedFragments)
		}
	}
	if len(legacyFragments) != len(zeroFragments) || len(shippedFragments) != len(zeroFragments) {
		t.Fatalf("fragment counts differ: zero=%d legacy=%d shipped=%d",
			len(zeroFragments), len(legacyFragments), len(shippedFragments))
	}
	t.Logf("one abyss room: window0 epics=%d / default epics=%d / legacy epics=%d; fragments=%d identical ids=%v",
		zeroEquipment, shippedEquipment, legacyEquipment, len(zeroFragments), zeroFragments)
}

// TestAbyssEpicWindowReadoutsReachTheRollEventAndTheStartupRecord 钉住"看得见"：
// 投骰事件带 7 个窗口字段、启动期激活记录带 3 个 —— 线上排障只认这几行。
func TestAbyssEpicWindowReadoutsReachTheRollEventAndTheStartupRecord(t *testing.T) {
	// ① 启动期：`dnf-dungeon-hell-party-reward-pool-ready`。
	t.Setenv(abyssEpicWindowEnv, "")
	monsterCatalog := hellPartyRewardReadyTestSyntheticCatalog(t)
	ready := hellPartyRewardPoolReadinessFor(monsterCatalog)
	pairs := hellPartyRewardEventPairs(t, hellPartyRewardPoolReadyEvent, ready.fields())
	hellPartyRewardEventRequireKeys(t, hellPartyRewardPoolReadyEvent, pairs,
		"epic_window", "epic_window_switch", "epic_window_source")
	if pairs["epic_window"] != strconv.FormatInt(abyssEpicWindowDefault, 10) ||
		pairs["epic_window_switch"] != abyssEpicWindowUnsetText ||
		pairs["epic_window_source"] != abyssEpicWindowSourceDefault {
		t.Fatalf("startup window readout = %q/%q/%q want %d/%s/%s",
			pairs["epic_window"], pairs["epic_window_switch"], pairs["epic_window_source"],
			abyssEpicWindowDefault, abyssEpicWindowUnsetText, abyssEpicWindowSourceDefault)
	}
	t.Setenv(abyssEpicWindowEnv, abyssEpicWindowLegacyValue)
	rollbackReady := hellPartyRewardPoolReadinessFor(monsterCatalog)
	rollbackPairs := hellPartyRewardEventPairs(t, hellPartyRewardPoolReadyEvent, rollbackReady.fields())
	if rollbackPairs["epic_window"] != strconv.FormatInt(abyssEpicWindowAlways, 10) ||
		rollbackPairs["epic_window_switch"] != abyssEpicWindowLegacyValue ||
		rollbackPairs["epic_window_source"] != "alias:"+abyssEpicWindowLegacyValue {
		t.Fatalf("rolled-back startup window readout = %q/%q/%q",
			rollbackPairs["epic_window"], rollbackPairs["epic_window_switch"],
			rollbackPairs["epic_window_source"])
	}
	t.Setenv(abyssEpicWindowEnv, "")

	// ② 投骰事件：默认档与回退档各打一行，逐字段检查。
	_, session, _, runtime, scene, _ := prepareHellPartyRewardRuntime(t)
	_ = session
	source := hellPartyRewardTestEquSource(hellPartyRewardRarityBandItems())
	request := hellPartyRewardRarityRequestForTest(t, source, 1024)
	_, last := hellPartyRewardActorKeys(t, runtime)
	objectKey := last[0]

	shipped := hellPartyRewardEpicWindowRunForTest(t, request, 20261006, "")
	shippedFields := hellPartyRewardRollFields(
		runtime, scene, objectKey, request, shipped, len(shipped.Selected), "test")
	shippedPairs := hellPartyRewardEventPairs(t, hellPartyRewardRollEvent, shippedFields)
	hellPartyRewardEventRequireKeys(t, hellPartyRewardRollEvent, shippedPairs,
		"epic_window", "epic_window_denominator", "epic_window_switch", "epic_window_source",
		"epic_window_active", "rolls_epic_window", "epic_expectation_ppm")
	if shippedPairs["epic_window_active"] != "true" ||
		shippedPairs["epic_window"] != strconv.FormatInt(abyssEpicWindowDefault, 10) ||
		shippedPairs["epic_window_denominator"] != strconv.FormatInt(abyssEpicWindowBound, 10) {
		t.Fatalf("shipped roll window fields = %v", shippedPairs)
	}

	rolledBack := hellPartyRewardEpicWindowRunForTest(
		t, request, 20261006, abyssEpicWindowLegacyValue)
	rollbackFields := hellPartyRewardRollFields(
		runtime, scene, objectKey, request, rolledBack, len(rolledBack.Selected), "test")
	rollbackRollPairs := hellPartyRewardEventPairs(t, hellPartyRewardRollEvent, rollbackFields)
	if rollbackRollPairs["epic_window_active"] != "false" ||
		rollbackRollPairs["rolls_epic_window"] != "0" ||
		rollbackRollPairs["epic_window_switch"] != abyssEpicWindowLegacyValue ||
		rollbackRollPairs["epic_window_source"] != "alias:"+abyssEpicWindowLegacyValue ||
		rollbackRollPairs["epic_expectation_ppm"] != "0" {
		t.Fatalf("rolled-back roll window fields = %v", rollbackRollPairs)
	}
	t.Logf("startup: EVENT kind=%s %s", hellPartyRewardPoolReadyEvent,
		hellPartyRewardPoolReadyRenderedLine(rollbackReady))
}
