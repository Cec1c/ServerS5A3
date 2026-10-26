package dnfbridge

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	dnfdrops "longheng.io/server/internal/modules/dnf/drop"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件是"深渊柱子/怪物已正常但**无掉落物**"（操作者清单 batch item ②）的 Go 侧修复。
//
// ── 厂商口径（唯一依据）──────────────────────────────────────────────────────
//
//  1. 深渊怪的死亡奖励走一条**独立的投骰路径**，入口在
//     `Network\Handlers\Dungeon\DungeonKillApplicationService.cs:908-921`：
//     当 `monster.IsHellPartyActor && roomState.IsHellPartyRoom` 时，**不**走普通怪掉落，
//     改调 `_services.Drops.GenerateAbyssPartyAndRegister(run, BuildAbyssPartyDropRequest(...))`。
//  2. 请求体 `:1548-1585 BuildAbyssPartyDropRequest`：`RewardRollCount = monster.HellRewardRollCount`
//     （`:1580`）；`IsLastGroupMonster` 由 `roomState.HellPartyGroupRemaining` 逐只递减得出
//     （`:1555-1572`，只剩 0 只的那一只才是最后一只）。
//  3. 投骰次数与门槛：`monster.HellRewardRollCount` 在**装配波次**那一刻按难度档位冻结 ——
//     `GameWorld\Dungeon.cs:1236-1241` 取 `HellPartyData.GetDifficultyRule(difficulty).RewardRollCount`
//     （`Math.Max(0, …)`），再写进每一只波次怪的 `MonsterSumInfo`（`:1264-1275`，`:1273`）。
//     档位表来自 `etc/hellparty.etc [difficulty]`，`HellPartyData.cs:253-261` 的字段映射是
//     `RewardRollCount = values[0]`、`Unknown1 = values[1]`、`RatioProbability = values[2]`、
//     `Probability = values[3]`、`Unknown4 = values[4]`。
//     真档那一行是 `4 `A` 8 5 100 100 0 …` ⇒ A 档 = **8 次投骰**。
//  4. 消费点 `Game\Dungeon\DropService.cs:159-205 GenerateAbyssPartyAndRegister`：
//     `:179` `if (request.IsLastGroupMonster && !request.IsAbyssMonsterScript)`
//     —— **只有每组的最后一只**才发奖，其余只走 `IndependentDropSystem`（`:168-177`）。
//     奖励本体 `:181-191` 调 `HellMonsterDropConfig.GenerateSpecificEquipmentDrops(
//     lcg, DungeonMinimumLevel, DungeonBasisLevel, run.Difficulty, AbyssPartyDifficulty,
//     RewardRollCount, epicBuffPotionActive, ref slotCounter)`。
//  5. 投骰本体 `Game\Dungeon\HellMonsterDropConfig.cs:28-93`：
//     - `:40` `if (lcg == null || rewardRollCount <= 0) return result;`（0 次 = 不投）
//     - `:43-50` `FindDropProb(itemBasisLevel)` 查 `etc/itemdropinfo_monster_hell.etc`
//     `[dungeon difficulty drop prob]`，`rate = DifficultyRates[run.Difficulty]`，`rate <= 0` 直接返回
//     - `:53` `chance = BaseDropRate * rate / 100`（`BaseDropRate = 100`，`:9`；整数除法）
//     - `:54-61` `for i < rewardRollCount`：`chanceRoll = lcg.Next(DropChanceDenominator)`
//     （`DropChanceDenominator = 1001`，`:10`），`chanceRoll > chance` ⇒ miss
//     - `:63-67` `RollHellRarity(lcg, hellDifficulty)`（`:247-267`）：
//     `:259` `roll = lcg.Next(1000000) + 1`，`:254` 行号按 `hellDifficulty - 1` 取
//     `[basis of rarity dicision]`，`:263` `roll <= threshold` ⇒ 该档
//     - `:69-79` `ChooseEquipment(...)`（`:314-369`）从 `EquipmentDropPoolProvider.GetPool()`
//     按 `(grade, rarity)` 取加权候选；`:360-366` `lcg.Next(totalWeight)` +
//     `roll < cumulative` 加权抽一；`:341-349` 稀有度空池回落到 rarity 0
//     - `:86-89` **命中即落地**：`slotCounter++; result.Add(DropInfo.CreateItem(slotCounter, itemId, 1))`
//
// ── 与厂商的**已声明差异**（本仓口径，不是等价替换）────────────────────────
//
//  1. **等级区间的来源**：厂商走 `EquipmentDropLevelRule.TryGetAtlasGradeRange(
//     dungeonMinimumLevel, itemBasisLevel, …)`（图鉴等级规则表），本仓没有这张表，
//     改用同一份 `[item drop ref table]` 在两个基准等级上的两行当区间端点
//     （`grade ∈ [basis - row(minimum).ValueA, basis + row(basis).ValueA)`，语义照抄
//     `dungeon_drop_catalog.go:429-489 collectGenericEquipmentLocked`，
//     与既有普通怪掉落分支**同一口径**）。真档 310 实测 minimum=10 / basis=90 ⇒
//     `[84, 93)`。
//  2. **EPIC 概率药水重投**：厂商 `:64-68 ApplyEpicBuffPotionRarityReroll` 本仓没有对应
//     定义源，**未实现**（口径等同"药水未激活"）。这不改变投骰次数与随机数消耗顺序，
//     只在"该不该重投稀有度"这一点上比厂商少一次 `lcg.Next(1000000)`。
//  3. **史诗碎片**：厂商 `DropService.cs:194-198 GenerateAbyssEpicPieceDrops` 走
//     `EpicPieceDropService`（另一套目录/表），本仓没有它的数据源 ⇒ 未实现。
//     这是**第二个**已知缺口，与本文件的 `RewardRollCount` 主线无关。
//  6. 落地的物品**不邮寄**：`DropService.cs:453-465 RegisterDrops` 把它们塞进
//     `run.Drops[SceneSlot]`（地面掉落账本），随后由
//     `DungeonKillApplicationService.cs:1418-1431 SendMonsterDieAsync` 把它们随
//     DIE_MONSTER(op38) 一起下发 ⇒ 客户端在地上看见、走过去捡。
//     ⇒ 本仓**同一条路**：注册进 `runtimeDungeonDropOwner` + 随 op38 下发 + 走既有
//     `dungeon_drop_pickup.go` 拾取入包。不发明第二条发放路径，也不用邮件。
//
// ── 本仓原先的状态（已核对，不是推断）──────────────────────────────────────
//
// `RewardRollCount` 在全仓 `*.go` 里共 **7 处**、**0 个生产消费者**：
//
//	internal/services/dnfbridge/dungeon_hell_party_catalog.go:80   （结构体字段声明）
//	internal/services/dnfbridge/dungeon_hell_party_catalog.go:250  （解析赋值 numbers[0]）
//	internal/services/dnfbridge/dungeon_hell_party_wave_real_pvf_test.go:73,78（用例断言）
//	internal/services/dnfbridge/dungeon_hell_party_wave_test.go:133,137      （用例断言）
//	（另一处是 `dungeon_hell_party_catalog.go:41` 的注释文字）
//
// ⇒ 规则被读进来了，**从来没有人用它投过一次骰**。本文件补上唯一的消费者。
//
// ── 开关 ───────────────────────────────────────────────────────────────────
//
// DNFBRIDGE_HELL_PARTY_REWARD_ROLL
//
//	vendor（默认，= 修复档）：波次组最后一只死亡时按 `etc/hellparty.etc` 的
//	    `RewardRollCount` 投骰，命中就按既有地面掉落通路发。
//	legacy（一键回退）      ：**一个随机数都不抽、一条掉落都不注册**，
//	    逐字节、逐随机序列等于改动前。
const hellPartyRewardRollEnv = "DNFBRIDGE_HELL_PARTY_REWARD_ROLL"

// hellPartyRewardRollLegacyValue 是唯一被承认的回退取值。
const hellPartyRewardRollLegacyValue = "legacy"

type hellPartyRewardRollMode int

const (
	// hellPartyRewardRollVendor 是默认档，也是修复档（零值 = 修复）。
	hellPartyRewardRollVendor hellPartyRewardRollMode = iota
	// hellPartyRewardRollLegacy 是改动前的"规则读了但不用"档。
	hellPartyRewardRollLegacy
)

func readHellPartyRewardRollMode(value string) hellPartyRewardRollMode {
	if strings.EqualFold(strings.TrimSpace(value), hellPartyRewardRollLegacyValue) {
		return hellPartyRewardRollLegacy
	}
	return hellPartyRewardRollVendor
}

func (mode hellPartyRewardRollMode) String() string {
	if mode == hellPartyRewardRollLegacy {
		return hellPartyRewardRollLegacyValue
	}
	return "vendor"
}

// rolls 报告该档位是否真的消费 `RewardRollCount`。
func (mode hellPartyRewardRollMode) rolls() bool { return mode == hellPartyRewardRollVendor }

// hellPartyRewardRollModeFromEnv 在**消费点**自读开关（与
// `mysql_char.go:222` / `equip/owner.go:877` / `gmtool/online_guard.go:67` 同一惯例：
// options 结构体处在并发编辑下，不往里加字段）。
func hellPartyRewardRollModeFromEnv() hellPartyRewardRollMode {
	return readHellPartyRewardRollMode(os.Getenv(hellPartyRewardRollEnv))
}

// 厂商常量（file:line 见文件头）。
const (
	// hellPartyRewardBaseDropRate = `HellMonsterDropConfig.cs:9 BaseDropRate = 100`。
	hellPartyRewardBaseDropRate = 100
	// hellPartyRewardDropChanceDenominator = `HellMonsterDropConfig.cs:10 DropChanceDenominator = 1001`。
	hellPartyRewardDropChanceDenominator = 1001
	// hellPartyRewardRarityRollBound = `HellMonsterDropConfig.cs:259 lcg.Next(1000000) + 1`。
	hellPartyRewardRarityRollBound = 1000000
	// hellPartyRewardMaxRarityRow = `HellMonsterDropConfig.cs:260 Math.Min(6, thresholds.Length)`。
	hellPartyRewardMaxRarityRow = 6
	// hellPartyRewardChancePercentDivisor = `HellMonsterDropConfig.cs:53 BaseDropRate * rate / 100`。
	hellPartyRewardChancePercentDivisor = 100
)

var (
	errDungeonHellPartyRewardLevels   = errors.New("dnf dungeon hell party reward dungeon levels are unusable")
	errDungeonHellPartyRewardDropRule = errors.New("dnf dungeon hell party reward drop rule is unavailable")
)

// hellPartyRewardRequest 是一次"组最后一只死亡"的奖励输入。
// 字段名照厂商 `AbyssPartyDropRequest`（`DropService.cs:637-647`）。
type hellPartyRewardRequest struct {
	MonsterCode         int64
	DungeonMinimumLevel int64
	DungeonBasisLevel   int64
	// DungeonDifficulty 是副本难度 `runtime.Request.Difficulty`（0..4）：
	// 厂商 `DropService.cs` 传给 `GenerateSpecificEquipmentDrops` 的是 `run.Difficulty`，
	// `HellMonsterDropConfig.cs:47-48` 用它取 rate 列。
	DungeonDifficulty    int64
	AbyssPartyDifficulty byte
	RewardRollCount      int64
	GroupID              int64

	dungeonMonsterCatalog *pvfDungeonMonsterCatalog
	dropCatalog           *pvfDungeonDropCatalog
	rules                 *dnfdrops.MonsterRules
	rng                   *currentDungeonDropLCG
}

// hellPartyRewardItem 是一条落地的奖励（照 `DropInfo.CreateItem(slot, itemId, 1)`）。
type hellPartyRewardItem struct {
	ItemID uint32
	Amount uint32
}

// hellPartyRewardResult 是一次奖励投骰的完整台账（供事件与测试对账）。
//
// Misses 只统计厂商口径的"概率没过"（`HellMonsterDropConfig.cs:54-61`），
// NoDrop 单独统计"概率过了但这一抽没有物品"（厂商 `:80-84` 那条 `continue`）；
// 后者按原因拆成三个计数器（`RarityFiltered` / `RarityBucketEmpty` / `EpicGated`，
// 见稀有度闸门 `dungeon_hell_party_reward_rarity.go` 与史诗窗口
// `dungeon_hell_party_reward_epic_window.go`），不变量
// `NoDrop == RarityFiltered + RarityBucketEmpty + EpicGated` 在所有档位下都成立。
type hellPartyRewardResult struct {
	GroupID         int64
	RewardRollCount int64
	Rate            int64
	RateSource      string
	Chance          int
	Rolls           int
	Misses          int
	NoDrop          int
	Pool            string
	CandidateCount  int
	GradeMin        int64
	GradeMax        int64
	// ── 稀有度闸门台账（操作者要求：深渊只掉史诗）──────────────────────────
	// RarityMode 是开关档位（`epic`/`vendor`/`list`）；RarityAllowed 是**生效**集合
	// （回退/降级档一定是全集 `0,1,2,3,4,5`）；RaritySwitch 是操作者写的原始取值。
	RarityMode    string
	RarityAllowed string
	RaritySwitch  string
	// RarityOrder 是"闸门与抽取的先后次序"档位：`within`（默认，= 在允许集合内抽，
	// 每次命中都产出允许档物品）或 `post_filter`（改动前：抽完再过滤）。
	// 见 `DNFBRIDGE_HELL_PARTY_REWARD_RARITY_ORDER`。
	RarityOrder string
	// RarityRolls 是**真的抽了几次稀有度**（= 概率命中的次数）：与 `vendor` 档逐位相等，
	// 是"只换掉掉什么、没换掉命中几次"最直接的证据。
	RarityRolls int
	// RarityDrawn 是厂商 `RollHellRarity` 抽出来的稀有度分布（两档相同）。
	RarityDrawn [hellPartyRewardRarityBuckets]int
	// RaritySelected 是**真的落地**的稀有度分布（默认档只可能 r4 > 0）。
	RaritySelected [hellPartyRewardRarityBuckets]int
	// RarityCandidates 是本波段六个 rarity 桶各有几件候选（只有修复档 known）。
	RarityCandidates      [hellPartyRewardRarityBuckets]int
	RarityCandidatesKnown bool
	// RarityFiltered = 命中、但厂商抽到的稀有度不在生效集合里 ⇒ 这一抽不掉东西
	// （默认档下这是绝大多数命中：A 档落在 r4 的只有 0.7%）。**绝不降级**。
	RarityFiltered int
	// RarityBucketEmpty = 命中且稀有度被允许，但该桶在波段里没有候选 ⇒ 这一抽不掉
	// 东西（厂商口径会回落 rarity 0，本档**不回落**）。
	RarityBucketEmpty int
	// ── 史诗落地概率闸门（操作者 2026-09-29 第 9 条：打 4–5 个房间看到 1 件）──────
	// EpicWindow 是**生效**窗口宽度（每百万）：命中后落地的概率 = `EpicWindow/1e6`。
	// `EpicWindowSwitch` 是操作者写的原始取值、`EpicWindowSource` 是它的来源
	// （`builtin_default` / `env` / `alias:<字面量>`）。见
	// `dungeon_hell_party_reward_epic_window.go`（算式在那份文件的头部）。
	EpicWindow       int64
	EpicWindowSwitch string
	EpicWindowSource string
	// EpicWindowActive = 这一次投骰**真的**在按窗口否决命中
	// （修复档 + 收窄闸门 + `within` 次序）。其余各档逐字节等于今天，闸门不参与。
	EpicWindowActive bool
	// EpicExpectationPPM = 这一次结算期望落地几件史诗 × 1e6
	// （`8 抽 × 876/1001 × 窗口`；闸门没生效时恒为 0，绝不假装一个数。
	// 默认档下是**准确值**，显式列表档下只当上界看，见 `abyssEpicExpectationPPM`）。
	EpicExpectationPPM int64
	// EpicGated = 命中、稀有度也允许、但**这一抽的稀有度抽签没有落进史诗窗口**
	// ⇒ 这一抽不掉东西。它和 `RarityFiltered`（抽到了不允许的档）是**不同**的原因：
	// 前者是"这一次运气不够"，后者是"这一档根本不许掉"。
	EpicGated int
	// Degraded 非空 = 这一次没能走修复档（hell 表或 `.equ` 池读不到/没有覆盖行），
	// 已按今天的三处读法降级；事件 `degraded=` 与每会话一条的
	// `game-dungeon-hell-party-reward-pool-degraded` 会把它暴露出来。
	Degraded string
	Selected []hellPartyRewardItem
}

// hellPartyRewardRolls 逐行对应厂商 `HellMonsterDropConfig.cs:28-93`
// `GenerateSpecificEquipmentDrops`（`lcg → rng`，`slotCounter` 的分配留在注册侧）。
//
// 每次投骰的随机数消费与厂商逐位相同：`lcg.Next(1001)` 概率 → `lcg.Next(1000000)+1`
// 稀有度 → `lcg.Next(totalWeight)` 加权取件（`dungeon_hell_party_reward_pool.go`
// 的 `.equ` 池；回退/降级档才是 `dungeon_drop_catalog.go:385-427` 的
// `SelectGenericEquipment`，逐字节 = 今天）。
//
// 在这之上多了一层**稀有度闸门**（操作者要求：深渊只掉史诗，见
// `dungeon_hell_party_reward_rarity.go`）：三抽照抽，只在"厂商抽到的稀有度不在生效
// 集合里"时把这一抽的**结果**丢掉（`rolls_rarity_filtered`），绝不降级成低稀有度物品。
//
// 再往上还有一层**史诗落地概率闸门**（操作者 2026-09-29 第 9 条「打 4-5 个深渊房间
// 看到一个史诗装备」，见 `dungeon_hell_party_reward_epic_window.go`）：`within` 档下
// 每一次命中都会取出一件史诗，本闸门按**已经抽出的那个稀有度随机数**再判一次区间
// （`epic_window`），只有落进窗口顶端的那几次才真的落地（`rolls_epic_window`）。
// 它同样**不多抽也不少抽任何随机数** ⇒ 房间掉落 LCG 轨迹逐位不变。
func hellPartyRewardRolls(request hellPartyRewardRequest) (hellPartyRewardResult, error) {
	result := hellPartyRewardResult{GroupID: request.GroupID, RewardRollCount: request.RewardRollCount}
	// 稀有度闸门（操作者要求：深渊只掉史诗，见 `dungeon_hell_party_reward_rarity.go`）：
	// **先**读开关，这样任何提前返回也把三个开关读数带进事件
	// （`rarity_mode=` / `rarity_allowed=` / `rarity_switch=`）。
	plan := hellPartyRewardRarityPlanFromEnv()
	result.RarityMode = plan.modeName()
	result.RarityAllowed = plan.allowedList()
	result.RaritySwitch = plan.switchText()
	// 史诗落地概率闸门（操作者 2026-09-29 第 9 条「打 4-5 个深渊房间看到一个史诗装备」）：
	// 同样**先**读，任何提前返回也把读数带进事件。
	epicWindow := abyssEpicWindowPlanFromEnv()
	result.EpicWindow = epicWindow.Width
	result.EpicWindowSwitch = epicWindow.switchText()
	result.EpicWindowSource = epicWindow.Source
	if request.rng == nil || request.rewardRollCount() <= 0 {
		return result, nil
	}
	if request.dungeonMonsterCatalog == nil || request.dropCatalog == nil || request.rules == nil {
		return result, errDungeonHellPartyRewardDropRule
	}
	if request.DungeonMinimumLevel <= 0 || request.DungeonMinimumLevel > math.MaxInt32 ||
		request.DungeonBasisLevel <= 0 || request.DungeonBasisLevel > math.MaxInt32 {
		// 绝不编一个等级：厂商这两个值来自副本表，算不出来就必须硬失败。
		return result, fmt.Errorf(
			"%w: minimum=%d basis=%d",
			errDungeonHellPartyRewardLevels,
			request.DungeonMinimumLevel,
			request.DungeonBasisLevel,
		)
	}

	// 档位裁定：修复档走 hell 表（按**副本难度**取列）；hell 表或 `.equ` 池读不到、
	// 或者该等级没有覆盖行 ⇒ **降级**成今天的三处读法（绝不在包路径上硬失败），
	// 原因进 `result.Degraded`，由事件与每会话一条的 `…-pool-degraded` 暴露。
	decision := request.hellPartyRewardTier(request.DungeonDifficulty)
	if !decision.UseFixedTier {
		// 回退（`POOL=itemdictionary`）/降级档 = 今天：生效集合退回全集，稀有度闸门
		// 在这两档下不参与（见 `dungeon_hell_party_reward_rarity.go` §生效范围）。
		result.RarityAllowed = hellPartyRewardRarityAllList()
	}
	// 闸门与抽取的先后次序（默认 = 在允许集合内抽；`post_filter` = 逐字节回到今天）。
	rarityOrder := hellPartyRewardRarityOrderModeFromEnv()
	result.RarityOrder = rarityOrder.String()
	// `withinAllowed` 是循环不变量（提到循环外：它同时决定史诗窗口闸门这一档生不生效）。
	// 语义与上一版逐字节相同：修复档 ∧ 允许集合**真的收窄了** ∧ 次序是 `within`。
	withinAllowed := decision.UseFixedTier && plan.restricts() && rarityOrder.within()
	result.EpicWindowActive = withinAllowed && epicWindow.gates()
	result.Degraded = decision.Degraded
	result.RateSource = decision.RateSource
	rate := int64(0)
	if decision.UseFixedTier && decision.RateFound {
		rate = decision.Rate
	} else {
		probability, probabilityFound := request.rules.Probability(request.DungeonBasisLevel)
		if !probabilityFound || len(probability.Rates) == 0 {
			return result, fmt.Errorf("%w: basis_level=%d", errDungeonHellPartyRewardDropRule, request.DungeonBasisLevel)
		}
		difficulty := request.difficultyIndex()
		rate = probability.Rates[difficulty]
	}
	result.Rate = rate
	if rate <= 0 {
		return result, nil
	}
	chance := int(rate) * hellPartyRewardBaseDropRate / hellPartyRewardChancePercentDivisor
	result.Chance = chance
	// 这一次结算期望落地几件史诗（× 1e6）：`抽数 × 命中率 × 窗口`。
	// 只在闸门真的生效时有意义（其余各档返回 0，绝不假装一个数）。
	result.EpicExpectationPPM = abyssEpicExpectationPPM(
		request.rewardRollCount(), int64(chance), epicWindow, result.EpicWindowActive)

	minimumReference, minimumFound := request.rules.ItemReference(request.DungeonMinimumLevel)
	basisReference, basisFound := request.rules.ItemReference(request.DungeonBasisLevel)
	if !minimumFound || !basisFound || minimumReference.ValueA < 0 || basisReference.ValueA < 0 {
		return result, fmt.Errorf(
			"%w: minimum_level=%d basis_level=%d",
			errDungeonHellPartyRewardDropRule,
			request.DungeonMinimumLevel,
			request.DungeonBasisLevel,
		)
	}

	for attempt := int64(0); attempt < request.rewardRollCount(); attempt++ {
		result.Rolls++
		roll := request.rng.Next(hellPartyRewardDropChanceDenominator)
		if roll > chance {
			result.Misses++
			continue
		}
		// 稀有度：**两档都恰好消费一次** `lcg.Next(1000000)+1`
		// （厂商 `HellMonsterDropConfig.cs:259`）。闸门只是**读**这次抽签的结果，
		// 不额外抽任何随机数 ⇒ 命中/未命中序列与房间掉落 LCG 轨迹逐位不变。
		verdict, err := request.hellPartyRewardRarityVerdict(decision, plan)
		if err != nil {
			return result, err
		}
		result.RarityRolls++
		result.RarityDrawn[hellPartyRewardRarityIndex(verdict.Drawn)]++
		if result.RarityRolls == 1 {
			// 本波段六个 rarity 桶的候选数（含"rarity 4 到底有没有候选"）只算一次。
			if counts, known := request.hellPartyRewardRarityCandidateCounts(decision); known {
				result.RarityCandidates = counts
				result.RarityCandidatesKnown = true
			}
		}
		// 取件桶：**默认（修复）档 = 允许集合内的并集**（见
		// `DNFBRIDGE_HELL_PARTY_REWARD_RARITY_ORDER` 与
		// `selectEquipmentWithinRarities`）——厂商抽到的那一档只用于台账；
		// 回退档（`post_filter`）逐字节保持今天：从厂商抽到的那个档取件、抽完再过滤。
		// 两档的随机数消费**完全一致**：稀有度一次 + 取件一次 ⇒ 房间掉落 LCG 轨迹逐位不变。
		var selection hellPartyRewardEquipmentSelection
		if withinAllowed {
			selection, err = request.hellPartyRewardEquipmentWithinRarities(
				decision,
				plan.Allowed,
				minimumReference.ValueA,
				basisReference.ValueA,
				verdict.Fallback,
			)
		} else {
			selection, err = request.hellPartyRewardEquipment(
				decision,
				verdict.Drawn,
				minimumReference.ValueA,
				basisReference.ValueA,
				verdict.Fallback,
			)
		}
		if err != nil {
			return result, err
		}
		result.Pool = selection.Pool
		result.CandidateCount = selection.CandidateCount
		result.GradeMin, result.GradeMax = selection.GradeMin, selection.GradeMax
		if result.Degraded == "" && selection.Degraded != "" {
			result.Degraded = selection.Degraded
		}
		if !withinAllowed && !verdict.Allowed {
			// 回退档：厂商抽到的稀有度不在生效集合里（默认档 = 不是史诗）⇒
			// 这一抽**什么都不掉**。取件的加权抽已经照抽过，结果丢掉；
			// **绝不**降级成低稀有度物品（操作者要去掉的正是这些）。
			result.RarityFiltered++
			result.NoDrop++
			continue
		}
		if !selection.Selected || selection.ItemID == 0 {
			// 稀有度被允许但该桶在波段里没有候选：这一抽也不掉东西，
			// 且**不**回落 rarity 0（厂商 `:246-250` 的回落由闸门按需关掉）。
			// 这一条**排在史诗窗口之前**：配置性问题（r4 桶空）必须永远看得见，
			// 不许被"这一次运气不够"掩盖。
			result.RarityBucketEmpty++
			result.NoDrop++
			continue
		}
		if result.EpicWindowActive && selection.Rarity == hellPartyRewardRarityEpic &&
			!epicWindow.permits(verdict.Roll) {
			// 史诗落地概率闸门（操作者 2026-09-29 第 9 条「打 4-5 个深渊房间看到一个
			// 史诗装备」）：命中、稀有度也允许、r4 桶里也有候选 —— 但这一抽的**稀有度
			// 抽签没有落进窗口顶端** ⇒ 这一抽不掉东西。
			//
			// 上面的取件加权抽**已经照抽过**（结果丢掉）：随机数消费次数与顺序一个字节
			// 都没变 ⇒ 房间掉落 LCG 轨迹、以及紧随其后的碎片保底抽签逐位不变。
			//
			// 只否决**史诗（rarity 4）**那一档：允许集合里别的档（显式列表档
			// `DNFBRIDGE_HELL_PARTY_REWARD_RARITY=0,4` 之类）不受本开关影响，
			// 照旧每一次命中都落地 —— 本开关的名字与口径都只是"史诗的落地概率"。
			result.EpicGated++
			result.NoDrop++
			continue
		}
		result.RaritySelected[hellPartyRewardRarityIndex(selection.Rarity)]++
		result.Selected = append(result.Selected, hellPartyRewardItem{ItemID: selection.ItemID, Amount: 1})
	}
	return result, nil
}

func (request hellPartyRewardRequest) rewardRollCount() int64 {
	count := request.RewardRollCount
	if count <= 0 {
		// 厂商 `Dungeon.cs:1241 Math.Max(0, difficultyRule?.RewardRollCount ?? 0)`。
		return 0
	}
	if count > math.MaxInt32 {
		return math.MaxInt32
	}
	return count
}

// difficultyIndex 对应厂商 `HellMonsterDropConfig.cs:47`：
// `Math.Max(0, Math.Min(dungeonDifficulty, rates.Length - 1))`。
func (request hellPartyRewardRequest) difficultyIndex() int {
	index := int(request.AbyssPartyDifficulty)
	if index < 0 {
		return 0
	}
	if index >= 5 {
		return 4
	}
	return index
}

// rollRarity 逐行对应厂商 `HellMonsterDropConfig.cs:247-267 RollHellRarity`：
// 行号 `hellDifficulty - 1`（1 = A/非常困难、2 = B/困难），`lcg.Next(1000000) + 1`
// 与阈值 `roll <= threshold` 的前缀比较。
func (request hellPartyRewardRequest) rollRarity() int64 {
	thresholds := request.rules.RawTables().RarityDecision
	row := int(request.AbyssPartyDifficulty) - 1
	if row < 0 {
		row = 0
	}
	if row >= len(thresholds) {
		row = len(thresholds) - 1
	}
	if row < 0 {
		return 0
	}
	values := thresholds[row]
	roll := int64(request.rng.Next(hellPartyRewardRarityRollBound)) + 1
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

// dungeonHellPartyGroupAliveCount 数出房间里该组**尚未退役**的波次怪只数。
//
// 厂商的对应物是 `roomState.HellPartyGroupRemaining`
// （`DungeonMapHandler.cs:1242-1258 BuildHellPartyGroupRemaining` 初值来自
// `startMapMaze.Monsters` 里 `IsHellPartyActor && HellPartyGroupId > 0` 的行数，
// 即**同组波次怪总数**；每次 op39 在 `DungeonKillApplicationService.cs:1555-1572`
// 递减）。本仓的波次怪是扩展 actor（`HellPartyGroupID`），等价读法就是
// "同组里还没被标成 defeated 的扩展 actor 数"，**不需要新增任何运行时状态**。
//
// 调用点必须是"本只已经被标成 defeated"之后，因此返回值 0 恰好等价于厂商的
// `after == 0 ⇒ IsLastGroupMonster = true`。
func dungeonHellPartyGroupAliveCount(room *runtimeDungeonRoom, groupID int64) int {
	if room == nil || groupID <= 0 {
		return 0
	}
	snapshot := room.Snapshot()
	alive := 0
	for _, actor := range snapshot.ExtendedActors {
		if actor.Kind != runtimeDungeonActorSpecialMonster || actor.HellPartyGroupID != groupID {
			continue
		}
		if actor.State != runtimeDungeonMonsterDefeated {
			alive++
		}
	}
	return alive
}

// dungeonHellPartyRewardWaves 报告"这次死亡要不要投奖"。
//
// 判据逐条照厂商 `DungeonKillApplicationService.cs:908-910`
// （`monster.IsHellPartyActor && roomState.IsHellPartyRoom`）+ `DropService.cs:179`
// （`IsLastGroupMonster`）：
//   - 死掉的那一只是**波次扩展 actor**（厂商 `IsHellPartyActor`，本仓 = kind
//     `special_monster` 且 `HellPartyGroupID > 0`）；
//   - 当前场景就是本次冻结的深渊房间（厂商 `roomState.IsHellPartyRoom`，
//     本仓 = `currentHellPartySceneIsHellRoom`）；
//   - 同组已经一只不剩（厂商 `HellPartyGroupRemaining[group] == 0`）。
//
// 房间 actor 表里查不到这个 objectKey（例如不是波次行）⇒ 一律不投，
// 逐字节回到改动前。
func dungeonHellPartyRewardWaves(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	objectKey uint32,
) (int64, bool) {
	if runtime == nil || runtime.Room == nil || objectKey == 0 {
		return 0, false
	}
	if !currentHellPartySceneIsHellRoom(runtime, scene) {
		return 0, false
	}
	groupID, found := dungeonHellPartyRewardActor(runtime.Room, objectKey)
	if !found {
		return 0, false
	}
	// Equipment and epic fragments share the authoritative last-wave boundary.
	// Clearing an earlier group must neither roll equipment nor advance its RNG.
	finalWave, _, known := abyssEpicFragmentFinalWaveFor(runtime, abyssEpicFragmentFinalWaveModeFromEnv())
	if !known || !finalWave.containsGroup(groupID) {
		return 0, false
	}
	if dungeonHellPartyGroupAliveCount(runtime.Room, groupID) != 0 {
		return 0, false
	}
	return groupID, true
}

// dungeonHellPartyRewardActor 取这只波次扩展 actor 的 `(组号, 怪物 code)`。
// `found == false` 覆盖三种"不投奖"：不是扩展 actor、不是 `special_monster`、
// 或者组号 <= 0（厂商 `BuildHellPartyGroupRemaining` 也只认 `HellPartyGroupId > 0`）。
func dungeonHellPartyRewardActor(room *runtimeDungeonRoom, objectKey uint32) (int64, bool) {
	if room == nil || objectKey == 0 {
		return 0, false
	}
	snapshot := room.Snapshot()
	for _, actor := range snapshot.ExtendedActors {
		if actor.ObjectKey != objectKey {
			continue
		}
		if actor.Kind != runtimeDungeonActorSpecialMonster || actor.HellPartyGroupID <= 0 {
			return 0, false
		}
		return actor.HellPartyGroupID, true
	}
	return 0, false
}

// dungeonHellPartyRewardMonsterCode 取这只 actor 的怪物 code（厂商
// `AbyssPartyDropRequest.MonsterCode = monster.Code`，`DungeonKillApplicationService.cs:1576`）。
func dungeonHellPartyRewardMonsterCode(room *runtimeDungeonRoom, objectKey uint32) int64 {
	if room == nil || objectKey == 0 {
		return 0
	}
	snapshot := room.Snapshot()
	for _, actor := range snapshot.ExtendedActors {
		if actor.ObjectKey == objectKey {
			return int64(actor.Packet.Code)
		}
	}
	return 0
}

// resolveHellPartyReward 造一次奖励投骰的输入（对应厂商
// `DungeonKillApplicationService.cs:1548-1585 BuildAbyssPartyDropRequest` 的字段来源）。
func (s *Service) resolveHellPartyReward(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	groupID int64,
	monsterCode int64,
) (hellPartyRewardRequest, error) {
	request := hellPartyRewardRequest{GroupID: groupID, MonsterCode: monsterCode}
	if s == nil || runtime == nil {
		return request, errDungeonWorldMapUnavailable
	}
	monsterCatalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		return request, err
	}
	rules, err := currentDungeonMonsterDropRules(monsterCatalog)
	if err != nil {
		return request, err
	}
	dropCatalog, err := monsterCatalog.DropCatalog()
	if err != nil {
		return request, err
	}
	catalog := s.dungeonHellPartyCatalog()
	rule, found := catalog.DifficultyRule(runtime.HellPartyDifficulty)
	if !found {
		// 厂商 `HellPartyData.GetDifficultyRule` 查不到就返回 null，
		// `Dungeon.cs:1241` 因此把 RollCount 取成 0 ⇒ 不投骰（而不是报错）。
		rule = pvfDungeonHellPartyDifficultyRule{}
	}
	visit, err := runtime.currentDungeonRoomVisit(scene)
	if err != nil {
		return request, err
	}
	minimumLevel := int64(0)
	if runtime.Dungeon.Metadata.MinimumRequiredLevel.Set {
		minimumLevel = runtime.Dungeon.Metadata.MinimumRequiredLevel.Value
	}
	basisLevel := int64(0)
	if runtime.Dungeon.Metadata.BasisLevel.Set {
		basisLevel = runtime.Dungeon.Metadata.BasisLevel.Value
	}
	request.DungeonMinimumLevel = minimumLevel
	request.DungeonBasisLevel = basisLevel
	request.AbyssPartyDifficulty = runtime.HellPartyDifficulty
	request.DungeonDifficulty = int64(runtime.Request.Difficulty)
	request.RewardRollCount = rule.RewardRollCount
	request.dungeonMonsterCatalog = monsterCatalog
	request.dropCatalog = dropCatalog
	request.rules = rules
	request.rng = newCurrentDungeonDropLCG(visit.DropRNG)
	return request, nil
}

// registerHellPartyRewardDrops 把投中的奖励按**既有地面掉落通路**落地：
// 与 `dungeon_drop_runtime.go:439-515` 同一条账本（`runtimeDungeonDropOwner`）、
// 同一个 objectKey/sceneSlot 发号器、同一份 op38 掉落行。
//
// 故意**不**走 `planCurrentDungeonMonsterDrops`：那一条会把普通怪的
// 概率/稀有度/池子全部再投一遍（厂商在深渊分支里明确**不**走 `GenerateAndRegister`）。
func (s *Service) registerHellPartyRewardDrops(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	request hellPartyRewardRequest,
	selected []hellPartyRewardItem,
	playerObjectKey uint16,
	playerCharacterID uint16,
) ([]currentDungeonDeathDropWire, error) {
	if runtime == nil || len(selected) == 0 {
		return nil, nil
	}
	if currentTeamRaidGroundDropsDisabled(runtime) {
		return nil, nil
	}
	owner := runtime.DropOwner
	if owner == nil {
		owner = newRuntimeDungeonDropOwner()
	}
	roomKey := runtimeDungeonRoomKeyFromScene(scene)
	// 掉落归属档位（`dungeon_abyss_party_drop_ledger.go`）：
	//   independent（默认，= 操作者口径「各掉各的」）：每名成员各注册一份；
	//   shared / legacy：只有一份（分别对应第 4 / 第 3 次发版的行为）。
	ownerMode := abyssPartyDropOwnerModeFromEnv()
	count := uint32(len(selected))
	// Allocation and registration share one lock, as in ordinary drops.
	instance := runtime.lockPartyDungeonInstance()
	defer unlockPartyDungeonInstance(instance)
	nextKeyStart := runtime.NextObjectKey
	if instance != nil {
		nextKeyStart = instance.nextObjectKey
	}
	if nextKeyStart == 0 || nextKeyStart > math.MaxUint16 ||
		count > math.MaxUint16-nextKeyStart {
		return nil, fmt.Errorf(
			"%w: next=%d count=%d",
			errDungeonDropObjectKeyRange,
			nextKeyStart,
			count,
		)
	}
	// 队伍副本实例的共享账本：建房那一刻 `DropOwner` 还是 nil，实例里存的就是 nil
	// （`dungeon_party_instance.go:250/338`），所以**在这里补写**并把所有会话指到同一份；
	// 否则每个人各持一份账本，跨人拾取必然 `errDungeonDropNotFound`（现场 18:49:17）。
	if ownerMode.sharesLedger() {
		if shared := partySharedDungeonDropOwnerLocked(runtime); shared != nil {
			owner = shared
		}
	}
	// Selection has already been independently rolled for this recipient.
	// Registration must never clone these items for the other members.
	if owner.nextSceneSlot == 0 || uint32(owner.nextSceneSlot)+count > math.MaxUint16 {
		return nil, fmt.Errorf(
			"%w: next=%d count=%d",
			errDungeonDropSceneSlotRange,
			owner.nextSceneSlot,
			count,
		)
	}

	drops := make([]*runtimeDungeonDrop, 0, count)
	wires := make([]currentDungeonDeathDropWire, 0, count)
	nextObjectKey := nextKeyStart
	nextSceneSlot := owner.nextSceneSlot
	for _, reward := range selected {
		if reward.ItemID == 0 || reward.Amount == 0 {
			continue
		}
		definition, err := request.dropCatalog.ResolveItem(reward.ItemID)
		if err != nil {
			return nil, err
		}
		itemState, err := currentDungeonDeathDropItemState(nextSceneSlot, definition, reward.Amount)
		if err != nil {
			return nil, err
		}
		freezeCurrentAikenInitialDrop(&definition, itemState)
		qualitySeed := uint32(0)
		if definition.Kind == dungeonDropItemEquipment {
			qualitySeed = binary.LittleEndian.Uint32(itemState.data[6:10])
		}
		drop := &runtimeDungeonDrop{
			ObjectKey:           nextObjectKey,
			SceneSlot:           nextSceneSlot,
			Item:                definition,
			Amount:              reward.Amount,
			QualitySeed:         qualitySeed,
			OwnerActorObjectKey: playerObjectKey,
			Room:                roomKey,
			Status:              runtimeDungeonDropAvailable,
			// 只有第 4 次发版那一档放宽过拾取校验（谁先谁得）；独立掉落与回退档
			// 都保持"只能捡自己的"（`owned()` 的严格所有者校验）。
			PartyShared: ownerMode.relaxed(),
		}
		drops = append(drops, drop)
		wires = append(wires, currentDungeonDeathDropWire{
			SceneObjectKey:      drop.ObjectKey,
			Item:                itemState,
			UnknownTailSentinel: math.MaxUint16,
			OwnerActorObjectKey: drop.OwnerActorObjectKey,
		})
		nextObjectKey++
		nextSceneSlot++
	}
	if len(drops) == 0 {
		return nil, nil
	}
	if err := owner.registerBatch(drops, nextSceneSlot); err != nil {
		return nil, err
	}
	runtime.DropOwner = owner
	runtime.commitDungeonNextObjectKeyLocked(nextObjectKey)
	return wires, nil
}

// hellPartyRewardItemList 把奖励压成一行可 grep 的 `item:count,...` 文本。
func hellPartyRewardItemList(items []hellPartyRewardItem) string {
	if len(items) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("%d:%d", item.ItemID, item.Amount))
	}
	return strings.Join(parts, ",")
}

// logHellPartyRewardSkipped 记录"这次死亡没有投骰/没有发奖"以及原因。
func (s *Service) logHellPartyRewardSkipped(
	session *gameSession,
	kind string,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	objectKey uint32,
	fields ...any,
) {
	args := make([]any, 0, len(fields)+24)
	args = append(args,
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"monster_object_key", objectKey,
		"hell_party_difficulty", runtime.HellPartyDifficulty,
		"group_key", string(dungeonHellPartyGroupKey(runtime.HellPartyDifficulty)),
		"switch", hellPartyRewardRollModeFromEnv().String(),
	)
	args = append(args, fields...)
	s.logGameEvent(session, kind, args...)
}

// hellPartyRewardRollEvent 是主事件名（线上 grep 的锚点，与激活记录
// `hellPartyRewardPoolReadyEvent` 一起构成"这一次投骰走了哪一档"的判据）。
const hellPartyRewardRollEvent = "game-dungeon-hell-party-reward-roll"

// hellPartyRewardRollFields 是主事件 `game-dungeon-hell-party-reward-roll` 的键值对表。
//
// 单独成函数（而不是内联进 `logHellPartyRewardRoll`）是为了让用例能对这份表直接断言
// "字段数偶数 + 每个值都有匹配的键 + 关键字段齐全"：同一批改动里的激活记录曾经因为
// **按下标改写**而打出 `pool=true false=false degraded=true`（见
// `dungeon_hell_party_reward_pool.go` 的 `hellPartyRewardPoolReadiness`）。
// 这类错位在事件里读起来像一个"自洽但错误"的状态，必须由用例挡住。
func hellPartyRewardRollFields(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	objectKey uint32,
	request hellPartyRewardRequest,
	result hellPartyRewardResult,
	granted int,
	source string,
) []any {
	return []any{
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"monster_code", request.MonsterCode,
		"monster_object_key", objectKey,
		"hell_party_difficulty", request.AbyssPartyDifficulty,
		"tier", string(dungeonHellPartyGroupKey(request.AbyssPartyDifficulty)),
		"group_id", result.GroupID,
		"reward_roll_count", result.RewardRollCount,
		"rolls_requested", result.RewardRollCount,
		"rolls_performed", result.Rolls,
		"rolls_missed", result.Misses,
		"rolls_passed_no_item", result.NoDrop,
		"drop_rate", result.Rate,
		"rate_source", result.RateSource,
		"chance", result.Chance,
		"chance_denominator", hellPartyRewardDropChanceDenominator,
		"dungeon_minimum_level", request.DungeonMinimumLevel,
		"dungeon_basis_level", request.DungeonBasisLevel,
		"dungeon_difficulty", request.DungeonDifficulty,
		"pool", result.Pool,
		"candidates", result.CandidateCount,
		"grade_min", result.GradeMin,
		"grade_max", result.GradeMax,
		// 稀有度闸门（操作者要求：深渊只掉史诗）。上面这些字段一个不改；
		// 下面这组是**新增**的判定读数：
		//   rarity_mode/rarity_allowed/rarity_switch 是开关读数；
		//   rarity_rolls 是"稀有度到底抽了几次"（两档相同的那个数）；
		//   rarity_drawn 是厂商抽到的分布、rarity_selected 是**真的落地**的分布；
		//   rarity_candidates 是本波段六个桶的候选数（让"r4 有没有候选"一眼可见）；
		//   rolls_rarity_filtered + rolls_rarity_bucket_empty == rolls_passed_no_item。
		"rarity_mode", result.RarityMode,
		"rarity_allowed", result.RarityAllowed,
		"rarity_order", result.RarityOrder,
		"rarity_switch", result.RaritySwitch,
		"rarity_rolls", result.RarityRolls,
		"rarity_drawn", hellPartyRewardRarityHistogram(result.RarityDrawn),
		"rarity_selected", hellPartyRewardRarityHistogram(result.RaritySelected),
		"rarity_candidates", hellPartyRewardRarityCandidatesText(
			result.RarityCandidates, result.RarityCandidatesKnown),
		"rolls_rarity_filtered", result.RarityFiltered,
		"rolls_rarity_bucket_empty", result.RarityBucketEmpty,
		// 史诗落地概率闸门（操作者 2026-09-29 第 9 条「打 4-5 个深渊房间看到一个
		// 史诗装备」）。上面这些字段一个不改；下面这组是**新增**的判定读数：
		//   epic_window/1e6 = 命中后落地的概率；epic_window_active 是"这一次真的在否决吗"；
		//   rolls_epic_window = 被窗口挡下的命中数；
		//   epic_expectation_ppm = 这一次结算期望落地几件史诗 × 1e6；
		//   不变量 rolls_passed_no_item == rolls_rarity_filtered
		//                       + rolls_rarity_bucket_empty + rolls_epic_window。
		"epic_window", result.EpicWindow,
		"epic_window_denominator", abyssEpicWindowBound,
		"epic_window_switch", result.EpicWindowSwitch,
		"epic_window_source", result.EpicWindowSource,
		"epic_window_active", result.EpicWindowActive,
		"rolls_epic_window", result.EpicGated,
		"epic_expectation_ppm", result.EpicExpectationPPM,
		"selected_items", hellPartyRewardItemList(result.Selected),
		"selected_count", len(result.Selected),
		"granted", granted,
		"degraded", result.Degraded,
		"switch", hellPartyRewardRollModeFromEnv().String(),
		"source", source,
	}
}

// logHellPartyRewardRoll 是主事件：一次奖励投骰的全部对账字段。
func (s *Service) logHellPartyRewardRoll(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	objectKey uint32,
	request hellPartyRewardRequest,
	result hellPartyRewardResult,
	granted int,
	source string,
) {
	s.logGameEvent(session, hellPartyRewardRollEvent,
		hellPartyRewardRollFields(runtime, scene, objectKey, request, result, granted, source)...)
	s.logHellPartyRewardPoolDegraded(session, runtime, scene, result)
}
