package dnfbridge

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件是「深渊掉落池读错了源」的修复本体（只读归因见 `_dist/abyss-drop-pool-grade-gap.md`）。
//
// ── 厂商口径（唯一依据，逐行对照）────────────────────────────────────────────
//
//	Game/Dungeon/HellMonsterDropConfig.cs
//	  :36   EnsureLoaded()                      —— 首次使用时建池（启动期由 DropService.cs:20 WarmUp 提前）
//	  :41   FindDropProb(itemBasisLevel)        —— 查 `etc/itemdropinfo_monster_hell.etc`
//	  :45   difficultyIndex = clamp(dungeonDifficulty, 0, rates.Length-1)   ← **副本难度**，不是深渊档位
//	  :46   rate = prob.DifficultyRates[difficultyIndex]
//	  :51   chance = BaseDropRate(100) * rate / 100
//	  :126  ParseDropProb   —— `[dungeon difficulty drop prob]` 每行 7 个整数，前两个是
//	        `levelMin levelMax`，后五个是 5 档 `rate`
//	  :151  ParseRarityThresholds —— `[basis of rarity dicision]` 首值是行数，每行 7 个阈值
//	  :173  ParseGradeRanges —— `[item drop ref table]` 三元组（厂商自己在这条链上**不用**它）
//	  :92   _equipPool = EquipmentDropPoolProvider.GetPool()   ← **池子来自 .equ，不是 ItemDictionary**
//
//	Game/Dungeon/EquipmentDropPoolProvider.cs
//	  :52   EquipmentDefinitionCatalog.GetAll()
//	  :101  门槛 creationRate <= 0 || grade <= 0 || rarity < 0 || rarity > 5 ⇒ 丢
//	  :104  键 = (long)grade * 10 + rarity，权重 = creationRate
//
//	Game/Inventory/EquipmentDefinitionCatalog.cs
//	  :51-59 遍历 `equipment/equipment.lst`，对每条 `Path.Combine("equipment", entry.FilePath)`
//	         读文本并解析 `[grade] [rarity] [creation rate]`
//
//	Game/Dungeon/EquipmentDropLevelRule.cs:27-28
//	  gradeMin = Math.Max(1, dungeonMinimumLevel)（先保证 min <= basis）
//	  gradeMax = Math.Min(200, dungeonBasisLevel + 7)   ← **闭区间**
//
//	Game/Dungeon/HellMonsterDropConfig.cs
//	  :245-250 稀有度空池回落 rarity 0
//	  :292-308 CollectWeightedFromPool：`for (grade = gradeMin; grade <= gradeMax; grade++)`
//	           取 `(long)grade*10+rarity` 桶
//	  :255-269 权重累加 + `lcg.Next(totalWeight)` + `roll < cumulative`
//
// ── 与厂商的**已声明差异** ───────────────────────────────────────────────────
//
//  1. `EquipmentDropLevelRule` 在厂商侧是"图鉴等级规则表"（`TryGetAtlasGradeRange` 的注释），
//     本仓没有那张表，因此按它**自己**的算术实现（`hellPartyRewardGradeRangeForLevels`）。
//     真档副本 310（minimum=10 / basis=90）⇒ grade ∈ [10, 97]（闭区间）。
//  2. `[item drop ref table]` 只解析并在事件里报行数（厂商侧 `TryGetGradeRangeFromItemDropRefTable`
//     全仓无调用点），不参与区间。
//  3. EPIC 概率药水重投（`:64-68`）本仓没有数据源，仍未实现（与既有 reward 文件同一处差异）。
//
// ── 内存上界（本文件的核心约束）─────────────────────────────────────────────
//
// 厂商的池子是 14 万次 `.equ` 读取。若走 `Archive.ReadText`，`platform/pvf` 的
// `chunks`/`texts` 两个 `sync.Map` 会把**整棵装备树解码后的内容留到进程结束**
// （只读归因实测峰值工作集 1,443 MB）。因此这里改走
// `platformpvf.Archive.ScanTextPrefix`：按 group 流式扫描，解压出来的 group 与
// 解码出来的文本都不进缓存，同一时刻只持有一个 group + 一条文本；
// 真正留下来的只有**通过厂商门槛**的 `(itemID, weight)` 索引。
// 事件 `dnf-dungeon-hell-party-reward-pool-ready` 会把 `pool/pool_items/pool_buckets/
// pool_min_grade/pool_max_grade/build_ms/scanned_equ_files/built/degraded/reason`
// 打出来，并在 `degraded` 上保持"有物品 ⇒ 不是降级"这条判据（见
// `hellPartyRewardPoolReadiness`）；真档用例另外断言
// `Snapshot().CachedChunks/CachedTexts` 在整轮扫描后**仍然是 0**。
//
// ── 开关 ────────────────────────────────────────────────────────────────────
//
// DNFBRIDGE_HELL_PARTY_REWARD_POOL
//
//	equ（默认，= 修复档）：池子 = `equipment/equipment.lst` + `equipment/<path>`
//	    的 `.equ` 本体（厂商口径），概率 = `etc/itemdropinfo_monster_hell.etc`
//	    按**副本难度**取列，等级区间 = `[min(minLv,basis), basis+7]` 闭区间。
//	itemdictionary（唯一回退字面量）：**逐字节回到今天** ——
//	    `Etc/ItemDictionary/ItemDictionary.etc` 池 + `Etc/ItemDropInfo_Monseter.etc`
//	    概率/稀有度 + `[basis-down, basis+up)` 区间 + 下标 = 深渊档位。
//	    回退档下本文件的一切都不参与，`dungeon_hell_party_reward.go` 的三处一行不改。
//
// 本文件之外还有第二个口径开关（操作者要求：深渊只掉史诗）：
// DNFBRIDGE_HELL_PARTY_REWARD_RARITY（默认 `epic`：只允许 rarity 4，
// 空池**不**回落 rarity 0），见 `dungeon_hell_party_reward_rarity.go`；
// 它只作用在本文件的**修复档**上，`itemdictionary` 与降级档逐字节等于今天。
const (
	// hellPartyRewardPoolEnv 是取池口径开关。
	hellPartyRewardPoolEnv = "DNFBRIDGE_HELL_PARTY_REWARD_POOL"
	// hellPartyRewardPoolEquValue 是修复档（也是默认档）的字面量。
	hellPartyRewardPoolEquValue = "equ"
	// hellPartyRewardPoolItemDictionaryValue 是唯一被承认的回退取值（= 今天）。
	hellPartyRewardPoolItemDictionaryValue = "itemdictionary"

	// hellPartyRewardDropInfoPath 是深渊概率表（厂商 `HellMonsterDropConfig.cs:118`）。
	hellPartyRewardDropInfoPath = "etc/itemdropinfo_monster_hell.etc"
	// hellPartyRewardDropInfoPathUpper 是厂商 `:121` 的大小写兜底读法。
	hellPartyRewardDropInfoPathUpper = "Etc/itemdropinfo_monster_hell.etc"
	// hellPartyRewardEquipmentListRoot 是厂商 `Path.Combine("equipment", entry.FilePath)` 的根。
	hellPartyRewardEquipmentListRoot = "equipment"

	// hellPartyRewardPoolKeyScale = 厂商 `(long)grade * 10 + rarity`。
	hellPartyRewardPoolKeyScale = 10
	// hellPartyRewardDifficultyRateCount = 每行 5 档 rate（`:137 rates = new int[5]`）。
	hellPartyRewardDifficultyRateCount = 5
	// hellPartyRewardDropProbRowWidth = `:132 vals.Count / 7`（levelMin levelMax + 5 档）。
	hellPartyRewardDropProbRowWidth = 7
	// hellPartyRewardRarityRowWidth = `:165 thresholds = new int[7]`。
	hellPartyRewardRarityRowWidth = 7
	// hellPartyRewardGradeRangeBound = `:179 _gradeRanges = new (int Down, int Up)[200]`。
	hellPartyRewardGradeRangeBound = 200
	// hellPartyRewardPoolGradeCeiling 是池子的等级上限（真档 `[grade]` 最高 96）。
	hellPartyRewardPoolGradeCeiling = 200
)

type hellPartyRewardPoolMode int

const (
	// hellPartyRewardPoolEqu 是默认档，也是修复档（零值 = 修复）。
	hellPartyRewardPoolEqu hellPartyRewardPoolMode = iota
	// hellPartyRewardPoolItemDictionary 是改动前的 ItemDictionary 池档。
	hellPartyRewardPoolItemDictionary
)

// readHellPartyRewardPoolMode 只把 `itemdictionary`（大小写不敏感、允许首尾空白）
// 认成回退档；未设置/空/任何其它值一律是修复档（与
// `readHellPartyRewardRollMode`、`readItemGrantNoticeMode` 同一惯例）。
func readHellPartyRewardPoolMode(value string) hellPartyRewardPoolMode {
	if strings.EqualFold(strings.TrimSpace(value), hellPartyRewardPoolItemDictionaryValue) {
		return hellPartyRewardPoolItemDictionary
	}
	return hellPartyRewardPoolEqu
}

// String 是事件字段 `pool=` 的取值，线上据此一眼分辨两档。
func (mode hellPartyRewardPoolMode) String() string {
	if mode == hellPartyRewardPoolItemDictionary {
		return hellPartyRewardPoolItemDictionaryValue
	}
	return hellPartyRewardPoolEquValue
}

// hellPartyRewardPoolModeFromEnv 在**消费点**自读开关（与 `mysql_char.go:222` /
// `equip/owner.go:877` / `gmtool/online_guard.go:67` 同一惯例：options 结构体不在本改动里加字段）。
func hellPartyRewardPoolModeFromEnv() hellPartyRewardPoolMode {
	return readHellPartyRewardPoolMode(os.Getenv(hellPartyRewardPoolEnv))
}

// ── `etc/itemdropinfo_monster_hell.etc`（厂商 `HellMonsterDropConfig.cs:126-187`）──

// hellPartyDropProbabilityRow 是 `[dungeon difficulty drop prob]` 的一行。
type hellPartyDropProbabilityRow struct {
	LevelMin int64
	LevelMax int64
	Rates    [hellPartyRewardDifficultyRateCount]int64
}

// hellPartyRewardGradeRange 是 `[item drop ref table]` 的一个三元组值对。
type hellPartyRewardGradeRange struct {
	Down int64
	Up   int64
}

// hellPartyRewardDropInfo 是 hell 概率表的三段解析结果。
type hellPartyRewardDropInfo struct {
	Probabilities    []hellPartyDropProbabilityRow
	RarityThresholds [][]int64
	// GradeRanges 是 `[item drop ref table]`（厂商这条链**不用**它，这里只报行数）。
	GradeRanges     map[int64]hellPartyRewardGradeRange
	GradeRangeCount int
}

// rateForLevel 对应厂商 `:41-48`：按 `itemBasisLevel` 找行，再按**副本难度**取列。
func (info *hellPartyRewardDropInfo) rateForLevel(level, dungeonDifficulty int64) (int64, bool) {
	if info == nil || len(info.Probabilities) == 0 {
		return 0, false
	}
	for _, row := range info.Probabilities {
		if level < row.LevelMin || level > row.LevelMax {
			continue
		}
		index := dungeonDifficulty
		if index < 0 {
			index = 0
		}
		if index >= hellPartyRewardDifficultyRateCount {
			index = hellPartyRewardDifficultyRateCount - 1
		}
		return row.Rates[index], true
	}
	return 0, false
}

// rarityForRoll 逐行对应厂商 `:203-223 RollHellRarity`（`roll` 已是 `lcg.Next(1000000)+1`）。
func (info *hellPartyRewardDropInfo) rarityForRoll(hellDifficulty byte, roll int64) int64 {
	if info == nil || len(info.RarityThresholds) == 0 {
		return 0
	}
	row := int(hellDifficulty) - 1
	if row < 0 {
		row = 0
	}
	if row >= len(info.RarityThresholds) {
		row = len(info.RarityThresholds) - 1
	}
	values := info.RarityThresholds[row]
	limit := len(values)
	if limit > hellPartyRewardMaxRarityRow {
		limit = hellPartyRewardMaxRarityRow
	}
	for rarity := 0; rarity < limit; rarity++ {
		if roll <= values[rarity] {
			return int64(rarity)
		}
	}
	return 0
}

// parseHellPartyRewardDropInfo 只认 hell 文件的三段；**不复用**
// `dnfdrops.LoadMonsterRules` —— 那个加载器写死读 `Etc/ItemDropInfo_Monseter.etc`
// 且按普通怪表的定长段校验，把 hell 文件喂给它必然在 `[drop prob]` 段失败。
func parseHellPartyRewardDropInfo(text string) hellPartyRewardDropInfo {
	info := hellPartyRewardDropInfo{}
	if values, ok := hellPartyRewardSectionInts(text, "dungeon difficulty drop prob"); ok &&
		len(values) >= hellPartyRewardDropProbRowWidth {
		rows := len(values) / hellPartyRewardDropProbRowWidth
		info.Probabilities = make([]hellPartyDropProbabilityRow, 0, rows)
		for row := 0; row < rows; row++ {
			base := row * hellPartyRewardDropProbRowWidth
			entry := hellPartyDropProbabilityRow{
				LevelMin: values[base],
				LevelMax: values[base+1],
			}
			for column := 0; column < hellPartyRewardDifficultyRateCount; column++ {
				entry.Rates[column] = values[base+2+column]
			}
			info.Probabilities = append(info.Probabilities, entry)
		}
	}
	if values, ok := hellPartyRewardSectionInts(text, "basis of rarity dicision"); ok && len(values) >= 2 {
		rowCount := values[0]
		if rowCount > 0 {
			info.RarityThresholds = make([][]int64, 0, rowCount)
			index := 1
			for row := int64(0); row < rowCount && index < len(values); row++ {
				thresholds := make([]int64, 0, hellPartyRewardRarityRowWidth)
				for column := 0; column < hellPartyRewardRarityRowWidth && index < len(values); column++ {
					thresholds = append(thresholds, values[index])
					index++
				}
				info.RarityThresholds = append(info.RarityThresholds, thresholds)
			}
		}
	}
	if values, ok := hellPartyRewardSectionInts(text, "item drop ref table"); ok && len(values) >= 3 {
		info.GradeRanges = make(map[int64]hellPartyRewardGradeRange)
		for index := 0; index+2 < len(values); index += 3 {
			level := values[index]
			info.GradeRangeCount++
			if level < 1 || level > hellPartyRewardGradeRangeBound {
				continue
			}
			info.GradeRanges[level] = hellPartyRewardGradeRange{Down: values[index+1], Up: values[index+2]}
		}
	}
	return info
}

// hellPartyRewardSectionInts 取 `[name]` 与下一个 `[` 之间的整数
// （厂商 `:310-337 ExtractSection + ParseInts`）。
func hellPartyRewardSectionInts(text, name string) ([]int64, bool) {
	tag := "[" + name + "]"
	start := strings.Index(strings.ToLower(text), strings.ToLower(tag))
	if start < 0 {
		return nil, false
	}
	start += len(tag)
	end := strings.Index(text[start:], "[")
	if end < 0 {
		end = len(text) - start
	}
	section := text[start : start+end]
	values := make([]int64, 0, 32)
	for _, token := range strings.Fields(section) {
		if !hellPartyRewardIsIntegerToken(token) {
			// 厂商 `int.TryParse` 只吃纯整数；`Sscanf("%d")` 会把 "12abc" 读成 12，
			// 因此这里先按 token 形状把关，再解析。
			continue
		}
		var value int64
		if _, err := fmt.Sscanf(token, "%d", &value); err != nil {
			continue
		}
		values = append(values, value)
	}
	return values, true
}

func hellPartyRewardIsIntegerToken(token string) bool {
	if token == "" {
		return false
	}
	body := token
	if body[0] == '+' || body[0] == '-' {
		body = body[1:]
	}
	if body == "" {
		return false
	}
	for index := 0; index < len(body); index++ {
		if body[index] < '0' || body[index] > '9' {
			return false
		}
	}
	return true
}

// loadHellPartyRewardDropInfo 读 hell 概率表（大小写兜底照厂商 `:116-124`）。
func loadHellPartyRewardDropInfo(source dnfpvf.Source) (hellPartyRewardDropInfo, error) {
	if source == nil {
		return hellPartyRewardDropInfo{}, errDungeonDropSourceRequired
	}
	text, err := source.ReadText(hellPartyRewardDropInfoPath)
	if err != nil {
		text, err = source.ReadText(hellPartyRewardDropInfoPathUpper)
	}
	if err != nil {
		return hellPartyRewardDropInfo{}, fmt.Errorf("read %s: %w", hellPartyRewardDropInfoPath, err)
	}
	return parseHellPartyRewardDropInfo(text), nil
}

// ── `.equ` 池（厂商 `EquipmentDropPoolProvider`）────────────────────────────

// hellPartyRewardEquipmentPool 是厂商键口径的池子：`grade*10+rarity` → 加权候选。
type hellPartyRewardEquipmentPool struct {
	// Buckets 的键 = `(long)grade * 10 + rarity`。
	Buckets map[int64][]dungeonDropWeightedItem
	// Items/Scanned/MaxGrade/MinGrade/BuildNanos 是事件与用例要用的台账。
	Items      int
	Scanned    int
	MaxGrade   int64
	MinGrade   int64
	BuildNanos int64
}

// hellPartyRewardPoolKey 与厂商 `(long)grade * 10 + rarity` 同一个键。
func hellPartyRewardPoolKey(grade, rarity int64) int64 {
	return grade*hellPartyRewardPoolKeyScale + rarity
}

// candidates 逐行对应厂商 `CollectWeightedFromPool`：`grade ∈ [gradeMin, gradeMax]`
// **闭区间**，逐个 grade 取对应 rarity 桶并按 grade 升序拼接（顺序与厂商一致）。
//
// **本仓新增的一道闸门**（`dungeon_abyss_item_level_floor.go`，操作者缺陷「深渊掉落了
// lv80 的史诗碎片」）：候选还必须通过按**物品等级**（`.equ [minimum level]`）的显式地板
// `DNFBRIDGE_ABYSS_ITEM_LEVEL_FLOOR`（默认 85）。回退档（`legacy`）下这一行是空操作，
// 候选与顺序逐字节等于本改动前。真档副本 310 的波段 [87,97] 上 948/948 候选都 ≥ 85
// ⇒ 线上**零行为差**；它挡住的是"换个 `[minimum required level] <= 86` 的副本就会放进
// lv80 史诗"这一类（同一份 PVF 里史诗 grade 只有 91/96 = lv85/lv90）。
func (pool *hellPartyRewardEquipmentPool) candidates(gradeMin, gradeMax, rarity int64) []dungeonDropWeightedItem {
	if pool == nil || gradeMax < gradeMin {
		return nil
	}
	band := abyssItemLevelBandFromEnv()
	result := make([]dungeonDropWeightedItem, 0, 64)
	for grade := gradeMin; grade <= gradeMax; grade++ {
		for _, item := range pool.Buckets[hellPartyRewardPoolKey(grade, rarity)] {
			if !band.permitsEquipmentLevel(item.MinimumLevel) {
				continue
			}
			result = append(result, item)
		}
	}
	return result
}

// rarityCandidateCounts 数出本波段六个 rarity 桶各有几件候选（事件字段
// `rarity_candidates=`，让"rarity 4 到底有没有候选"在日志里一眼可见）。
func (pool *hellPartyRewardEquipmentPool) rarityCandidateCounts(
	gradeMin, gradeMax int64,
) [hellPartyRewardRarityBuckets]int {
	var counts [hellPartyRewardRarityBuckets]int
	if pool == nil {
		return counts
	}
	for rarity := int64(0); rarity <= hellPartyRewardRarityMax; rarity++ {
		counts[hellPartyRewardRarityIndex(rarity)] = len(pool.candidates(gradeMin, gradeMax, rarity))
	}
	return counts
}

// loadHellPartyRewardEquipmentPool 建厂商的 `.equ` 池。
//
// 扫描口径：`equipment/equipment.lst` 的每一行 `<id> \`<path>\“ 都读
// `equipment/<path>`（`readDungeonDropText` 已实现厂商 `Path.Combine` 与 `(r)` 兜底），
// 门槛 = `creationRate > 0 && grade > 0 && 0 <= rarity <= 5`（`EquipmentDropPoolProvider.cs:101`）。
//
// 内存：优先走 `ScanTextPrefix`（不填归档缓存，同一时刻一个 group + 一条文本）；
// 只有拿不到该能力的测试替身 source 才逐文件 `ReadText`。
func loadHellPartyRewardEquipmentPool(source dnfpvf.Source) (*hellPartyRewardEquipmentPool, error) {
	if source == nil {
		return nil, errDungeonDropSourceRequired
	}
	started := time.Now()
	pool := &hellPartyRewardEquipmentPool{Buckets: make(map[int64][]dungeonDropWeightedItem, 256)}

	// `equipment/equipment.lst`：id → `<path>`（相对 `equipment/`）。
	text, err := source.ReadText(dungeonDropEquipmentList)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dungeonDropEquipmentList, err)
	}
	document, err := dnfpvf.Parse(dungeonDropEquipmentList, text)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", dungeonDropEquipmentList, err)
	}
	paths := make(map[int64]string, 1024)
	ids := make([]int64, 0, 1024)
	for _, entry := range dnfpvf.ParseList(document) {
		clean := cleanDungeonDropPath(entry.Path)
		if entry.ID <= 0 || clean == "" {
			continue
		}
		if _, exists := paths[entry.ID]; exists {
			continue
		}
		paths[entry.ID] = clean
		ids = append(ids, entry.ID)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: %s is empty", errDungeonDropItemUnresolved, dungeonDropEquipmentList)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })

	add := func(itemID, grade, rarity, creationRate, minimumLevel int64) {
		if itemID <= 0 || itemID > math.MaxUint32 {
			return
		}
		// 厂商 `EquipmentDropPoolProvider.cs:101` 的门槛，逐字。
		if creationRate <= 0 || grade <= 0 || rarity < 0 || rarity > 5 {
			return
		}
		if grade > hellPartyRewardPoolGradeCeiling {
			return
		}
		key := hellPartyRewardPoolKey(grade, rarity)
		pool.Buckets[key] = append(pool.Buckets[key], dungeonDropWeightedItem{
			ItemID:       uint32(itemID),
			Weight:       uint32(creationRate),
			MinimumLevel: minimumLevel,
		})
		pool.Items++
		if grade > pool.MaxGrade {
			pool.MaxGrade = grade
		}
		if pool.MinGrade == 0 || grade < pool.MinGrade {
			pool.MinGrade = grade
		}
	}

	visit := func(itemID int64, body string) {
		pool.Scanned++
		participant, err := dnfpvf.Parse(dungeonDropEquipmentList, body)
		if err != nil {
			return
		}
		grade, found := participant.Int("grade")
		if !found {
			return
		}
		rarity, found := participant.Int("rarity")
		if !found {
			rarity = 0
		}
		creationRate, found := participant.Int("creation rate")
		if !found {
			return
		}
		// `[minimum level]` = 装备的使用等级（玩家看到的 lvNN）。它是
		// `DNFBRIDGE_ABYSS_ITEM_LEVEL_FLOOR` 的判据（`dungeon_abyss_item_level_floor.go`）。
		// 缺少该字段 = 0 = 未知 ⇒ 不过滤（fail-open，见该文件头）。
		minimumLevel, _ := participant.Int("minimum level")
		add(itemID, grade, rarity, creationRate, minimumLevel)
	}

	if scanner, ok := source.(hellPartyRewardTextScanner); ok {
		// group 顺序扫描：`equipment/` 前缀下的文件集合与 `equipment.lst` 的行一一对应
		// （真档 140,862/140,862 命中，见归因报告 §3）。键用归一化后的相对路径，
		// 与厂商 `Path.Combine("equipment", entry.FilePath)` 的对应关系一致。
		matched := make(map[string]int64, len(ids))
		for _, itemID := range ids {
			matched[strings.ToLower(cleanDungeonDropPath(paths[itemID]))] = itemID
		}
		prefix := hellPartyRewardEquipmentListRoot + "/"
		scanErr := scanner.ScanTextPrefix(hellPartyRewardEquipmentListRoot, func(archivePath, body string) error {
			relative := strings.ToLower(cleanDungeonDropPath(archivePath))
			if !strings.HasPrefix(relative, prefix) {
				return nil
			}
			itemID, ok := matched[strings.TrimPrefix(relative, prefix)]
			if !ok {
				return nil
			}
			visit(itemID, body)
			return nil
		})
		if scanErr != nil {
			return nil, fmt.Errorf("scan %s: %w", hellPartyRewardEquipmentListRoot, scanErr)
		}
	} else {
		for _, itemID := range ids {
			_, body, err := readDungeonDropText(source, hellPartyRewardEquipmentListRoot, paths[itemID])
			if err != nil {
				continue
			}
			visit(itemID, body)
		}
	}

	pool.BuildNanos = time.Since(started).Nanoseconds()
	return pool, nil
}

// hellPartyRewardTextScanner 是 `platformpvf.Archive` 提供的**有界**批量读法。
// 用可选接口而不是直接依赖具体类型：内存替身 source（用例）没有它，
// 生产源的 `*pvf.Archive` 有它。
type hellPartyRewardTextScanner interface {
	ScanTextPrefix(prefix string, visit func(archivePath string, text string) error) error
}

// ── 等级区间（厂商 `EquipmentDropLevelRule`）────────────────────────────────

// hellPartyRewardGradeRangeForLevels 逐行对应厂商 `EquipmentDropLevelRule.cs:7-30`：
// 两个等级都为 0 ⇒ 失败；单个为 0 ⇒ 取另一个；min > basis ⇒ 交换；
// `gradeMin = Max(1, minimum)`、`gradeMax = Min(200, basis + 7)`，**闭区间**。
func hellPartyRewardGradeRangeForLevels(dungeonMinimumLevel, dungeonBasisLevel int64) (int64, int64, bool) {
	if dungeonMinimumLevel <= 0 && dungeonBasisLevel <= 0 {
		return 0, 0, false
	}
	if dungeonMinimumLevel <= 0 {
		dungeonMinimumLevel = dungeonBasisLevel
	}
	if dungeonBasisLevel <= 0 {
		dungeonBasisLevel = dungeonMinimumLevel
	}
	if dungeonMinimumLevel > dungeonBasisLevel {
		dungeonMinimumLevel, dungeonBasisLevel = dungeonBasisLevel, dungeonMinimumLevel
	}
	gradeMin := dungeonMinimumLevel
	if gradeMin < 1 {
		gradeMin = 1
	}
	gradeMax := dungeonBasisLevel + 7
	if gradeMax > hellPartyRewardPoolGradeCeiling {
		gradeMax = hellPartyRewardPoolGradeCeiling
	}
	if gradeMax < gradeMin {
		return 0, 0, false
	}
	return gradeMin, gradeMax, true
}

// ── 选池（`ChooseEquipment` 的等价物）──────────────────────────────────────

// hellPartyRewardEquipmentSelection 是"这一抽选了什么/池子长什么样"的完整台账，
// 供事件、用例与 reward 文件的接入点共用。
type hellPartyRewardEquipmentSelection struct {
	ItemID         uint32
	Selected       bool
	Pool           string
	CandidateCount int
	GradeMin       int64
	GradeMax       int64
	// Rarity 是这一抽**真的取了哪个桶**：正常等于请求的 rarity；厂商的
	// `rarity 0 回落`（`:246-250`）命中时回落成 0。
	Rarity int64
	// FallbackUsed 非空 = 这一抽用了厂商的 rarity 0 回落（`:246-250`）。
	FallbackUsed bool
	// Degraded 非空 = 这一抽是在**降级**口径下取的（池子读不到 ⇒ 回落今天的取件）。
	Degraded string
}

// selectEquipment 逐行对应厂商 `HellMonsterDropConfig.cs:225-270 ChooseEquipment`（`equ` 档）：
//   - 稀有度空池 + rarity > 0 + `allowCommonFallback` ⇒ 回落 rarity 0（`:246-250`）；
//   - 权重累加、`roll = lcg.Next(totalWeight)`、`roll < cumulative`（`:255-269`）；
//   - 候选为空 ⇒ 不选中（厂商返回 -1）。
//
// `allowCommonFallback` 是**稀有度闸门**的唯一注入口（`dungeon_hell_party_reward_rarity.go`）：
// 修复档 = `plan.allowsCommonFallback()`（默认档 `epic` ⇒ false ⇒ 空池不掉普通装备，
// **绝不降级**）；回退/降级档与厂商口径 = true（= 今天）。
func (pool *hellPartyRewardEquipmentPool) selectEquipment(
	rng *currentDungeonDropLCG,
	dungeonMinimumLevel, dungeonBasisLevel, rarity int64,
	allowCommonFallback bool,
) (hellPartyRewardEquipmentSelection, error) {
	selection := hellPartyRewardEquipmentSelection{Pool: hellPartyRewardPoolEquValue, Rarity: rarity}
	if rng == nil {
		return selection, errDungeonDropSourceRequired
	}
	if pool == nil {
		return selection, errDungeonHellPartyRewardDropRule
	}
	gradeMin, gradeMax, ok := hellPartyRewardGradeRangeForLevels(dungeonMinimumLevel, dungeonBasisLevel)
	if !ok {
		return selection, fmt.Errorf(
			"%w: minimum=%d basis=%d",
			errDungeonHellPartyRewardLevels,
			dungeonMinimumLevel,
			dungeonBasisLevel,
		)
	}
	selection.GradeMin, selection.GradeMax = gradeMin, gradeMax
	candidates := pool.candidates(gradeMin, gradeMax, rarity)
	if len(candidates) == 0 && rarity > 0 && allowCommonFallback {
		candidates = pool.candidates(gradeMin, gradeMax, 0)
		selection.FallbackUsed = true
		selection.Rarity = 0
	}
	selection.CandidateCount = len(candidates)
	if len(candidates) == 0 {
		return selection, nil
	}
	var total int64
	for _, candidate := range candidates {
		if candidate.ItemID == 0 || candidate.Weight == 0 {
			continue
		}
		total += int64(candidate.Weight)
	}
	if total <= 0 {
		return selection, nil
	}
	if total > math.MaxUint32 {
		// 厂商 `lcg.Next(int max)` 也是 32 位取模；真档池子（权重 ≤ 数百、候选 ≤ 数千）
		// 离这个上界有四个数量级，撞上就是数据坏了，硬失败而不是静默偏斜。
		return selection, fmt.Errorf("%w: total_weight=%d", errDungeonHellPartyRewardDropRule, total)
	}
	roll := int64(rng.Next(int(total))) // 厂商 `:261 lcg.Next(totalWeight)`
	var cumulative int64
	for _, candidate := range candidates {
		if candidate.ItemID == 0 || candidate.Weight == 0 {
			continue
		}
		cumulative += int64(candidate.Weight)
		if roll < cumulative {
			selection.ItemID = candidate.ItemID
			selection.Selected = true
			return selection, nil
		}
	}
	selection.ItemID = candidates[len(candidates)-1].ItemID
	selection.Selected = selection.ItemID != 0
	return selection, nil
}

// hellPartyRewardEquipmentSelectionFor 是投骰路径的**唯一**取件入口。
//
//	修复档：厂商 `.equ` 池（本文件）。
//	降级/回退档：逐字节转调今天的 `SelectGenericEquipment`
//	    （`ItemDictionary.etc` 池 + `[basis-down, basis+up)` 区间），行为与改动前一致。
//
// `allowCommonFallback` 只在修复档里参与（稀有度闸门，见 `dungeon_hell_party_reward_rarity.go`）；
// 降级/回退档的 `SelectGenericEquipment` 自己带 rarity 0 回落，逐字节不改。
func (c *pvfDungeonDropCatalog) hellPartyRewardEquipmentSelectionFor(
	decision hellPartyRewardTierDecision,
	rng *currentDungeonDropLCG,
	monsterLevel int,
	gradeDown, gradeUp, rarity int64,
	dungeonMinimumLevel, dungeonBasisLevel int64,
	allowCommonFallback bool,
) (hellPartyRewardEquipmentSelection, error) {
	if decision.UseFixedTier {
		pool, err := c.hellPartyRewardEquipmentPool()
		if err == nil {
			return pool.selectEquipment(rng, dungeonMinimumLevel, dungeonBasisLevel, rarity, allowCommonFallback)
		}
		// 裁定之后池子又不可用（例如热更换源）：落到今天的取件口径并留原因。
		legacy, legacyErr := c.legacyEquipmentSelection(rng, monsterLevel, gradeDown, gradeUp, rarity)
		legacy.Degraded = hellPartyRewardDegradedReason(dungeonDropEquipmentList, err)
		return legacy, legacyErr
	}
	return c.legacyEquipmentSelection(rng, monsterLevel, gradeDown, gradeUp, rarity)
}

// legacyEquipmentSelection 是**今天**的取件口径（逐字节：`SelectGenericEquipment`），
// 修复档与回退档在降级时共用它，保证"降级 = 今天的行为"。
func (c *pvfDungeonDropCatalog) legacyEquipmentSelection(
	rng *currentDungeonDropLCG,
	monsterLevel int,
	gradeDown, gradeUp, rarity int64,
) (hellPartyRewardEquipmentSelection, error) {
	selection := hellPartyRewardEquipmentSelection{Pool: hellPartyRewardPoolItemDictionaryValue}
	if rng == nil {
		return selection, errDungeonDropSourceRequired
	}
	itemID, selected, err := c.SelectGenericEquipment(rng, monsterLevel, gradeDown, gradeUp, rarity)
	if err != nil {
		return selection, err
	}
	selection.ItemID = itemID
	selection.Selected = selected
	selection.GradeMin = int64(monsterLevel) - gradeDown
	selection.GradeMax = int64(monsterLevel) + gradeUp - 1
	selection.Rarity, selection.CandidateCount =
		c.legacyEquipmentBucket(monsterLevel, gradeDown, gradeUp, rarity)
	return selection, nil
}

// legacyEquipmentBucket 给出回退档这一抽**真的用了**哪个 rarity 桶与几件候选：
// 与 `SelectGenericEquipment` 同一条判据（该桶为空且 rarity > 0 ⇒ 回落 rarity 0）。
// 只服务事件台账（`candidates=`）与按档直方图（`rarity_selected=`）。
func (c *pvfDungeonDropCatalog) legacyEquipmentBucket(
	monsterLevel int,
	gradeDown, gradeUp, rarity int64,
) (int64, int) {
	count := len(c.genericEquipmentCandidates(monsterLevel, gradeDown, gradeUp, rarity))
	if count == 0 && rarity > 0 {
		return 0, len(c.genericEquipmentCandidates(monsterLevel, gradeDown, gradeUp, 0))
	}
	return rarity, count
}

// ── reward 路径的三个入口（`dungeon_hell_party_reward.go` 的接入点）─────────
//
// 这三个方法定义在本文件、作用在 reward 文件的 `hellPartyRewardRequest` 上：
// 接入方先裁定一次"这一次走哪一档"（`hellPartyRewardTier`），再依次取 rate / 稀有度 / 装备。
// 开关、两张池子与表的差异全部留在这里。
//
// **降级（degrade）而不是报错**：hell 表或 `.equ` 池读不到时，这一次投骰**逐字节回到今天**
// （`rules.Probability` + `rollRarity` + `SelectGenericEquipment`），原因记在
// `hellPartyRewardTierDecision.Degraded` 里 ⇒ 奖励事件的 `degraded=` 字段 + **每会话一条**
// 的 `game-dungeon-hell-party-reward-pool-degraded` 事件都会把它暴露出来，
// 启动预热事件的 `built=false degraded=true` 是第三条（生产上"修复档没生效"不许静默）。
// 唯一的硬失败留给"两档都算不出来"（今天的行为：宁可零掉落也不发明数据）。

// 降级原因里出现的名字（事件字段 `reason=` 用）。
const (
	hellPartyRewardDegradedNoCatalog = "drop_catalog_unavailable"
	// hellPartyRewardLegacyRateSource 是"今天的读法"在事件里显示的名字。
	hellPartyRewardLegacyRateSource = "Etc/ItemDropInfo_Monseter.etc"
)

// hellPartyRewardTierDecision 是"这一次投骰走哪一档"的裁定（每次投骰算一次）。
type hellPartyRewardTierDecision struct {
	// UseFixedTier = true ⇒ rate/稀有度/取件都走厂商口径（`.equ` 池 + hell 表）。
	UseFixedTier bool
	// Rate / RateFound 只在 UseFixedTier 时有效（`[dungeon difficulty drop prob]` 按副本难度取列）。
	Rate      int64
	RateFound bool
	// RateSource 是事件字段 `rate_source=`。
	RateSource string
	// Degraded 非空 = 这一次没能走修复档的原因（空 = 走了修复档，或用户显式选了回退档）。
	Degraded string
}

// PoolName 是事件字段 `pool=` 的取值（`equ` / `itemdictionary`）。
func (decision hellPartyRewardTierDecision) PoolName() string {
	if decision.UseFixedTier {
		return hellPartyRewardPoolEquValue
	}
	return hellPartyRewardPoolItemDictionaryValue
}

// hellPartyRewardTier 裁定本次投骰的档位。任何"读不到"都降级成今天的行为，绝不报错、
// 也绝不发明一个率；调用方据此决定要不要走 `rules.Probability` 那一支。
func (request hellPartyRewardRequest) hellPartyRewardTier(dungeonDifficulty int64) hellPartyRewardTierDecision {
	decision := hellPartyRewardTierDecision{RateSource: hellPartyRewardLegacyRateSource}
	if hellPartyRewardPoolModeFromEnv() == hellPartyRewardPoolItemDictionary {
		// 显式回退档：这就是用户要的"今天"，不算降级。
		return decision
	}
	if request.dropCatalog == nil {
		decision.Degraded = hellPartyRewardDegradedNoCatalog
		return decision
	}
	info, err := request.dropCatalog.hellPartyRewardDropInfo()
	if err != nil {
		decision.Degraded = hellPartyRewardDegradedReason(hellPartyRewardDropInfoPath, err)
		return decision
	}
	if _, err := request.dropCatalog.hellPartyRewardEquipmentPool(); err != nil {
		// 池子读不到 ⇒ 即使 rate 走了修复档也只会空手，因此整体降级（含 rate）。
		decision.Degraded = hellPartyRewardDegradedReason(dungeonDropEquipmentList, err)
		return decision
	}
	rate, found := info.rateForLevel(request.DungeonBasisLevel, dungeonDifficulty)
	if !found {
		// 没有覆盖该等级的行 ⇒ 整档降级（`RateSource` 保持"今天的表"，原因里点名 hell 表）。
		decision.Degraded = fmt.Sprintf(
			"%s: no row covers basis_level=%d dungeon_difficulty=%d",
			hellPartyRewardDropInfoPath, request.DungeonBasisLevel, dungeonDifficulty)
		return decision
	}
	decision.UseFixedTier = true
	decision.Rate = rate
	decision.RateFound = true
	decision.RateSource = hellPartyRewardDropInfoPath
	return decision
}

// hellPartyRewardDegradedReason 把"哪条路径 + 什么错"压成一行可 grep 的原因。
func hellPartyRewardDegradedReason(path string, err error) string {
	if err == nil {
		return path
	}
	return path + ": " + err.Error()
}

// hellPartyRewardRarity 抽一个稀有度（两档都恰好消费一次 `lcg.Next(1000000)+1`）：
//
//	修复档 → `[basis of rarity dicision]`（hell 文件，首值 = 行数），行号 = 深渊档位 - 1；
//	降级/回退档 → 逐字节调用今天的 `request.rollRarity()`。
func (request hellPartyRewardRequest) hellPartyRewardRarity(
	decision hellPartyRewardTierDecision,
) (int64, string, error) {
	rarity, source, _, err := request.hellPartyRewardRarityRoll(decision)
	return rarity, source, err
}

// hellPartyRewardRarityRoll 与上一版逐字节相同，额外把**这一次抽出的原始均匀数**
// `lcg.Next(1000000) + 1 ∈ [1, 1000000]` 返回给调用方。
//
// 它服务的是"史诗落地概率"闸门（`dungeon_hell_party_reward_epic_window.go`）：
// 窗口判读只是对**已经抽出的那个数**再做一次区间比较，**不多抽也不少抽任何一个随机数**
// ⇒ 房间掉落 LCG 的轨迹逐位不变。
//
// 回退/降级两档没有 hell 表的稀有度抽（走今天的 `rollRarity()`），此时返回 `roll = 0`，
// 闸门据此**放行**（fail-open）：拿不到那个均匀数时闸门没有资格否决任何一抽。
func (request hellPartyRewardRequest) hellPartyRewardRarityRoll(
	decision hellPartyRewardTierDecision,
) (int64, string, int64, error) {
	if !decision.UseFixedTier {
		return request.rollRarity(), hellPartyRewardLegacyRateSource, 0, nil
	}
	info, err := request.dropCatalog.hellPartyRewardDropInfo()
	if err != nil {
		// 裁定之后表又读不到了（例如热更）：退回今天的映射，绝不改随机数次数。
		return request.rollRarity(), hellPartyRewardLegacyRateSource, 0, nil
	}
	roll := int64(request.rng.Next(hellPartyRewardRarityRollBound)) + 1
	return info.rarityForRoll(request.AbyssPartyDifficulty, roll), hellPartyRewardDropInfoPath, roll, nil
}

// hellPartyRewardEquipment 选一件装备（`ChooseEquipment` 的调用点）：
//
//	修复档 → `.equ` 池 + 厂商闭区间 `[min(minLv,basis), basis+7]`；
//	降级/回退档 → 逐字节调用今天的 `SelectGenericEquipment`
//	（`gradeDown/gradeUp` 就是今天那两个 `ValueA`）。
//
// `allowCommonFallback` 由稀有度闸门给出（默认档 `epic` ⇒ false ⇒ 空池不掉普通装备）。
func (request hellPartyRewardRequest) hellPartyRewardEquipment(
	decision hellPartyRewardTierDecision,
	rarity, gradeDown, gradeUp int64,
	allowCommonFallback bool,
) (hellPartyRewardEquipmentSelection, error) {
	if request.dropCatalog == nil {
		return hellPartyRewardEquipmentSelection{Pool: decision.PoolName()},
			errDungeonHellPartyRewardDropRule
	}
	return request.dropCatalog.hellPartyRewardEquipmentSelectionFor(
		decision,
		request.rng,
		int(request.DungeonBasisLevel),
		gradeDown,
		gradeUp,
		rarity,
		request.DungeonMinimumLevel,
		request.DungeonBasisLevel,
		allowCommonFallback,
	)
}

// selectEquipmentWithinRarities 是**稀有度闸门生效在抽取之前**那一档的取件：
// 候选 = 允许集合里六个 rarity 桶的**并集**（按 rarity 升序、每个桶保持池内原序），
// 然后**只抽一次**加权 `lcg.Next(totalWeight)` —— 与厂商 `ChooseEquipment`
// （`HellMonsterDropConfig.cs:314-360`）的抽签次数逐位相同，只是候选集合换成了并集。
//
// 语义（操作者 2026-09-29 口径「深渊要真的掉出史诗」）：闸门允许哪几档，
// 就**从哪几档里抽**；抽出来的那一件**一定**属于允许档 ⇒ 每一次命中都产出物品。
//
// 空并集时：只有 `allowCommonFallback`（= rarity 0 本身也被允许）才回落 rarity 0，
// 与厂商 `:341-348` 同口径；默认档 `epic` 的 `allowCommonFallback=false` ⇒ 不掉东西。
func (pool *hellPartyRewardEquipmentPool) selectEquipmentWithinRarities(
	rng *currentDungeonDropLCG,
	dungeonMinimumLevel, dungeonBasisLevel int64,
	allowed []int64,
	allowCommonFallback bool,
) (hellPartyRewardEquipmentSelection, error) {
	selection := hellPartyRewardEquipmentSelection{Pool: hellPartyRewardPoolEquValue}
	if rng == nil {
		return selection, errDungeonDropSourceRequired
	}
	if pool == nil {
		return selection, errDungeonHellPartyRewardDropRule
	}
	gradeMin, gradeMax, ok := hellPartyRewardGradeRangeForLevels(dungeonMinimumLevel, dungeonBasisLevel)
	if !ok {
		return selection, fmt.Errorf(
			"%w: minimum=%d basis=%d",
			errDungeonHellPartyRewardLevels,
			dungeonMinimumLevel,
			dungeonBasisLevel,
		)
	}
	selection.GradeMin, selection.GradeMax = gradeMin, gradeMax
	buckets := append([]int64(nil), allowed...)
	sort.Slice(buckets, func(left, right int) bool { return buckets[left] < buckets[right] })
	if len(buckets) == 0 {
		return selection, nil
	}
	type rarityBucket struct {
		rarity     int64
		candidates []dungeonDropWeightedItem
	}
	collect := func(rarities []int64) ([]rarityBucket, int64, int) {
		collected := make([]rarityBucket, 0, len(rarities))
		total := int64(0)
		candidateCount := 0
		for _, rarity := range rarities {
			bucket := pool.candidates(gradeMin, gradeMax, rarity)
			if len(bucket) == 0 {
				continue
			}
			collected = append(collected, rarityBucket{rarity: rarity, candidates: bucket})
			candidateCount += len(bucket)
			for _, candidate := range bucket {
				if candidate.ItemID == 0 || candidate.Weight == 0 {
					continue
				}
				total += int64(candidate.Weight)
			}
		}
		return collected, total, candidateCount
	}
	collected, total, candidateCount := collect(buckets)
	if total <= 0 && allowCommonFallback {
		// 厂商 `:341-348` 的"稀有度空池回落 rarity 0"：只有 rarity 0 本身被允许时才走。
		common := []int64{0}
		if buckets[0] == 0 {
			common = nil
		}
		if len(common) != 0 {
			collected, total, candidateCount = collect(common)
			selection.FallbackUsed = total > 0
		}
	}
	selection.CandidateCount = candidateCount
	if total <= 0 {
		return selection, nil
	}
	if total > math.MaxUint32 {
		return selection, fmt.Errorf("%w: total_weight=%d", errDungeonHellPartyRewardDropRule, total)
	}
	roll := int64(rng.Next(int(total)))
	var cumulative int64
	for _, bucket := range collected {
		for _, candidate := range bucket.candidates {
			if candidate.ItemID == 0 || candidate.Weight == 0 {
				continue
			}
			cumulative += int64(candidate.Weight)
			if roll < cumulative {
				selection.ItemID = candidate.ItemID
				selection.Selected = true
				selection.Rarity = bucket.rarity
				return selection, nil
			}
		}
	}
	last := collected[len(collected)-1]
	selection.ItemID = last.candidates[len(last.candidates)-1].ItemID
	selection.Selected = selection.ItemID != 0
	selection.Rarity = last.rarity
	return selection, nil
}

// hellPartyRewardEquipmentWithinRaritiesFor 是上面那条取件在**两档口径**下的分派：
// 修复档走 `.equ` 池的并集抽签；池子不可用时与 `hellPartyRewardEquipmentSelectionFor`
// 一样降级到今天的取件（并留下 `Degraded` 原因）。
func (c *pvfDungeonDropCatalog) hellPartyRewardEquipmentWithinRaritiesFor(
	decision hellPartyRewardTierDecision,
	rng *currentDungeonDropLCG,
	monsterLevel int,
	gradeDown, gradeUp int64,
	allowed []int64,
	dungeonMinimumLevel, dungeonBasisLevel int64,
	allowCommonFallback bool,
) (hellPartyRewardEquipmentSelection, error) {
	if decision.UseFixedTier {
		pool, err := c.hellPartyRewardEquipmentPool()
		if err == nil {
			return pool.selectEquipmentWithinRarities(
				rng, dungeonMinimumLevel, dungeonBasisLevel, allowed, allowCommonFallback,
			)
		}
		rarity := int64(0)
		if len(allowed) != 0 {
			rarity = allowed[0]
		}
		legacy, legacyErr := c.legacyEquipmentSelection(rng, monsterLevel, gradeDown, gradeUp, rarity)
		legacy.Degraded = hellPartyRewardDegradedReason(dungeonDropEquipmentList, err)
		return legacy, legacyErr
	}
	rarity := int64(0)
	if len(allowed) != 0 {
		rarity = allowed[0]
	}
	return c.legacyEquipmentSelection(rng, monsterLevel, gradeDown, gradeUp, rarity)
}

// hellPartyRewardEquipmentWithinRarities 是投骰循环用的入口（与
// `hellPartyRewardEquipment` 同形，只把"取哪一档"换成"允许集合内任意一档"）。
func (request hellPartyRewardRequest) hellPartyRewardEquipmentWithinRarities(
	decision hellPartyRewardTierDecision,
	allowed []int64,
	gradeDown, gradeUp int64,
	allowCommonFallback bool,
) (hellPartyRewardEquipmentSelection, error) {
	if request.dropCatalog == nil {
		return hellPartyRewardEquipmentSelection{Pool: decision.PoolName()},
			errDungeonHellPartyRewardDropRule
	}
	return request.dropCatalog.hellPartyRewardEquipmentWithinRaritiesFor(
		decision,
		request.rng,
		int(request.DungeonBasisLevel),
		gradeDown,
		gradeUp,
		allowed,
		request.DungeonMinimumLevel,
		request.DungeonBasisLevel,
		allowCommonFallback,
	)
}

// logHellPartyRewardPoolDegraded 在**降级**时发一条每会话一次的事件：
// 生产上"修复档没生效"必须一眼看得见，而不是看起来像"操作者运气不好"。
func (s *Service) logHellPartyRewardPoolDegraded(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	result hellPartyRewardResult,
) {
	if s == nil || session == nil || result.Degraded == "" {
		return
	}
	if _, loaded := hellPartyRewardDegradedSessions.LoadOrStore(session, struct{}{}); loaded {
		return
	}
	fields := []any{
		"reason", result.Degraded,
		"switch", hellPartyRewardPoolModeFromEnv().String(),
		"pool", result.Pool,
		"rate_source", result.RateSource,
		"hell_drop_info_path", hellPartyRewardDropInfoPath,
		"equipment_list_path", dungeonDropEquipmentList,
		"effect", "abyss_reward_falls_back_to_todays_monster_rules_and_itemdictionary_pool",
		"vendor_behavior", "HellMonsterDropConfig_EnsureLoaded_logs_INIT_FAILED_and_then_yields_no_equ_pool",
	}
	if runtime != nil {
		fields = append(fields,
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"hell_party_difficulty", runtime.HellPartyDifficulty,
		)
	}
	s.logGameEvent(session, "game-dungeon-hell-party-reward-pool-degraded", fields...)
}

// hellPartyRewardDegradedSessions 让降级事件每会话只发一次（与
// `currentDungeonMonsterDropRulesByCatalog` 同一种 sync.Map 惯例）。
var hellPartyRewardDegradedSessions sync.Map

// hellPartyRewardDropInfo 取（必要时读）hell 概率表，按 catalog 缓存。
func (c *pvfDungeonDropCatalog) hellPartyRewardDropInfo() (hellPartyRewardDropInfo, error) {
	if c == nil || c.source == nil {
		return hellPartyRewardDropInfo{}, errDungeonDropSourceRequired
	}
	c.mu.Lock()
	if c.hellPartyRewardDropInfoLoaded {
		info, err := c.hellPartyRewardDropInfoValue, c.hellPartyRewardDropInfoErr
		c.mu.Unlock()
		return info, err
	}
	c.mu.Unlock()

	info, err := loadHellPartyRewardDropInfo(c.source)
	c.mu.Lock()
	c.hellPartyRewardDropInfoValue = info
	c.hellPartyRewardDropInfoErr = err
	c.hellPartyRewardDropInfoLoaded = err == nil
	c.mu.Unlock()
	return info, err
}

// ── 每次进程一份的池子缓存（挂在 drop catalog 上，随 PVF source 走）────────

var hellPartyRewardPoolBuildMu sync.Mutex

// hellPartyRewardEquipmentPool 取（必要时建）本 catalog 的 `.equ` 池。单飞：
// 并发调用只有一个真的扫，其余等它。建失败不缓存"已加载"，下次还会再试
// （厂商 `HellMonsterDropConfig.cs:89-98` 也是失败后不再重试，但这里宁可重试，
// 因为本仓的 source 可能在热更后变化）。
func (c *pvfDungeonDropCatalog) hellPartyRewardEquipmentPool() (*hellPartyRewardEquipmentPool, error) {
	if c == nil || c.source == nil {
		return nil, errDungeonDropSourceRequired
	}
	c.mu.Lock()
	if c.hellPartyRewardPoolLoaded {
		pool, err := c.hellPartyRewardPool, c.hellPartyRewardPoolErr
		c.mu.Unlock()
		return pool, err
	}
	c.mu.Unlock()

	hellPartyRewardPoolBuildMu.Lock()
	defer hellPartyRewardPoolBuildMu.Unlock()
	c.mu.Lock()
	if c.hellPartyRewardPoolLoaded {
		pool, err := c.hellPartyRewardPool, c.hellPartyRewardPoolErr
		c.mu.Unlock()
		return pool, err
	}
	c.mu.Unlock()

	pool, err := loadHellPartyRewardEquipmentPool(c.source)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.hellPartyRewardPool = pool
	c.hellPartyRewardPoolErr = nil
	c.hellPartyRewardPoolLoaded = true
	c.mu.Unlock()
	return pool, nil
}

// hellPartyRewardPoolReadyEvent 是启动期激活记录的事件名（线上 grep 的锚点）：
// "修复档到底有没有生效"在 `packet_log.txt` 上只认这一行。
const hellPartyRewardPoolReadyEvent = "dnf-dungeon-hell-party-reward-pool-ready"

// hellPartyRewardPoolReadiness 是这条**激活记录**的唯一台账：预载阶段建没建池、
// 建成的规模、有没有降级，全部只在这里裁定；事件只是它的渲染（`fields()`）。
//
// ⚠ 历史缺陷（线上 2026-09-28 20:16:52，部署后的第一条记录）：这段代码曾经用**下标**
// 改写键值对数组 —— `fields[3] = true` 落在了 `pool` 的**值**上、`fields[4] = false`
// 落在了 `built` 的**键**上 —— 于是线上打出
//
//	path=etc/itemdropinfo_monster_hell.etc pool=true false=false degraded=true
//	reason=warmed pool_items=7107 pool_buckets=98 … scanned_equ_files=140862 build_ms=8626
//
// 一条自相矛盾的激活记录（7107 件物品却写着 `degraded=true`，且布尔值出现在键位）。
// 根因是"键与值分开写、按下标对接"：现在键值对只能由 `fields()` 一次生成，
// 任何下标改写都写不出来这种错位。字段名与含义不变，只是补齐/修正取值。
type hellPartyRewardPoolReadiness struct {
	// Pool 是 `pool=` 字段：`equ`（修复档）/ `itemdictionary`（操作者显式回退档）。
	Pool string
	// Built = 这一次真的把厂商口径的 `.equ` 池建出来了（下面的台账才有意义）。
	Built bool
	// Degraded = 没能建出可用的池，奖励路径届时逐字节回落今天（与
	// `hellPartyRewardTier` 同一口径）。
	Degraded bool
	// Reason 是 `reason=` 字段（可 grep 的短标签：warmed / pool_build_failed / …）。
	Reason string
	// PoolItems/PoolBuckets/PoolMinGrade/PoolMaxGrade/ScannedEquFiles/BuildMS 与
	// `hellPartyRewardEquipmentPool` 的台账逐字相等（池没建出来时全 0）。
	PoolItems       int
	PoolBuckets     int
	PoolMinGrade    int64
	PoolMaxGrade    int64
	ScannedEquFiles int
	BuildMS         int64
	// Error 只在失败时出现（`error=` 字段），是 `Reason` 的展开。
	Error string
	// RarityMode / RarityAllowed / RaritySwitch 是**稀有度闸门开关**的读数
	// （`rarity_mode=` / `rarity_allowed=` / `rarity_switch=`，操作者要求"深渊只掉史诗"）：
	// 与投骰事件同一口径（`RarityMode` = 开关档位、`RarityAllowed` = **生效**集合，
	// 回退/降级档下生效集合一定是全集 `0,1,2,3,4,5`）。启动期就能 grep 到，
	// 不必等第一次深渊击杀。见 `dungeon_hell_party_reward_rarity.go`。
	RarityMode    string
	RarityAllowed string
	RaritySwitch  string
	// EpicWindow / EpicWindowSwitch / EpicWindowSource 是**史诗落地概率闸门**的读数
	// （`epic_window=` / `epic_window_switch=` / `epic_window_source=`，操作者第 9 条
	// 「打 4-5 个深渊房间看到一个史诗装备」）：启动期就能 grep 到，
	// 不必等第一次深渊击杀。投骰事件的 `epic_window_active=` 才是"这一次真的在否决吗"。
	// 见 `dungeon_hell_party_reward_epic_window.go`。
	EpicWindow       int64
	EpicWindowSwitch string
	EpicWindowSource string
}

// consistent 是"这一行自洽吗"的判据（事件与用例同源）：
//
//	pool_items > 0  ⇒  built 且非 degraded（不许再出现 `pool_items=7107 degraded=true`）；
//	degraded        ⇒  一定没建成、pool_items == 0（"池子可用"与"建不出来"不可能混淆）。
//
// 唯一"没有池、也不算降级"的状态是操作者**显式**选的回退档（`pool=itemdictionary`）。
func (ready hellPartyRewardPoolReadiness) consistent() bool {
	if ready.PoolItems > 0 && (!ready.Built || ready.Degraded) {
		return false
	}
	if ready.Degraded && (ready.Built || ready.PoolItems != 0) {
		return false
	}
	return true
}

// fields 把这笔台账渲染成键值对 —— **唯一**的书写点：每个键恰好出现一次，
// 值一定紧跟在自己的键后面。
func (ready hellPartyRewardPoolReadiness) fields() []any {
	fields := []any{
		"path", hellPartyRewardDropInfoPath,
		"pool", ready.Pool,
		"built", ready.Built,
		"degraded", ready.Degraded,
		"reason", ready.Reason,
		"pool_items", ready.PoolItems,
		"pool_buckets", ready.PoolBuckets,
		"pool_min_grade", ready.PoolMinGrade,
		"pool_max_grade", ready.PoolMaxGrade,
		"scanned_equ_files", ready.ScannedEquFiles,
		"build_ms", ready.BuildMS,
		// 稀有度闸门开关读数（新增；上面的既有字段一个不改）。
		"rarity_mode", ready.RarityMode,
		"rarity_allowed", ready.RarityAllowed,
		"rarity_switch", ready.RaritySwitch,
		// 史诗落地概率闸门读数（新增）：这一档"每几个房间一件史诗"的开关状态。
		"epic_window", ready.EpicWindow,
		"epic_window_switch", ready.EpicWindowSwitch,
		"epic_window_source", ready.EpicWindowSource,
	}
	if ready.Error != "" {
		fields = append(fields, "error", ready.Error)
	}
	return fields
}

// hellPartyRewardPoolReadinessFor 在**预载**阶段把池子建好（厂商
// `DropService.cs:20 HellMonsterDropConfig.WarmUp()` 的等价物）：全树扫描是一次性的
// O(14 万文件) 工作，放在启动期（还没有玩家进图）就不必由第一次深渊击杀在包路径上承担。
//
// 回退档（itemdictionary）**不建池**：那一档必须逐字节等于今天，包括"没有这一次扫描"；
// 它是操作者的显式选择，因此**不算降级**（`degraded=false`，与 `hellPartyRewardTier` 一致）。
func hellPartyRewardPoolReadinessFor(monsterTable *pvfDungeonMonsterCatalog) hellPartyRewardPoolReadiness {
	mode := hellPartyRewardPoolModeFromEnv()
	ready := hellPartyRewardPoolReadiness{Pool: mode.String(), Reason: "-"}
	// 稀有度闸门读数（与投骰事件同一口径）：`rarity_allowed` 是**生效**集合 ——
	// 回退/降级档下闸门不参与，因此生效集合退回全集。
	plan := hellPartyRewardRarityPlanFromEnv()
	ready.RarityMode = plan.modeName()
	ready.RarityAllowed = plan.allowedList()
	ready.RaritySwitch = plan.switchText()
	// 史诗落地概率闸门读数（与投骰事件同一份解析）：`epic_window` 是**生效**宽度。
	window := abyssEpicWindowPlanFromEnv()
	ready.EpicWindow = window.Width
	ready.EpicWindowSwitch = window.switchText()
	ready.EpicWindowSource = window.Source
	if monsterTable == nil {
		ready.Degraded = true
		ready.Reason = "no_monster_catalog"
		ready.RarityAllowed = hellPartyRewardRarityAllList()
		return ready
	}
	if mode == hellPartyRewardPoolItemDictionary {
		ready.Reason = "rollback_tier_keeps_itemdictionary_pool"
		ready.RarityAllowed = hellPartyRewardRarityAllList()
		return ready
	}
	catalog, err := monsterTable.DropCatalog()
	if err != nil {
		ready.Degraded = true
		ready.Reason = "drop_catalog_unavailable"
		ready.Error = err.Error()
		ready.RarityAllowed = hellPartyRewardRarityAllList()
		return ready
	}
	pool, err := catalog.hellPartyRewardEquipmentPool()
	if err != nil {
		ready.Degraded = true
		ready.Reason = "pool_build_failed"
		ready.Error = err.Error()
		ready.RarityAllowed = hellPartyRewardRarityAllList()
		return ready
	}
	ready.Built = true
	ready.Reason = "warmed"
	ready.PoolItems = pool.Items
	ready.PoolBuckets = len(pool.Buckets)
	ready.PoolMinGrade = pool.MinGrade
	ready.PoolMaxGrade = pool.MaxGrade
	ready.ScannedEquFiles = pool.Scanned
	ready.BuildMS = pool.BuildNanos / int64(time.Millisecond)
	return ready
}

// hellPartyRewardPoolWarmUpFields 返回 `dnf-dungeon-hell-party-reward-pool-ready`
// 的键值对（预载调用点唯一使用）。
func hellPartyRewardPoolWarmUpFields(monsterTable *pvfDungeonMonsterCatalog) []any {
	return hellPartyRewardPoolReadinessFor(monsterTable).fields()
}
