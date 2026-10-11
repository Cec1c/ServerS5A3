// 便携强化/增幅消耗品（一次性增幅器 / 凯丽的强化器）在 op50 上的判定与消耗。
//
// 现场缺陷（运维 2026-09-29「一次性增幅器出错」，截图公告栏「强化装备出现错误：
// 错误代码：174」、物品窗口标题「一次性增幅器」、底部「持有20个装备增幅保护券」）：
//
//	packet_log.txt:196377（全窗口**唯一**一条 error=174）
//	... mode=amplify target=(23,101010856) materialSlot=107 materialPVF=(3242,11)
//	    ticket=-1 ... error=174; returned current-EXE op50 error ACK body_len=28
//	    t=2026-09-28T18:36:26.908892579Z
//	同角色随后 4 条 error=22（18:40:22 / 18:40:38 / 18:42:39 / 18:45:23），同一族现象。
//
// 材料槽 107 里的那一行是 `10164868`（`dnf_inventory_items` 0:107 = 10164868 ×50，
// pvf_path=`stackable/10164001/10164868.stk`）＝**一次性增幅器**：
//
//	[explain] `使用后， 可以对装备进行增幅。 不消耗矛盾的结晶体和金币。`
//	[action type] `[portable amplify]` 0 30
//
// 也就是说客户端把**消耗品本身放进材料槽**发 op50（ticket 恒 -1，线上 62/62 都是 -1），
// 而服务端改动前完全没有 portable 档：那一行被当成普通材料，请求落进克伦特档
// （要求表里的 3242×11 + 金币），并且在看材料之前就先撞上"增幅属性未鉴定"的门
// ⇒ 玩家看到 174（目标未鉴定）或 22（目标已鉴定但材料不对），消耗品一件不少。
//
// 判定真源 = 运行时 PVF 的 `[action type]`（dnfbridge/upgrade_portable_consumable.go），
// 语义照厂商 `ItemUpgradeScene.Portable`：
//
//   - 消耗品**替代**表里的材料与金币（Cost = 该道具 ×1、Gold = 0）；
//   - 成功率与**惩罚档**都走同一张正常表（`etc/upgrade.etc` / `etc/amplifyupgrade.etc`）
//     ⇒ 失败**可以摧毁**，所以保护券在这一档同样生效（同一条摧毁分支）；
//   - 允许的当前等级窗口 = `[action type]` 第 1、2 个参数（厂商
//     `AllowsConsumableCurrentLevel`，越界 = 错误码 19 restriction）；
//   - 第 3 个参数 = 十万分比**乘算**加成（厂商 `SuccessRateBonusWeight`，
//     真档唯一形态是「成功之兆强化器」`[new portable upgrade] 0 14 30000` = ×1.3）。
//
// 开关（自读环境 `os.Getenv`，与 upgrade_protect_ticket.go 同惯例，**不动**
// dnfbridge/options.go）：
//
//	DNFBRIDGE_UPGRADE_PORTABLE_CONSUMABLE
//	  vendor（默认，= 本轮修复档）
//	      按上面的厂商语义走 portable 档。
//	  legacy（回退字面量，另接受 off / 0）
//	      解析器不再给出 portable 身份 ⇒ 请求逐字节回到改动前的行为
//	      （消耗品当普通材料 ⇒ 174 / 22），不需要回滚二进制。
//	  其它取值（含空、含大小写混杂）= 默认档。
package inventory

import (
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// UpgradePortableConsumableEnv 是便携强化/增幅消耗品判定的环境变量名。
	UpgradePortableConsumableEnv = "DNFBRIDGE_UPGRADE_PORTABLE_CONSUMABLE"
	// UpgradePortableConsumableVendor 是本轮修复档（默认）字面量。
	UpgradePortableConsumableVendor = "vendor"
	// UpgradePortableConsumableLegacy 是修复前的回退字面量。
	UpgradePortableConsumableLegacy = "legacy"
	// upgradePortableRestrictionError 是"消耗品不允许用在当前等级"的客户端可见错误码
	// （厂商 `ItemUpgradeResult.ErrorRestriction = 19`，ItemUpgradeCommand.cs:23）。
	upgradePortableRestrictionError byte = 19
)

// upgradePortableConsumableEnabled 报告便携档是否生效（默认生效）。
func upgradePortableConsumableEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(UpgradePortableConsumableEnv))) {
	case UpgradePortableConsumableLegacy, "off", "0":
		return false
	default:
		return true
	}
}

// upgradeSuccessWeightScale 是成功率的满值（十万分比）。与
// owner.go 的 currentUpgradeSuccessBase / 厂商 `WeightScale` 同值。
const upgradeSuccessWeightScale = 100000

// applyUpgradeSuccessMultiplier 把"十万分比乘算加成"作用到基础成功率上。
//
//	weight * (100000 + bonus) / 100000        （厂商 CalculateFinalSuccessWeight:652-653）
//
// bonus <= 0 时返回入参本身（逐字节不变）。结果**封顶**在 100000：
// 满值 = 必定成功，且与"失败惩罚档"互斥（`rolled < successWeight` 在
// weight = 100000 时恒真，因此永远走不到摧毁/降级分支）。下界同样是 0。
//
// 为什么封顶值取 100000：它就是本仓与厂商共用的成功率量纲满值
// （`currentUpgradeSuccessBase = 100000`、`WeightScale = 100000`），
// PVF 的成功率段（`[equipment reinforcement ticket]` 的百分数 ×1000、
// 升级表 failure_weight 的 100000 基数）都以它为满值。厂商自己也是这么夹的
// （`CalculateFinalSuccessWeight` 末尾 `Math.Min(WeightScale, finalWeight)`）。
//
// **口径只有这一处**（成功率 buff 药水与便携消耗品共用）：
//   - 乘算（当前，上游 2026-09-29 确认 `newRate = oldRate × 1.1`）= 下面带 `* factor` 那一行；
//   - 若哪天要改成"加 10 个百分点"，只改那一行为 `scaled := weight + int64(bonus)`（bonus 单位
//     仍是十万分比：10 个百分点 = 10000），本函数其余部分（封顶/下界/无加成短路）不动。
func applyUpgradeSuccessMultiplier(weight int64, bonus int) int64 {
	if bonus <= 0 {
		return weight
	}
	factor := int64(upgradeSuccessWeightScale) + int64(bonus)
	if factor < 0 {
		factor = 0
	}
	scaled := weight * factor / int64(upgradeSuccessWeightScale)
	if scaled > upgradeSuccessWeightScale {
		return upgradeSuccessWeightScale
	}
	if scaled < 0 {
		return 0
	}
	return scaled
}

// upgradePortableConsumableError 校验"这一件便携消耗品能不能用在这次请求上"，
// 返回客户端可见错误码（0 = 通过）。判据全部来自运行时 PVF + 请求本身：
//
//	模式必须相同（厂商 `materialConfig.Mode != command.Mode` → 23 wrong mode）；
//	当前等级必须落在 `[action type]` 的窗口里（厂商 `AllowsConsumableCurrentLevel`
//	  → 19 restriction）。
func upgradePortableConsumableError(portable *alignedcmd.UpgradePortableConsumable, cmd Command, oldLevel byte) byte {
	if portable == nil {
		return 0
	}
	if !strings.EqualFold(strings.TrimSpace(portable.Mode), strings.TrimSpace(cmd.Mode)) {
		return upgradeErrorWrongMode
	}
	if int(oldLevel) < portable.LevelMin || int(oldLevel) > portable.LevelMax {
		return upgradePortableRestrictionError
	}
	return 0
}

// upgradePortableConsumableMaterial 从材料槽取出这一件消耗品。
// 返回 (行, 错误码)。错误码沿用克伦特档的 22（invalid material）：
// 槽里没有东西 / 不是那一件 / 数量不足一张，都是"材料不合法"。
func upgradePortableConsumableMaterial(
	items map[string]dnfrepo.ItemStack,
	cmd Command,
	portable *alignedcmd.UpgradePortableConsumable,
) (dnfrepo.ItemStack, string, byte) {
	if portable == nil || cmd.MaterialSlotIndex < 0 {
		return dnfrepo.ItemStack{}, "", upgradeErrorInvalidMaterial
	}
	if cmd.MaterialSlotIndex == cmd.TargetSlotIndex {
		return dnfrepo.ItemStack{}, "", upgradeErrorInvalidMaterial
	}
	key := slotKey(listTypeMain, cmd.MaterialSlotIndex)
	stack, ok := items[key]
	if !ok || stack.Count < 1 || stack.ItemID != portable.ItemID {
		return dnfrepo.ItemStack{}, "", upgradeErrorInvalidMaterial
	}
	return stack, key, 0
}
