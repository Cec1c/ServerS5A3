// 强化/增幅失败「装备被摧毁」时补偿道具的落位（自动堆叠）。
//
// 现场（2026-09-28，HK 线上库 dnf_inventory_items，只读查得）：
//
//	角色 6（test78，dnf:10011）item 3243「强烈之痕迹」四行：
//	  0:129 ×27 / 0:137 ×16 / 0:138 ×9 / 0:141 ×9，
//	  四行的 Extra 逐行只有一条 source=upgrade_destroy_bonus。
//	角色 5（扫射之王浩成）item 3244「浓缩的纯洁之骸」三行：
//	  0:60 ×4 / 0:123 ×4 / 0:124 ×4 —— 同一族的增幅档。
//
// 两个 item id 都是运行时 PVF 明写的「摧毁补偿道具」：
// `etc/upgrade.etc` 的 `[disjoint bonus item] 3243`、
// `etc/amplifyupgrade.etc` 的 `[disjoint bonus item] 3244`（GM 索引同证
// kind=stackable / tag=material / stackLimit=0 = 无 [stack limit] 段）。
//
// 两轮缺陷（同一个位置、两层）：
//
//	第一层（2026-09-28 已修）：旧代码**只找空格、从不看既有行**——
//	  `destroySlot := firstEmptyUpgradeSlot(items)` 然后无条件 `items[slotKey(...)] = bonusStack`。
//	  每摧毁一把武器就多一行；判据是仓库层唯一实现 dnfrepo.CanMergeItemStack。
//	第二层（本轮，操作者复报「浓缩的纯洁之骸还是不能正常堆叠」）：那一版虽然会并行了，
//	  但**新开的那一行不写 PVF 堆叠契约键**（只有 source）。于是：
//	  ① 手动拖拽合并（op19 → `canStack` → `pvfStackContract`，owner.go:2972/2998）
//	     要求 Extra 里 `item_kind=stackable` 且 `pvf_path` 以 `stackable/` 开头，
//	     这类行两条都不满足 ⇒ 判成"不可堆叠"，服务端把拖拽当成 **swap** 回给客户端
//	     （owner.go:892-899），玩家看到的就是"两行换了位置、没有并起来"；
//	  ② 新格落点仍是 `firstEmptyUpgradeSlot()` 的 60..69 → 9..59，也就是**装备页**
//	     （item_target_container.go: `[material]` 属于 list 0 121..176），
//	     材料被放进装备页，和玩家材料页里那一堆永远合不到一起。
//
//	线上取证（packet_log.txt，2026-09-28T16:24:33Z，角色 6 增幅档）：
//	  item_id=3244 amount=1 slot=12 merged_into_existing_stack=false created_new_slot=true
//	  —— slot 12 落在装备页 9..64，正是第 ② 层；而 16:26 的 op19 事件
//	  `src=(0,137) dst=(0,129) moveMode=swap moveCount=9` 是第 ① 层。
//	同一次普查里"PVF 可堆叠但缺契约键"的行**只有** 3243（角色 6/15/16，12 行）
//	与 3244（角色 5/6，4 行），另外还有一行历史遗留的更名卡；全服其它 1876 行都带契约键。
//
// 开关（自读环境 `os.Getenv`，与 equip/worn_slot_reassert_gate.go、skillcmd/refund_water.go
// 同惯例，**不动** dnfbridge/options.go）：
//
//	DNFBRIDGE_UPGRADE_DESTROY_BONUS_PLACEMENT
//	  merge_existing_stack（默认，= 本轮修复档）
//	      先并同身份既有行：**本道具 PVF 段内的行优先**（升序），段外行次之（升序，
//	      让历史错页行继续吸收新发放而不是再开一格）；每个既有行按 PVF `[stack limit]`
//	      封顶（<= 0 = 无上限，与 3243/3244 的真实 PVF 一致）。剩余部分新开一格：
//	      落点是**本道具 PVF 段内第一个空格**（`[material]` → 121..176），段满才退回
//	      firstEmptyUpgradeSlot()。新行的 Extra 写全 PVF 堆叠契约键
//	      （item_kind / pvf_path / stackable_type / stack_limit>0），
//	      与 grantCurrentDisjointWarehouseStack 等同口径 ⇒ 手动拖拽也能并。
//	  merge_existing_stack_legacy_row（2026-09-28 那一版，逐字节复现）
//	      合并判据与扫描顺序同旧版（全 list 0 升序），新开格恒为 firstEmptyUpgradeSlot()，
//	      新行 Extra 只有 source=upgrade_destroy_bonus。
//	  first_empty_slot（2026-09-28 之前那一版，逐字节复现）
//	      不看任何既有行，直接 firstEmptyUpgradeSlot() 另开一格，数量恒为本次补偿量。
//	  其它取值（含空、含大小写混杂）= 默认档；回退只需改 env 重启，无需回滚二进制。
package inventory

import (
	"os"
	"sort"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfplacement "longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// UpgradeDestroyBonusPlacementEnv 是摧毁补偿道具落位开关的环境变量名。
	UpgradeDestroyBonusPlacementEnv = "DNFBRIDGE_UPGRADE_DESTROY_BONUS_PLACEMENT"
	// UpgradeDestroyBonusPlacementMerge 是本轮修复档（默认）字面量。
	UpgradeDestroyBonusPlacementMerge = "merge_existing_stack"
	// UpgradeDestroyBonusPlacementLegacyRow 是 2026-09-28 那一版的回退档：
	// 合并既有行但不写 PVF 契约键，新格仍走 firstEmptyUpgradeSlot()，逐字节复现。
	UpgradeDestroyBonusPlacementLegacyRow = "merge_existing_stack_legacy_row"
	// UpgradeDestroyBonusPlacementFirstEmptySlot 是修复前的原始档：不看既有行、直接开一格。
	UpgradeDestroyBonusPlacementFirstEmptySlot = "first_empty_slot"
)

// upgradeDestroyBonusPlacementMode 归一化开关取值；任何未识别的取值都落到默认档
// （绝不静默变成某个回退档）。
func upgradeDestroyBonusPlacementMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(UpgradeDestroyBonusPlacementEnv))) {
	case UpgradeDestroyBonusPlacementFirstEmptySlot:
		return UpgradeDestroyBonusPlacementFirstEmptySlot
	case UpgradeDestroyBonusPlacementLegacyRow:
		return UpgradeDestroyBonusPlacementLegacyRow
	default:
		return UpgradeDestroyBonusPlacementMerge
	}
}

// upgradeDestroyBonusPlacement 描述一次摧毁补偿发放的落位结果。
type upgradeDestroyBonusPlacement struct {
	// Slot 是 ACK 里报给客户端的那个槽（op50 摧毁档 body 的 i16）：有并入时是
	// 第一个并入的既有行，没有可并的行时就是新开的那一格。
	Slot int16
	// Slots 是本次真正改动的主背包槽位（并入的每一行 + 溢出新开格），供 op14 逐槽增量。
	Slots []int16
	// Merged 表示至少并进了一个既有同身份行。
	Merged bool
	// Created 表示新开了一格。
	Created bool
	// Placed 是真正落位的数量（既有行封顶 + 新格容量之后）。
	Placed int64
	// ContractWritten 表示新开的那一行写了 PVF 堆叠契约键（默认档才有；
	// 两个回退档都不写，因此回退档的字节与各自复现的那一版相同）。
	ContractWritten bool
	// InContainer 表示新开的那一格落在本道具 PVF 家族段内。
	InContainer bool
}

// upgradeDestroyBonusSlotRef 是一个 (list, slot) 引用。
type upgradeDestroyBonusSlotRef struct {
	listType byte
	slot     int16
}

// upgradeDestroyBonusMergeSlots 是"并进既有行"的扫描顺序：
//
//	① 本道具 PVF 段内、且已占用的行（升序）——这是玩家在正确页面上看到的那一堆；
//	② 其余主背包（list 0）已占用的行（升序）——让历史错页行（现场 0:12 / 0:60）
//	   继续吸收新发放，而不是每来一次就再开一格；
//	③ 容器不是主背包时，该容器里段外的行（升序）。
//
// 为什么扫全 list 0 而不是 firstEmptyUpgradeSlot 的 60..69/9..59：现场那四行在
// 0:129/0:137/0:138/0:141（PVF 材料段），旧顺序根本看不到它们。
//
// 账号共享槽（list 0 的晶块/灵魂仓段，`dnfrepo.IsAccountSharedInventorySlot`）显式排除：
// 那是账号级固定槽，补偿道具是角色背包物，永远不该并到那里去。
func upgradeDestroyBonusMergeSlots(items map[string]dnfrepo.ItemStack, container alignedcmd.StackGrantContainer) []upgradeDestroyBonusSlotRef {
	inContainer := make([]upgradeDestroyBonusSlotRef, 0, len(items))
	rest := make([]upgradeDestroyBonusSlotRef, 0, len(items))
	for key, stack := range items {
		listType, slot, ok := parseSlotKey(key)
		if !ok || stack.Count <= 0 || !dnfplacement.SlotOpen(listType, slot, container.ContainerState) {
			continue
		}
		ref := upgradeDestroyBonusSlotRef{listType: listType, slot: slot}
		switch {
		case container.Contains(listType, slot):
			inContainer = append(inContainer, ref)
		case listType != listTypeMain && listType != container.ListType:
			continue
		case listType == listTypeMain && dnfrepo.IsAccountSharedInventorySlot(listTypeMain, slot):
			continue
		default:
			rest = append(rest, ref)
		}
	}
	less := func(refs []upgradeDestroyBonusSlotRef) {
		sort.Slice(refs, func(i, j int) bool {
			if refs[i].listType != refs[j].listType {
				return refs[i].listType < refs[j].listType
			}
			return refs[i].slot < refs[j].slot
		})
	}
	less(inContainer)
	less(rest)
	return append(inContainer, rest...)
}

// upgradeDestroyBonusNewSlot 选新开格的落点：先在本道具 PVF 段内找第一个空格，
// 有有效 PVF 分类时共用分类页/快捷格顺序，两处皆满即拒绝选格；
// 仅无分类证据的历史调用继续使用旧顺序，不让已知材料满页后进入装备页。
func upgradeDestroyBonusNewSlot(items map[string]dnfrepo.ItemStack, container alignedcmd.StackGrantContainer) (byte, int16) {
	if container.Valid() {
		policy := dnfplacement.SlotPolicy{ListType: container.ListType, Start: container.SlotStart, End: container.SlotEnd,
			ContainerState:   container.ContainerState,
			AllowQuickSlots:  container.ListType == listTypeMain && container.SlotStart >= 65 && container.SlotStart < 289,
			PreferQuickSlots: container.ListType == listTypeMain && container.SlotStart == 65}
		for _, slot := range policy.EmptySlots() {
			if _, ok := items[slotKey(container.ListType, slot)]; !ok {
				return container.ListType, slot
			}
		}
		return container.ListType, -1
	}
	slot := firstEmptyUpgradeSlot(items)
	if slot < 0 {
		return listTypeMain, -1
	}
	return listTypeMain, slot
}

// placeUpgradeDestroyBonus 把摧毁补偿道具放进背包。
//
//	mode == merge_existing_stack            ：并既有行（段内优先），新格落 PVF 段内并写契约键；
//	mode == merge_existing_stack_legacy_row ：并既有行（旧顺序），新格恒为 firstEmptyUpgradeSlot()；
//	mode == first_empty_slot                ：不看既有行，直接开一格（原始档）。
//
// 返回 ok=false 只有一种情况：既没并上任何一行、也没有空格可开——与修复前
// "firstEmptyUpgradeSlot() < 0 就整笔不发放（装备照样被摧毁）"同口径。
func placeUpgradeDestroyBonus(
	items map[string]dnfrepo.ItemStack,
	itemID int64,
	count int64,
	identity alignedcmd.StackGrantIdentity,
	container alignedcmd.StackGrantContainer,
	mode string,
) (upgradeDestroyBonusPlacement, bool) {
	out := upgradeDestroyBonusPlacement{Slot: -1}
	if items == nil || itemID <= 0 || count <= 0 {
		return out, false
	}
	switch mode {
	case UpgradeDestroyBonusPlacementFirstEmptySlot:
		slot := firstEmptyUpgradeSlot(items)
		if slot < 0 {
			return out, false
		}
		items[slotKey(listTypeMain, slot)] = upgradeDestroyBonusLegacyStack(itemID, count)
		out.Slot = slot
		out.Slots = []int16{slot}
		out.Created = true
		out.Placed = count
		return out, true
	case UpgradeDestroyBonusPlacementLegacyRow:
		// 2026-09-28 那一版：容器信息与契约键都不参与。
		return placeUpgradeDestroyBonusMerging(items, out, itemID, count, identity,
			alignedcmd.StackGrantContainer{}, false)
	default:
		return placeUpgradeDestroyBonusMerging(items, out, itemID, count, identity, container, true)
	}
}

// placeUpgradeDestroyBonusMerging 是两版"先并后开格"的共同实现。
// writeContract=false 时逐字节复现 2026-09-28 那一版（旧扫描顺序 + 旧新格顺序 + 旧 Extra）。
func placeUpgradeDestroyBonusMerging(
	items map[string]dnfrepo.ItemStack,
	out upgradeDestroyBonusPlacement,
	itemID int64,
	count int64,
	identity alignedcmd.StackGrantIdentity,
	container alignedcmd.StackGrantContainer,
	writeContract bool,
) (upgradeDestroyBonusPlacement, bool) {
	// incoming 只用于**比较**，带走 PVF 身份键（pvf_path / item_kind / stackable_type；
	// 缺省即不写 ⇒ 判据按"缺省即兼容"处理）。既有行的记账键与身份键原样保留。
	incoming := dnfrepo.ItemStack{ItemID: itemID, Extra: identity.Extra()}
	incoming.Extra["source"] = "upgrade_destroy_bonus"
	stackLimit := identity.StackLimit
	remaining := count
	for _, ref := range upgradeDestroyBonusMergeSlots(items, container) {
		if remaining <= 0 {
			break
		}
		key := slotKey(ref.listType, ref.slot)
		stack, exists := items[key]
		if !exists || stack.Count <= 0 {
			continue
		}
		if !dnfrepo.CanMergeItemStack(stack, incoming) {
			continue
		}
		if stackLimit > 0 && stack.Count >= stackLimit {
			continue
		}
		room := remaining
		if stackLimit > 0 && stack.Count+room > stackLimit {
			room = stackLimit - stack.Count
		}
		if room <= 0 {
			continue
		}
		stack = cloneStack(stack)
		stack.Count += room
		updateStackRawAmount(&stack)
		items[key] = stack
		if ref.listType == listTypeMain {
			out.Slots = append(out.Slots, ref.slot)
		}
		if out.Slot < 0 && ref.listType == listTypeMain {
			out.Slot = ref.slot
		}
		out.Merged = true
		out.Placed += room
		remaining -= room
	}
	if remaining <= 0 {
		return out, out.Merged
	}
	listType, slot := listTypeMain, int16(-1)
	if writeContract {
		listType, slot = upgradeDestroyBonusNewSlot(items, container)
	} else {
		// 回退档：新格落点恒为历史顺序（与 2026-09-28 那一版逐字节相同）。
		slot = firstEmptyUpgradeSlot(items)
	}
	if slot < 0 {
		// 没有空格：能并的已经并进去了；一个都没并上就整笔不发放（旧行为）。
		return out, out.Merged
	}
	if writeContract {
		items[slotKey(listType, slot)] = upgradeDestroyBonusContractStack(itemID, remaining, identity)
		out.ContractWritten = true
		out.InContainer = container.Contains(listType, slot)
	} else {
		items[slotKey(listType, slot)] = upgradeDestroyBonusLegacyStack(itemID, remaining)
	}
	if listType == listTypeMain {
		out.Slots = append(out.Slots, slot)
	}
	if out.Slot < 0 && listType == listTypeMain {
		out.Slot = slot
	}
	out.Created = true
	out.Placed += remaining
	return out, true
}

// upgradeDestroyBonusLegacyStack 构造一行"旧形状"补偿道具：只有唯一记账键
// source=upgrade_destroy_bonus + raw 行数量镜像。两个回退档的新行都用它，
// 保证回退档的落库字节与各自复现的那一版完全相同。
func upgradeDestroyBonusLegacyStack(itemID int64, count int64) dnfrepo.ItemStack {
	stack := dnfrepo.ItemStack{
		ItemID: itemID,
		Count:  count,
		Extra:  map[string]string{"source": "upgrade_destroy_bonus"},
	}
	updateStackRawAmount(&stack)
	return stack
}

// upgradeDestroyBonusEntryStack 是 op14 增量在"查不到落库行"时的兜底行
// （只有旧调用点才会缺 MainRefresh 的那一格）：形状与真正落库的行一致，
// 默认档带全 PVF 堆叠契约键，两个回退档保持各自的旧形状。
func upgradeDestroyBonusEntryStack(cmd Command, result UpgradeResult) dnfrepo.ItemStack {
	itemID := int64(result.DestroyBonusItemID)
	count := int64(result.DestroyBonusCount)
	if upgradeDestroyBonusPlacementMode() == UpgradeDestroyBonusPlacementMerge {
		return upgradeDestroyBonusContractStack(itemID, count, cmd.UpgradeDestroyBonusIdentity)
	}
	return upgradeDestroyBonusLegacyStack(itemID, count)
}

// upgradeDestroyBonusContractStack 构造一行**带全 PVF 堆叠契约键**的补偿道具。
//
// 为什么必须写：手动拖拽合并的判据是 owner.go 的 `pvfStackContract`，
// 它要求 Extra 里 `item_kind=stackable` 且 `pvf_path` 以 `stackable/` 开头，
// 否则该行被判成"不可堆叠"，op19 会被当成 swap 回给客户端（现场
// `moveMode=swap` 就是这么来的）。键与 limit 的口径与
// dnfbridge/grantCurrentDisjointWarehouseStack 一致（stack_limit 只在上限 > 0 时写，
// 缺省 = PVF 没写 [stack limit] = 无上限）。
//
// 只在"新开格"时写；并入既有行时不动既有行的 Extra——玩家既有行的记账键原样保留，
// 也不会因为一次发放被改写身份。
func upgradeDestroyBonusContractStack(itemID int64, count int64, identity alignedcmd.StackGrantIdentity) dnfrepo.ItemStack {
	extra := identity.Extra()
	extra["source"] = "upgrade_destroy_bonus"
	if identity.StackLimit > 0 {
		extra["stack_limit"] = strconv.FormatInt(identity.StackLimit, 10)
	}
	stack := dnfrepo.ItemStack{ItemID: itemID, Count: count, Extra: extra}
	updateStackRawAmount(&stack)
	return stack
}
