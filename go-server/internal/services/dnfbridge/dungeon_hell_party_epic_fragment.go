package dnfbridge

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件实现操作者的深渊保底要求：
//
//	「保底应该在击杀深渊**最后一波 npc 怪物最后一个怪物**时候掉落，
//	  掉落为**必定掉落史诗碎片**和**概率掉落史诗**，然后**必定掉落的史诗碎片和副本难度挂钩**」
//	（交付方式由操作者选：**地面掉落**）
//
// ── 触发点：厂商的单元 vs 操作者选定的单元（**本包默认是后者**）────────────
//
// 厂商的调用点（逐跳读过，不是推断）：
//
//	Network\Handlers\Dungeon\DungeonKillApplicationService.cs:908-921
//	    // 击杀结算：monster.IsHellPartyActor && roomState.IsHellPartyRoom
//	    generatedDrops = _services.Drops.GenerateAbyssPartyAndRegister(run, BuildAbyssPartyDropRequest(...));
//	Game\Dungeon\DropService.cs:159-205 GenerateAbyssPartyAndRegister
//	    :179  if (request.IsLastGroupMonster && !request.IsAbyssMonsterScript) {
//	    :181-192  HellMonsterDropConfig.GenerateSpecificEquipmentDrops(...)   // 概率那一支
//	    :194-198  var epicPieceDrops = GenerateAbyssEpicPieceDrops(run, request, ref slotCounter);  // ★ 碎片
//	    :207-243  GenerateAbyssEpicPieceDrops → EpicPieceDropService.Instance.Roll(...)
//	Network\Handlers\Dungeon\DungeonKillApplicationService.cs:1555-1572
//	    var after = Math.Max(0, remaining - 1);
//	    if (after == 0) { …; isLastGroupMonster = true; }     // ← IsLastGroupMonster 的来源
//
// ⇒ **厂商的单元 = 每一个波次组的最后一只**（`IsLastGroupMonster`），
//
//	**不是**"最后一波"、**不是**整房间、**不是**整副本、**没有**定时器：
//	碎片与概率装备共用**同一道门**（`:179`），击杀几只怪就结算几次（每组的最后一只各一次）。
//	唯一的额外条件 `!IsAbyssMonsterScript` 是"这只是深渊怪物脚本"的逐怪标记
//	（`DungeonKillApplicationService.cs:1582 monster.IsHellMonsterScript`），
//	本仓波次怪没有这个标记，与既有概率那一支同口径按 false 处理。
//
// ⚠⚠ **本包默认的单元比厂商更窄，这是操作者明确选择的结果**（不是厂商口径的复刻）：
//
//	操作者原话：「保底应该在击杀深渊**最后一波** npc 怪物**最后一个**怪物时候掉落」。
//	父代理把"厂商 = 每房间 2 次（组 125 一次 + 组 211 一次）/ 原话 = 1 次（只有组 211）"
//	完整摆给操作者之后，操作者**选择了他自己的原话口径（只最后一波）**。
//	因此本包默认 = **`final_wave`**（map 60083 每次进房 1 次结算，即 65747 死亡那一刻）；
//	厂商口径 `group_last` 保留为**非默认**的 env 档
//	（`DNFBRIDGE_ABYSS_EPIC_FRAGMENT_TRIGGER=group_last`，map 60083 会变成 2 次）。
//	**不要把默认档当成"厂商口径"**：厂商的 `DropService.cs:179` 对**每一组**都成立，
//	而默认档只对**最后一波**成立 —— 两档都是"必掉"，只在**次数**上不同。
//
// 本仓的落点：波次怪是**冻结态** `runtime.HellPartyWaves`
// （`dungeon_hell_party_wave.go:263-288` 按 order 升序装配、每个 order 一条 wave），
// 死亡走 `Room.RetireSpecialMonsterActor`（就是 op39→op38 那一处）。
//   - 厂商 `IsLastGroupMonster` 的等价物 = 死亡结算后"同组一条活口都没有"
//     （`dungeonHellPartyGroupAliveCount`，既有函数，零新增状态）；
//   - **默认档额外要求**"该组是 Order 最大的那一波"（`dungeonHellPartyFinalWave`）；
//     判不出来（波次表为空 / order 全非法）时默认档**不发**，记
//     `-deferred reason=final_wave_unknown`，绝不猜哪一波是最后一波。
//
// 为什么**不**用"房间清空"（厂商 `DungeonKillApplicationService.cs:584-591` 的
// `HellPartyPhase: Started → Complete`）：本仓波次怪是**扩展 actor**，**不**进
// `scene.Monsters`，不参与清房判定（`dungeon_hell_party_wave.go:77-85` 的已声明边界），
// 用 `scene.Cleared` 当触发点在这份实现里根本不可达。
//
// 判定永远**显式**、不静默：`-skipped` 带 `reason=not_group_last`（带
// `group_alive_count`）/`reason=not_final_wave`，`-deferred` 带
// `reason=final_wave_unknown`；两条事件都记 `trigger` / `payout_unit` /
// `group_id` / `final_wave_order` / `final_wave_groups` / `wave_count` / `is_final_wave`，
// 所以下一次线上跑能直接看出**是哪一次、按哪个单元**发的（也能一眼看出"最后一波"判对了没有）。
//
// ── 必定掉落：与概率那一支**完全独立**（这是操作者的产品要求，不是补丁）──────
//
// 厂商把"必掉"与"概率"写在同一个门里（`DropService.cs:179-199`）：
//
//	if (request.IsLastGroupMonster && !request.IsAbyssMonsterScript) {
//	    var rewardDrops = HellMonsterDropConfig.GenerateSpecificEquipmentDrops(...);   // 概率
//	    drops.AddRange(rewardDrops);
//	    var epicPieceDrops = GenerateAbyssEpicPieceDrops(run, request, ref slotCounter); // 碎片
//	    drops.AddRange(epicPieceDrops);
//	}
//
// 本实现照这个**门**落点，但保底那一支**不消费概率那一支的
// 任何输入**：不读 `[drop prob]`、不读装备池、不看 `[difficulty]` 的 `Probability`、
// 不依赖 `hellPartyRewardRolls` 成功与否。碎片身份只来自**本仓既有的史诗碎片目录**
// `etc/epicpieceinfo.etc [piece list]`（966 条 `(成品装备 id, 碎片 id)` 二元组，
// 由 `epic_piece_book.go` 的 `currentEpicPieceCatalog` 提供）。
// 碎片 id **全部来自 PVF**，本文件没有写死任何物品 id。
//
// ⚠ 本轮**已推翻**的两条旧前提（不许再当成理由写进任何注释/报告）——
// 它们是并发只读调查的结论，本文件因此**不**以"概率几乎必 miss"或"装备池是空的"
// 作为保底的理由：
//
//	推翻 1：旧口径"真档 310 的 A 档 rate=10 ⇒ 每次 ≈1%、8 次 ≈92% 全 miss"是
//	    **读错了文件**——那个 `10` 来自 `Etc/ItemDropInfo_Monseter.etc [drop prob]` 的
//	    81..200 行；深渊该读的是 `etc/itemdropinfo_monster_hell.etc`
//	    `[dungeon difficulty drop prob]` 的 `45 200 335 875 1000 1400 1000` 行
//	    （厂商 `HellMonsterDropConfig.cs:162`）。副本难度 1 ⇒ `rate=875` ⇒
//	    `chance=875` ⇒ 每次 876/1001 ⇒ 8 次至少中 1 的概率 = 99.999994%。
//	推翻 2：旧口径"grade 84..95 装备池为空、PVF 缺内容"是**本仓装载器的缺陷**——
//	    `dungeon_drop_catalog.go:551-595 loadGenericEquipmentLocked` 只读
//	    `Etc/ItemDictionary/ItemDictionary.etc`（那是厂商**普通怪**装载器
//	    `MonsterDropConfig.cs:284-369` 的口径），而厂商自己会用 `.equ` 池覆盖它
//	    （`MonsterDropConfig.cs:35`），深渊同样用 `.equ`（`HellMonsterDropConfig.cs:136`）。
//	    全量扫 `equipment/equipment.lst` 的 140,862 条后 `[grade]` 最大 96，
//	    `[84,96)` 区间有 6,951 件。**内容一直在，是取池口径错了**；
//	    该修复由另一条改动（`DNFBRIDGE_HELL_PARTY_REWARD_POOL`）负责，
//	    本文件**不碰** `dungeon_drop_catalog.go`、**不**自建池、**不**改概率/难度表来源。
//
// 因此保底与概率两支互不干扰、可叠加：同一次击杀**既可以**走概率那一支，
// **也一定**产出保底碎片（两批掉落行按厂商同序追加到同一条 op38）。
//
// ── 难度键：为什么用"副本难度"而不是"深渊档位"────────────────────────────
//
// 候选一（深渊档位 `runtime.HellPartyDifficulty`，1=A/2=B/…）：它就是
//
//	`etc/hellparty.etc [difficulty]` 的字母，也是 `RewardRollCount` 的来源。
//	但本仓 `resolveDungeonHellPartyMode`（`dungeon_hell_room.go:206-211`）对
//	"玩家没点名难度"写死回退 **1（恒 A）**，且分组键 `dungeonHellPartyGroupKey`
//	把 3/4 也折成 'A'（`dungeon_hell_party_catalog.go:315-322`）
//	⇒ **本档下档位字母恒为 A**，拿它当唯一键会让"和难度挂钩"在线上恒等于一个常量。
//
// 候选二（副本难度 `runtime.Request.Difficulty`，0..4）：**它是厂商自己在这条链上用的键**
//
//	  ——厂商的碎片投骰次数就是 `EpicPieceDropService.cs:120-141`
//	  `ResolveRollCount(dungeonDifficulty, hellDifficulty)`：
//
//		hellCount    = hellDifficulty == 1 ? 1 : 0
//		dungeonCount = { 0:5, 1:6, 2:8, 3:9, 4:10 }
//		rollCount    = hellCount + dungeonCount        // 主项是**副本难度**
//
//	  即"深渊史诗碎片的数量按难度给"这件事，厂商键的就是**副本难度**（深渊档位只加 0/1）。
//	  副本基准等级（310 的 basis=90）**不是**难度（同一副本内恒 90），拿它当键无法表达难度。
//
// ⇒ 本实现取**候选二：副本难度 `runtime.Request.Difficulty`（0..4）**，深渊档位字母作为
// **可选的第二键**保留（env 里写 `A:3` 这类字母键仍然有效，只是在本档恒 A）。
// 事件里同时记 `dungeon_difficulty`、`tier`（**真实**档位字母，不折成 A/B 两档）与命中的键
// （`amount_key`），所以"键选错了"在日志里一眼可见。
//
// ── 数量表（默认值 = **厂商自己的按难度计数**）────────────────────────────
//
// PVF **没有**任何一处定义"每档掉几个碎片"：
//
//   - `etc/itemdropinfo_monster_hell.etc` 全文只有 `[drop prob count]` /
//     `[dungeon difficulty drop prob]` / `[basis of rarity dicision]` /
//     `[item drop ref table]` / `[item drop rarity control]`，**一个碎片 id 都没有**；
//
//   - `etc/epicpieceinfo.etc` 只有 `[equipment piece drop info]/[piece list]` 与
//     `[equipment recipe info]`，**没有任何难度字段**；
//
//   - `etc/hellparty.etc [difficulty]` 的 `字母 + 5 整数` 里第 2 个（厂商命名 `Unknown1`
//     = A:5/B:4/C:3/D:3）**没有消费者**（`HellPartyData.cs` 只读第 1/3/4 个），
//     `[hellparty basic values]`（base value / base rising value / difficulty rates …）
//     在厂商源码里**零引用**。
//
//     ⇒ 默认表直接取**厂商自己的按难度计数**（`EpicPieceDropService.cs:120-141`
//     `ResolveRollCount`，档位按本档恒 A ⇒ hellCount=1）：
//     `0:6 1:7 2:9 3:10 4:11` + 兜底 `*:1`（未知键恒 1，绝不 0）。
//     操作者若要下调，一条 env 即可（例如保守档 `0:1,1:1,2:2,3:3,4:3`）。
//
// ⚠ **语义差异（必须知道）**：厂商那个数是**投骰次数**——`Roll` 循环 `rollCount` 次，
// 每次 `TryRoll` 都可能失败（`:62-66 if (TryRoll(...)) results.Add(result);`，
// `TryRoll` 在"等级区间内没有带碎片映射的史诗装备"时返回 false）⇒ 厂商口径是
// **最多** rollCount 个碎片。操作者要的是「必定掉落」，所以本实现把这个数当
// **保底数量**用：填几个就**一定**发几个（见 grantAbyssEpicFragmentGuarantee 的
// 抽取循环——没有"抽不到就不发"的分支，只有"目录读不到"才整支不发并记 deferred）。
// 也就是说：**难度 1 + 档位 A ⇒ 7 个必掉碎片**（厂商在同条件下是"投 7 次、可能为 0"）。
//
// ── 发放路线：地面掉落（操作者指定）──────────────────────────────────────
//
// 与概率那一支**完全同一条路**：`registerHellPartyRewardDrops`
// （`dungeon_hell_party_reward.go:474-567`）→ `runtimeDungeonDropOwner` 账本 →
// 随既有 op38 `DIE_MONSTER` 下发 → 客户端走过去发 op43 拾取。
// **不邮寄、不直写背包、不发明第四条包。**
//
// 碎片被捡起来之后**沿用既有史诗碎片特性**（`epic_piece_book.go`）：
// `dungeon_drop_pickup.go:410-506` 认出 `isEpicPiece(drop.Item.ItemID)` ⇒
// 不写任何背包槽位，改记**账号级史诗图鉴账本**（`dnf_account_metadata` 的
// `epic_piece:<碎片id>`），并发 class0/`0x047D` 增量包（`DNFBRIDGE_EPIC_PIECE_PUSH`
// 默认 `login_only` ⇒ 线上这一次不发包，下次登录走全量快照）。
// **本文件没有改那条特性的任何行为**，只是把地面掉落摆到它面前。
//
// ── 开关 ─────────────────────────────────────────────────────────────────
//
// DNFBRIDGE_ABYSS_EPIC_FRAGMENT
//
//	guaranteed（默认，= 操作者要求）：最后一波最后一只死亡 ⇒ 必掉 N 个史诗碎片。
//	off / legacy（一键回退）        ：**一个随机数都不抽、一条掉落都不注册、一条日志都不写**，
//	    逐字节、逐随机序列等于本改动前（概率那一支照旧）。
//
// DNFBRIDGE_ABYSS_EPIC_FRAGMENT_AMOUNTS
//
//	`键:数量`，逗号/分号/空白分隔；键 = 副本难度 `0`..`4`（首选）、深渊档位字母
//	`A`..`D`（次选）、`*`（兜底）。空/未设置 = 内置默认表
//	（= **厂商自己的按难度计数** `0:6 1:7 2:9 3:10 4:11` + `*:1`）。
//	**非法取值一律回退到内置默认表 + 记一条 `…-amounts-invalid`，绝不静默变成 0。**
//
// DNFBRIDGE_ABYSS_EPIC_FRAGMENT_TRIGGER
//
//	final_wave（**默认**，= 操作者 2026-09-28 选定的口径）：
//	    只有 **Order 最大那一波**的组最后一只结算保底（map 60083 ⇒ 1 次/房间，
//	    即 65747 死亡那一刻）。波次表为空 / order 全非法 ⇒ 记
//	    `-deferred reason=final_wave_unknown` 且**不发**（绝不猜哪一波是最后一波）。
//	group_last（**非默认**，= 厂商 `DropService.cs:179` 的 `IsLastGroupMonster`）：
//	    深渊房间里**每一组**的最后一只都结算一次（map 60083 ⇒ 2 次/房间，产量翻倍）。
//	两档都是"必掉"，只在**次数**上差（每房间 = 结算次数 × 每单元数量）。
const (
	// abyssEpicFragmentEnv 是保底盘的总开关。
	abyssEpicFragmentEnv = "DNFBRIDGE_ABYSS_EPIC_FRAGMENT"
	// abyssEpicFragmentAmountsEnv 是"每场保底几个碎片"的数量表（免重编译可调）。
	abyssEpicFragmentAmountsEnv = "DNFBRIDGE_ABYSS_EPIC_FRAGMENT_AMOUNTS"

	// abyssEpicFragmentValueGuaranteed 是默认档（= 操作者要求）。
	abyssEpicFragmentValueGuaranteed = "guaranteed"
	// abyssEpicFragmentValueOff 是回退档。
	abyssEpicFragmentValueOff = "off"
	// abyssEpicFragmentRollbackValue 是回退档的第二个字面量（与全仓 `legacy` 同一惯例）。
	abyssEpicFragmentRollbackValue = "legacy"

	// abyssEpicFragmentFallbackKey 是没有匹配键时的兜底键。
	abyssEpicFragmentFallbackKey = "*"
	// abyssEpicFragmentMaxAmount 是单次事件的碎片数上限。厂商自己按难度给的最大值是
	// 11（`ResolveRollCount(4, 1)`），留足余量的同时挡住把 env 写错成 99999。
	abyssEpicFragmentMaxAmount = 50
	// abyssEpicFragmentHardFallbackAmount 是"连兜底键都没有"时的硬回退值。
	// **不是 0**：保底档下任何一条路径都不允许静默变成零掉落。
	abyssEpicFragmentHardFallbackAmount = 1
	// abyssEpicFragmentRoute 是事件里的发放路线口径。
	abyssEpicFragmentRoute = "ground_drop_op38_then_pickup_account_epic_piece_ledger"
	// abyssEpicFragmentSource 是事件里的依据。
	abyssEpicFragmentSource = "operator_guarantee_final_wave_last_monster_via_etc_epicpieceinfo_piece_list"
)

var errAbyssEpicFragmentAmountSpec = errors.New("dnf abyss epic fragment amount spec is invalid")

// ─────────────────────────── 开关 ───────────────────────────

type abyssEpicFragmentMode int

const (
	// abyssEpicFragmentGuaranteed 是默认档，也是操作者要求的档（零值 = 保底）。
	abyssEpicFragmentGuaranteed abyssEpicFragmentMode = iota
	// abyssEpicFragmentOff 是改动前的"这一支不存在"档。
	abyssEpicFragmentOff
)

func (mode abyssEpicFragmentMode) String() string {
	if mode == abyssEpicFragmentOff {
		return abyssEpicFragmentValueOff
	}
	return abyssEpicFragmentValueGuaranteed
}

// guaranteed 报告该档位是否执行保底掉落。
func (mode abyssEpicFragmentMode) guaranteed() bool {
	return mode == abyssEpicFragmentGuaranteed
}

// readAbyssEpicFragmentMode 解析 DNFBRIDGE_ABYSS_EPIC_FRAGMENT。
// 默认 guaranteed：空/未设置/非法值都按 guaranteed；只有显式写
// off（或 legacy/0/false/disabled）才回退。
func readAbyssEpicFragmentMode(value string) abyssEpicFragmentMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case abyssEpicFragmentValueOff, abyssEpicFragmentRollbackValue, "0", "false", "disabled":
		return abyssEpicFragmentOff
	default:
		return abyssEpicFragmentGuaranteed
	}
}

// abyssEpicFragmentModeFromEnv 在**消费点**自读开关（与 `hellPartyRewardRollModeFromEnv`
// / `mysql_char.go:222` / `equip/owner.go:877` 同一惯例：options 结构体处在并发编辑下，
// 不往里加字段）。
func abyssEpicFragmentModeFromEnv() abyssEpicFragmentMode {
	return readAbyssEpicFragmentMode(os.Getenv(abyssEpicFragmentEnv))
}

// ─────────────────────── 触发单元（厂商 vs 操作者原话）───────────────────────

// abyssEpicFragmentTriggerEnv 选择"一次深渊房间里，保底按什么单元结算"。
//
// 厂商口径（默认）：`DropService.cs:179 if (request.IsLastGroupMonster && !request.IsAbyssMonsterScript)`
// —— `EpicPieceDropService` 的调用点在 `DropService.cs:194-198 GenerateAbyssEpicPieceDrops`，
// 它就在那道门里；而 `IsLastGroupMonster` 由
// `DungeonKillApplicationService.cs:1555-1572` 的 `after == 0` 得出 ⇒
// **厂商的单元 = 每一个波次组的最后一只**（不是最后一波、不是整房间、不是整副本）。
//
// 操作者 2026-09-28 的原话是「击杀深渊**最后一波** npc 怪物**最后一个**怪物时候掉落」，
// 那比厂商更窄（只结算最后一波）。两者在 map 60083 上差一倍：厂商 2 次（组 125、组 211
// 各一次）、原话口径 1 次（只有组 211）。默认按操作者最新的「按厂商的来」取 **group_last**，
// `final_wave` 档保留原话口径（一条 env 即可切）。
//
// 两个档位**都**是"必掉"，只在**次数**上不同。
const (
	abyssEpicFragmentTriggerEnv = "DNFBRIDGE_ABYSS_EPIC_FRAGMENT_TRIGGER"
	// abyssEpicFragmentTriggerValueFinalWave = 操作者选定的单元（**默认档**）。
	abyssEpicFragmentTriggerValueFinalWave = "final_wave"
	// abyssEpicFragmentTriggerValueGroupLast = 厂商单元（**非默认**，env 可选）。
	abyssEpicFragmentTriggerValueGroupLast = "group_last"
)

type abyssEpicFragmentTrigger int

const (
	// abyssEpicFragmentTriggerFinalWave 是零值 = **默认档** = 操作者选定的口径
	// （只结算 Order 最大那一波的组最后一只）。
	abyssEpicFragmentTriggerFinalWave abyssEpicFragmentTrigger = iota
	// abyssEpicFragmentTriggerGroupLast 是**厂商口径**（`DropService.cs:179` 的
	// `IsLastGroupMonster`：每一组的最后一只都结算）。现在**不是**默认档，
	// 一条 `DNFBRIDGE_ABYSS_EPIC_FRAGMENT_TRIGGER=group_last` 可选。
	abyssEpicFragmentTriggerGroupLast
)

func (trigger abyssEpicFragmentTrigger) String() string {
	if trigger == abyssEpicFragmentTriggerGroupLast {
		return abyssEpicFragmentTriggerValueGroupLast
	}
	return abyssEpicFragmentTriggerValueFinalWave
}

// requiresFinalWave 报告该档位是否需要"最后一波"这一判定。
// 默认档（操作者口径）**需要**；厂商档不需要（所以波次表判不出来时厂商档照发）。
func (trigger abyssEpicFragmentTrigger) requiresFinalWave() bool {
	return trigger == abyssEpicFragmentTriggerFinalWave
}

// payoutUnit 是事件里的"这一次钱是按哪个单元发的"口径文本。
func (trigger abyssEpicFragmentTrigger) payoutUnit() string {
	if trigger == abyssEpicFragmentTriggerGroupLast {
		return "hell_party_group_last_monster_vendor_DropService_cs_179"
	}
	return "hell_party_final_wave_last_monster_operator_wording_2026_09_28"
}

// readAbyssEpicFragmentTrigger 解析 DNFBRIDGE_ABYSS_EPIC_FRAGMENT_TRIGGER。
// **默认 `final_wave`**（操作者选定的口径）：空/未设置/非法值都按 final_wave；
// 只有显式写 `group_last`（大小写不敏感、允许首尾空白）才切成厂商口径。
func readAbyssEpicFragmentTrigger(value string) abyssEpicFragmentTrigger {
	if strings.EqualFold(strings.TrimSpace(value), abyssEpicFragmentTriggerValueGroupLast) {
		return abyssEpicFragmentTriggerGroupLast
	}
	return abyssEpicFragmentTriggerFinalWave
}

// abyssEpicFragmentTriggerFromEnv 在消费点自读触发单元开关。
func abyssEpicFragmentTriggerFromEnv() abyssEpicFragmentTrigger {
	return readAbyssEpicFragmentTrigger(os.Getenv(abyssEpicFragmentTriggerEnv))
}

// ─────────────────────────── 数量表 ───────────────────────────

// abyssEpicFragmentAmountRule 是数量表里的一行。
type abyssEpicFragmentAmountRule struct {
	Key    string
	Amount int
}

// abyssEpicFragmentAmountTable 是归一化后的数量表。
type abyssEpicFragmentAmountTable struct {
	entries map[string]int
	order   []string
	// source ∈ {"builtin_default", "env"}；envValue 是原始 env 文本（日志用）。
	source   string
	envValue string
}

// abyssEpicFragmentDefaultAmountRules 是**内置默认数量表**，取值 = 厂商自己的
// 按难度计数（`EpicPieceDropService.cs:120-141 ResolveRollCount`，hellDifficulty=1
// ⇒ hellCount=1，加上 dungeonCount `{0:5,1:6,2:8,3:9,4:10}`）：
//
//	副本难度 0 → 6、1 → 7、2 → 9、3 → 10、4 → 11；其余（未知键）→ 兜底 `*` = 1。
//
// 两处**故意**的口径声明：
//  1. 厂商那个数是**投骰次数**（每次 `TryRoll` 可能失败 ⇒ 厂商口径是"最多 N 个"）；
//     操作者要「必定掉落」，所以这里把它当**保底数量**用 —— 填几个就一定发几个。
//  2. 档位项按本档实际恒为 A（`dungeon_hell_room.go:206-211` 写死回退 1）取 hellCount=1。
//
// 单调不减，且**任何一档都不小于 1** ⇒ 与"必定掉落"自洽。
// 操作者可一条 env 覆盖（保守档：`0:1,1:1,2:2,3:3,4:3`）。
func abyssEpicFragmentDefaultAmountRules() []abyssEpicFragmentAmountRule {
	return []abyssEpicFragmentAmountRule{
		{Key: "0", Amount: 6},
		{Key: "1", Amount: 7},
		{Key: "2", Amount: 9},
		{Key: "3", Amount: 10},
		{Key: "4", Amount: 11},
		{Key: abyssEpicFragmentFallbackKey, Amount: abyssEpicFragmentHardFallbackAmount},
	}
}

func abyssEpicFragmentDefaultAmountTable() abyssEpicFragmentAmountTable {
	rules := abyssEpicFragmentDefaultAmountRules()
	table := abyssEpicFragmentAmountTable{
		entries: make(map[string]int, len(rules)),
		order:   make([]string, 0, len(rules)),
		source:  "builtin_default",
	}
	for _, rule := range rules {
		table.entries[rule.Key] = rule.Amount
		table.order = append(table.order, rule.Key)
	}
	return table
}

// abyssEpicFragmentAmountsFromEnv 读 DNFBRIDGE_ABYSS_EPIC_FRAGMENT_AMOUNTS。
// 返回的 error 非 nil 时，**表已经被换成内置默认表**（调用方据此记事件 + WARN），
// 调用方必须继续用返回的表发保底。
func abyssEpicFragmentAmountsFromEnv() (abyssEpicFragmentAmountTable, error) {
	return readAbyssEpicFragmentAmounts(os.Getenv(abyssEpicFragmentAmountsEnv))
}

// readAbyssEpicFragmentAmounts 解析数量表 spec。空/未设置 = 内置默认表（无错）。
// 任何一处非法 ⇒ 整份 spec 作废、回退内置默认表**并返回具名错误**（不静默）。
func readAbyssEpicFragmentAmounts(value string) (abyssEpicFragmentAmountTable, error) {
	table := abyssEpicFragmentDefaultAmountTable()
	table.envValue = value
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return table, nil
	}
	entries := make(map[string]int, 8)
	order := make([]string, 0, 8)
	fields := strings.FieldsFunc(trimmed, func(r rune) bool {
		switch r {
		case ',', ';', ' ', '\t', '\n', '\r':
			return true
		default:
			return false
		}
	})
	for _, field := range fields {
		key, raw, found := strings.Cut(field, ":")
		if !found {
			key, raw, found = strings.Cut(field, "=")
		}
		if !found {
			return table, fmt.Errorf("%w: token=%q want KEY:COUNT", errAbyssEpicFragmentAmountSpec, field)
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		if !validAbyssEpicFragmentAmountKey(key) {
			return table, fmt.Errorf("%w: key=%q", errAbyssEpicFragmentAmountSpec, key)
		}
		amount, convErr := strconv.Atoi(strings.TrimSpace(raw))
		if convErr != nil || amount <= 0 || amount > abyssEpicFragmentMaxAmount {
			return table, fmt.Errorf(
				"%w: key=%s count=%q want 1..%d",
				errAbyssEpicFragmentAmountSpec, key, strings.TrimSpace(raw), abyssEpicFragmentMaxAmount,
			)
		}
		if _, duplicate := entries[key]; duplicate {
			return table, fmt.Errorf("%w: duplicate key=%s", errAbyssEpicFragmentAmountSpec, key)
		}
		entries[key] = amount
		order = append(order, key)
	}
	if len(entries) == 0 {
		return table, fmt.Errorf("%w: no entries in %q", errAbyssEpicFragmentAmountSpec, trimmed)
	}
	table.entries = entries
	table.order = order
	table.source = "env"
	return table, nil
}

// validAbyssEpicFragmentAmountKey：单字符且是 `0`..`4` 或字母 A..Z，或者是兜底键 `*`。
func validAbyssEpicFragmentAmountKey(key string) bool {
	if key == abyssEpicFragmentFallbackKey {
		return true
	}
	if len(key) != 1 {
		return false
	}
	value := key[0]
	if value >= '0' && value <= '4' {
		return true
	}
	return value >= 'A' && value <= 'Z'
}

// amountFor 解析一次事件该发几个碎片，并返回**命中的键**（供日志对账）。
//
// 键的优先级：副本难度（`0`..`4`）> 深渊档位字母（`A`..`D`）> `*` > 硬回退 1。
// 任何一条路径都**不会返回 0**。
func (table abyssEpicFragmentAmountTable) amountFor(
	dungeonDifficulty int,
	tier byte,
) (int, string) {
	if dungeonDifficulty >= 0 && dungeonDifficulty <= 4 {
		key := strconv.Itoa(dungeonDifficulty)
		if amount, found := table.entries[key]; found && amount > 0 {
			return amount, "dungeon_difficulty:" + key
		}
	}
	if tier != 0 {
		key := string(rune(tier))
		if amount, found := table.entries[key]; found && amount > 0 {
			return amount, "hell_tier:" + key
		}
	}
	if amount, found := table.entries[abyssEpicFragmentFallbackKey]; found && amount > 0 {
		return amount, "fallback:" + abyssEpicFragmentFallbackKey
	}
	return abyssEpicFragmentHardFallbackAmount, "hardcoded_fallback"
}

// specText 把表压成一行可 grep 的 `KEY:COUNT,...`（按解析顺序）。
func (table abyssEpicFragmentAmountTable) specText() string {
	if len(table.order) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(table.order))
	for _, key := range table.order {
		parts = append(parts, fmt.Sprintf("%s:%d", key, table.entries[key]))
	}
	return strings.Join(parts, ",")
}

// ─────────────────────── 难度键 / 最后一波 ───────────────────────

// abyssEpicFragmentTierLetter 把 `runtime.HellPartyDifficulty` 还原成
// `etc/hellparty.etc [difficulty]` 的**真实**字母（1=A/非常困难 … 4=D）。
//
// 故意**不**用 `dungeonHellPartyGroupKey`：那一条是厂商的"分组键"口径
// （`difficulty == 2 ? 'B' : 'A'`），会把 C/D 折成 A；事件里要的是真实档位。
// 未知值返回 0（事件里记 `-`）。
func abyssEpicFragmentTierLetter(difficulty byte) byte {
	switch difficulty {
	case 1:
		return 'A'
	case 2:
		return 'B'
	case 3:
		return 'C'
	case 4:
		return 'D'
	default:
		return 0
	}
}

// abyssEpicFragmentTierText 是上一条的日志文本（未知 = `-`）。
func abyssEpicFragmentTierText(difficulty byte) string {
	if letter := abyssEpicFragmentTierLetter(difficulty); letter != 0 {
		return string(rune(letter))
	}
	return "-"
}

// abyssEpicFragmentFinalWave 是"最后一波"的判定结果（也是日志口径）。
type abyssEpicFragmentFinalWave struct {
	// Order 是最后一波的 order。
	Order int64
	// GroupIDs 是最后一波用到的组号（order 唯一时恰好一个）。
	GroupIDs []int64
	// WaveCount 是本次冻结的波次总数（`runtime.HellPartyWaves` 的长度）。
	WaveCount int
	// Index 是最后一波在波次表里的下标（0 基；order 并列时取第一条）。
	Index int
}

// containsGroup 报告某组号是不是最后一波的组。
func (wave abyssEpicFragmentFinalWave) containsGroup(groupID int64) bool {
	for _, candidate := range wave.GroupIDs {
		if candidate == groupID {
			return true
		}
	}
	return false
}

// groupList 把组号压成一行 `id,id` 文本（日志用）。
func (wave abyssEpicFragmentFinalWave) groupList() string {
	if len(wave.GroupIDs) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(wave.GroupIDs))
	for _, groupID := range wave.GroupIDs {
		parts = append(parts, strconv.FormatInt(groupID, 10))
	}
	return strings.Join(parts, ",")
}

// dungeonHellPartyFinalWave 取"最后一波"。
//
// `runtime.HellPartyWaves` 由 `resolveDungeonHellPartyWaves`（`dungeon_hell_party_wave.go:263-288`）
// 按 order **升序**装配、每个 order 一条 ⇒ 最后一波 = `Order` 最大的那一条。
// 这里仍然显式求最大值而不是取末元素：**并列的 order 一并算作最后一波**
// （并把全部组号记进日志），这样"哪天变成同 order 多组"时不会静默漏掉。
//
// 返回 false 的两种情况（调用方记 `final_wave_unknown`，绝不猜）：
//   - 波次表为空（`HellPartyWaves=[]`：两个波次开关都是 legacy，或 `[hellparty]` 没解析出条目）；
//   - 所有波次的 `Order <= 0`（真档不会：order 是 1 基且升序）。
func dungeonHellPartyFinalWave(runtime *runtimeDungeonState) (abyssEpicFragmentFinalWave, bool) {
	result := abyssEpicFragmentFinalWave{}
	if runtime == nil || len(runtime.HellPartyWaves) == 0 {
		return result, false
	}
	result.WaveCount = len(runtime.HellPartyWaves)
	maxOrder := int64(0)
	maxIndex := -1
	for index, wave := range runtime.HellPartyWaves {
		if wave.Order <= 0 || wave.GroupID <= 0 {
			continue
		}
		if wave.Order > maxOrder {
			maxOrder = wave.Order
			maxIndex = index
		}
	}
	if maxOrder <= 0 || maxIndex < 0 {
		return result, false
	}
	result.Order = maxOrder
	result.Index = maxIndex
	for _, wave := range runtime.HellPartyWaves {
		if wave.Order != maxOrder || wave.GroupID <= 0 {
			continue
		}
		duplicate := false
		for _, existing := range result.GroupIDs {
			if existing == wave.GroupID {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result.GroupIDs = append(result.GroupIDs, wave.GroupID)
		}
	}
	if len(result.GroupIDs) == 0 {
		return result, false
	}
	return result, true
}

// ─────────────────────── 保底发放 ───────────────────────

// grantAbyssEpicFragmentGuarantee 是保底盘的全部逻辑：判定 → 抽碎片 → 注册地面掉落。
//
// 返回值是**本事件追加到 op38 的掉落行**（可能为空）。任何"没发"的情况都在这里记
// 具名事件；回退档（`off`）下**一条日志都不写、一个随机数都不抽**，
// 逐字节等于本改动前。
//
// ⚠ 与 `DNFBRIDGE_HELL_PARTY_REWARD_ROLL=legacy` 的关系：那一条的已声明契约是
// "这一支一个随机数都不抽、一条掉落都不注册"。保底盘**从属于**它（记一条
// `-skipped reason=reward_roll_switch_legacy` 后什么都不做），否则
// `HELL_PARTY_REWARD_ROLL=legacy` 就不再是"这一支完全静默"的回退档了。
func (s *Service) grantAbyssEpicFragmentGuarantee(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	objectKey uint32,
	rewardMode hellPartyRewardRollMode,
) []currentDungeonDeathDropWire {
	if s == nil || runtime == nil || runtime.Room == nil || session == nil || objectKey == 0 {
		return nil
	}
	mode := abyssEpicFragmentModeFromEnv()
	if !mode.guaranteed() {
		// 回退档：完全静默（连日志都没有），逐字节等于本改动前。
		return nil
	}
	// 作用域：必须是**波次扩展 actor**（kind special_monster 且组号 > 0）且在深渊房间里。
	// 房图自带的普通怪、房间 actor 表里查不到的 key —— 一律**静默**返回（与概率那一支同）。
	groupID, found := dungeonHellPartyRewardActor(runtime.Room, objectKey)
	if !found || !currentHellPartySceneIsHellRoom(runtime, scene) {
		return nil
	}
	if !rewardMode.rolls() {
		s.logAbyssEpicFragmentSkipped(session, runtime, scene, objectKey, groupID,
			abyssEpicFragmentFinalWave{},
			"reason", "reward_roll_switch_legacy",
			"env", hellPartyRewardRollEnv,
			"mode", mode.String())
		return nil
	}
	trigger := abyssEpicFragmentTriggerFromEnv()
	// 最后一波是**默认档（final_wave）的判据**；只有 `group_last`（厂商档）下它才只用于日志。
	//
	// **P0 修复**：默认档以**共享房间**的波次行为准（`dungeon_hell_party_final_wave_source.go`），
	// 不再以"本会话自己抽到的那张波次表"为准 —— 后者在组队局里两人各不相同，
	// 于是"队员打死房间里最后一波的最后一只"会被判成 `not_final_wave` ⇒ 全队都不掉碎片。
	finalWaveMode := abyssEpicFragmentFinalWaveModeFromEnv()
	finalWave, finalWaveSource, finalWaveOK := abyssEpicFragmentFinalWaveFor(runtime, finalWaveMode)
	if trigger.requiresFinalWave() && !finalWaveOK {
		s.logAbyssEpicFragmentSkipped(session, runtime, scene, objectKey, groupID,
			abyssEpicFragmentFinalWave{},
			"reason", "final_wave_unknown",
			"waves", len(runtime.HellPartyWaves),
			"mode", mode.String(),
			"trigger", trigger.String(),
			"final_wave_mode", finalWaveMode.String(),
			"final_wave_source", finalWaveSource.String(),
			"binding", "abyssEpicFragmentFinalWaveFor")
		return nil
	}
	// 组内最后一只（厂商 `DropService.cs:179` 的 `IsLastGroupMonster`，
	// 由 `DungeonKillApplicationService.cs:1555-1572` 的 `after == 0` 得出；
	// 调用点在 `RetireSpecialMonsterActor` 之后，所以"同组一条活口都没有"就是它）。
	// **两档都要求它**（这是厂商那道门里唯一被两档共同承认的条件）。
	if alive := dungeonHellPartyGroupAliveCount(runtime.Room, groupID); alive != 0 {
		s.logAbyssEpicFragmentSkipped(session, runtime, scene, objectKey, groupID, finalWave,
			"reason", "not_group_last",
			"group_alive_count", alive,
			"mode", mode.String(),
			"trigger", trigger.String())
		return nil
	}
	if trigger.requiresFinalWave() && !finalWave.containsGroup(groupID) {
		// **默认档**：不是最后一波 ⇒ 不发（操作者选定的"只最后一波"口径；
		// 厂商的 `IsLastGroupMonster` 对**每一组**都成立，所以这一条比厂商更窄）。
		// `group_last` 档**不**走这一条（那才是厂商口径）。
		//
		// 这一条就是 P0 那条（队员打死房间最后一波的最后一只却判成"不是最后一波"）：
		// `final_wave_source=` 点名用的是房间表还是会话表，`room_wave_groups=` 把房间真值
		// 一起打出来，两者不一致时一眼可判。
		roomWave, _ := currentDungeonHellPartyRoomFinalWave(runtime)
		s.logAbyssEpicFragmentSkipped(session, runtime, scene, objectKey, groupID, finalWave,
			"reason", "not_final_wave",
			"mode", mode.String(),
			"trigger", trigger.String(),
			"final_wave_mode", finalWaveMode.String(),
			"final_wave_source", finalWaveSource.String(),
			"room_wave_order", roomWave.Order,
			"room_wave_groups", roomWave.groupList(),
			"session_wave_groups", abyssEpicFragmentSessionWaveGroupList(runtime))
		return nil
	}

	// 碎片身份：只用既有的 `etc/epicpieceinfo.etc [piece list]` 目录（复用 epic_piece_book.go）。
	catalog, catalogErr := s.epicPieceCatalog()
	if catalogErr != nil || catalog == nil || catalog.count() == 0 {
		s.logAbyssEpicFragmentDeferred(session, runtime, scene, objectKey, groupID, finalWave,
			"reason", "epic_piece_catalog_unavailable",
			"path", currentEpicPieceInfoPath,
			"error", catalogErr)
		return nil
	}
	monsterCatalog, monsterErr := s.dungeonMonsterCatalog()
	if monsterErr != nil {
		s.logAbyssEpicFragmentDeferred(session, runtime, scene, objectKey, groupID, finalWave,
			"reason", "drop_route_unavailable",
			"error", monsterErr)
		return nil
	}
	dropCatalog, dropErr := monsterCatalog.DropCatalog()
	if dropErr != nil {
		s.logAbyssEpicFragmentDeferred(session, runtime, scene, objectKey, groupID, finalWave,
			"reason", "drop_route_unavailable",
			"error", dropErr)
		return nil
	}
	visit, visitErr := runtime.currentDungeonRoomVisit(scene)
	if visitErr != nil || visit == nil {
		s.logAbyssEpicFragmentDeferred(session, runtime, scene, objectKey, groupID, finalWave,
			"reason", "drop_route_unavailable",
			"error", visitErr)
		return nil
	}

	amounts, amountsErr := abyssEpicFragmentAmountsFromEnv()
	dungeonDifficulty := int(runtime.Request.Difficulty)
	tier := abyssEpicFragmentTierLetter(runtime.HellPartyDifficulty)
	amount, amountKey := amounts.amountFor(dungeonDifficulty, tier)
	if amountsErr != nil {
		// 非法 spec **不静默**：记一条具名事件 + WARN，并继续用内置默认表发保底
		//（绝不因此变成零掉落）。
		s.logGameEvent(session, "game-dungeon-abyss-epic-fragment-amounts-invalid",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"env", abyssEpicFragmentAmountsEnv,
			"env_value", amounts.envValue,
			"fallback_spec", amounts.specText(),
			"dungeon_difficulty", dungeonDifficulty,
			"tier", abyssEpicFragmentTierText(runtime.HellPartyDifficulty),
			"amount", amount,
			"amount_key", amountKey,
			"reason", "amount_spec_invalid_using_builtin_default",
			"error", amountsErr)
		s.logWarn("dnfbridge abyss epic fragment amount spec invalid",
			"env", abyssEpicFragmentAmountsEnv,
			"env_value", amounts.envValue,
			"fallback_spec", amounts.specText(),
			"error", amountsErr)
	}

	// 抽碎片：均匀抽**候选下标**（PVF 顺序），用的是**既有房间掉落 LCG**
	// （`currentDungeonDropLCG`，与概率那一支同一个流、同一个种子存档字段）。
	// 每次一个随机数、每次一条掉落（与厂商"N 次投骰 → N 条 CreateItem(id, 1)"同形；
	// 同一件碎片被抽中两次是允许的，厂商独立投骰同样会重复）。
	//
	// **候选集 = 等级带内的碎片**（操作者缺陷「深渊掉落了 lv80 的史诗碎片」）：
	// 碎片的等级 = 它的成品装备在 `.equ` 里的 `[minimum level]`，生效带默认 `[85,90]`
	// （`DNFBRIDGE_ABYSS_ITEM_LEVEL_FLOOR` / `_CEILING`，见
	// `dungeon_abyss_item_level_floor.go`）。回退档（`legacy`）返回的就是
	// `catalog.pieces` 原序全量，抽法逐字节等于本改动前。真档实测：全量 966 件里
	// 只有 248 件在带内（lv85 138 件 + lv90 110 件），被挡下的 718 件 = 65/70/75/80。
	//
	// **保底仍然是结构性的**：候选列表非空时循环体没有"抽不到就跳过"的分支 ——
	// `amount` 个碎片**一定**被抽出来。唯一能让本支不发的理由是候选为空
	// （回退档不可能：目录装载期就拒绝空表）或等级索引读不到（上面已判）。
	levelBand := abyssItemLevelBandFromEnv().forDungeon(runtime.Dungeon.Metadata.MinimumRequiredLevel.Value, runtime.Dungeon.Metadata.BasisLevel.Value)
	pieces, levelIndex, levelErr := currentAbyssEpicPieceLevelCandidates(monsterCatalog, catalog, levelBand)
	if levelErr != nil {
		s.logAbyssEpicFragmentDeferred(session, runtime, scene, objectKey, groupID, finalWave,
			"reason", "epic_piece_level_index_unavailable",
			"path", dungeonDropEquipmentList,
			"catalog_path", currentEpicPieceInfoPath,
			"item_level_band", levelBand.String(),
			"switch", levelBand.String(),
			"error", levelErr)
		return nil
	}
	if len(pieces) == 0 {
		s.logAbyssEpicFragmentDeferred(session, runtime, scene, objectKey, groupID, finalWave,
			"reason", "no_fragment_inside_item_level_band",
			"item_level_band", levelBand.String(),
			"catalog_entries", catalog.count(),
			"catalog_path", currentEpicPieceInfoPath)
		return nil
	}
	rng := newCurrentDungeonDropLCG(visit.DropRNG)
	recipients := partyDungeonDropRecipients(runtime, session.selectedCharacterID, currentSceneActorObjectKey(session.selectedCharacterID))
	selected := make([]hellPartyRewardItem, 0, amount)
	selectedLevels := make([]int64, 0, amount)
	var wires []currentDungeonDeathDropWire
	for _, recipient := range recipients {
		memberSelected := make([]hellPartyRewardItem, 0, amount)
		for attempt := 0; attempt < amount; attempt++ {
			pieceID := pieces[rng.Next(len(pieces))]
			memberSelected = append(memberSelected, hellPartyRewardItem{
				ItemID: pieceID,
				Amount: 1,
			})
			if level, found := levelIndex.levelOf(pieceID); found {
				selectedLevels = append(selectedLevels, level)
			} else {
				selectedLevels = append(selectedLevels, 0)
			}
		}
		if len(memberSelected) != amount {
			// 到这里说明目录在装载后被改过（不可能，但绝不静默少发）。
			s.logAbyssEpicFragmentDeferred(session, runtime, scene, objectKey, groupID, finalWave,
				"reason", "fragment_draw_shortfall",
				"amount", amount,
				"drawn", len(memberSelected),
				"catalog_entries", catalog.count())
			return nil
		}

		request := hellPartyRewardRequest{
			GroupID:               groupID,
			MonsterCode:           dungeonHellPartyRewardMonsterCode(runtime.Room, objectKey),
			AbyssPartyDifficulty:  runtime.HellPartyDifficulty,
			dungeonMonsterCatalog: monsterCatalog,
			dropCatalog:           dropCatalog,
			rng:                   rng,
		}
		memberWires, registerErr := s.registerHellPartyRewardDrops(
			runtime,
			scene,
			request,
			memberSelected,
			recipient.ActorKey,
			recipient.CharacterID,
		)
		// 随机数消耗无论成功与否都要写回：否则失败重试会复用同一个随机序列。
		visit.DropRNG = rng.Seed()
		if registerErr != nil {
			s.logAbyssEpicFragmentDeferred(session, runtime, scene, objectKey, groupID, finalWave,
				"reason", "fragment_drop_registration_failed",
				"amount", len(memberSelected),
				"recipient_char_id", recipient.CharacterID,
				"error", registerErr)
			continue
		}
		selected = append(selected, memberSelected...)
		wires = append(wires, memberWires...)
		s.logGameEvent(session, "game-dungeon-party-member-epic-fragment-rolled", "monster_object_key", objectKey, "recipient_char_id", recipient.CharacterID, "fragments", hellPartyRewardItemList(memberSelected), "amount", len(memberSelected))
	}

	s.logGameEvent(session, "game-dungeon-abyss-epic-fragment-granted",
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"monster_code", dungeonHellPartyRewardMonsterCode(runtime.Room, objectKey),
		"monster_object_key", objectKey,
		"hell_party_difficulty", runtime.HellPartyDifficulty,
		"tier", abyssEpicFragmentTierText(runtime.HellPartyDifficulty),
		"dungeon_difficulty", dungeonDifficulty,
		"group_id", groupID,
		"group_last", true,
		"trigger", trigger.String(),
		"payout_unit", trigger.payoutUnit(),
		"final_wave_order", finalWave.Order,
		"final_wave_index", finalWave.Index,
		"final_wave_groups", finalWave.groupList(),
		"final_wave_source", finalWaveSource.String(),
		"final_wave_mode", finalWaveMode.String(),
		"wave_count", finalWave.WaveCount,
		"is_final_wave", finalWave.containsGroup(groupID),
		"fragments", hellPartyRewardItemList(selected),
		"fragment_count", len(selected),
		"recipient_count", len(recipients),
		"fragment_levels", abyssItemLevelList(selectedLevels),
		"item_level_band", levelBand.String(),
		"item_level_floor", levelBand.Floor,
		"item_level_ceiling", levelBand.Ceiling,
		"item_level_floor_switch", levelBand.FloorSwitch,
		"item_level_ceiling_switch", levelBand.CeilingSwitch,
		"item_level_band_active", levelBand.Active,
		"level_source", abyssItemLevelSource,
		"catalog_candidates_total", catalog.count(),
		"catalog_candidates_in_band", len(pieces),
		"catalog_level_resolved", abyssLevelResolvedCount(levelIndex),
		"catalog_level_unresolved", abyssLevelUnresolvedCount(levelIndex),
		"amount", len(selected),
		"amount_key", amountKey,
		"amount_spec", amounts.specText(),
		"amount_source", amounts.source,
		"amount_env", abyssEpicFragmentAmountsEnv,
		"catalog_entries", catalog.count(),
		"catalog_path", currentEpicPieceInfoPath,
		"route", abyssEpicFragmentRoute,
		"granted", len(wires),
		"switch", mode.String(),
		"reward_roll_switch", rewardMode.String(),
		"asset_owner", "account_epic_piece_metadata_counter",
		"source", abyssEpicFragmentSource)
	return wires
}

// logAbyssEpicFragmentSkipped 记录"这次死亡不是保底事件"以及判定依据。
// `finalWave.WaveCount == 0` 表示连最后一波都没判出来。
func (s *Service) logAbyssEpicFragmentSkipped(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	objectKey uint32,
	groupID int64,
	finalWave abyssEpicFragmentFinalWave,
	fields ...any,
) {
	args := make([]any, 0, len(fields)+20)
	args = append(args,
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"monster_object_key", objectKey,
		"hell_party_difficulty", runtime.HellPartyDifficulty,
		"tier", abyssEpicFragmentTierText(runtime.HellPartyDifficulty),
		"dungeon_difficulty", int(runtime.Request.Difficulty),
		"group_id", groupID,
		"final_wave_order", finalWave.Order,
		"final_wave_groups", finalWave.groupList(),
		"wave_count", finalWave.WaveCount,
		"is_final_wave", finalWave.containsGroup(groupID),
		"trigger", abyssEpicFragmentTriggerFromEnv().String(),
		"payout_unit", abyssEpicFragmentTriggerFromEnv().payoutUnit(),
		"switch", abyssEpicFragmentModeFromEnv().String(),
		"source", abyssEpicFragmentSource,
	)
	args = append(args, fields...)
	s.logGameEvent(session, "game-dungeon-abyss-epic-fragment-skipped", args...)
}

// logAbyssEpicFragmentDeferred 记录"该发但发不出去"（带 reason/error）。
func (s *Service) logAbyssEpicFragmentDeferred(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	objectKey uint32,
	groupID int64,
	finalWave abyssEpicFragmentFinalWave,
	fields ...any,
) {
	args := make([]any, 0, len(fields)+20)
	args = append(args,
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"monster_object_key", objectKey,
		"hell_party_difficulty", runtime.HellPartyDifficulty,
		"tier", abyssEpicFragmentTierText(runtime.HellPartyDifficulty),
		"dungeon_difficulty", int(runtime.Request.Difficulty),
		"group_id", groupID,
		"final_wave_order", finalWave.Order,
		"final_wave_groups", finalWave.groupList(),
		"wave_count", finalWave.WaveCount,
		"is_final_wave", finalWave.containsGroup(groupID),
		"trigger", abyssEpicFragmentTriggerFromEnv().String(),
		"payout_unit", abyssEpicFragmentTriggerFromEnv().payoutUnit(),
		"switch", abyssEpicFragmentModeFromEnv().String(),
		"source", abyssEpicFragmentSource,
	)
	args = append(args, fields...)
	s.logGameEvent(session, "game-dungeon-abyss-epic-fragment-deferred", args...)
}

// abyssEpicFragmentDrawForTest 是抽碎片那一步的**纯函数**版本（供用例直接钉语义）：
// 从目录的 PVF 顺序里均匀抽一个，消耗**恰好一个**随机数。
func abyssEpicFragmentDrawForTest(
	rng *currentDungeonDropLCG,
	catalog *currentEpicPieceCatalog,
) (uint32, bool) {
	if rng == nil || catalog == nil || len(catalog.pieces) == 0 {
		return 0, false
	}
	return catalog.pieces[rng.Next(len(catalog.pieces))], true
}
