package equip

import (
	"os"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件是 op19 的**方向判定本身**：客户端那条"登录复述穿戴态"的 op19 只能被当成
// **声明**（claim），永远不许改写权威穿戴状态。
//
// 现场那条包（每一步都有 packet_log.txt 原文）：
//
//	src=(list0 主背包, slotN, instance=0) count=1 dst=(list3 装备, slotS, instance=<那件 item id>)
//
// 两个方向都是同一个缺陷的两面（操作者 2026-09-28 更正口径）：
//
//	(a) 槽 S 上**穿着**点名那件 ⇒ 客户端在说"这件属于这一格"。旧口径按"两端占用"
//	    反转端点 ⇒ o.unequip ⇒ 穿着的武器在**每一次重登**被卸进背包
//	    （现场 packet_log.txt:76912 = `equipMove=unequip … item=101040493 changed=true`，
//	    同一形状的复述在 :93098 / :91884 每条登录都会出现）。
//	(b) 槽 S **空着**（操作者故意把它脱下来了）⇒ 客户端仍在声明"这一格穿着这件"，
//	    而权威状态说不是。旧口径走到最后那支 `if aOccupied { … }` ⇒ o.equip ⇒
//	    **玩家刚脱下来的武器又被穿了回去**。
//
// 两面的共同点是：这条包的身份写在**目的（装备）端点**上，它声明的是"那一格现在是什么"，
// 而不是"把某件物品搬过去"（真搬运把身份写在**源**端点 —— 现场 :93127 手动穿上、
// 13:30:17 手动换回光剑都是这个口径）。所以它必须判 noop、一个字节都不写、
// moveValue 照实为 0；**与时间窗、与任何开关、与到达时刻无关**。
//
// 为什么"复述 vs 真移动"不能靠包体字节区分（量出来的，不是猜的）：同一形状出现过两种
// 结局，且包体逐字节相同 —— 复述 `packet_log.txt:93098`（t=13:24:21.46511482Z）与
// `:91884`（13:20:21.733523941Z）的前 6 字节计数器不同、28 字节包体完全相同；旧口径下
// 真落库的 `:76912`（入站 t=10:37:20.89653374Z）与它们同样只差计数器：
//
//	hex 01130029000000c97de1fe22000010000000000001000000030c006dc1050600000000ffffffff0000  :93098
//	hex 01130029000000805fff851f000010000000000001000000030c006dc1050600000000ffffffff0000  :91884
//	hex 01130029000000162069b12b00000a000000000001000000030c006dc1050600000000ffffffff0000  :76912
//
// 站得住的口径因此只有端点角色 + 权威状态：
//
//	目的端 = list3 装备、目的端实例 != 0（客户端在装备端点上指名那件）
//	源端   = list0 主背包 或 list1 装扮栏、源端实例 == 0（客户端没有声明要搬的东西）
//	count  = 1
//
// 这条形状即 dnfbridge 的 loginWearReassertFamilyOp19Shape（桥侧只做形状作用域，
// 权威占用仍然只有这里一处真相）。

// WornSlotReassertGate 是方向判定的具名值之一：目的槽上**穿着**客户端点名的那件 ——
// 声明与权威状态一致，请求要的结果已经达成 ⇒ noop。
//
// 它必须与同族另外四条**互不重叠**（现场按 `noopGate=` 计数时才分得开）：
//   - identity_echo                   要求 SourceInstanceValue  > 0（身份写在背包端点）；
//   - login_wear_reassert_noop        要求登录引导窗口打开 + 主开关 =1（时间窗兜底）；
//   - login_wear_undeclared_reversal  要求装备端点指名的是**别**的物品（身份矛盾）；
//   - duplicate_endpoint_identity     要求两端实例非零且相等；
//   - 本条与本文件的 WornSlotStaleClaimGate 只处理"源端实例为 0 + 装备端点点名那件"
//     这一种形状，与窗口、与时间无关。
const WornSlotReassertGate = "worn_slot_reassert"

// WornSlotStaleClaimGate 是方向判定的另一个具名值：目的槽**空着**，客户端却声明它穿着
// 点名那件 —— 声明与权威状态矛盾（玩家刚把它脱下来），**同样 noop**：绝不用一条陈述
// 把背包里那件穿回去。现场 AD 组合（脱武器 → 选角 → 再进角色）就是这一支。
const WornSlotStaleClaimGate = "worn_slot_stale_claim"

// equipmentMirrorReequipEchoGate 是"镜像回穿"这条闸门的具名值（写进 MoveResult.NoopGate）。
//
// 场景：我们刚把某件装备**真落库脱下**，客户端在 0.6~0.8 s 后发来一条形状 c
// （`src=(0,背包格,IV=物品) dst=(3,装备槽,IV=0)`）把它穿回去 —— 那是客户端在复述
// 它**脱下之前**的本地视图（厂商参考实现把这一族判 ClientEcho）。
// 判据来自**我们自己的落库记录**（同一件 + 同一槽 + 窗口内的间隔），与客户端声明无关。
// 现场：2026-09-29T10:35:31.967 unequip → 10:35:32.733 equip（间隔 0.765 s）。
// 开关与窗口见 dnfbridge/equip_mirror_requip_echo.go。
const equipmentMirrorReequipEchoGate = "mirror_requip_echo"

// wornSlotReassertSwitchEnv 是这条方向判定的开关名。
const wornSlotReassertSwitchEnv = "DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT"

// equipmentWornSlotReassertEnabled 是 DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT 的运行时
// 读取（每次调用都读 env，不缓存、不改 options.go）。读法与
// equipmentMoveDuplicateIdentityGuardEnabled / equipmentMoveReversalIdentityGuardEnabled
// 完全一致：现场改档不必重新编译，回退必须**显式**写出来。
//
// **默认开启（修复档）**：只有显式写 off/0/false/no/legacy/disable/disabled/none 才是
// 回退档。回退档逐字节等于今天（两个方向都回到旧口径）：
//   - 槽上穿着那件 ⇒ resolveMoveEndpoints 末尾的反转端点兜底 ⇒ o.unequip
//     （现场 :76912 的 `equipMove=unequip changed=true`）；
//   - 槽空着 ⇒ 末尾 `if aOccupied` ⇒ o.equip（刚脱下来的那件又被穿回去）。
func equipmentWornSlotReassertEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(wornSlotReassertSwitchEnv))) {
	case "off", "0", "false", "no", "legacy", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

// equipmentWornSlotReassertClaim 只看**客户端自己写的字段**：这条 op19 是不是在声明
// 装备槽的穿戴态（而不是请求一次搬运）。
//
// 真搬运把身份写在源端点：手动穿上（packet_log.txt:93127，操作者把背包 11 格的钝器拖上
// 武器格 12）是 src=(0,11,instance=101020668) dst=(3,12,instance=0)；本判据要求
// 源端实例 == 0 且目的端实例 != 0，两条都相反 ⇒ 真穿上一律不受影响。
func equipmentWornSlotReassertClaim(cmd MoveCommand) bool {
	if !equipmentWornSlotReassertEnabled() {
		return false
	}
	// 玩家请求标志位（op19 尾字节 body[26]==1，客户端模块 8 只在严格形状上写）：
	// 这条包**不是**复述，是玩家亲手拖拽 ⇒ 本族门禁一律不吸收，交给反转端点兜底
	// 按厂商口径执行真·脱下。见 dnfbridge/equip_player_request_flag.go。
	if cmd.PlayerRequestFlag {
		return false
	}
	// 重试档（默认，见 MoveCommand.WornSlotClaimOccurrence 与
	// dnfbridge/equip_worn_slot_claim_occurrence.go）：
	//
	// 同一个装备槽在**同一个登录纪元**里第 2 条及以后的同形声明，不是登录批的复述，
	// 而是玩家"拖下来了但没生效"的重试 —— 必须放行给厂商口径的反转端点搬运（= 真脱下）。
	// 只有第 1 条（登录批每槽一条）继续按复述吞掉。
	//
	// 0 = 不启用重试档（今天的默认：全部吞掉；桥侧 DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT
	// 写 all 或 env 缺省且桥侧不计数时的取值）；1 = 本纪元第 1 条（吞）；
	// ≥2 = 重试（放行 ⇒ resolveMoveEndpoints 的反转端点兜底把它执行成 unequip）。
	if cmd.WornSlotClaimOccurrence > 1 {
		return false
	}
	if cmd.SourceListType != wireListMain && cmd.SourceListType != wireListAvatar {
		return false
	}
	if cmd.DestinationListType != wireListEquipment {
		return false
	}
	if cmd.MoveCount != 1 {
		return false
	}
	if cmd.SourceInstanceValue != 0 {
		return false
	}
	return cmd.DestinationInstanceValue != 0
}

// equipmentWornSlotReassert 把这条声明与**权威状态**对照，回答"要不要判 noop、由哪条
// 具名闸门具名"：
//
//   - 目的槽**空着** ⇒ 声明与权威状态矛盾（玩家刚把它脱下来）⇒ noop
//     （WornSlotStaleClaimGate），绝不用这条陈述把背包里那件穿上去；
//   - 目的槽穿着**点名那件** ⇒ 声明已经成立 ⇒ noop（WornSlotReassertGate）；
//   - 目的槽穿着**别的**东西 ⇒ **不归本条**（客户端点的不是穿着的那件；这一格今天
//     仍然走反转端点兜底，pinned 用例
//     TestOwnerMoveUnequipUsesNextEmptyMainSlotFromStaleClientDestination 钉的就是它）。
//
// 为什么对真穿/脱/换装安全（每条都有现有用例）：
//   - 真穿上（源端写身份、目的端为空，现场 :93127）：源端实例 != 0 ⇒ 不成立；
//   - 真脱下（穿着的那件在**源端装备槽**、目的端背包槽）：源端 list 必须是 0/1 ⇒ 不成立；
//   - 换装/顶上（源端有东西、目的端指名另一件）：目的端身份不符 ⇒ 不成立；
//   - 双端点同名（src 实例 == dst 实例 != 0）：源端实例 != 0 ⇒ 不成立
//     （仍由 duplicate_endpoint_identity 具名）；
//   - 厂商同源复述（源端写身份、目的端为 0）：目的端实例 == 0 ⇒ 不成立
//     （仍由 identity_echo 具名）。
//
// 残余风险（必须知情，操作者已明确接受这一口径）：玩家**手动**把身上那件拖下来时，
// 若客户端发的也是这个形状（源端空背包实例 0 + 目的端装备槽指名穿着的那件），本条会判
// noop：客户端按 moveValue=0 回滚、**不丢数据**（那件仍在身上），但玩家需要改用"源端为
// 装备槽"的拖法。反过来按旧口径执行，同一形状在**每一次**登录都会改写穿戴状态
// （要么卸下、要么把刚脱下来的穿回去）—— 两害相权取前者。
func equipmentWornSlotReassert(cmd MoveCommand, equipped dnfrepo.EquipmentEntry, equipmentOccupied bool) (bool, string) {
	if !equipmentWornSlotReassertClaim(cmd) {
		return false, ""
	}
	if !equipmentOccupied {
		return true, WornSlotStaleClaimGate
	}
	if equipped.ItemID > 0 && int64(cmd.DestinationInstanceValue) == equipped.ItemID {
		return true, WornSlotReassertGate
	}
	return false, ""
}
