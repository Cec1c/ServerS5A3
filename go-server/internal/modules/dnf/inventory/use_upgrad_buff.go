// use_upgrad_buff.go 还承载 op44/op251 的**可用性判据**（第二轮改造）：
//
//   - `stackableUseProvenance` = "PVF 优先、元数据兜底"身份判据（见该函数注释）；
//   - `stackableTypeSkipsSourceConsumption` = `[unlimited waste]` 族的"用而不耗"分支。
//
// 开关（每个族/每个根因各一个，互不牵连；都在本文件自读 env，不动 options.go）：
//
//	DNFBRIDGE_ITEM_UPGRAD_BUFF_USE     第一轮：强化/增幅成功率 buff 药水两族（默认修）
//	DNFBRIDGE_ITEM_USE_PVF_PROVENANCE  第二轮：可用性判据改读运行时 PVF（默认修）
//	DNFBRIDGE_ITEM_UNLIMITED_WASTE_USE 第二轮：[unlimited waste] 族（默认修，用而不耗）
//	DNFBRIDGE_ITEM_WASTE_INSTANT_USE   第二轮：[waste instant item] 族（默认修，扣 1）
package inventory

import (
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件锁死运维报障「强化的奥秘 完全无法使用 不消耗无效果」的服务端根因与修复。
//
// 线上现场（/opt/dnf90/logs/dnf90.out 3406 起、packet_log.txt 27308 起）：
// 右键 490006368 / 490007878 / 490008465 / 490002394 后，op44 一路落到
// `inventory owner 未落库：use stackable requires PVF-proven [waste] provenance`，
// 服务端按 fail-closed 只回 error_code=13 的失败 ACK：
//
//	SEND kind=game-upper pkt_seq=374 msg_id=44 ... hex=012c001b...000d003500000060e7341d
//	                                                          ^^ 00=失败 0d=13
//
// 客户端因此既不扣道具也不生效——玩家看到的就是"完全无法使用、不消耗、无效果"。
//
// 根因：这几个道具的 `[stackable type]` 是 `[item upgrad buff usual upgrade]`，
// 而 op44 owner 契约此前只认 `[waste]` 前缀的堆叠行。
//
// PVF 原文证据（真档 Script.pvf，stackable/490006001/chn_490006368.stk）：
//
//	[stackable type] `[item upgrad buff usual upgrade]` 1000
//	[item group name] `potion`
//	[expert type] `[alchemist]`
//	[cool time] 1800000
//	[stat change duration] 1800000 `myself`
//	[action usable place] `[village]` `[seria room]`
//
// 即：它在 PVF 里就是一瓶"药水"（`[item group name] potion`），和 `[waste]` 药水同族。
// 服务端能如实做的只有"扣 1 件 + 回 op44 成功 ACK"（与 `[waste]` 药水完全同一条路径，
// 见 owner.go 的 UseStackable）；buff 的具体数值 PVF 里没有任何参数，见报告"未证实"节。
//
// 开关：DNFBRIDGE_ITEM_UPGRAD_BUFF_USE
//   - 默认（未设置 / 空 / 其它任意值）= 修复后行为；
//   - 回退字面量 legacy / off / 0 / false / disabled = 逐字节回到旧行为
//     （这两个家族重新落回 fail-closed，客户端只收到 error_code=13）。
var currentItemUpgradBuffStackableTypes = []string{
	"[item upgrad buff usual upgrade]",
	"[item upgrad buff amplify upgrade]",
}

// currentItemUpgradBuffUseRollback 报告是否回退到"只有 [waste] 前缀能过"的旧行为。
func currentItemUpgradBuffUseRollback() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DNFBRIDGE_ITEM_UPGRAD_BUFF_USE"))) {
	case "legacy", "off", "0", "false", "disabled":
		return true
	default:
		return false
	}
}

// stackableTypeToken 取 `[stackable type]` 原文的首列记号。
//
// PVF 原文是"记号 + 堆叠上限"两列（真档：`[waste] 4`、`[item upgrad buff usual upgrade] 1000`），
// 数据库 Extra 里保存的是整行原文，所以判据必须先归约到首列记号再比较。
// 记号之前只允许反引号/引号/空白；出现别的前缀就判为不可识别（缺证据 = 拒绝）。
func stackableTypeToken(stackableType string) string {
	normalized := strings.ReplaceAll(stackableType, "`", "")
	start := strings.Index(normalized, "[")
	if start < 0 {
		return ""
	}
	if strings.TrimSpace(strings.Trim(normalized[:start], `"'`)) != "" {
		return ""
	}
	end := strings.Index(normalized[start:], "]")
	if end < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(normalized[start : start+end+1]))
}

// stackableTypeIsItemUpgradBuff 报告 `[stackable type]` 首列记号是否属于
// "强化/增幅成功率 buff 药水"这两个家族。逐字相等，不做前缀匹配。
func stackableTypeIsItemUpgradBuff(stackableType string) bool {
	token := stackableTypeToken(stackableType)
	if token == "" {
		return false
	}
	for _, candidate := range currentItemUpgradBuffStackableTypes {
		if token == candidate {
			return true
		}
	}
	return false
}

// stackableTypeIsPVFProvenUseConsumable 是 op44 owner 契约的类型判据：
// `[waste]` 首列记号（既有行为，逐字节等价于原来的 HasPrefix("[waste]") 判据）
// 加上三个默认开启、各自独立开关的族（见 currentItemUpgradBuffUseRollback 等）。
func stackableTypeIsPVFProvenUseConsumable(stackableType string) bool {
	token := stackableTypeToken(stackableType)
	if token == "" {
		return false
	}
	// Throwing items (for example PVF item 6004, berry_sky.stk) use the
	// same op44 consumption receipt. The client owns the projectile effect;
	// the inventory owner must debit the proved source once on success.
	if token == "[waste]" || token == "[throw]" {
		return true
	}
	if !currentItemUpgradBuffUseRollback() && stackableTypeIsItemUpgradBuff(token) {
		return true
	}
	// 前缀 bug：`[unlimited waste]` / `[waste instant item]` 的族名以 waste 打头，
	// 但原有判据是 HasPrefix(token, "[waste]")，所以这两族一直吃不到兜底。
	// 语义依据见 use_limited_waste.go（不是猜的：PVF 原文 + 同族 [explain]）。
	if !currentUnlimitedWasteUseRollback() && token == currentUnlimitedWasteToken {
		return true
	}
	if !currentWasteInstantItemUseRollback() && token == currentWasteInstantItemToken {
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// 第二轮：可用性判据以运行时 PVF 为准（PVF 优先、元数据兜底）
// ---------------------------------------------------------------------------

// stackableUseProvenance 是 op44/op251 的可用性判据唯一入口。
//
// 为什么不能只读元数据：判据原先读的是库存行 `Extra["stackable_type"]`，而授予链路
// 上有一批行**只写了 item_kind + pvf_path**（线上 146 行 slots / 69 行 warehouse
// 缺该键，其中 141 行有 pvf_path），于是连 `[waste]` 药水也被 fail-closed 拒掉：
// 线上实例 `10007484 她的心意`（PVF 就是 `[waste]`）被拒 2 次。
//
// 现在的顺序：
//  1. 运行时 PVF（解析器注入，键是**库里的 item id**，不是客户端请求里的字段）
//     给出了可解析的 `[stackable type]` ⇒ **以它为准**（PVF 是权威）；
//  2. PVF 没有答案（解析器为零 / 报错 / 该 id 不在 stackable.lst / 类型段为空）
//     ⇒ 逐字节回退到改动前的元数据判据（pvfProvenWasteStack）。
//
// 解析器报错**不**等于拒绝：读不到 PVF 时元数据仍是既有的证据来源，这里不制造
// 新的失败面（回退路径 = 改动前的行为）。开关 DNFBRIDGE_ITEM_USE_PVF_PROVENANCE
// 的 legacy 档 = 完全不调用解析器 = 逐字节回到改动前。
func stackableUseProvenance(
	stack dnfrepo.ItemStack,
	resolver alignedcmd.StackableUseProvenanceResolver,
) (string, string, bool) {
	if stack.Extra == nil || !strings.EqualFold(strings.TrimSpace(stack.Extra["item_kind"]), "stackable") {
		return "", "", false
	}
	if resolver != nil && !currentUsePVFProvenanceRollback() {
		resolution, err := resolver(stack.ItemID)
		if err == nil && resolution.Found && resolution.ItemID == stack.ItemID {
			// This server-side reward must use the guild/inventory transaction.
			// Generic op44/op251 consumption cannot apply it.
			if strings.EqualFold(strings.TrimSpace(resolution.ActionType), "[add guildmileage]") {
				return "", "", false
			}
			path := strings.TrimSpace(resolution.PVFPath)
			token := stackableTypeToken(resolution.StackableType)
			if path != "" && token != "" {
				if stackableTypeIsPVFProvenUseConsumable(token) {
					return path, token, true
				}
				// PVF 是权威：它说这一族不是"可右键使用的消耗品"就不放行
				// （与改动前的元数据判据同为 fail-closed，只是证据更强）。
				return "", "", false
			}
		}
	}
	// The paired archive's known guild bag must never fall back to a bare
	// [waste] debit if PVF is unavailable or provenance rollback is enabled.
	if stack.ItemID == 10153530 || strings.EqualFold(strings.TrimSpace(stack.Extra["action_type"]), "[add guildmileage]") {
		return "", "", false
	}
	return pvfProvenWasteStack(stack)
}

// stackableTypeSkipsSourceConsumption 报告这次使用**不扣源道具数量**。
//
// 只有 `[unlimited waste]` 一族走这条分支，依据是该族 PVF 原文的
// `[explain]`（20 件逐字写明"不会被消耗 / 无限使用 / 可永久使用"）与
// `[waste]` 同名同效果对照件（见 use_limited_waste.go）。
// 回退开关打开时该族根本不被接受，所以这里返回 false 也不会改变旧行为。
func stackableTypeSkipsSourceConsumption(stackableType string) bool {
	if currentUnlimitedWasteUseRollback() {
		return false
	}
	return stackableTypeToken(stackableType) == currentUnlimitedWasteToken
}
