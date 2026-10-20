package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/inventory"
)

// game-upgrade-destroy-bonus-stack-merge 是"强化/增幅失败、装备被摧毁"时补偿道具
// 落位的**唯一**事件名（现场缺陷：角色 6 的 item 3243 强烈之痕迹被拆成四行
// 0:129/0:137/0:138/0:141，四行 Extra 全是 source=upgrade_destroy_bonus；
// 第二轮复报：3244 浓缩的纯洁之骸在 2026-09-28T16:24:33Z 被放到 0:12 —— 装备页）。
//
// 复测时一行就能判读"这次到底并进去了还是又开了一格、落在哪一段"：
//
//	item_id=3243 amount=7 placed_amount=7 slot=129 slots=[129]
//	merged_into_existing_stack=true created_new_slot=false
//	stack_limit=0 stack_pvf_path=stackable/material/vestiges_intensive.stk stack_item_kind=stackable
//	stack_stackable_type=[material]
//	container_list=0 container_slot_start=121 container_slot_end=176
//	placement_switch=merge_existing_stack
//	placement_switch_env=DNFBRIDGE_UPGRADE_DESTROY_BONUS_PLACEMENT
//	placement_switch_default=merge_existing_stack
//	placement_switch_legacy_row_literal=merge_existing_stack_legacy_row
//	placement_switch_rollback_literal=first_empty_slot
//
// 为什么事件在 bridge 而不是领域层：internal/modules/dnf/inventory 没有 logger，
// 它会话口径也不该知道日志格式。领域层只把事实（alignedcmd.UpgradeDestroyBonusMerge）
// 交出来，格式与落点由这里决定。
const upgradeDestroyBonusMergeEventName = "game-upgrade-destroy-bonus-stack-merge"

// upgradeDestroyBonusMergeEventFields 组装该事件的业务字段（会话字段由调用方前置）。
// grant == nil（非摧毁档）时返回 nil，调用方因此一条都不打。
func upgradeDestroyBonusMergeEventFields(grant *alignedcmd.UpgradeDestroyBonusMerge) []any {
	if grant == nil {
		return nil
	}
	return []any{
		"item_id", grant.ItemID,
		"amount", grant.GrantedCount,
		"placed_amount", grant.PlacedCount,
		"slot", grant.Slot,
		"slots", fmt.Sprint(grant.Slots),
		"merged_into_existing_stack", grant.Merged,
		"created_new_slot", grant.Created,
		"stack_limit", grant.Identity.StackLimit,
		"stack_pvf_path", grant.Identity.PVFPath,
		"stack_item_kind", grant.Identity.ItemKind,
		"stack_stackable_type", grant.Identity.StackableType,
		"container_list", grant.Container.ListType,
		"container_slot_start", grant.Container.SlotStart,
		"container_slot_end", grant.Container.SlotEnd,
		"placement_switch", grant.PlacementMode,
		"placement_switch_env", inventory.UpgradeDestroyBonusPlacementEnv,
		"placement_switch_default", inventory.UpgradeDestroyBonusPlacementMerge,
		"placement_switch_legacy_row_literal", inventory.UpgradeDestroyBonusPlacementLegacyRow,
		"placement_switch_rollback_literal", inventory.UpgradeDestroyBonusPlacementFirstEmptySlot,
	}
}
