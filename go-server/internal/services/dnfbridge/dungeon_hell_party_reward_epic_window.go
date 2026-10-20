package dnfbridge

import (
	"os"
	"strconv"
	"strings"
)

// 本文件是「深渊每 4–5 个房间 1 件史诗」（操作者 2026-09-29 第 9 条口径）的**落地概率闸门**。
//
// ── 操作者原话（逐字，`_dist/运维实测-20260929-0500.md` 第 9 条）────────────────
//
//	「深渊爆率问题 **要写成评价打4-5个深渊房间看到一个史诗装备才对**
//	  **碎片还是每次都要有** **难度1掉落7个碎片按难度增加掉率**」
//
// ⇒ 史诗装备的**期望掉率 = 每 4–5 个房间 1 件**（每房间 0.20–0.25 件）。
//
// ── 为什么需要这个闸门（先读 `_dist/abyss-epic-roll-order.md`）────────────────
//
// 上一轮把"稀有度闸门与抽取的次序"修对了（`DNFBRIDGE_HELL_PARTY_REWARD_RARITY_ORDER`
// 默认 `within`：候选 = 允许档各桶的**并集**，抽一次加权）—— 这是**对的，不要动**。
// 但它的算术副作用是：默认档 `rarity_allowed=4` 下并集里**只有**史诗，
// 于是"每一次命中都落地一件史诗"。线上实测（2026-09-29T04:41–04:55Z，同一名玩家）：
//
//	04:44:51 grp=128 rolls_performed=8 rolls_missed=0 rolls_passed_no_item=0 selected_count=8 granted=8
//	04:44:54 grp=213 rolls_performed=8 rolls_missed=4 rolls_passed_no_item=0 selected_count=4 granted=4
//	04:48:23 grp=125 … selected_count=7 granted=7 ／ 04:48:30 grp=212 … selected_count=6 granted=6
//	04:55:09 grp=127 … selected_count=7 granted=7 ／ 04:55:15 grp=213 … selected_count=7 granted=7
//
// 一次深渊房间（map 60083 / room `3:0`）有**两个波次组** ⇒ **两次奖励结算**
// （`dungeonHellPartyRewardWaves` 对每一组的最后一只都成立），
// 难度 1 每次结算 8 抽、命中率 876/1001 ⇒ 每间 ≈ 14.0 次命中 ⇒ **每间 ≈ 14 件史诗**。
//
// ⇒ 本文件要动的**不是次序、不是取哪一档**，只是"命中的那些抽里，**哪几次真的落地**"。
//
// ── 口径：史诗 = 稀有度抽签落在最顶端的 W/1,000,000 窗口内 ────────────────────
//
// 厂商 `HellMonsterDropConfig.cs:63-67 RollHellRarity` 的那一抽
// （`lcg.Next(1000000) + 1`，均匀分布在 `[1, 1000000]`）**已经抽了**，它本身就携带
// "这一抽算多稀有"的全部信息。本闸门**不再抽任何随机数**，只对这一次已经抽出的均匀数
// 做**第二次区间判读**：
//
//	roll > 1000000 - W  ⇒  这一抽落地（W = 窗口宽度）
//
// 于是：
//
//	W = 1000000 ⇒ **每一次命中都落地**（= 今天的 shipped 行为，字面量 `legacy`/`off`）；
//	W = 7000    ⇒ 与厂商自己的 r4 档**逐位重合**（A 档阈值 `0 599000 993000 993000 1000000`
//	              ⇒ r4 = `roll ∈ [993001, 1000000]` = 7000/1e6 = 0.7%），字面量 `vendor`；
//	W = 16000   ⇒ **默认档**（= 操作者口径，算式见下）；
//	W = 0       ⇒ 一件史诗都不掉（碎片照旧，见 `dungeon_hell_party_epic_fragment.go`）。
//
// ── 算式（默认档 W = 16000 是怎么算出来的，不拍脑袋）────────────────────────
//
// 记号（全部来自 PVF 与线上实测，不是估计）：
//
//	N = `etc/hellparty.etc [difficulty]` A 档 `RewardRollCount` = **8**
//	    （每次结算的投骰次数；线上 `reward_roll_count=8`）
//	r = `etc/itemdropinfo_monster_hell.etc [dungeon difficulty drop prob]` 覆盖 basis=90 的那一行
//	    = `335 875 1000 1400 1000`（副本难度 0..4；线上 `drop_rate=` 与难度逐一对上）
//	c = `rate * BaseDropRate(100) / 100` = `r`（`HellMonsterDropConfig.cs:53`）
//	p_hit = (min(c, 1000) + 1) / 1001 —— 判定是 `roll > c ⇒ miss`，`roll = lcg.Next(1001) ∈ [0,1000]`
//	S = 每个深渊房间的**结算次数**：map 60083 两个波次组 ⇒ **S = 2**（线上实测，见上）
//	W = 窗口宽度（本开关）
//
// ① 每次结算的期望命中次数（副本难度 1）：
//
//	E[hits/settlement] = N × p_hit = 8 × 876/1001 = 7.0010
//
// ② 每个房间的期望命中次数：
//
//	E[hits/room] = S × N × p_hit = 2 × 7.0010 = **14.0020**
//
// ③ 每个房间的期望史诗件数（闸门生效时，命中后落地的概率 = W/1e6）：
//
//	E[epics/room] = 14.0020 × W/1e6
//
// ④ 操作者要"4–5 个房间 1 件" ⇒ E[epics/room] ∈ [1/5, 1/4] = [0.20, 0.25]：
//
//	W ∈ [0.20 × 1e6 / 14.0020, 0.25 × 1e6 / 14.0020] = [14284, 17855]
//
// ⑤ 取带内整千的数 **W = 16000**：
//
//	E[epics/room] = 14.0020 × 0.016 = **0.2240 件/间** ⇒ 1 / 0.2240 = **4.46 间 1 件** ✓ 在 4–5 之间
//	E[epics/settlement] = 7.0010 × 0.016 = 0.1120 件
//
// 同一算式下别的副本难度（命中率不同 ⇒ **史诗也随难度递增**，与操作者
// 「按难度增加掉率」自洽；这一条是 `r` 那一行自己的形状，不是我们编的）：
//
//	难度 0：E[hits/room] = 2 × 8 × 336/1001 = 5.371 ⇒ 0.0859 件/间 ⇒ 11.6 间 1 件
//	难度 1：E[hits/room] = 2 × 8 × 876/1001 = 14.002 ⇒ 0.2240 件/间 ⇒ **4.46 间 1 件**
//	难度 2：E[hits/room] = 2 × 8 × 1001/1001 = 16.000 ⇒ 0.2560 件/间 ⇒ 3.91 间 1 件
//	难度 3：同一行 `1400`（> 1000 ⇒ 恒命中） 16.000 ⇒ 0.2560 件/间 ⇒ 3.91 间 1 件
//	难度 4：同一行 `1000`（恒命中）          16.000 ⇒ 0.2560 件/间 ⇒ 3.91 间 1 件
//
// ── 与"抽取次序"的关系（为什么不会把上一轮的修复弄回去）──────────────────────
//
// 本闸门**一个随机数都不多抽、也不少抽**：
//
//	命中那一抽：`lcg.Next(1001)`（概率）→ `lcg.Next(1000000)+1`（稀有度，本闸门读它）
//	          → `lcg.Next(totalWeight)`（取件，**照抽**，被窗口挡下时把结果丢掉）
//
// ⇒ 房间掉落 LCG（`dungeon_combat_runtime.go` 的 `visit.DropRNG`）轨迹**逐位不变**：
// 后面紧跟的碎片保底（同一条 LCG，`dungeon_hell_party_epic_fragment.go:826`）
// 抽出来的 7 个碎片与今天**逐件相同**，"取消史诗"绝不会顺手改掉碎片。
// 由 `TestAbyssEpicWindowKeepsTheRandomStreamIdentical` 与真档用例逐位断言。
//
// ── 生效范围（哪里不生效，必须一眼可见）──────────────────────────────────────
//
//  1. 只在**修复档 + 收窄闸门 + `within` 次序**下生效
//     （`decision.UseFixedTier && plan.restricts() && rarityOrder.within()`）——
//     也就是"今天每一次命中都落地一件史诗"的那一档。其余各档逐字节等于今天：
//     `DNFBRIDGE_HELL_PARTY_REWARD_RARITY_ORDER=post_filter`（厂商次序，只有 0.7% 命中落地）、
//     `DNFBRIDGE_HELL_PARTY_REWARD_RARITY=vendor`（全集档）、
//     `DNFBRIDGE_HELL_PARTY_REWARD_POOL=itemdictionary`（回退档）与降级档。
//  2. 只否决**史诗（rarity 4）**那一件：显式列表档（`RARITY=0,4` 之类）的并集里
//     还有别的档，那些档**不受本开关影响**，照旧每一次命中都落地。
//     默认档（`epic` ⇒ 允许集合 = `{4}`）下两者重合，公式与实测都按"只有史诗"算。
//  3. 取不到原始稀有度 roll（`roll <= 0`：回退/降级/热更换那一支）⇒ **放行**
//     （fail-open）：闸门只在真的拿到那个均匀数时才有资格否决，绝不因为"读到 0"而静默吃掉掉落。
//
// ── 开关 ────────────────────────────────────────────────────────────────────
//
// DNFBRIDGE_ABYSS_EPIC_WINDOW
//
//	16000（**默认**，= 操作者口径）：每次命中 1.6% 落地 ⇒ 难度 1 每 4.46 间 1 件。
//	<整数 0..1000000>：窗口宽度（每百万）。14284 ⇒ 恰好 5.00 间 1 件；17855 ⇒ 恰好 4.00 间 1 件。
//	0（或 0 以下）：一件史诗都不掉（碎片照旧必掉）。
//	legacy / off / unlimited / all：**逐字节回退到今天**（闸门关掉 ⇒ 每一次命中都落地，
//	    难度 1 约 14 件/间），回退字面量与本仓惯例一致。
//	vendor：窗口 = 厂商自己的 r4 档 = 7000 ⇒ 每间 0.098 件 ⇒ 10.2 间 1 件
//	    （= "厂商原味爆率"，用来看"按厂商来会长什么样"）。
//	其余一切（空串、拼错、越界数字）：按**默认档**处理，原始取值照进事件字段
//	    `epic_window_switch=`，拼错不会被静默吞掉（与 `readHellPartyRewardRarityPlan`
//	    / `readHellPartyRewardRarityOrderMode` 同一惯例）。
const (
	// abyssEpicWindowEnv 是史诗窗口开关（消费点自读，`options.go` 不加字段）。
	abyssEpicWindowEnv = "DNFBRIDGE_ABYSS_EPIC_WINDOW"
	// abyssEpicWindowBound 是窗口的分母 = 稀有度那一抽的上界
	// （`lcg.Next(1000000) + 1 ∈ [1, 1000000]`，与 `hellPartyRewardRarityRollBound` 同源）。
	abyssEpicWindowBound = int64(1000000)
	// abyssEpicWindowDefault 是默认档宽度（= 操作者口径，算式见文件头 ①②③④⑤）。
	abyssEpicWindowDefault = int64(16000)
	// abyssEpicWindowVendor 是厂商自己 r4 档的宽度（A 档阈值 `… 993000 1000000` ⇒ 7000）。
	abyssEpicWindowVendor = int64(7000)
	// abyssEpicWindowAlways 是"闸门关掉"的宽度（每一次命中都落地 = 今天）。
	abyssEpicWindowAlways = abyssEpicWindowBound
	// abyssEpicWindowUnsetText 是事件字段 `epic_window_switch=` 在"开关未设置"时的取值。
	abyssEpicWindowUnsetText = "-"
	// abyssEpicWindowSource* 是事件字段 `epic_window_source=` 的取值。
	abyssEpicWindowSourceDefault = "builtin_default"
	abyssEpicWindowSourceEnv     = "env"
	abyssEpicWindowSourceAlias   = "alias:"
	// abyssEpicWindowLegacyValue 是"逐字节回退到今天"的字面量（本仓惯例 `legacy`）。
	abyssEpicWindowLegacyValue = "legacy"
	// abyssEpicWindowOffValue 是同一个回退档的第二个字面量。
	abyssEpicWindowOffValue = "off"
	// abyssEpicWindowVendorValue 是"厂商自己的 r4 档"字面量。
	abyssEpicWindowVendorValue = "vendor"
	// abyssEpicSettlementsPerRoom 是"一次深渊房间有几次奖励结算"。
	//
	// 线上实测（map 60083 / room `3:0`，2026-09-28 12:30 起**每一次**进房）：
	// **恒定 2 次**（`dungeonHellPartyRewardWaves` 对每一组的最后一只都成立，
	// 每个房间两个波次组；`hellPartyRewardWaves` 的两条事件相隔 3–7 秒）。
	// 这个数是**地图的性质**，本文件算不出来，只作为报表与用例的算术钉子。
	abyssEpicSettlementsPerRoom = 2
)

// abyssEpicWindowPlan 是一次投骰的"史诗窗口"裁定（也是事件字段的来源）。
type abyssEpicWindowPlan struct {
	// Width 是**生效**窗口宽度（每百万，`0..1000000`）。
	Width int64
	// Switch 是操作者写的原始取值（事件 `epic_window_switch=`，未设置 = `-`）。
	Switch string
	// Source 是宽度的来源：`builtin_default` / `env` / `alias:<字面量>`。
	Source string
}

// gates 报告闸门**是否真的在否决命中**（关掉 = 每一次命中都落地 = 今天）。
func (plan abyssEpicWindowPlan) gates() bool {
	return plan.Width < abyssEpicWindowAlways
}

// permits 报告这一抽的**原始稀有度随机数**有没有落进史诗窗口。
//
//	roll <= 0（拿不到那个均匀数）⇒ **放行**（fail-open，见文件头"生效范围"第 2 条）；
//	Width <= 0                  ⇒ 不放行（窗口宽度 0 = 一件都不掉）；
//	Width >= 1000000            ⇒ 放行（闸门关掉 = 今天）；
//	否则                        ⇒ `roll > 1000000 - Width`（顶端的那一段）。
func (plan abyssEpicWindowPlan) permits(roll int64) bool {
	if roll <= 0 {
		return true
	}
	if plan.Width <= 0 {
		return false
	}
	if plan.Width >= abyssEpicWindowBound {
		return true
	}
	return roll > abyssEpicWindowBound-plan.Width
}

// switchText 是事件字段 `epic_window_switch=`（未设置 = `-`）。
func (plan abyssEpicWindowPlan) switchText() string {
	if plan.Switch == "" {
		return abyssEpicWindowUnsetText
	}
	return plan.Switch
}

// defaultAbyssEpicWindowPlan 是默认档（也是"任何未识别取值"的落点）。
func defaultAbyssEpicWindowPlan(raw string) abyssEpicWindowPlan {
	return abyssEpicWindowPlan{
		Width:  abyssEpicWindowDefault,
		Switch: raw,
		Source: abyssEpicWindowSourceDefault,
	}
}

// readAbyssEpicWindowPlan 解析 DNFBRIDGE_ABYSS_EPIC_WINDOW。
//
// 只把"纯整数 0..1000000"与六个具名字面量当真；其余一切（含空串、拼错、越界）
// 一律回**默认档**，原始取值留在 `Switch` 里由事件暴露。
func readAbyssEpicWindowPlan(value string) abyssEpicWindowPlan {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return defaultAbyssEpicWindowPlan("")
	}
	lower := strings.ToLower(raw)
	switch lower {
	case abyssEpicWindowLegacyValue, abyssEpicWindowOffValue, "unlimited", "all":
		return abyssEpicWindowPlan{
			Width:  abyssEpicWindowAlways,
			Switch: raw,
			Source: abyssEpicWindowSourceAlias + lower,
		}
	case abyssEpicWindowVendorValue:
		return abyssEpicWindowPlan{
			Width:  abyssEpicWindowVendor,
			Switch: raw,
			Source: abyssEpicWindowSourceAlias + abyssEpicWindowVendorValue,
		}
	}
	width, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || width < 0 || width > abyssEpicWindowBound {
		return defaultAbyssEpicWindowPlan(raw)
	}
	return abyssEpicWindowPlan{
		Width:  width,
		Switch: raw,
		Source: abyssEpicWindowSourceEnv,
	}
}

// abyssEpicWindowPlanFromEnv 在**消费点**自读开关（与 `hellPartyRewardRarityPlanFromEnv`
// 同一惯例：options 结构体处在并发编辑下，不往里加字段）。
func abyssEpicWindowPlanFromEnv() abyssEpicWindowPlan {
	return readAbyssEpicWindowPlan(os.Getenv(abyssEpicWindowEnv))
}

// ── 算式（纯函数：事件、用例与报告共用同一份算术，不各算一遍）─────────────────

// abyssEpicHitsPerSettlementPPM 是"一次结算期望命中几次"（× 1e6）：
//
//	E[hits] = rolls × (min(chance, 1000) + 1) / 1001
//
// `chance >= 1000` ⇒ 判定 `roll > chance` 恒不成立（`roll ∈ [0,1000]`）⇒ 恒命中。
func abyssEpicHitsPerSettlementPPM(rolls, chance int64) int64 {
	if rolls <= 0 {
		return 0
	}
	limit := chance
	if limit > hellPartyRewardDropChanceDenominator-1 {
		limit = hellPartyRewardDropChanceDenominator - 1
	}
	if limit < 0 {
		limit = 0
	}
	return rolls * (limit + 1) * abyssEpicWindowBound / hellPartyRewardDropChanceDenominator
}

// abyssEpicExpectationPPM 是"一次结算期望落地几件史诗"（× 1e6）：
//
//	E[epics] = E[hits] × Width / 1e6
//
// （事件字段 `epic_expectation_ppm`；`active == false` 时返回 0 —— 闸门没生效时
// 这一栏没有意义，绝不假装一个数。）
//
// ⚠ 精度声明：这个公式假设"并集里只有史诗"（**默认档**的实际情况）⇒ 在默认档下它是
// **准确值**；显式列表档（`RARITY=0,4` 之类）下并集里还有不受窗口影响的其他档，
// 真实落地件数**低于**这个读数，此时它只当**上界**看。
func abyssEpicExpectationPPM(rolls, chance int64, plan abyssEpicWindowPlan, active bool) int64 {
	if !active {
		return 0
	}
	return abyssEpicHitsPerSettlementPPM(rolls, chance) * plan.Width / abyssEpicWindowBound
}

// abyssEpicRoomsPerDrop 返回"多少间房一件史诗"（报表与用例的算术钉子）：
//
//	E[epics/room] = settlementsPerRoom × E[hits/settlement] × width / 1e6
//	rooms per epic = 1 / E[epics/room]
//
// `width <= 0`（一件都不掉）⇒ 返回 0 表示**不可达**（不是"无穷大"，
// 免得报表里出现 Inf）。
func abyssEpicRoomsPerDrop(settlementsPerRoom, rolls, chance, width int64) float64 {
	if width <= 0 || settlementsPerRoom <= 0 {
		return 0
	}
	perRoom := float64(settlementsPerRoom) *
		float64(abyssEpicHitsPerSettlementPPM(rolls, chance)) / float64(abyssEpicWindowBound) *
		float64(width) / float64(abyssEpicWindowBound)
	if perRoom <= 0 {
		return 0
	}
	return 1 / perRoom
}
