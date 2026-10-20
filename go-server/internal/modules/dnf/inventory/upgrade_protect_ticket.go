// 强化/增幅失败「装备被摧毁」时的**装备保护券**判定与消耗。
//
// 现场缺陷（运维原话 2026-09-29）：
//
//	「装备增幅保护券 和装备强化保护券系列道具 未正常生效 强化和增幅时会显示持有
//	 保护券类道具但是不实际保护装备不被破坏」
//
// 拆开是两件事，两件都有原始证据：
//
//	① 客户端 UI 说"你持有保护券" —— 那是**客户端自己扫背包**扫出来的。op50 请求体
//	   里那个"可选券槽"（`UpgradeItemRequest.OptionalTicketSlotIndex`，body +10 的
//	   i16）在线上**恒为 -1**：
//	     packet_log.txt（62 条 `owner applied` op50 行，逐行 ticket=-1）：
//	       ... mode=amplify target=(12,101040148) materialSlot=8 materialPVF=(3242,11)
//	           ticket=-1 name="..."; ... level=10->10 ... resultCode=3
//	       ... mode=reinforce target=(12,101040148) materialSlot=358 materialPVF=(3037,180)
//	           ticket=-1 ... level=13->13 ... resultCode=3
//	   也就是说"用哪张保护券"这件事**客户端从来不说**，服务端必须自己找。
//
//	② 服务端**从来没有找过**。改动前 owner.go 的摧毁档只有一句 `delete(items, targetKey)`：
//	     case 3: // destroy
//	         resultCode = upgradeResultCodeDestroy
//	         delete(items, targetKey)
//	   全树没有任何一处读 `[action type]` 的 `[protect equipment]` /
//	   `[amplify protect equipment]`（grep 见报告），所以保护券既没参与判定、
//	   也没被消耗 —— resultCode=3 与补偿道具 3243/3244 照发。
//
// 厂商参考实现（`a21zigai/86JP联机开源分享包-20260812`，C#
// `DfoServer/Game/Inventory/Services/InventoryItemUpgradeService.cs`）写得很清楚：
//
//	179  else if (penaltyType == 3)
//	181      protectTicket = FindFirstProtectTicket(inventory, command.Mode);
//	182      if (protectTicket != null) {
//	184          protectedByTicket = true;
//	185          effectivePenaltyType = 2;
//	186          newLevel = (byte)Clamp(protectTicket.Config.FailureRetainLevel, 0, oldLevel);
//	188      } else { destroyed = true; newLevel = 0; }
//	199  var resultCode = success ? (byte)0 : (byte)Math.Max(1, effectivePenaltyType);
//	239  if (protectedByTicket && !ConsumeMaterial(inventory, protectTicket.SlotIndex,
//	          protectTicket.Item, protectTicket.Config.Cost, out protectTicketUpdate))
//	336  if (protectTicketUpdate != null)
//	337      AddRefreshSlot(upgradeResult.MainRefreshSlots, protectTicketUpdate.SlotIndex);
//	561  private static ProtectTicketSelection FindFirstProtectTicket(InventoryService inventory, ItemUpgradeMode mode)
//	563      var ticket = FindFirstProtectTicketInRange(inventory, mode, QuickSlotRange);   // (3,8)
//	567      if (!ItemSlotBoundService.TryGetSlotRange(ItemCore.KindMaterial, ...)) ...
//	575      return FindFirstProtectTicketInRange(inventory, mode, materialRange);
//	597  private static bool IsProtectTicketForMode(config, mode) =>
//	         config.Mode == mode && (mode == Amplify ? Kind == ProtectAmplify : Kind == ProtectReinforcement)
//	278  private static ItemUpgradeConsumableConfig FromProtect(...)  // `[protect equipment]`
//	295      config.FailureRetainLevel = ActionTypeParams.Count > 0 ? ActionTypeParams[0] : 0;
//
// 判定真源 = 运行时 PVF 的 `[action type]`（本次改动的**唯一**判据，不硬编码任何 id）：
// 线上 Script.pvf（sha256 afa00e75…）里 9 个真实保护券 stackable 逐字节长这样：
//
//	stackable/10303001/10303749.stk  装备增幅保护券  [action type] `[amplify protect equipment]`
//	stackable/10093001/10093372.stk  装备增幅保护券  [action type] `[amplify protect equipment]`
//	stackable/10098001/10098989.stk  装备增幅保护券  [action type] `[amplify protect equipment]`
//	stackable/10164001/10164047.stk  装备增幅保护券  [action type] `[amplify protect equipment]`
//	stackable/10304001/10304363.stk  装备增幅保护券  [action type] `[amplify protect equipment]`
//	stackable/10093001/10093371.stk  装备强化保护券  [action type] `[protect equipment]`
//	stackable/10147001/10147773.stk  装备保护券      [action type] `[protect equipment]`
//	stackable/10164001/10164034.stk  装备强化保护券  [action type] `[protect equipment]`
//	stackable/10304001/10304354.stk  装备强化保护券  [action type] `[protect equipment]`
//
// 九个的 `[action type]` 段里**都没有整数参数**，所以 FailureRetainLevel = 0
// （参考实现的缺省档），与它们自己的 `[explain]` 一致：
//
//	`强化失败造成装备损坏时， 可以使用该物品， 使装备回到未强化状态。`
//	`… 使装备回到非增幅状态。`
//	10304354 更直白：`强化失败时， 装备的强化等级归零。`
//
// 开关（自读环境 `os.Getenv`，与 upgrade_destroy_bonus.go 同惯例，**不动**
// dnfbridge/options.go）：
//
//	DNFBRIDGE_UPGRADE_PROTECT_TICKET
//	  consume_first_match（默认，= 本轮修复档）
//	      失败 + 惩罚=摧毁 时，按"主背包槽升序（从 3 起，跳过账号共享固定槽 /
//	      材料槽 / 目标槽）"问运行时 PVF 每一行是不是本模式的保护券；第一个命中的
//	      行消耗 1 张，装备改为回落到该券 `[action type]` 首参（缺省 0）且
//	      resultCode 由 3（摧毁）变成 2（= 参考的 max(1, effectivePenaltyType)）。
//	  legacy（回退字面量）
//	      一次 PVF 都不问，摧毁档与改动前逐字节相同（resultCode=3 + 补偿道具照发）。
//	  其它取值（含空、含大小写混杂）= 默认档；回退只需改 env 重启，无需回滚二进制。
//
// 扫描顺序为什么是"主背包升序"而不是照抄参考的两段：
// 参考先扫快捷栏 (3,8) 再扫材料段（本仓库 `item_target_container.go`: `[material]`
// → list 0 121..176）。升序扫描**先经过 3..8 再经过材料段**，因此只要券落在这两段里，
// 选中的行与参考完全一致；差别只在参考**不看的** 9..120（装备页/消耗品页）—— 券被玩家
// 拖到那些页时参考会说"没有券"，本实现照样认。这是有意放宽（现场报障正是"UI 说持有、
// 服务端不认"），并且只在"券确实是本模式的保护券"时才生效。
//
// 为什么跳过账号共享固定槽（`dnfrepo.IsAccountSharedInventorySlot`）：那是晶块/灵魂的
// 账号级固定槽，玩家拖不进保护券；把它排除掉可以避免任何越权消耗的想象空间。
package inventory

import (
	"os"
	"sort"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// UpgradeProtectTicketEnv 是装备保护券判定的环境变量名。
	UpgradeProtectTicketEnv = "DNFBRIDGE_UPGRADE_PROTECT_TICKET"
	// UpgradeProtectTicketConsumeFirstMatch 是本轮修复档（默认）字面量。
	UpgradeProtectTicketConsumeFirstMatch = "consume_first_match"
	// UpgradeProtectTicketLegacy 是修复前的回退字面量：一次 PVF 都不问，
	// 摧毁档与改动前逐字节相同。
	UpgradeProtectTicketLegacy = "legacy"
	// upgradeProtectTicketScanStart 是主背包里可以被保护券占用的最小槽位。
	// 参考实现的快捷栏下界（ItemSlotBoundService.MainQuickSlotStart = 3）；0..2 是
	// 虚拟货币槽，永远不是道具行。
	upgradeProtectTicketScanStart int16 = 3
	// upgradeProtectTicketDefaultRetainLevel 是 PVF `[action type]` 没写整数参数时的
	// 回落等级（参考实现 FailureRetainLevel 的缺省 0 = 回到未强化/非增幅状态）。
	upgradeProtectTicketDefaultRetainLevel = 0
)

// upgradeProtectTicketMode 归一化开关取值；任何未识别的取值都落到默认档
// （绝不静默变成回退档）。
func upgradeProtectTicketMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(UpgradeProtectTicketEnv))) {
	case UpgradeProtectTicketLegacy, "off", "0":
		return UpgradeProtectTicketLegacy
	default:
		return UpgradeProtectTicketConsumeFirstMatch
	}
}

// upgradeProtectTicketSelection 是一次"用哪张券"的选中结果。
type upgradeProtectTicketSelection struct {
	slot        int16
	itemID      int64
	count       int64
	cost        int64
	retainLevel int
	pvfPath     string
}

// upgradeProtectTicketLookupActive 报告这一档是否真的会做保护券判定：
// 开关不是回退档、解析器可用、mode 合法。owner 用它填 ProtectTicketScanned
// （= 日志里"这次到底走没走保护券这一步"），findUpgradeProtectTicket 用它做同样的
// 短路，两处口径只在这一处定义。
func upgradeProtectTicketLookupActive(cmd Command) bool {
	if upgradeProtectTicketMode() == UpgradeProtectTicketLegacy || cmd.UpgradeProtectTicketResolver == nil {
		return false
	}
	return strings.TrimSpace(cmd.Mode) != ""
}

// findUpgradeProtectTicket 在**本角色的主背包**里找第一张本模式的装备保护券。
//
// 返回 (选中行, 被问过的行数, 第一条解析失败原因, 是否命中)。四种"不命中"都返回 ok=false，
// 于是摧毁档保持既有行为：
//   - 开关是 legacy（回退档）；
//   - 解析器缺失（非 dnfbridge 调用方 / 测试）；
//   - 背包里没有任何一行的 `[action type]` 是本模式保护券；
//   - 命中的行数量不足一张（count < cost）。
//
// 解析失败（PVF 读不出来）只记账、不中断扫描：把它当成"不是券"，绝不因为一次解析失败
// 就多发一张保护，也绝不因此改变其它行的判定。
func findUpgradeProtectTicket(
	items map[string]dnfrepo.ItemStack,
	cmd Command,
	excludedSlots ...int16,
) (upgradeProtectTicketSelection, int, string, bool) {
	if !upgradeProtectTicketLookupActive(cmd) {
		return upgradeProtectTicketSelection{}, 0, "", false
	}
	slots := upgradeProtectTicketCandidateSlots(items, cmd, excludedSlots...)
	asked := 0
	firstErr := ""
	for _, slot := range slots {
		stack := items[slotKey(listTypeMain, slot)]
		if stack.ItemID <= 0 || stack.Count <= 0 {
			continue
		}
		asked++
		resolution, err := cmd.UpgradeProtectTicketResolver(stack.ItemID)
		if err != nil {
			if firstErr == "" {
				firstErr = "slot=" + strconv.Itoa(int(slot)) + " item=" + strconv.FormatInt(stack.ItemID, 10) + ": " + err.Error()
			}
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(resolution.Mode), strings.TrimSpace(cmd.Mode)) {
			continue
		}
		cost := resolution.UnitCost
		if cost <= 0 {
			cost = 1
		}
		if stack.Count < cost {
			continue
		}
		retain := resolution.RetainLevel
		if retain < 0 {
			retain = upgradeProtectTicketDefaultRetainLevel
		}
		return upgradeProtectTicketSelection{
			slot:        slot,
			itemID:      stack.ItemID,
			count:       stack.Count,
			cost:        cost,
			retainLevel: retain,
			pvfPath:     resolution.PVFPath,
		}, asked, firstErr, true
	}
	return upgradeProtectTicketSelection{}, asked, firstErr, false
}

// upgradeProtectTicketCandidateSlots 是扫描顺序：主背包 list 0 的槽位升序，
// 从 3 起，显式排除账号共享固定槽 —— 见文件头"扫描顺序为什么是主背包升序"。
// 目标槽与材料槽一并排除：目标是装备（永远不可能是 stackable 券），材料槽这一轮已经
// 被当成材料按张扣过（同一行既当材料又当保护券会把一张券扣两次）。
func upgradeProtectTicketCandidateSlots(items map[string]dnfrepo.ItemStack, cmd Command, excludedSlots ...int16) []int16 {
	slots := make([]int16, 0, len(items))
	for key, stack := range items {
		if stack.Count <= 0 || stack.ItemID <= 0 {
			continue
		}
		listType, slot, ok := parseSlotKey(key)
		if !ok || listType != listTypeMain || slot < upgradeProtectTicketScanStart {
			continue
		}
		if slot == cmd.TargetSlotIndex || slot == cmd.MaterialSlotIndex {
			continue
		}
		if dnfrepo.IsAccountSharedInventorySlot(listTypeMain, slot) {
			continue
		}
		excluded := false
		for _, reserved := range excludedSlots {
			if slot == reserved {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		slots = append(slots, slot)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
	return slots
}

// consumeUpgradeProtectTicket 从选中行扣掉 cost 张，返回扣减后的那一行
// （count <= 0 时返回零值 ItemStack，调用方据此删行 + 让 op14 发空行）。
// 数量不足时返回 ok=false（调用方必须放弃保护、回到摧毁档）。
func consumeUpgradeProtectTicket(
	items map[string]dnfrepo.ItemStack,
	selection upgradeProtectTicketSelection,
) (dnfrepo.ItemStack, bool) {
	cost := selection.cost
	if cost <= 0 {
		cost = 1
	}
	key := slotKey(listTypeMain, selection.slot)
	stack, ok := items[key]
	if !ok || stack.Count < cost || stack.ItemID != selection.itemID {
		return dnfrepo.ItemStack{}, false
	}
	stack = cloneStack(stack)
	stack.Count -= cost
	if stack.Count <= 0 {
		delete(items, key)
		return dnfrepo.ItemStack{}, true
	}
	updateStackRawAmount(&stack)
	items[key] = stack
	return stack, true
}

// upgradeProtectTicketPayload 把这次判定如实交给 dnfbridge（领域层没有 logger）。
// 只有真的走到"失败 + 惩罚=摧毁"这一档时才由 owner 调用；legacy 档下 scanned=false。
func upgradeProtectTicketPayload(
	result UpgradeResult,
	switchValue string,
) *alignedcmd.UpgradeProtectTicket {
	if !result.ProtectTicketScanned {
		return nil
	}
	return &alignedcmd.UpgradeProtectTicket{
		Mode:          result.Mode,
		Consumed:      result.ProtectTicketConsumed,
		Slot:          result.ProtectTicketSlot,
		ItemID:        result.ProtectTicketItemID,
		Remaining:     result.ProtectTicketRemaining,
		PVFPath:       result.ProtectTicketPVFPath,
		RetainLevel:   result.ProtectTicketRetainLevel,
		OldLevel:      result.OldLevel,
		NewLevel:      result.NewLevel,
		Candidates:    result.ProtectTicketCandidates,
		ResolverError: result.ProtectTicketScanError,
		Switch:        switchValue,
	}
}
