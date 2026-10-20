// `[unlimited waste]` / `[waste instant item]`：两个"族名以 waste 打头、但吃不到
// `HasPrefix(token, "[waste]")` 兜底"的族。本文件只承载**判据**，语义依据如下
// （全部逐字取自线上真档 Script.pvf，sha256 afa00e75…05fe）。
//
// # 一、[unlimited waste] = 同一个消耗品族，但"使用后不会被消耗"
//
// 1) 同名同效果对照件（真档原文）：
//
//	stackable/cash/hands_remy_shine.stk        [stackable type] `[waste]` 0
//	stackable/cash/chn_490700370.stk           [stackable type] `[unlimited waste]` 1
//	两者 [name] 都是 `闪亮的雷米援助`，[icon] 都是 `Item/stackable/cash.img` 778，
//	[item group name] `potion`、[move wav] `POTION_TOUCH`、[cool time] 10000、
//	[cooltime group] 2，以及逐字节相同的效果块：
//	    [hp recovery] `%` 30 500 `myself`
//	    [mp recovery] `%` 30 500 `myself`
//	    [active status recovery] `any` 999 `myself`
//	唯一的差别就是 [stackable type] 的记号（以及 [stack limit]）。
//
// 2) 族内 223 件里有 20 件的 [explain] 逐字写明"不消耗"：
//
//	10008480 新手恢复药剂  stackable/…/chn_2015_newbie_recovery_potion_10008480.stk
//	    `使用后， 恢复30%%的HP和MP。 该道具使用后不会被消耗。`
//	10006618 F1庆典药剂   stackable/event/…/chn_hpmp_potion_10006618.stk
//	    `使用后， 恢复30%%的HP和MP。 12月17日维护前可以无限次数地使用。`
//	10301306 HP/MP祝福药剂 stackable/10301001/10301306.stk
//	    `在剩余时间内使用该药剂， 可以恢复HP和MP， 并且药剂不会被消耗。`
//	51 无限猫头鹰          stackable/cash/owl_unlimited.stk   `… (可永久使用)`
//	1120 服务器喇叭 (GM)   stackable/megaphone_heartbeatgm.stk `… (无限使用)`
//	10008051 不灭之远古精灵的秘药 `… 不灭之远古精灵的秘药不会被消耗。`
//
// 结论（不是推测）：`[unlimited waste]` 与 `[waste]` **是同一类消耗品**，
// `unlimited` 这个修饰词修饰的是"用完之后道具不消失"，不是"另一种东西"。
// 所以**不能**把它并进 `[waste]` 的扣数分支——那会把"永久使用的药剂"用一次就吃掉
// （490700370 的 [stack limit] 就是 1）。本族走
// `stackableTypeSkipsSourceConsumption` 的"回成功 ACK、不动数量"分支。
//
// 族内并非全是药水（还有 51/2660234 猫头鹰、1120 喇叭、1143-1198 混沌王牌等召唤/服务
// 类道具，它们的效果需要服务端后续链路）。这一族按"用而不耗"放行的**最坏结果只是
// 维持现状**（客户端没有后续动作 ⇒ 仍然没有效果），永远不会吃掉玩家道具，
// 因此是全族放行的安全边界。
//
// # 二、[waste instant item] = 塔内即时消耗品，扣数是正确的
//
//	stackable/fp/towercompptn_health.stk  `(塔) 特级生命药剂`
//	    [explain] `离开塔时， 该物品会消失。`
//	    [stackable type] `[waste instant item]` 1
//	    [cool time] 10000 / [move wav] `POTION_TOUCH` / [hp recovery] `%` 50 1000 `myself`
//
// 它是普通药水形状（与 `[waste]` 药水同一组效果段），差别只在生命周期（离开塔时消失）。
// "instant" 描述的是塔内生命周期，不是"用了不消耗"，所以本族走**正常扣 1** 分支。
//
// # 三、开关（都在本文件自读 env，不动 options.go）
//
//	DNFBRIDGE_ITEM_UNLIMITED_WASTE_USE  默认 = 修复（族被接受 + 用而不耗）
//	                                     回退字面量 legacy / off / 0 / false / disabled
//	                                     = 该族重新落回改动前（fail-closed，error_code=13）
//	DNFBRIDGE_ITEM_WASTE_INSTANT_USE     同上，作用于 [waste instant item]
package inventory

import (
	"os"
	"strings"
)

// currentUnlimitedWasteToken / currentWasteInstantItemToken 是两族的
// `[stackable type]` 首列记号，逐字取自真档 PVF（不做前缀匹配）。
const (
	currentUnlimitedWasteToken   = "[unlimited waste]"
	currentWasteInstantItemToken = "[waste instant item]"
)

// useFamilyRollback 是三个第二轮开关共用的归一化：回退字面量统一为
// legacy / off / 0 / false / disabled（与第一轮 DNFBRIDGE_ITEM_UPGRAD_BUFF_USE 一致）。
func useFamilyRollback(env string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(env))) {
	case "legacy", "off", "0", "false", "disabled":
		return true
	default:
		return false
	}
}

// currentUsePVFProvenanceRollback 报告是否回退到"只读库存元数据"的旧判据。
func currentUsePVFProvenanceRollback() bool {
	return useFamilyRollback("DNFBRIDGE_ITEM_USE_PVF_PROVENANCE")
}

// currentUnlimitedWasteUseRollback 报告是否把 `[unlimited waste]` 重新拒掉。
func currentUnlimitedWasteUseRollback() bool {
	return useFamilyRollback("DNFBRIDGE_ITEM_UNLIMITED_WASTE_USE")
}

// currentWasteInstantItemUseRollback 报告是否把 `[waste instant item]` 重新拒掉。
func currentWasteInstantItemUseRollback() bool {
	return useFamilyRollback("DNFBRIDGE_ITEM_WASTE_INSTANT_USE")
}
