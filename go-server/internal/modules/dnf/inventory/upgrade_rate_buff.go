// 「强化/增幅成功率 buff 药水」的服务端记账与成功率加成。
//
// 运维需求（2026-09-29）：「强化的奥秘 增幅秘药 使用正常 在pvf写入参数 也在服务器加入
// 字段 增加的成功率为**乘算的百分之10**增加」，并由上游确认为
// **`newRate = oldRate × 1.1`**（乘算，不是加 10 个百分点）。
//
// # 为什么需要"记账"
//
// 这一族药水在 PVF 里是 `[stackable type] [item upgrad buff usual upgrade]`（强化）
// / `[item upgrad buff amplify upgrade]`（增幅），效果由 `[stat change duration]` 描述
// （真档 15 件：1800000ms=30 分钟、600000ms=10 分钟、180000ms=3 分钟）。**客户端**靠
// 它自己算 buff 图标与面板；但 op50 的掷点在服务端，所以服务端必须自己知道"这一刻这个
// 角色有没有 buff"，否则永远只能按基础成功率掷点。
//
// op50 请求体里没有任何 buff 字段（线上 62/62 条 op50 都是 16+nameLen 字节，没有尾随
// 数据；`ticket=-1` 同理），所以唯一可行的口径是：**op44 使用药水时记账，op50 掷点前读账**。
//
// # 存在哪里
//
// 角色记录的 `Stats` 映射（`dnf_character_stats` 的"额外键"）：
//
//	upgrade_rate_buff_usual_until_unix    强化 buff 到期时间（Unix 秒）
//	upgrade_rate_buff_usual_bonus         强化 buff 的十万分比加成（10000 = ×1.1）
//	upgrade_rate_buff_usual_item_id       强化 buff 来自哪件药水（取证用）
//	upgrade_rate_buff_amplify_*           增幅侧同形三键
//
// 为什么不是新表/新 scope：op44 的"消耗药水"与 op50 的"读 buff"都要同一条记录，
// 而 `Upgrade` 本来就已经 `o.characters.Load` 拿这条记录（金币就在同一个 Stats 里）。
// 记账发生在**同一个角色资产事务**里（`UseStackable` 对这几件道具改用
// `withinCharacterAssetTransaction`），所以"药水被扣掉"与"buff 被记上"要么一起成立、
// 要么一起不成立 —— 不会出现"扣了药水没有 buff"或"白送 buff"。
//
// # 叠加规则（同模式只取一件，取最高）
//
// 真档 15 件药水的加成在本次 PVF 改动后**都是** 10000，所以"取最高"与"取最新"在当前
// 数据下不可区分；实现按更保守的**同模式只保留最高的一件**（`max`），因此
// 「强化的奥秘 + 强化秘药」不会叠成 ×1.21。厂商参考里**没有**这族药水的任何实现
// （C# `ItemUpgradeConsumableResolver` 只认券/便携/随机附魔三族），所以这条规则是
// 按上游"厂商无参考就只取最高的一件"的指示定的，不是抄来的。
//
// 强化 buff 与增幅 buff 分属两个键、互不影响：强化 buff 永远不会加成增幅掷点，反之亦然。
//
// # 开关
//
//	DNFBRIDGE_UPGRADE_SUCCESS_BOOST
//	  未设置 / 空        = 默认档：加成值取 PVF `[upgrade prob increase]`（本次 PVF 改动
//	                       给 15 件都写了 10000 ⇒ ×1.1）。
//	  数字（如 `1.1`）    = 运营覆盖：所有模式的加成强制用这个**乘数**（1.1 = ×1.1）。
//	                       写成 `1.0` / `1` / `off` / `none` / `legacy` / `0` = **不加成**
//	                       （= 逐字节回到改动前：药水照旧只被消耗）。
//	  其它无法解析的值    = 默认档（绝不静默变成"不加成"）。
//
// 乘算实现只有一处（`applyUpgradeSuccessMultiplier`）：`weight * (100000+bonus)/100000`。
// 若上游要把口径改成"加 10 个百分点"，只需把该函数里的乘法那**一行**换成加法
// （见该函数注释，以及测试 TestApplyUpgradeSuccessMultiplierMultipliesAndCaps）。
package inventory

import (
	"os"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// UpgradeRateBuffBoostEnv 是运营覆盖成功率高加成的环境变量名。
	UpgradeRateBuffBoostEnv = "DNFBRIDGE_UPGRADE_SUCCESS_BOOST"

	// upgradeRateBuffUntilKeyPrefix / bonus / item 三键按模式拼后缀。
	upgradeRateBuffUntilKeyPrefix = "upgrade_rate_buff_"
	upgradeRateBuffBonusSuffix    = "_bonus"
	upgradeRateBuffItemSuffix     = "_item_id"
	upgradeRateBuffUntilSuffix    = "_until_unix"

	// upgradeRateBuffReinforceMode / amplify 是这条账本支持的两个模式（只有这两条
	// 强化/增幅路径会读它；掉落、附魔、锻造、净化一概不读）。
	upgradeRateBuffReinforceMode = "reinforce"
	upgradeRateBuffAmplifyMode   = "amplify"

	// upgradeRateBuffNoBoost 是"加成被显式关掉"的哨兵值（与"没记账"= 0 区分）。
	upgradeRateBuffNoBoost = -1
)

// upgradeRateBuffKeys 返回某个模式的三键名。
func upgradeRateBuffKeys(mode string) (untilKey, bonusKey, itemKey string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case upgradeRateBuffReinforceMode:
		mode = upgradeRateBuffReinforceMode
	case upgradeRateBuffAmplifyMode:
		mode = upgradeRateBuffAmplifyMode
	default:
		return "", "", "", false
	}
	base := upgradeRateBuffUntilKeyPrefix + mode
	return base + upgradeRateBuffUntilSuffix, base + upgradeRateBuffBonusSuffix, base + upgradeRateBuffItemSuffix, true
}

// upgradeRateBuffBoostOverride 解析 DNFBRIDGE_UPGRADE_SUCCESS_BOOST。
//
// 返回 (bonusWeight, override)：
//
//	override=false          = 没设置/空/无法解析 ⇒ 用 PVF 的值（默认档）
//	override=true, bonus<0  = 显式关掉加成（`1` / `off` / …）
//	override=true, bonus>=0 = 用这个十万分比加成（`1.1` → 10000）
func upgradeRateBuffBoostOverride() (int, bool) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(UpgradeRateBuffBoostEnv)))
	if raw == "" {
		return 0, false
	}
	switch raw {
	case "off", "none", "legacy", "disabled":
		return upgradeRateBuffNoBoost, true
	}
	factor, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	if factor <= 1 {
		return upgradeRateBuffNoBoost, true
	}
	bonus := int((factor - 1) * float64(upgradeSuccessWeightScale))
	if bonus <= 0 {
		return upgradeRateBuffNoBoost, true
	}
	return bonus, true
}

// upgradeRateBuffPVFBonus 把解析器给出的 PVF 加成归一化：只有正数才算加成。
func upgradeRateBuffPVFBonus(resolution alignedcmd.UpgradeRateBuffResolution) int {
	if resolution.BonusWeight > 0 {
		return resolution.BonusWeight
	}
	return upgradeRateBuffNoBoost
}

// upgradeRateBuffApplyBonus 按开关决定这次"记账"要写下的加成值。
func upgradeRateBuffApplyBonus(resolution alignedcmd.UpgradeRateBuffResolution) int {
	if bonus, override := upgradeRateBuffBoostOverride(); override {
		return bonus
	}
	return upgradeRateBuffPVFBonus(resolution)
}

// recordUpgradeRateBuff 把一次药水使用记进角色 Stats（会在调用方的事务里落库）。
//
// 同模式只保留**最高**的加成（max），到期时间取"现在 + PVF 持续时间"。
// 显式关掉加成（bonus < 0）时仍然刷新到期时间——客户端那边 buff 图标照样会出现，
// 我们只是不把它算进掷点。
func recordUpgradeRateBuff(
	character *dnfrepo.CharacterRecord,
	resolution alignedcmd.UpgradeRateBuffResolution,
	itemID int64,
	now time.Time,
) (int64, int, bool) {
	if character == nil || resolution.DurationMillis <= 0 {
		return 0, 0, false
	}
	untilKey, bonusKey, itemKey, ok := upgradeRateBuffKeys(resolution.Mode)
	if !ok {
		return 0, 0, false
	}
	bonus := upgradeRateBuffApplyBonus(resolution)
	until := now.Unix() + resolution.DurationMillis/1000
	if character.Stats == nil {
		character.Stats = make(map[string]int64, 3)
	}
	// 同模式取最高：既有的、还没过期的加成更高时不降级（只把时间往后推）。
	if existingUntil := character.Stats[untilKey]; existingUntil > now.Unix() {
		if existingBonus := character.Stats[bonusKey]; existingBonus > int64(bonus) {
			bonus = int(existingBonus)
		}
	}
	character.Stats[untilKey] = until
	character.Stats[bonusKey] = int64(bonus)
	character.Stats[itemKey] = itemID
	return until, bonus, true
}

// upgradeRateBuffActiveBonus 读出某个模式当前生效的加成（十万分比）。
// 没记账 / 已过期 / 显式关掉（<0）都返回 0 = 不加成。
//
// 读取时**再套一次** `DNFBRIDGE_UPGRADE_SUCCESS_BOOST` 覆盖：这样 `off` / `1` 对
// **已经记上的** buff 立刻生效（不用等它过期），而记账里保留的仍是"当时生效的加成"。
// 覆盖本身**不会**凭空产生 buff：没有生效中的账，一律返回 0。
func upgradeRateBuffActiveBonus(character dnfrepo.CharacterRecord, mode string, now time.Time) (int, int64) {
	untilKey, bonusKey, _, ok := upgradeRateBuffKeys(mode)
	if !ok || character.Stats == nil {
		return 0, 0
	}
	until := character.Stats[untilKey]
	if until <= now.Unix() {
		return 0, until
	}
	bonus := character.Stats[bonusKey]
	if bonus <= 0 {
		return 0, until
	}
	if bonus > upgradeSuccessWeightScale {
		bonus = upgradeSuccessWeightScale
	}
	if override, ok := upgradeRateBuffBoostOverride(); ok {
		if override <= 0 {
			return 0, until
		}
		bonus = int64(override)
	}
	return int(bonus), until
}
