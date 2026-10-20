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

// 本文件钉住「深渊奖励取池口径」修复（`dungeon_hell_party_reward_pool.go`）：
//
//  1. 开关语义：默认 = `equ`（修复档），唯一回退字面量 = `itemdictionary`（= 今天）。
//  2. hell 概率表（`etc/itemdropinfo_monster_hell.etc`）解析出的 `rate` 按**副本难度**取列，
//     而不是普通怪表 `Etc/ItemDropInfo_Monseter.etc`（D2/D3）。
//  3. 等级区间 = 厂商 `EquipmentDropLevelRule`（`[min(minLv,basis), basis+7]` 闭区间，D4）。
//  4. `equ` 档从 `.equ` 池取件（D1），回退档逐字节走今天的 `SelectGenericEquipment`。
//  5. 真档（`DNFBRIDGE_REAL_PVF_SMOKE` 门控，与 `TestRealPVFHellPartyRewardRewardTablesExist`
//     同一写法）上：候选数、选中件的 `[grade]`、以及**归档缓存保持 0 增长**（内存有界）。

// hellPartyRewardTestHellDropInfoText 是真档 `etc/itemdropinfo_monster_hell.etc` 的原文
// （线上 sha256 afa00e75… 那份，2000 B 的 dump 逐字抄录；三段齐全）。
const hellPartyRewardTestHellDropInfoText = "[drop prob count]\n" +
	"2 \n" +
	"[dungeon difficulty drop prob]\n" +
	"1 44 0 0 0 0 0 45 200 335 875 1000 1400 1000 \n" +
	"[basis of rarity dicision]\n" +
	"2 0 599000 993000 993000 1000000 1000001 1000001 0 800000 1000000 1000000 1000001 1000002 1000001 \n" +
	"[item drop ref table]\n" +
	"1 0 3 2 1 3 3 2 3 4 3 3 5 3 3 6 3 3 7 3 3 8 4 3 9 4 3 10 5 3 11 5 3 12 6 3 13 7 3 " +
	"87 6 2 88 6 1 89 6 0 90 6 3 91 6 3 92 7 2 93 8 1 94 6 3 95 6 3 96 6 3 97 6 3 98 6 3 \n" +
	"[item drop rarity control]\n" +
	"2 13 17 0 -5 \n" +
	"[/item drop rarity control]\n"

// hellPartyRewardTestMonsterDropInfoText 是普通怪表 `Etc/ItemDropInfo_Monseter.etc` 的同形段
// （只有 `[drop prob]`，**没有** `[dungeon difficulty drop prob]`）——修复档必须读不到它。
const hellPartyRewardTestMonsterDropInfoText = "[drop prob count]\n" +
	"1\n" +
	"[drop prob]\n" +
	"81 200 750 10 135 8 5\n" +
	"[/drop prob]\n"

// hellPartyRewardTestHellDropInfoTextFor 造 hell 概率表的同形替身：两行（1-44 / 45-200），
// 五档 rate 全给 rate；稀有度两行全 1000000（⇒ rarity 恒 0，与 fixture 池的 rarity 0 对齐）。
func hellPartyRewardTestHellDropInfoTextFor(rate int64) string {
	row := "45 200 " + strings.TrimSpace(strings.Repeat(strconv.FormatInt(rate, 10)+" ", 5))
	return "[drop prob count]\n2\n" +
		"[dungeon difficulty drop prob]\n" +
		"1 44 0 0 0 0 0\n" + row + "\n" +
		"[basis of rarity dicision]\n" +
		"2 1000000 1000000 1000000 1000000 1000000 1000000 1000000 " +
		"1000000 1000000 1000000 1000000 1000000 1000000 1000000\n" +
		"[item drop ref table]\n" +
		strconv.FormatInt(hellPartyRewardTestMinimumLevel, 10) + " 1 1 " +
		strconv.FormatInt(hellPartyRewardTestBasisLevel, 10) + " 1 1\n"
}

// hellPartyRewardTestHellDropInfoRealRatesText 用**真档那一行**的五档（覆盖 45..200）：
// 用来钉住"rate 按副本难度取列"（335/875/1000/1400/1000）。
func hellPartyRewardTestHellDropInfoRealRatesText() string {
	return "[drop prob count]\n2\n" +
		"[dungeon difficulty drop prob]\n" +
		"1 44 0 0 0 0 0\n" +
		"45 200 335 875 1000 1400 1000\n" +
		"[basis of rarity dicision]\n" +
		"2 0 599000 993000 993000 1000000 1000001 1000001 " +
		"0 800000 1000000 1000000 1000001 1000002 1000001\n" +
		"[item drop ref table]\n" +
		strconv.FormatInt(hellPartyRewardTestMinimumLevel, 10) + " 1 1 " +
		strconv.FormatInt(hellPartyRewardTestBasisLevel, 10) + " 1 1\n"
}

// invalidateHellPartyRewardDropInfoCache 清掉 hell 概率表的按 catalog 缓存
// （用例改写 source 之后必须调用，否则会读到改写前的那份表）。
func invalidateHellPartyRewardDropInfoCache(t *testing.T, service *Service) {
	t.Helper()
	if service == nil || service.dungeonMonsterTable == nil {
		return
	}
	catalog, err := service.dungeonMonsterTable.DropCatalog()
	if err != nil {
		t.Fatalf("drop catalog: %v", err)
	}
	catalog.mu.Lock()
	catalog.hellPartyRewardDropInfoLoaded = false
	catalog.hellPartyRewardDropInfoValue = hellPartyRewardDropInfo{}
	catalog.hellPartyRewardDropInfoErr = nil
	catalog.mu.Unlock()
}

// hellPartyRewardTestFixedTier / hellPartyRewardTestLegacyTier 是接入点要的档位裁定。
func hellPartyRewardTestFixedTier() hellPartyRewardTierDecision {
	return hellPartyRewardTierDecision{UseFixedTier: true, RateSource: hellPartyRewardDropInfoPath}
}

func hellPartyRewardTestLegacyTier() hellPartyRewardTierDecision {
	return hellPartyRewardTierDecision{RateSource: hellPartyRewardLegacyRateSource}
}

// hellPartyRewardTestEquSource 造一棵最小 `.equ` 树：
// `equipment/equipment.lst` + 每件 `<id> \`path\“ 的 `.equ`，覆盖给定 (grade, rarity, rate)。
func hellPartyRewardTestEquSource(items []struct {
	ID     int64
	Grade  int64
	Rarity int64
	Rate   int64
}) bridgePVFSource {
	source := bridgePVFSource{
		// pvfDungeonDropCatalog 构造要读的三张清单。
		"monster/monster.lst":       "",
		dungeonDropStackableList:    "",
		dungeonDropEquipmentList:    "",
		hellPartyRewardDropInfoPath: hellPartyRewardTestHellDropInfoText,
		dnfdrops.MonsterRulePath:    hellPartyRewardTestRuleText(1000),
	}
	list := make([]string, 0, len(items))
	for _, item := range items {
		path := "test/g" + strconv.FormatInt(item.Grade, 10) + ".equ"
		list = append(list, strconv.FormatInt(item.ID, 10)+" `"+path+"`")
		source["equipment/"+path] = "[name]\n`test grade " + strconv.FormatInt(item.Grade, 10) + "`\n" +
			"[equipment type]\n`[weapon]`\n" +
			"[grade]\n" + strconv.FormatInt(item.Grade, 10) + "\n" +
			"[rarity]\n" + strconv.FormatInt(item.Rarity, 10) + "\n" +
			"[creation rate]\n" + strconv.FormatInt(item.Rate, 10) + "\n"
	}
	source[dungeonDropEquipmentList] = strings.Join(list, " ")
	return source
}

// TestHellPartyRewardEntryPointsSplitTiersBySwitch 钉住 reward 路径的三个接入点：
// 同一个 request，`equ` 档读 hell 表 + `.equ` 池，`itemdictionary` 档逐字节回到今天。
func TestHellPartyRewardEntryPointsSplitTiersBySwitch(t *testing.T) {
	items := []struct {
		ID     int64
		Grade  int64
		Rarity int64
		Rate   int64
	}{
		{ID: 9101, Grade: 90, Rarity: 1, Rate: 100},
		{ID: 9102, Grade: 96, Rarity: 1, Rate: 250},
	}
	source := hellPartyRewardTestEquSource(items)
	source[dungeonDropEquipmentDictionaryPath] = "9103 1 70 0 10001 100 `itemdictionary only grade 70`"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatalf("build synthetic drop catalog: %v", err)
	}
	monsterCatalog := &pvfDungeonMonsterCatalog{source: source}
	rules, err := currentDungeonMonsterDropRules(monsterCatalog)
	if err != nil {
		t.Fatalf("synthetic %s is unusable: %v", dnfdrops.MonsterRulePath, err)
	}
	request := hellPartyRewardRequest{
		DungeonMinimumLevel:  87,
		DungeonBasisLevel:    90,
		AbyssPartyDifficulty: 1,
		RewardRollCount:      8,
		dropCatalog:          catalog,
		rules:                rules,
		rng:                  newCurrentDungeonDropLCG(4242),
	}

	// 修复档：rate 来自 hell 表按**副本难度**取列（覆盖 90 级那行 `45 200 335 875 1000 1400 1000`）。
	t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolEquValue)
	decision := request.hellPartyRewardTier(1)
	if !decision.UseFixedTier || !decision.RateFound || decision.Rate != 875 ||
		decision.RateSource != hellPartyRewardDropInfoPath || decision.Degraded != "" {
		t.Fatalf("fixed tier decision=%+v want rate=875 source=%s without degradation",
			decision, hellPartyRewardDropInfoPath)
	}
	if other := request.hellPartyRewardTier(0); !other.RateFound || other.Rate != 335 {
		t.Fatalf("fixed tier difficulty 0 decision=%+v want rate=335", other)
	}
	request.rng = newCurrentDungeonDropLCG(4242)
	rarity, raritySource, err := request.hellPartyRewardRarity(decision)
	if err != nil {
		t.Fatalf("fixed tier rarity: %v", err)
	}
	if raritySource != hellPartyRewardDropInfoPath {
		t.Fatalf("fixed tier rarity source=%q", raritySource)
	}
	// A 档 `[basis of rarity dicision]` 行 1 = `0 599000 993000 993000 1000000 …`：
	// 只可能落到 rarity 1 / 2 / 4。
	if rarity != 1 && rarity != 2 && rarity != 4 {
		t.Fatalf("fixed tier rarity=%d want one of 1/2/4", rarity)
	}
	// `hellPartyRewardEquipment` 的第五个参数是稀有度闸门的"要不要保留厂商 rarity 0 回落"
	// （`allowCommonFallback`）；这一档钉的是厂商口径 ⇒ true。
	selection, err := request.hellPartyRewardEquipment(decision, rarity, 6, 6, true)
	if err != nil {
		t.Fatalf("fixed tier equipment: %v", err)
	}
	if !selection.Selected || selection.Pool != hellPartyRewardPoolEquValue || selection.Degraded != "" {
		t.Fatalf("fixed tier equipment=%+v want a selected .equ pool item", selection)
	}
	if selection.GradeMin != 87 || selection.GradeMax != 97 {
		t.Fatalf("fixed tier band=%d..%d want 87..97", selection.GradeMin, selection.GradeMax)
	}

	// 回退档（用户显式选择）：rate 交回调用方（今天的 monster 规则表），稀有度与取件逐字节回到今天
	// （ItemDictionary 池在 [84,96) 区间没有候选 ⇒ 不选中）；显式回退**不是**降级。
	t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolItemDictionaryValue)
	legacyDecision := request.hellPartyRewardTier(1)
	if legacyDecision.UseFixedTier || legacyDecision.RateFound ||
		legacyDecision.RateSource != hellPartyRewardLegacyRateSource || legacyDecision.Degraded != "" {
		t.Fatalf("rollback tier decision=%+v want todays table without degradation", legacyDecision)
	}
	request.rng = newCurrentDungeonDropLCG(4242)
	legacyRarity, legacyRaritySource, err := request.hellPartyRewardRarity(legacyDecision)
	if err != nil {
		t.Fatalf("rollback tier rarity: %v", err)
	}
	if legacyRaritySource != hellPartyRewardLegacyRateSource || legacyRarity != 0 {
		t.Fatalf("rollback tier rarity=%d source=%q want 0 / %s（fixture 四行全 1000000）",
			legacyRarity, legacyRaritySource, hellPartyRewardLegacyRateSource)
	}
	request.rng = newCurrentDungeonDropLCG(9)
	legacySelection, err := request.hellPartyRewardEquipment(legacyDecision, 1, 6, 6, true)
	if err != nil {
		t.Fatalf("rollback tier equipment: %v", err)
	}
	if legacySelection.Selected || legacySelection.ItemID != 0 ||
		legacySelection.Pool != hellPartyRewardPoolItemDictionaryValue {
		t.Fatalf("rollback tier equipment=%+v want today's empty ItemDictionary pool", legacySelection)
	}
}

// TestHellPartyRewardTierDegradesToTodaysReadingAndSaysWhy 钉住**降级**语义：
// 修复档下 hell 表读不到 / 没有覆盖行 / 池子读不到时，`hellPartyRewardTier` 不得报错，
// 而是退回今天（`UseFixedTier=false` + `Degraded` 非空，供事件与每会话一条的降级事件）；
// 用户显式回退档（`itemdictionary`）**不算降级**（`Degraded` 必须为空）。
func TestHellPartyRewardTierDegradesToTodaysReadingAndSaysWhy(t *testing.T) {
	items := []struct {
		ID     int64
		Grade  int64
		Rarity int64
		Rate   int64
	}{
		{ID: 9201, Grade: 90, Rarity: 0, Rate: 100},
	}

	t.Run("hell table missing degrades to todays reading", func(t *testing.T) {
		source := hellPartyRewardTestEquSource(items)
		delete(source, hellPartyRewardDropInfoPath)
		catalog, err := newPVFDungeonDropCatalog(source)
		if err != nil {
			t.Fatalf("build catalog: %v", err)
		}
		request := hellPartyRewardRequest{DungeonBasisLevel: 90, DungeonMinimumLevel: 87, dropCatalog: catalog}
		t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolEquValue)
		decision := request.hellPartyRewardTier(1)
		if decision.UseFixedTier || decision.RateFound {
			t.Fatalf("missing hell table must degrade: %+v", decision)
		}
		if !strings.Contains(decision.Degraded, hellPartyRewardDropInfoPath) {
			t.Fatalf("degraded reason=%q must name %s", decision.Degraded, hellPartyRewardDropInfoPath)
		}
		if decision.RateSource != hellPartyRewardLegacyRateSource {
			t.Fatalf("degraded rate source=%q want %q", decision.RateSource, hellPartyRewardLegacyRateSource)
		}
		if decision.PoolName() != hellPartyRewardPoolItemDictionaryValue {
			t.Fatalf("degraded pool=%q want %q", decision.PoolName(), hellPartyRewardPoolItemDictionaryValue)
		}
		// 降级后稀有度/取件也必须逐字节走今天（不报错）。
		request.rng = newCurrentDungeonDropLCG(31)
		rarity, source_, err := request.hellPartyRewardRarity(decision)
		if err != nil || rarity != 0 || source_ != hellPartyRewardLegacyRateSource {
			t.Fatalf("degraded rarity=%d source=%q err=%v want 0 / %s", rarity, source_, err, hellPartyRewardLegacyRateSource)
		}
	})

	t.Run("rate row missing for the level degrades and names the level", func(t *testing.T) {
		source := hellPartyRewardTestEquSource(items)
		// 让两行都不覆盖 90 级。
		source[hellPartyRewardDropInfoPath] = strings.Replace(
			source[hellPartyRewardDropInfoPath], "45 200", "45 89", 1)
		catalog, err := newPVFDungeonDropCatalog(source)
		if err != nil {
			t.Fatalf("build catalog: %v", err)
		}
		request := hellPartyRewardRequest{DungeonBasisLevel: 90, DungeonMinimumLevel: 87, dropCatalog: catalog}
		t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolEquValue)
		decision := request.hellPartyRewardTier(1)
		if decision.UseFixedTier || decision.RateFound {
			t.Fatalf("uncovered level must degrade: %+v", decision)
		}
		if !strings.Contains(decision.Degraded, "basis_level=90") {
			t.Fatalf("degraded reason=%q must name the basis level", decision.Degraded)
		}
	})

	t.Run("equipment list missing degrades the whole tier", func(t *testing.T) {
		source := hellPartyRewardTestEquSource(items)
		delete(source, dungeonDropEquipmentList)
		catalog, err := newPVFDungeonDropCatalog(source)
		if err != nil {
			// 目录构造本身就会拒绝没有 equipment.lst 的源，这也是可接受的收口。
			return
		}
		request := hellPartyRewardRequest{DungeonBasisLevel: 90, DungeonMinimumLevel: 87, dropCatalog: catalog}
		t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolEquValue)
		decision := request.hellPartyRewardTier(1)
		if decision.UseFixedTier || decision.Degraded == "" {
			t.Fatalf("missing .equ list must degrade: %+v", decision)
		}
	})

	t.Run("explicit rollback tier is not a degradation", func(t *testing.T) {
		source := hellPartyRewardTestEquSource(items)
		catalog, err := newPVFDungeonDropCatalog(source)
		if err != nil {
			t.Fatalf("build catalog: %v", err)
		}
		request := hellPartyRewardRequest{DungeonBasisLevel: 90, DungeonMinimumLevel: 87, dropCatalog: catalog}
		t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolItemDictionaryValue)
		decision := request.hellPartyRewardTier(1)
		if decision.UseFixedTier || decision.Degraded != "" {
			t.Fatalf("explicit rollback must not be reported as degraded: %+v", decision)
		}
	})
}

// TestHellPartyRewardPoolSwitchDefaultsToEquAndOnlyItemDictionaryRollsBack 钉住开关语义
// （默认 = 修复档；只有 `itemdictionary` 大小写不敏感 + 允许首尾空白时才是回退档）。
func TestHellPartyRewardPoolSwitchDefaultsToEquAndOnlyItemDictionaryRollsBack(t *testing.T) {
	for _, test := range []struct {
		value string
		want  hellPartyRewardPoolMode
	}{
		{value: "", want: hellPartyRewardPoolEqu},
		{value: "   ", want: hellPartyRewardPoolEqu},
		{value: "equ", want: hellPartyRewardPoolEqu},
		{value: "EQU", want: hellPartyRewardPoolEqu},
		{value: "vendor", want: hellPartyRewardPoolEqu},
		{value: "ItemDictionary", want: hellPartyRewardPoolItemDictionary},
		{value: " itemdictionary ", want: hellPartyRewardPoolItemDictionary},
	} {
		if got := readHellPartyRewardPoolMode(test.value); got != test.want {
			t.Fatalf("readHellPartyRewardPoolMode(%q)=%v want %v", test.value, got, test.want)
		}
	}
	if got := hellPartyRewardPoolEqu.String(); got != hellPartyRewardPoolEquValue {
		t.Fatalf("equ tier event value=%q want %q", got, hellPartyRewardPoolEquValue)
	}
	if got := hellPartyRewardPoolItemDictionary.String(); got != hellPartyRewardPoolItemDictionaryValue {
		t.Fatalf("rollback tier event value=%q want %q", got, hellPartyRewardPoolItemDictionaryValue)
	}
	// 开关名字与字面量是线上可判据，钉死。
	if hellPartyRewardPoolEnv != "DNFBRIDGE_HELL_PARTY_REWARD_POOL" {
		t.Fatalf("switch env=%q", hellPartyRewardPoolEnv)
	}
	if hellPartyRewardPoolItemDictionaryValue != "itemdictionary" {
		t.Fatalf("rollback literal=%q", hellPartyRewardPoolItemDictionaryValue)
	}
}

// TestHellPartyRewardGradeRangeMirrorsVendorEquipmentDropLevelRule 钉住 D4：
// 厂商 `EquipmentDropLevelRule.cs:7-30` 的闭区间与 0/交换语义。
func TestHellPartyRewardGradeRangeMirrorsVendorEquipmentDropLevelRule(t *testing.T) {
	for _, test := range []struct {
		name     string
		minimum  int64
		basis    int64
		wantMin  int64
		wantMax  int64
		wantOkay bool
	}{
		{name: "dungeon_310_real_min10_basis90", minimum: 10, basis: 90, wantMin: 10, wantMax: 97, wantOkay: true},
		{name: "swaps_inverted_pair", minimum: 90, basis: 10, wantMin: 10, wantMax: 97, wantOkay: true},
		{name: "minimum_zero_falls_back_to_basis", minimum: 0, basis: 90, wantMin: 90, wantMax: 97, wantOkay: true},
		{name: "basis_zero_falls_back_to_minimum", minimum: 90, basis: 0, wantMin: 90, wantMax: 97, wantOkay: true},
		{name: "grade_max_clamps_at_200", minimum: 150, basis: 200, wantMin: 150, wantMax: 200, wantOkay: true},
		{name: "both_zero_is_unusable", minimum: 0, basis: 0, wantOkay: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			minimum, maximum, okay := hellPartyRewardGradeRangeForLevels(test.minimum, test.basis)
			if okay != test.wantOkay || minimum != test.wantMin || maximum != test.wantMax {
				t.Fatalf("range(%d,%d)=[%d,%d] okay=%t want [%d,%d] okay=%t",
					test.minimum, test.basis, minimum, maximum, okay,
					test.wantMin, test.wantMax, test.wantOkay)
			}
		})
	}
}

// TestHellPartyRewardDropInfoReadsTheHellTableNotTheMonsterTable 钉住 D2/D3：
// rate 来自 hell 表按**副本难度**取列；普通怪表那份文本在修复档下取不到任何 rate。
func TestHellPartyRewardDropInfoReadsTheHellTableNotTheMonsterTable(t *testing.T) {
	info := parseHellPartyRewardDropInfo(hellPartyRewardTestHellDropInfoText)
	if len(info.Probabilities) != 2 {
		t.Fatalf("hell [dungeon difficulty drop prob] rows=%d want 2", len(info.Probabilities))
	}
	// 覆盖 90 级那一行是 `45 200 335 875 1000 1400 1000`。
	for _, test := range []struct {
		difficulty int64
		wantRate   int64
	}{
		{difficulty: 0, wantRate: 335},
		{difficulty: 1, wantRate: 875}, // 操作者现场 = difficulty 1
		{difficulty: 2, wantRate: 1000},
		{difficulty: 3, wantRate: 1400},
		{difficulty: 4, wantRate: 1000},
		{difficulty: 9, wantRate: 1000}, // 越界夹到最后一位（厂商 `:45` clamp）
	} {
		rate, found := info.rateForLevel(90, test.difficulty)
		if !found || rate != test.wantRate {
			t.Fatalf("rateForLevel(90,%d)=%d found=%t want %d", test.difficulty, rate, found, test.wantRate)
		}
	}
	// 覆盖 90 级的是第二行；第一行只覆盖 1..44。
	if _, found := info.rateForLevel(44, 1); !found {
		t.Fatal("row 1 must cover level 44")
	}
	if _, found := info.rateForLevel(45, 1); !found {
		t.Fatal("row 2 must cover level 45")
	}
	if _, found := info.rateForLevel(201, 1); found {
		t.Fatal("level 201 must not be covered")
	}
	// 稀有度阈值：A 档（hellDifficulty=1）行 `0 599000 993000 993000 1000000 …`。
	for _, test := range []struct {
		roll       int64
		wantRarity int64
	}{
		{roll: 1, wantRarity: 1},
		{roll: 599000, wantRarity: 1},
		{roll: 599001, wantRarity: 2},
		{roll: 993000, wantRarity: 2},
		{roll: 993001, wantRarity: 4},
		{roll: 1000000, wantRarity: 4},
	} {
		if got := info.rarityForRoll(1, test.roll); got != test.wantRarity {
			t.Fatalf("rarityForRoll(1,%d)=%d want %d", test.roll, got, test.wantRarity)
		}
	}

	// 普通怪表：没有 `[dungeon difficulty drop prob]` ⇒ 修复档在它身上取不到 rate（=
	// 今天那个 `10` 在新档下不可达）。
	monster := parseHellPartyRewardDropInfo(hellPartyRewardTestMonsterDropInfoText)
	if len(monster.Probabilities) != 0 {
		t.Fatalf("monster table must not provide hell probabilities, got %d rows", len(monster.Probabilities))
	}
	if rate, found := monster.rateForLevel(90, 1); found {
		t.Fatalf("monster table rateForLevel(90,1)=%d found=true; the legacy 10 must be unreachable", rate)
	}
	// 反过来：hell 表也没有 `[drop prob]`（两份表不是同一张）。
	if _, found := hellPartyRewardSectionInts(hellPartyRewardTestHellDropInfoText, "drop prob"); found {
		t.Fatal("hell table must not expose the monster table's [drop prob] section")
	}
}

// TestHellPartyRewardPoolSelectionSplitsEquFromRollbackTier 钉住 D1：
// `equ` 档从 `.equ` 池取件；回退档必须逐字节等于今天的 `SelectGenericEquipment`。
func TestHellPartyRewardPoolSelectionSplitsEquFromRollbackTier(t *testing.T) {
	items := []struct {
		ID     int64
		Grade  int64
		Rarity int64
		Rate   int64
	}{
		{ID: 9001, Grade: 96, Rarity: 0, Rate: 100}, // 池内（vendor 档 [10,97]）
		{ID: 9002, Grade: 95, Rarity: 2, Rate: 250}, // 池内
		{ID: 9003, Grade: 90, Rarity: 4, Rate: 50},  // 池内
		{ID: 9004, Grade: 60, Rarity: 0, Rate: 100}, // 池内但不在 90 级区间的高段
		{ID: 9005, Grade: 89, Rarity: 0, Rate: 0},   // creation rate <= 0 ⇒ 必须被门槛丢掉
		{ID: 9006, Grade: 80, Rarity: 6, Rate: 100}, // rarity > 5 ⇒ 必须被门槛丢掉
	}
	source := hellPartyRewardTestEquSource(items)
	// 9007 只存在于 ItemDictionary 池、**不在** `equipment.lst` 里：这正是今天那张
	// "只到 grade 70" 的表的代表行，也因此它不会进 `.equ` 池。
	// ItemDictionary 池里只放一件 grade 70 的（= 今天真档的口径：只到 70）。
	source[dungeonDropEquipmentDictionaryPath] =
		"9007 0 70 0 10001 100 `itemdictionary only grade 70`"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatalf("build synthetic drop catalog: %v", err)
	}
	pool, err := catalog.hellPartyRewardEquipmentPool()
	if err != nil {
		t.Fatalf("build synthetic .equ pool: %v", err)
	}
	if pool.Items != 4 {
		t.Fatalf(".equ pool items=%d want 4 (rate<=0 / rarity>5 must be rejected)", pool.Items)
	}
	if pool.MaxGrade != 96 {
		t.Fatalf(".equ pool max grade=%d want 96", pool.MaxGrade)
	}

	// equ 档：闭区间 [10,97]，rarity 0 → 候选 = grade 96 + grade 60 两件（按 grade 升序）。
	candidates := pool.candidates(10, 97, 0)
	if len(candidates) != 2 || candidates[0].ItemID != 9004 || candidates[1].ItemID != 9001 {
		t.Fatalf("equ candidates(10,97,0)=%+v want [9004 9001]（grade 升序，厂商 CollectWeightedFromPool 顺序）",
			candidates)
	}
	selection, err := catalog.hellPartyRewardEquipmentSelectionFor(
		hellPartyRewardTestFixedTier(), newCurrentDungeonDropLCG(7), 90, 6, 6, 0, 10, 90, true)
	if err != nil {
		t.Fatalf("equ tier selection: %v", err)
	}
	if selection.Pool != hellPartyRewardPoolEquValue || !selection.Selected ||
		(selection.ItemID != 9001 && selection.ItemID != 9004) {
		t.Fatalf("equ tier selection=%+v want a pool item from the .equ pool", selection)
	}
	if selection.GradeMin != 10 || selection.GradeMax != 97 || selection.CandidateCount != 2 {
		t.Fatalf("equ tier band=%d..%d candidates=%d want 10..97 / 2",
			selection.GradeMin, selection.GradeMax, selection.CandidateCount)
	}
	// 高段 rarity 2：vendor 档内只有 grade 95 那一件。
	gradeMin, gradeMax, _ := hellPartyRewardGradeRangeForLevels(10, 90)
	selection, err = catalog.hellPartyRewardEquipmentSelectionFor(
		hellPartyRewardTestFixedTier(), newCurrentDungeonDropLCG(11), 90, 6, 6, 2, 10, 90, true)
	if err != nil {
		t.Fatalf("equ tier rarity2 selection: %v", err)
	}
	if !selection.Selected || selection.ItemID != 9002 {
		t.Fatalf("equ tier rarity2 selection=%+v want item 9002 (range %d..%d)",
			selection, gradeMin, gradeMax)
	}

	// 回退档：逐字节等于今天的 `SelectGenericEquipment`（ItemDictionary 池 + 半开区间）。
	legacyRNG := newCurrentDungeonDropLCG(23)
	legacy, err := catalog.hellPartyRewardEquipmentSelectionFor(
		hellPartyRewardTestLegacyTier(), legacyRNG, 90, 6, 6, 0, 10, 90, true)
	if err != nil {
		t.Fatalf("rollback tier selection: %v", err)
	}
	if legacy.Pool != hellPartyRewardPoolItemDictionaryValue {
		t.Fatalf("rollback tier pool=%q", legacy.Pool)
	}
	// 真档 310 的旧区间 [84,96)：ItemDictionary 池只到 70 ⇒ 没有候选（= 今天的 "no equipment"）。
	if legacy.Selected || legacy.ItemID != 0 || legacy.CandidateCount != 0 {
		t.Fatalf("rollback tier must reproduce today's empty pool, got %+v", legacy)
	}
	directRNG := newCurrentDungeonDropLCG(23)
	directID, directSelected, err := catalog.SelectGenericEquipment(directRNG, 90, 6, 6, 0)
	if err != nil {
		t.Fatalf("direct SelectGenericEquipment: %v", err)
	}
	if directSelected != legacy.Selected || directID != legacy.ItemID {
		t.Fatalf("rollback tier diverged from SelectGenericEquipment: %d/%t vs %d/%t",
			legacy.ItemID, legacy.Selected, directID, directSelected)
	}
}

// TestRealPVFHellPartyRewardEquipmentPoolCoversDungeon310 是真档用例
// （`DNFBRIDGE_REAL_PVF_SMOKE` 门控，写法照 `TestRealPVFHellPartyRewardRewardTablesExist`）：
//   - 厂商 `.equ` 池在真档上的规模与最高 grade；
//   - 副本 310 的**厂商闭区间**里每个会被抽到的 rarity 都有非零候选（池覆盖 D1/D4）；
//   - 选中件的 `[grade]` 落在该区间内；
//   - 整轮扫描**不填归档缓存**（内存有界：`cached_texts`/`cached_chunks` 不增长）；
//   - 回退档在同一输入上仍然选不出东西（今天的缺口被钉成回归）。
func TestRealPVFHellPartyRewardEquipmentPoolCoversDungeon310(t *testing.T) {
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

	before := archive.Snapshot()
	pool, err := catalog.hellPartyRewardEquipmentPool()
	if err != nil {
		t.Fatalf("build real .equ pool: %v", err)
	}
	after := archive.Snapshot()
	t.Logf("real .equ pool: items=%d buckets=%d min_grade=%d max_grade=%d scanned=%d build_ms=%d",
		pool.Items, len(pool.Buckets), pool.MinGrade, pool.MaxGrade, pool.Scanned, pool.BuildNanos/int64(1e6))
	t.Logf("real dungeon 310 minimum=%d basis=%d ⇒ vendor grade band [%d,%d] closed",
		minimumLevel, basisLevel, gradeMin, gradeMax)
	for rarity := int64(0); rarity <= 5; rarity++ {
		t.Logf("real band [%d,%d] rarity=%d candidates=%d", gradeMin, gradeMax, rarity,
			len(pool.candidates(gradeMin, gradeMax, rarity)))
	}
	t.Logf("real archive caches before scan: chunks=%d texts=%d / after scan: chunks=%d texts=%d",
		before.CachedChunks, before.CachedTexts, after.CachedChunks, after.CachedTexts)

	// 内容存在性（归因报告 §3.1：真档 `.equ` 最高 grade 96，[87,97] 里 948 件过门槛）。
	if pool.MaxGrade < 91 {
		t.Fatalf("real .equ pool max grade=%d want >= 91（内容缺失的判断不成立）", pool.MaxGrade)
	}
	if pool.Items < 7000 {
		t.Fatalf("real .equ pool items=%d want >= 7000", pool.Items)
	}
	// 内存有界：扫描只读、不填归档缓存。
	if after.CachedTexts != before.CachedTexts || after.CachedChunks != before.CachedChunks {
		t.Fatalf("the .equ scan must not populate the archive caches: chunks %d->%d texts %d->%d",
			before.CachedChunks, after.CachedChunks, before.CachedTexts, after.CachedTexts)
	}
	// A 档真实会抽到的 rarity 是 1/2/4（`[basis of rarity dicision]` 行 1），三者都必须有候选。
	for _, rarity := range []int64{1, 2, 4} {
		if count := len(pool.candidates(gradeMin, gradeMax, rarity)); count == 0 {
			t.Fatalf("real band [%d,%d] rarity=%d has no candidate", gradeMin, gradeMax, rarity)
		}
	}

	// 固定种子选一件：必须命中，且 `[grade]` 落在厂商闭区间里。
	selection, err := catalog.hellPartyRewardEquipmentSelectionFor(
		hellPartyRewardTestFixedTier(), newCurrentDungeonDropLCG(20260928), int(basisLevel),
		basisLevel-gradeMin, gradeMax-basisLevel, 1, minimumLevel, basisLevel, true)
	if err != nil {
		t.Fatalf("real equ tier selection: %v", err)
	}
	if !selection.Selected || selection.ItemID == 0 {
		t.Fatalf("real equ tier selection=%+v want a selected item", selection)
	}
	if selection.CandidateCount == 0 {
		t.Fatalf("real equ tier selection reported zero candidates: %+v", selection)
	}
	definition, err := catalog.ResolveItem(selection.ItemID)
	if err != nil {
		t.Fatalf("resolve selected item %d: %v", selection.ItemID, err)
	}
	if definition.Grade < gradeMin || definition.Grade > gradeMax {
		t.Fatalf("selected item %d grade=%d outside vendor band [%d,%d]",
			selection.ItemID, definition.Grade, gradeMin, gradeMax)
	}
	t.Logf("real selection: item=%d grade=%d rarity_bucket=1 candidates=%d pool=%s",
		selection.ItemID, definition.Grade, selection.CandidateCount, selection.Pool)

	// 回退档：同一输入仍是今天的空池 ⇒ 不选中（把缺口钉成回归）。
	rollback, err := catalog.hellPartyRewardEquipmentSelectionFor(
		hellPartyRewardTestLegacyTier(), newCurrentDungeonDropLCG(20260928), int(basisLevel),
		basisLevel-gradeMin, gradeMax-basisLevel, 1, minimumLevel, basisLevel, true)
	if err != nil {
		t.Fatalf("real rollback tier selection: %v", err)
	}
	if rollback.Selected || rollback.ItemID != 0 {
		t.Fatalf("rollback tier must keep today's empty ItemDictionary pool, got %+v", rollback)
	}
}

// TestRealPVFHellPartyRewardRateComesFromTheHellTable 是真档用例：同一个副本 310 上，
// 修复档的 `rate` 来自 hell 表按副本难度取列（difficulty 1 = 875），
// 旧口径（普通怪表按深渊档位取列）得到的 10 在新档下**不可达**。
func TestRealPVFHellPartyRewardRateComesFromTheHellTable(t *testing.T) {
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
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatalf("load real worldmap: %v", err)
	}
	dungeon, ok := table.FindDungeon(310)
	if !ok {
		t.Fatal("real dungeon 310 not indexed")
	}
	basisLevel := dungeon.Metadata.BasisLevel.Value

	info, err := loadHellPartyRewardDropInfo(archive)
	if err != nil {
		t.Fatalf("load real hell drop info: %v", err)
	}
	rates := map[int64]int64{}
	for difficulty := int64(0); difficulty <= 4; difficulty++ {
		rate, found := info.rateForLevel(basisLevel, difficulty)
		if !found {
			t.Fatalf("real hell table has no row covering basis level %d", basisLevel)
		}
		rates[difficulty] = rate
		chance := rate * hellPartyRewardBaseDropRate / hellPartyRewardChancePercentDivisor
		t.Logf("real hell rate: dungeon 310 basis=%d difficulty=%d rate=%d chance=%d (of %d)",
			basisLevel, difficulty, rate, chance, hellPartyRewardDropChanceDenominator)
	}
	if rates[1] != 875 {
		t.Fatalf("real hell rate at difficulty 1 = %d want 875（操作者现场难度）", rates[1])
	}
	for difficulty, rate := range rates {
		if rate == 10 {
			t.Fatalf("difficulty=%d 取到了普通怪表的 10：rate 源没换过来", difficulty)
		}
		if rate <= 0 {
			t.Fatalf("difficulty=%d rate=%d want > 0", difficulty, rate)
		}
	}

	// 旧口径（普通怪表 + 深渊档位）在同一份真档上就是 10：这是"修复档不可达"的对照。
	rules, err := currentDungeonMonsterDropRules(monsterCatalog)
	if err != nil {
		t.Fatalf("real %s is unusable: %v", dnfdrops.MonsterRulePath, err)
	}
	probability, found := rules.Probability(basisLevel)
	if !found {
		t.Fatalf("real [drop prob] has no row covering level %d", basisLevel)
	}
	legacyRate := probability.Rates[1]
	if legacyRate != 10 {
		t.Fatalf("legacy monster rate at tier A = %d want 10 (the reading this fix replaces)", legacyRate)
	}
	t.Logf("legacy reading: %s rate=%d ⇒ chance=%d（修复档不再走这条）",
		dnfdrops.MonsterRulePath, legacyRate,
		legacyRate*hellPartyRewardBaseDropRate/hellPartyRewardChancePercentDivisor)

	// 接入点（request 级）在真档上的行为：修复档 = hell 表 875；回退档 = 交回调用方。
	catalog, err := monsterCatalog.DropCatalog()
	if err != nil {
		t.Fatalf("real drop catalog is unusable: %v", err)
	}
	request := hellPartyRewardRequest{
		DungeonBasisLevel:    basisLevel,
		AbyssPartyDifficulty: 1,
		dropCatalog:          catalog,
		rules:                rules,
	}
	t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolEquValue)
	decision := request.hellPartyRewardTier(1)
	if !decision.UseFixedTier || !decision.RateFound || decision.Rate != 875 ||
		decision.Degraded != "" {
		t.Fatalf("real fixed tier decision=%+v want rate=875 without degradation", decision)
	}
	t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolItemDictionaryValue)
	if legacy := request.hellPartyRewardTier(1); legacy.UseFixedTier || legacy.Degraded != "" ||
		legacy.RateSource != hellPartyRewardLegacyRateSource {
		t.Fatalf("real rollback tier must defer to today's table without degradation: %+v", legacy)
	}
}

// TestHellPartyRewardDegradationKeepsTodaysBehaviourThroughTheWiredPath 钉住"降级 = 今天"：
// 修复档（默认）但 hell 表不存在时，**接线后的**投骰路径不得报错，必须逐字节等于今天
// （rate 来自 monster 规则表、池来自 ItemDictionary），并在结果里留下降级原因。
func TestHellPartyRewardDegradationKeepsTodaysBehaviourThroughTheWiredPath(t *testing.T) {
	service, _, _, runtime, scene, _ := prepareHellPartyRewardRuntime(t)
	// 把 fixture 的 hell 表拿走：这正是"合成 source、没有真档文件"的那些用例的形状。
	source := mustBridgeSource(t, service)
	delete(source, hellPartyRewardDropInfoPath)
	invalidateHellPartyRewardDropInfoCache(t, service)
	t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolEquValue)

	request, err := service.resolveHellPartyReward(runtime, scene, hellPartyRewardTestGroupLast, 65747)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	seed := request.rng.Seed()
	result, err := hellPartyRewardRolls(request)
	if err != nil {
		t.Fatalf("degraded path must not hard-fail: %v", err)
	}
	if result.Degraded == "" || !strings.Contains(result.Degraded, hellPartyRewardDropInfoPath) {
		t.Fatalf("degraded=%q want the missing %s named", result.Degraded, hellPartyRewardDropInfoPath)
	}
	if result.RateSource != hellPartyRewardLegacyRateSource || result.Pool != hellPartyRewardPoolItemDictionaryValue {
		t.Fatalf("degraded tier rate_source=%q pool=%q want today's reading",
			result.RateSource, result.Pool)
	}
	// 与"今天的读法"独立复刻逐字节对账（同一个种子）。
	reference := request
	reference.rng = newCurrentDungeonDropLCG(seed)
	want := replayVendorHellRewardRolls(t, reference, 1000)
	if len(want) != len(result.Selected) {
		t.Fatalf("degraded grants=%d want today's %d", len(result.Selected), len(want))
	}
	for index := range want {
		if want[index] != result.Selected[index] {
			t.Fatalf("degraded roll#%d item=%+v want today's %+v", index+1, result.Selected[index], want[index])
		}
	}
}

// TestHellPartyRewardRateFollowsTheRuntimeDungeonDifficulty 钉住接线 hunk 4：
// `request.DungeonDifficulty` 必须来自 `runtime.Request.Difficulty`，rate 按它取列
// （真档那一行 335/875/1000/1400/1000）。缺了这一步会恒按难度 0 取列（335）。
func TestHellPartyRewardRateFollowsTheRuntimeDungeonDifficulty(t *testing.T) {
	service, _, _, runtime, scene, _ := prepareHellPartyRewardRuntime(t)
	source := mustBridgeSource(t, service)
	source[hellPartyRewardDropInfoPath] = hellPartyRewardTestHellDropInfoRealRatesText()
	invalidateHellPartyRewardDropInfoCache(t, service)
	t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolEquValue)

	for _, test := range []struct {
		difficulty byte
		wantRate   int64
	}{
		{difficulty: 0, wantRate: 335},
		{difficulty: 1, wantRate: 875},
		{difficulty: 2, wantRate: 1000},
		{difficulty: 3, wantRate: 1400},
		{difficulty: 4, wantRate: 1000},
	} {
		runtime.Request.Difficulty = test.difficulty
		request, err := service.resolveHellPartyReward(runtime, scene, hellPartyRewardTestGroupLast, 65747)
		if err != nil {
			t.Fatalf("difficulty %d: resolve: %v", test.difficulty, err)
		}
		if request.DungeonDifficulty != int64(test.difficulty) {
			t.Fatalf("DungeonDifficulty=%d want %d（必须来自 runtime.Request.Difficulty）",
				request.DungeonDifficulty, test.difficulty)
		}
		result, err := hellPartyRewardRolls(request)
		if err != nil {
			t.Fatalf("difficulty %d: roll: %v", test.difficulty, err)
		}
		if result.Rate != test.wantRate || result.Chance != int(test.wantRate) {
			t.Fatalf("difficulty %d: rate=%d chance=%d want rate=%d chance=%d",
				test.difficulty, result.Rate, result.Chance, test.wantRate, test.wantRate)
		}
		if result.RateSource != hellPartyRewardDropInfoPath || result.Pool != hellPartyRewardPoolEquValue {
			t.Fatalf("difficulty %d: rate_source=%q pool=%q want hell table + .equ pool",
				test.difficulty, result.RateSource, result.Pool)
		}
	}
}

// TestHellPartyRewardRollbackTierMatchesTodayThroughTheWiredPath 钉住回退档的**逐字节**语义：
// `DNFBRIDGE_HELL_PARTY_REWARD_POOL=itemdictionary` 时，接线后的投骰结果必须与"今天的读法"
// （`replayVendorHellRewardRolls` 逐行复刻）完全一致，而且两档的随机数**消费次数与顺序相同**
// （同一种子跑完，LCG 终态必须相等）⇒ 线上 A/B 干净。
func TestHellPartyRewardRollbackTierMatchesTodayThroughTheWiredPath(t *testing.T) {
	service, _, _, runtime, scene, _ := prepareHellPartyRewardRuntime(t)
	request := hellPartyRewardRequestForTest(t, service, runtime, scene, 1000)
	seed := request.rng.Seed()

	t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolItemDictionaryValue)
	legacyRequest := request
	legacyRequest.rng = newCurrentDungeonDropLCG(seed)
	legacyResult, err := hellPartyRewardRolls(legacyRequest)
	if err != nil {
		t.Fatalf("rollback tier roll: %v", err)
	}
	legacySeed := legacyRequest.rng.Seed()

	// 今天的读法：独立复刻（同一个种子）。
	reference := request
	reference.rng = newCurrentDungeonDropLCG(seed)
	want := replayVendorHellRewardRolls(t, reference, 1000)
	if len(want) != len(legacyResult.Selected) {
		t.Fatalf("rollback grants=%d want today's %d", len(legacyResult.Selected), len(want))
	}
	for index := range want {
		if want[index] != legacyResult.Selected[index] {
			t.Fatalf("rollback roll#%d item=%+v want today's %+v", index+1, legacyResult.Selected[index], want[index])
		}
	}
	if legacyResult.RateSource != hellPartyRewardLegacyRateSource ||
		legacyResult.Pool != hellPartyRewardPoolItemDictionaryValue ||
		legacyResult.Degraded != "" || legacyResult.Rate != 1000 || legacyResult.Chance != 1000 {
		t.Fatalf("rollback tier result=%+v want today's rate/pool without degradation", legacyResult)
	}

	// 修复档：同一种子跑完的 LCG 终态必须相同（消费次数与顺序一致）。
	// 稀有度闸门在这里显式钉成 `vendor`（= 今天）：本用例钉的是**两张池子**的 A/B
	// 性质，闸门必须中性；"闸门自己"的 A/B 性质（同种子、同命中序列、只有落地物品
	// 不同）由 `dungeon_hell_party_reward_rarity_test.go` 负责。
	t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolEquValue)
	t.Setenv(hellPartyRewardRarityEnv, hellPartyRewardRarityVendorValue)
	fixedRequest := request
	fixedRequest.rng = newCurrentDungeonDropLCG(seed)
	fixedResult, err := hellPartyRewardRolls(fixedRequest)
	if err != nil {
		t.Fatalf("fixed tier roll: %v", err)
	}
	fixedSeed := fixedRequest.rng.Seed()
	if fixedSeed != legacySeed {
		t.Fatalf("tiers consumed different random streams: fixed=%d rollback=%d", fixedSeed, legacySeed)
	}
	if fixedResult.RateSource != hellPartyRewardDropInfoPath || fixedResult.Pool != hellPartyRewardPoolEquValue {
		t.Fatalf("fixed tier result=%+v want hell table + .equ pool", fixedResult)
	}
	// fixture 两档给同一个 rate（1000）且池里都是同一件 ⇒ 落地物品序列也必须相同。
	if len(fixedResult.Selected) != len(legacyResult.Selected) {
		t.Fatalf("tiers granted different counts: fixed=%d rollback=%d",
			len(fixedResult.Selected), len(legacyResult.Selected))
	}
	for index := range fixedResult.Selected {
		if fixedResult.Selected[index] != legacyResult.Selected[index] {
			t.Fatalf("tier A/B roll#%d fixed=%+v rollback=%+v",
				index+1, fixedResult.Selected[index], legacyResult.Selected[index])
		}
	}
}

// TestRealPVFHellPartyRewardRollsUseTheFixedTierAndNeverDegrade 是真档用例：在**接线后的**
// 投骰路径上跑副本 310（minimum=87 / basis=90 / difficulty=1），必须真的走修复档 ——
// rate=875（hell 表）、`pool=equ`、候选 948 量级、无降级、选中件 `[grade] ∈ [87,97]`。
// 如果哪天降级路径把真档遮蔽了（例如读表失败后悄悄回落），这条用例会失败。
//
// ⚠ 稀有度闸门在这里显式钉成 `vendor`（= 今天），所以这条用例仍然是"修复档 + 厂商稀有度"
// 的 948 件候选/7 件落地的老口径；**默认档**（`epic`：只掉史诗）在同一份真档上的口径由
// `TestRealPVFHellPartyRewardEpicTierDropsOnlyTheRealRarity4Pool` 负责。
func TestRealPVFHellPartyRewardRollsUseTheFixedTierAndNeverDegrade(t *testing.T) {
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

	t.Setenv(hellPartyRewardPoolEnv, hellPartyRewardPoolEquValue)
	// 本用例钉"修复档 + 厂商稀有度"（948 件候选、~7 件落地）：闸门显式设 `vendor`。
	t.Setenv(hellPartyRewardRarityEnv, hellPartyRewardRarityVendorValue)
	request := hellPartyRewardRequest{
		MonsterCode:           65747,
		DungeonMinimumLevel:   minimumLevel,
		DungeonBasisLevel:     basisLevel,
		DungeonDifficulty:     1, // 现场三次深渊房冻结口径
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
	t.Logf("real wired roll: rate=%d rate_source=%s chance=%d rolls=%d missed=%d no_item=%d pool=%s candidates=%d grade=%d..%d selected=%d degraded=%q",
		result.Rate, result.RateSource, result.Chance, result.Rolls, result.Misses, result.NoDrop,
		result.Pool, result.CandidateCount, result.GradeMin, result.GradeMax,
		len(result.Selected), result.Degraded)

	if result.Degraded != "" {
		t.Fatalf("real PVF must never take the degradation path: degraded=%q", result.Degraded)
	}
	if result.Rate != 875 || result.Chance != 875 {
		t.Fatalf("real rate=%d chance=%d want 875 (dungeon difficulty 1)", result.Rate, result.Chance)
	}
	if result.RateSource != hellPartyRewardDropInfoPath || result.Pool != hellPartyRewardPoolEquValue {
		t.Fatalf("real rate_source=%q pool=%q want hell table + .equ pool", result.RateSource, result.Pool)
	}
	if result.GradeMin != gradeMin || result.GradeMax != gradeMax {
		t.Fatalf("real band=%d..%d want %d..%d", result.GradeMin, result.GradeMax, gradeMin, gradeMax)
	}
	if result.CandidateCount < 60 {
		t.Fatalf("real candidates=%d want the 948-item band (rarity buckets 68/135/248)", result.CandidateCount)
	}
	if len(result.Selected) < 7 {
		t.Fatalf("real selected=%d want ~7 grants at chance 875/1001", len(result.Selected))
	}
	for _, item := range result.Selected {
		definition, err := catalog.ResolveItem(item.ItemID)
		if err != nil {
			t.Fatalf("resolve %d: %v", item.ItemID, err)
		}
		if definition.Grade < gradeMin || definition.Grade > gradeMax {
			t.Fatalf("real selected item %d grade=%d outside [%d,%d]",
				item.ItemID, definition.Grade, gradeMin, gradeMax)
		}
	}
}
