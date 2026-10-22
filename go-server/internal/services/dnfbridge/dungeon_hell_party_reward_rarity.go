package dnfbridge

import (
	"os"
	"sort"
	"strconv"
	"strings"
)

// 本文件是「深渊只掉史诗」（操作者要求）的**稀有度闸门**本体。
//
// ── 操作者的原话与要求 ──────────────────────────────────────────────────────
//
//	「我记得这个版本不会掉落史诗以外的装备了 这些可以去掉」
//
// 即：这一版客户端的深渊奖励**只应产出史诗**（rarity 4），普通/高级/稀有/神器
// （rarity 0/1/2/3）必须从深渊奖励里去掉。线上实测（dungeon 310 / room `3:0` /
// map 60083 / hell_party_difficulty=1 tier=A）一次深渊掉 6 件：A 档
// `[basis of rarity dicision]` 行 = `0 599000 993000 993000 1000000` ⇒
// r1 59.9% / r2 39.4% / r4 0.7%，那 6 件几乎必然是 rarity 1/2。
//
// ── 只改"命中之后取哪一档"，不改"命中不命中" ────────────────────────────────
//
// 厂商 `HellMonsterDropConfig.cs:54-90` 每次投骰消费**恰好三个**随机数：
//
//	:57  chanceRoll = lcg.Next(1001)          概率（`chance = 100 * rate / 100`）
//	:259 roll       = lcg.Next(1000000) + 1   稀有度（`RollHellRarity`）
//	:261 roll       = lcg.Next(totalWeight)   加权取件（`ChooseEquipment`）
//
// 本闸门**一次都不少抽**：第二抽照抽（阈值表原样）、第三抽照抽（同一个
// `(grade, rarity)` 桶、同一套 `rarity 0 回落`口径），只是第三抽取出来的那件物品在
// "厂商抽到的稀有度不在允许集合里"时被**丢掉**并记进 `rolls_rarity_filtered`。
// ⇒ 房间掉落 LCG（`dungeon_combat_runtime.go:344 visit.DropRNG = rewardRequest.rng.Seed()`）
// 的轨迹、以及"一共命中几次"在两档下**逐位相同**（同种子跑完 `rng.Seed()` 相等，
// 由 `TestHellPartyRewardRarityGateKeepsTheProbabilityStreamIdentical` 与真档用例钉住）：
// **变的只有掉什么，不变的是一次深渊命中几次**。
//
// 这也是"为什么照抽还要丢掉"的答案：丢弃一抽的**结果**不影响任何后续随机数，
// 而少抽一抽会把整个房间之后的掉落序列整体挪位。
//
// ── 绝不降级（不回落到低稀有度）──────────────────────────────────────────────
//
// 厂商 `:246-250` 在"该稀有度空池"时回落 rarity 0（普通装备）。史诗档下这条回落会
// 直接把普通装备塞进深渊奖励，正好是操作者要去掉的东西 ⇒
// `allowCommonFallback = plan.allowsCommonFallback()`：
// **只有 rarity 0 本身在允许集合里时才回落**（默认档 `epic` 不在 ⇒ 关）。
// 关掉之后"允许档在波段里没有候选"的结果是**这一抽不掉东西**
// （`rolls_rarity_bucket_empty` 计数 + 同一抽的 `rarity_candidates` 台账），
// 而不是掉一件普通装备 —— 绝不静默降级。
//
// ── 开关 ────────────────────────────────────────────────────────────────────
//
// DNFBRIDGE_HELL_PARTY_REWARD_RARITY
//
//	epic（默认，= 操作者要求）：只允许 rarity 4。厂商的稀有度抽照抽（A 档有 0.7% 落在
//	    r4）—— 落在 r4 的那一抽取史诗，落在别的档的那一抽**什么都不掉**；
//	    rarity 4 在波段里没有候选时也不回落普通装备。
//	vendor（唯一回退字面量）：逐字节回到今天 —— hell 表的稀有度分布（59.9/39.4/0.7）
//	    + 厂商自己的 rarity 0 回落。
//	<稀有度列表>（`4`、`4,3`、`2,0` …）：允许集合 = 给定集合（去重升序）。`4` 与
//	    `epic` 等价；多档时"厂商抽到哪一档就取哪一档"，抽到集合外的那一抽不掉东西
//	    （`4,3` 在 A 档下与 `4` 同效：r3 的阈值区间 `(993000, 993000]` 是空集）。
//	未识别的取值（空串、拼错、越界数字、空 token）一律按**默认档**处理（与
//	    `readHellPartyRewardPoolMode` / `readHellPartyRewardRollMode` 同一惯例），
//	    原始取值照进事件字段 `rarity_switch=`，拼错不会被静默吞掉。
//
// ── 生效范围（为什么不作用在另外两档上）────────────────────────────────────
//
// 只在**修复档**（`pool=equ`，默认档）里生效。`POOL=itemdictionary` 与**降级**两档
// 逐字节等于今天（含低稀有度掉落）：
//
//  1. 史诗候选只存在于 `.equ` 池的 rarity 4 桶里（真档波段 [87,97] 共 248 件）；
//     `ItemDictionary.etc` 那张池子只到 grade 70，没有"史诗集合"这个概念的载体。
//  2. "降级 = 今天的读法"是本仓既有的一条硬不变量（`degraded=` 字段 + 每会话一条
//     `game-dungeon-hell-party-reward-pool-degraded`），由既有用例逐字节对账。
//
// 闸门没生效时事件会当场暴露：`rarity_allowed` 会退回全集 `0,1,2,3,4,5`，
// 而 `rarity_switch` 仍然是操作者设的那个值（例如 `epic`），
// `pool=` / `degraded=` 给出原因。
const (
	// hellPartyRewardRarityEnv 是稀有度闸门开关（消费点自读，与
	// `mysql_char.go:222` / `equip/owner.go:877` 同一惯例：options 结构体不在本改动里加字段）。
	hellPartyRewardRarityEnv = "DNFBRIDGE_HELL_PARTY_REWARD_RARITY"
	// hellPartyRewardRarityEpicValue 是默认档字面量（= 操作者要求：只掉史诗）。
	hellPartyRewardRarityEpicValue = "epic"
	// hellPartyRewardRarityVendorValue 是唯一被承认的回退取值（= 今天）。
	hellPartyRewardRarityVendorValue = "vendor"
	// hellPartyRewardRarityEpic 是"史诗"在厂商 `(long)grade * 10 + rarity` 键里的 rarity 值。
	hellPartyRewardRarityEpic = int64(4)
	// hellPartyRewardRarityMax 是厂商 `EquipmentDropPoolProvider.cs:101` 的上界
	// （`rarity < 0 || rarity > 5 ⇒ 丢`）⇒ 池子里只可能出现 rarity 0..5。
	hellPartyRewardRarityMax = int64(5)
	// hellPartyRewardRarityBuckets 是事件直方图的档数（rarity 0..5 共 6 档）。
	hellPartyRewardRarityBuckets = int(hellPartyRewardRarityMax) + 1
	// hellPartyRewardRarityUnsetText 是事件字段 `rarity_switch=` 在"开关未设置"时的取值。
	hellPartyRewardRarityUnsetText = "-"
)

type hellPartyRewardRarityMode int

const (
	// hellPartyRewardRarityEpicMode 是默认档（只允许 rarity 4）。
	hellPartyRewardRarityEpicMode hellPartyRewardRarityMode = iota
	// hellPartyRewardRarityVendorMode 是唯一回退档（= 今天）。
	hellPartyRewardRarityVendorMode
	// hellPartyRewardRarityListMode 是显式稀有度列表档。
	hellPartyRewardRarityListMode
)

// hellPartyRewardRarityPlan 是"这一次投骰允许哪些稀有度产出物品"的裁定：
// `Mode` 是开关档位（事件 `rarity_mode=`），`Allowed` 是允许集合（事件 `rarity_allowed=`），
// `Switch` 是操作者写的原始取值（事件 `rarity_switch=`，未设置 = `-`）。
type hellPartyRewardRarityPlan struct {
	Mode    hellPartyRewardRarityMode
	Allowed []int64
	Switch  string
}

// hellPartyRewardRarityModeName 是事件字段 `rarity_mode=` 的取值。
func (mode hellPartyRewardRarityMode) modeName() string {
	switch mode {
	case hellPartyRewardRarityVendorMode:
		return hellPartyRewardRarityVendorValue
	case hellPartyRewardRarityListMode:
		return "list"
	default:
		return hellPartyRewardRarityEpicValue
	}
}

// epicRarityPlan 是默认档（也是"任何未识别取值"的落点）。
func epicRarityPlan(raw string) hellPartyRewardRarityPlan {
	return hellPartyRewardRarityPlan{
		Mode:    hellPartyRewardRarityEpicMode,
		Allowed: []int64{hellPartyRewardRarityEpic},
		Switch:  raw,
	}
}

// hellPartyRewardRarityAll 是全集（rarity 0..5），回退/降级档的**生效**集合。
func hellPartyRewardRarityAll() []int64 {
	all := make([]int64, 0, hellPartyRewardRarityBuckets)
	for rarity := int64(0); rarity <= hellPartyRewardRarityMax; rarity++ {
		all = append(all, rarity)
	}
	return all
}

// readHellPartyRewardRarityPlan 只把 `epic`（默认）、`vendor`（唯一回退字面量）与
// "纯整数稀有度列表"三类取值当真；其余一切（含空串、拼错、越界、空 token）一律回
// **默认档**（与 `readHellPartyRewardPoolMode`、`readHellPartyRewardRollMode` 同一惯例）。
func readHellPartyRewardRarityPlan(value string) hellPartyRewardRarityPlan {
	raw := strings.TrimSpace(value)
	plan := epicRarityPlan(raw)
	switch {
	case raw == "":
		return epicRarityPlan("")
	case strings.EqualFold(raw, hellPartyRewardRarityEpicValue):
		return plan
	case strings.EqualFold(raw, hellPartyRewardRarityVendorValue):
		return hellPartyRewardRarityPlan{
			Mode:    hellPartyRewardRarityVendorMode,
			Allowed: hellPartyRewardRarityAll(),
			Switch:  raw,
		}
	}
	allowed := make([]int64, 0, hellPartyRewardRarityBuckets)
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		if !hellPartyRewardIsIntegerToken(token) {
			// 拼错/空 token：绝不猜一个集合，回默认档；原始取值留在 `rarity_switch=`。
			return plan
		}
		rarity, err := strconv.ParseInt(token, 10, 64)
		if err != nil || rarity < 0 || rarity > hellPartyRewardRarityMax {
			return plan
		}
		allowed = append(allowed, rarity)
	}
	if len(allowed) == 0 {
		return plan
	}
	sort.Slice(allowed, func(left, right int) bool { return allowed[left] < allowed[right] })
	unique := make([]int64, 0, len(allowed))
	for _, rarity := range allowed {
		if len(unique) == 0 || unique[len(unique)-1] != rarity {
			unique = append(unique, rarity)
		}
	}
	return hellPartyRewardRarityPlan{
		Mode:    hellPartyRewardRarityListMode,
		Allowed: unique,
		Switch:  raw,
	}
}

// hellPartyRewardRarityPlanFromEnv 在**消费点**自读开关。
func hellPartyRewardRarityPlanFromEnv() hellPartyRewardRarityPlan {
	return readHellPartyRewardRarityPlan(os.Getenv(hellPartyRewardRarityEnv))
}

// allows 报告"厂商抽到这个稀有度时，这一抽允不允许产出物品"。
func (plan hellPartyRewardRarityPlan) allows(rarity int64) bool {
	for _, allowed := range plan.Allowed {
		if allowed == rarity {
			return true
		}
	}
	return false
}

// allowsCommonFallback 报告"要不要保留厂商 `:246-250` 的 rarity 0 回落"：
// 只有 rarity 0 本身在允许集合里时才留（默认档 `epic` ⇒ 关 ⇒ 空池不掉东西，绝不降级）。
func (plan hellPartyRewardRarityPlan) allowsCommonFallback() bool {
	return plan.allows(0)
}

// modeName 是事件字段 `rarity_mode=`。
func (plan hellPartyRewardRarityPlan) modeName() string { return plan.Mode.modeName() }

// allowedList 是事件字段 `rarity_allowed=`（升序、逗号分隔）。
func (plan hellPartyRewardRarityPlan) allowedList() string {
	parts := make([]string, 0, len(plan.Allowed))
	for _, rarity := range plan.Allowed {
		parts = append(parts, strconv.FormatInt(rarity, 10))
	}
	return strings.Join(parts, ",")
}

// hellPartyRewardRarityAllList 渲染全集 `0,1,2,3,4,5`：回退/降级档下
// `rarity_allowed=` 就是这个值（那两档的生效集合就是今天，闸门不参与）。
func hellPartyRewardRarityAllList() string {
	return hellPartyRewardRarityPlan{Allowed: hellPartyRewardRarityAll()}.allowedList()
}

// switchText 是事件字段 `rarity_switch=`（操作者写的原始取值，未设置 = `-`）。
func (plan hellPartyRewardRarityPlan) switchText() string {
	if plan.Switch == "" {
		return hellPartyRewardRarityUnsetText
	}
	return plan.Switch
}

// hellPartyRewardRarityVerdict 是"这一抽稀有度抽到了什么 + 闸门开不开"的裁定：
// `Drawn` 是厂商 `RollHellRarity` 的结果（**两档都照抽**），`Allowed` 是闸门，
// `Fallback` 是这一抽取件时要不要保留厂商的 rarity 0 回落，`Source` 是稀有度的来源表。
type hellPartyRewardRarityVerdict struct {
	Drawn    int64
	Allowed  bool
	Fallback bool
	Source   string
	// Roll 是这一次稀有度抽签的**原始均匀数** `lcg.Next(1000000) + 1 ∈ [1, 1000000]`
	// （`Drawn` 只是它按 hell 表阈值映射出来的档位）。史诗落地概率闸门
	// `DNFBRIDGE_ABYSS_EPIC_WINDOW` 就是对这个数再做一次区间判读 —— **不额外抽随机数**，
	// 所以它一个字节都不影响随机数轨迹（见 `dungeon_hell_party_reward_epic_window.go`）。
	// 取不到它时（回退/降级/热更换：稀有度来自今天的表）恒为 0，闸门因此**放行**。
	Roll int64
}

// hellPartyRewardRarityVerdict 抽一次稀有度并给出闸门裁定。
//
// 随机数消费与 `hellPartyRewardRarity` 逐字节相同：修复档恰好一次
// `lcg.Next(1000000)+1`（hell 表阈值），回退/降级档恰好一次
// `request.rollRarity()`（今天的表）。裁定只是**读**，不额外抽任何随机数；
// `Roll` 是那一次抽签的原始均匀数（供史诗窗口判读，同样是**只读**）。
func (request hellPartyRewardRequest) hellPartyRewardRarityVerdict(
	decision hellPartyRewardTierDecision,
	plan hellPartyRewardRarityPlan,
) (hellPartyRewardRarityVerdict, error) {
	drawn, source, roll, err := request.hellPartyRewardRarityRoll(decision)
	if err != nil {
		return hellPartyRewardRarityVerdict{}, err
	}
	verdict := hellPartyRewardRarityVerdict{
		Drawn:    drawn,
		Source:   source,
		Roll:     roll,
		Allowed:  true,
		Fallback: true,
	}
	if !decision.UseFixedTier {
		// 回退（`POOL=itemdictionary`）/降级两档 = 今天：闸门全开、保留厂商的
		// rarity 0 回落。见文件头"生效范围"。
		return verdict, nil
	}
	verdict.Allowed = plan.allows(drawn)
	verdict.Fallback = plan.allowsCommonFallback()
	return verdict, nil
}

// restricts 报告这一档是否**真的收窄了**稀有度集合（收窄才谈得上"在集合内抽"）。
// 全集 `0,1,2,3,4,5` 与空集合都不算收窄（后者由调用方的 `allows` 逐档判定）。
func (plan hellPartyRewardRarityPlan) restricts() bool {
	all := hellPartyRewardRarityAll()
	if len(plan.Allowed) == 0 || len(plan.Allowed) >= len(all) {
		return false
	}
	for _, rarity := range all {
		if !plan.allows(rarity) {
			return true
		}
	}
	return false
}

// ── 闸门与抽取的**先后次序**（操作者 2026-09-29：「深渊要真的掉出史诗」）──────────
//
// 现场（`_dist/abyss-epic-roll-order.md` §1，线上原文）：默认档 `rarity_mode=epic
// rarity_allowed=4` 下，每一抽都是"先按 hell 表抽稀有度（r1/r2），再从**那一档**的桶里
// 取件，最后才用 allowed 过滤"⇒ `rolls_rarity_filtered=rarity_rolls`、`selected_count=0`
// —— 因为史诗档在 A 档阈值里只有 0.7%，**深渊实际上一件都不掉**。
//
// 厂商 `HellMonsterDropConfig.cs:63-89` **没有**任何稀有度过滤：抽到哪一档就从哪一档取件
// （`:69-79 allowFallback: true`），只有"桶空且回落也空"才不掉（`:80-84`）。
// 所以这不是厂商缺陷，是**我们**在上一轮加闸门时把次序放反了。
//
// DNFBRIDGE_HELL_PARTY_REWARD_RARITY_ORDER
//
//	未设置/空/`within`/任意其它值 = 修复档（默认）：**在允许集合内抽**——
//	    候选 = 允许档六个桶的并集，抽一次加权（与厂商同一次抽签）⇒ 每一次命中都产出
//	    允许档的物品（默认档 = 每次命中都是一件史诗）。
//	字面量 `post_filter`（也接受 `legacy`）= 逐字节回到今天：先按厂商抽到的档取件、
//	    再用 allowed 过滤掉（= `rarity_selected` 恒为 0 的那一版）。
const hellPartyRewardRarityOrderEnv = "DNFBRIDGE_HELL_PARTY_REWARD_RARITY_ORDER"

const (
	hellPartyRewardRarityOrderWithinValue     = "within"
	hellPartyRewardRarityOrderPostFilterValue = "post_filter"
)

type hellPartyRewardRarityOrderMode int

const (
	// hellPartyRewardRarityOrderWithin 是默认档，也是修复档（零值 = 修复）。
	hellPartyRewardRarityOrderWithin hellPartyRewardRarityOrderMode = iota
	// hellPartyRewardRarityOrderPostFilter 是改动前：抽完再过滤。
	hellPartyRewardRarityOrderPostFilter
)

func readHellPartyRewardRarityOrderMode(value string) hellPartyRewardRarityOrderMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case hellPartyRewardRarityOrderPostFilterValue, abyssItemLevelLegacyValue, "off":
		return hellPartyRewardRarityOrderPostFilter
	default:
		return hellPartyRewardRarityOrderWithin
	}
}

func (mode hellPartyRewardRarityOrderMode) String() string {
	if mode == hellPartyRewardRarityOrderPostFilter {
		return hellPartyRewardRarityOrderPostFilterValue
	}
	return hellPartyRewardRarityOrderWithinValue
}

// within 报告该档位是否"闸门生效在抽取之前"。
func (mode hellPartyRewardRarityOrderMode) within() bool {
	return mode == hellPartyRewardRarityOrderWithin
}

func hellPartyRewardRarityOrderModeFromEnv() hellPartyRewardRarityOrderMode {
	return readHellPartyRewardRarityOrderMode(os.Getenv(hellPartyRewardRarityOrderEnv))
}

// hellPartyRewardRarityIndex 把稀有度夹进直方图下标（0..5）。
func hellPartyRewardRarityIndex(rarity int64) int {
	if rarity < 0 {
		return 0
	}
	if rarity > hellPartyRewardRarityMax {
		return hellPartyRewardRarityBuckets - 1
	}
	return int(rarity)
}

// hellPartyRewardRarityHistogram 把按档计数渲染成一行可 grep 的
// `r0:68,r1:68,r2:135,r3:429,r4:248,r5:0`。
func hellPartyRewardRarityHistogram(counts [hellPartyRewardRarityBuckets]int) string {
	parts := make([]string, 0, hellPartyRewardRarityBuckets)
	for rarity := 0; rarity < hellPartyRewardRarityBuckets; rarity++ {
		parts = append(parts, "r"+strconv.Itoa(rarity)+":"+strconv.Itoa(counts[rarity]))
	}
	return strings.Join(parts, ",")
}

// hellPartyRewardRarityCandidatesText 渲染"本波段六个桶各有几件候选"；
// `known == false`（回退/降级档没有 `.equ` 池）时打 `-`，绝不假装 0 件。
func hellPartyRewardRarityCandidatesText(
	counts [hellPartyRewardRarityBuckets]int,
	known bool,
) string {
	if !known {
		return hellPartyRewardRarityUnsetText
	}
	return hellPartyRewardRarityHistogram(counts)
}

// hellPartyRewardRarityCandidateCounts 算本波段（厂商闭区间 `[min(minLv,basis), basis+7]`）
// 里六个 rarity 桶各有几件候选（事件字段 `rarity_candidates=`）。
// 只在**修复档**且有 `.equ` 池时有意义；回退/降级档返回 `known == false`。
func (request hellPartyRewardRequest) hellPartyRewardRarityCandidateCounts(
	decision hellPartyRewardTierDecision,
) ([hellPartyRewardRarityBuckets]int, bool) {
	var counts [hellPartyRewardRarityBuckets]int
	if !decision.UseFixedTier || request.dropCatalog == nil {
		return counts, false
	}
	pool, err := request.dropCatalog.hellPartyRewardEquipmentPool()
	if err != nil {
		return counts, false
	}
	gradeMin, gradeMax, ok := hellPartyRewardGradeRangeForLevels(
		request.DungeonMinimumLevel, request.DungeonBasisLevel)
	if !ok {
		return counts, false
	}
	return pool.rarityCandidateCounts(gradeMin, gradeMax, abyssItemLevelBandFromEnv().forDungeon(request.DungeonMinimumLevel, request.DungeonBasisLevel)), true
}
