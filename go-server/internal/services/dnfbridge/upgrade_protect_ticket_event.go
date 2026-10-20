package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/inventory"
)

// game-upgrade-protect-ticket-consumed / -absent 是 op50「失败即摧毁」档的保护券
// 判定**唯一**的两个事件名。
//
// 现场缺陷（运维原话 2026-09-29）：「强化和增幅时会显示持有保护券类道具但是不实际
// 保护装备不被破坏」。改动前这一档根本没有保护券判定（证据见
// inventory/upgrade_protect_ticket.go 文件头），所以复测时一行就能判读
// "这次到底是消耗了券、还是背包里本来就没有券"：
//
//	EVENT kind=game-upgrade-protect-ticket-consumed … mode=reinforce target_slot=12
//	  old_level=13 new_level=0 result_code=2 ticket_item_id=10303749 ticket_slot=121
//	  ticket_left=998 ticket_pvf_path=stackable/10303001/10303749.stk retain_level=0
//	  scanned_slots=41 switch=consume_first_match
//	  switch_env=DNFBRIDGE_UPGRADE_PROTECT_TICKET switch_default=consume_first_match
//	  switch_rollback_literal=legacy
//
//	EVENT kind=game-upgrade-protect-ticket-absent … mode=amplify target_slot=12
//	  old_level=10 new_level=0 result_code=3 scanned_slots=41 resolver_error=""
//	  switch=consume_first_match …
//
// 为什么事件在 bridge 而不是领域层：internal/modules/dnf/inventory 没有 logger，
// 它会话口径也不该知道日志格式。领域层只把事实（alignedcmd.UpgradeProtectTicket）
// 交出来，格式与事件名由这里决定。与 upgrade_destroy_bonus_event.go 同惯例。
const (
	upgradeProtectTicketConsumedEventName = "game-upgrade-protect-ticket-consumed"
	upgradeProtectTicketAbsentEventName   = "game-upgrade-protect-ticket-absent"
)

// upgradeProtectTicketEventName 按"这次有没有消耗掉一张券"选事件名。
func upgradeProtectTicketEventName(fact *alignedcmd.UpgradeProtectTicket) string {
	if fact != nil && fact.Consumed {
		return upgradeProtectTicketConsumedEventName
	}
	return upgradeProtectTicketAbsentEventName
}

// upgradeProtectTicketEventFields 组装该事件的业务字段（会话字段由调用方前置）。
// fact == nil（没走到摧毁档）时返回 nil，调用方因此一条都不打。
func upgradeProtectTicketEventFields(fact *alignedcmd.UpgradeProtectTicket) []any {
	if fact == nil {
		return nil
	}
	return []any{
		"mode", fact.Mode,
		"consumed", fact.Consumed,
		"ticket_slot", fact.Slot,
		"ticket_item_id", fact.ItemID,
		"ticket_left", fact.Remaining,
		"ticket_pvf_path", fact.PVFPath,
		"retain_level", fact.RetainLevel,
		"old_level", fact.OldLevel,
		"new_level", fact.NewLevel,
		"scanned_slots", fact.Candidates,
		"resolver_error", fact.ResolverError,
		"switch", fact.Switch,
		"switch_env", inventory.UpgradeProtectTicketEnv,
		"switch_default", inventory.UpgradeProtectTicketConsumeFirstMatch,
		"switch_rollback_literal", inventory.UpgradeProtectTicketLegacy,
	}
}
