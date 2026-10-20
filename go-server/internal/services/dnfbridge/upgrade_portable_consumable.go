package dnfbridge

import (
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 本文件是 op50「便携强化/增幅消耗品」这一档的**真源**：把材料槽里那一行 stackable
// 翻成厂商 `ItemUpgradeScene.Portable` 的身份（模式 + 允许的等级窗口 + 乘算加成）。
//
// 现场缺陷（运维 2026-09-29：「一次性增幅器出错」，截图公告栏「强化装备出现错误：
// 错误代码：174」、物品窗口标题「一次性增幅器」、底部「持有20个装备增幅保护券」）：
//
//	packet_log.txt:196377（唯一一条 error=174）
//	EVENT kind=game-aligned-command-real-response conn_id=game-000011 ...
//	  account_id=dnf:10001 selected_character_id=1 ... operation=upgrade_item
//	  reason=inventory owner rejected: account="dnf:10001" char=1 mode=amplify
//	  target=(23,101010856) materialSlot=107 materialPVF=(3242,11) ticket=-1
//	  ... error=174; returned current-EXE op50 error ACK body_len=28
//	  t=2026-09-28T18:36:26.908892579Z
//
// 材料槽 107 里那一行是 **10164868**（`dnf_inventory_items` 0:107 = 10164868 ×50，
// Extra pvf_path=`stackable/10164001/10164868.stk`），`pvfscriptedit -dump-text` 逐字：
//
//	[name]
//	`一次性增幅器`
//	[explain]
//	`使用后， 可以对装备进行增幅。
//	    不消耗矛盾的结晶体和金币。`
//	[stackable type]
//	`[town and dungeon]` 0
//	[action type]
//	`[portable amplify]` 0 30
//	[/action type]
//	[check usable itemlevel]
//	1 90
//
// 即：客户端把**消耗品本身放进材料槽**发 op50（mode=amplify、ticket=-1），
// 服务端以前完全没有这一档，于是按普通克伦特档校验（要求 3242×11 + 金币，
// 并且先撞上"增幅属性未鉴定"的门）⇒ 玩家看到 174 / 22，消耗品一件不少。
//
// 厂商参考（C# `ItemUpgradeConsumableResolver.FromActionType` /
// `InventoryItemUpgradeService.AllowsConsumableCurrentLevel` /
// `CalculateFinalSuccessWeight`）：
//
//	233  if (action == "[new portable upgrade]" || action == "[portable upgrade]")
//	234      return FromPortable(..., PortableReinforcement, Reinforce);
//	236  if (action == "[new portable amplify]" || action == "[portable amplify]")
//	237      return FromPortable(..., PortableAmplify, Amplify);
//	248  private static ItemUpgradeConsumableConfig FromPortable(...) {
//	259      Scene = ItemUpgradeScene.Portable,
//	268      Cost  = new ItemUpgradeCost { MaterialItemId = itemTemplateId, MaterialCount = 1, Gold = 0 },
//	271      if (stackable.ActionTypeParams.Count > 2)
//	272          // 86 [action type] 第3参数按十万分比额外加成保存，30000表示在基础成功率上额外加30%。
//	273          config.SuccessRateBonusWeight = stackable.ActionTypeParams[2];
//	647  var additiveWeight = baseWeight + SuccessRateAddWeight + EquippedUpgradeProbabilityIncrease;
//	652  var multiplierWeight = WeightScale + (long)context.SuccessRateBonusWeight;
//	653  var finalWeight = additiveWeight * multiplierWeight / WeightScale;   // ← 乘算
//	769  AllowsConsumableCurrentLevel(config, currentLevel):
//	777      return currentLevel >= config.ActionTypeParams[0] && currentLevel <= config.ActionTypeParams[1];
//
// 真档全量普查（生产 Script.pvf sha256=afa00e75…，54789 个 stackable）：
// **253 个**模板的 `[action type]` 是 portable 族，三大类必须分清：
//
//	[portable upgrade]                        100 个 ┐
//	[new portable upgrade]                      2 个 ┘→ 强化档（凯丽的强化器 …）共 102 个
//	[portable amplify]                         61 个 ┐
//	[new portable amplify]                      1 个 ┘→ 增幅档（一次性增幅器 …）共  62 个
//	[portable genuine damage upgrade upgrede]  89 个  → **便携式锻造炉**（锻造 /
//	                                                    separate upgrade 路径），本实现**不收**
//
// 「成功之兆强化器 / 增幅器」（item 10007838 / 10007839）是真档里唯一带第 3 个参数的
// 形态：`[new portable upgrade] 0 14 30000` / `[new portable amplify] 0 14 30000`
// —— 30000 = 基础成功率 ×1.3，正是厂商注释里的"十万分比乘算加成"。
const (
	currentUpgradeScenePortable = "portable"

	currentUpgradePortableUpgradeAction    = "[portable upgrade]"
	currentUpgradePortableAmplifyAction    = "[portable amplify]"
	currentUpgradePortableNewUpgradeAction = "[new portable upgrade]"
	currentUpgradePortableNewAmplifyAction = "[new portable amplify]"

	// currentUpgradePortableMaximumLevel 是 `[action type]` 没写第 2 个参数时的等级窗口上界。
	// 与厂商 `Clamp(chance.TargetLevel, 0, 31)` / 打包等级字节的 5 位宽一致。
	currentUpgradePortableMaximumLevel = 31
)

// resolvePortableUpgradeConsumable 读一个 stackable 的 `[action type]`，判断它是不是
// 便携强化/增幅消耗品。
//
// 逐字比对记号（不做前缀匹配）：`[portable genuine damage upgrade upgrede]`（便携式锻造炉）
// 因此不会被误收成便携增幅——那是另一条玩法路径。
// 参数按厂商口径取：第 1、2 个 = 允许的当前等级窗口（缺省 0..31），第 3 个 = 十万分比乘算加成。
func resolvePortableUpgradeConsumable(document *dnfpvf.Document) (string, int, int, int, bool) {
	if document == nil {
		return "", 0, 0, 0, false
	}
	actionType, found := document.Text("action type")
	if !found {
		return "", 0, 0, 0, false
	}
	token := strings.ToLower(strings.Trim(strings.TrimSpace(actionType), "`"))
	var mode string
	switch token {
	case currentUpgradePortableUpgradeAction, currentUpgradePortableNewUpgradeAction:
		mode = "reinforce"
	case currentUpgradePortableAmplifyAction, currentUpgradePortableNewAmplifyAction:
		mode = "amplify"
	default:
		return "", 0, 0, 0, false
	}
	params := document.Ints("action type")
	levelMin, levelMax := 0, currentUpgradePortableMaximumLevel
	if len(params) > 0 {
		levelMin = int(params[0])
	}
	if len(params) > 1 {
		levelMax = int(params[1])
	}
	bonus := 0
	if len(params) > 2 && params[2] > 0 {
		bonus = int(params[2])
	}
	return mode, levelMin, levelMax, bonus, true
}
