package dnfbridge

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// sendSelectedPetMoveSlotRefreshes 是宠物/神器穿戴（op19 落点 list 3 槽 26..29）
// 回包后的 list 7（宠物容器）逐槽刷新。口径、厂商依据、现场证据与三个档位见
// equip_move_refresh.go 的 petMoveContainerRefreshEnv 注释。
//
// 与 sendSelectedIncrementalItemSlotRefreshes 的区别只有一个，但正是关键：那个函数
// 对 list 7 会**改期整容器**（current_item_list_send.go:222-230 的
// scheduleDeferredPetInventoryRefresh，1.5 s 后一包 list 7 全量），而搬运后的宠物容器
// 需要的只是"刚离开/刚回到的那一格"。厂商在同一处用的是逐槽 op14
// （PetInventoryMoveCoordinator.cs:76-77），这里照抄它的槽集合语义：
// 有货 ⇒ 该槽的权威行；空槽 ⇒ `slot + int32(-1)` 空行（ItemListUpdateBuilder.cs:81
// 的 WriteEmptyEntry，等同我方 current_item_list_send.go:211-213 的 patchCore 空行）。
func (s *Service) sendSelectedPetMoveSlotRefreshes(
	session *gameSession,
	operation string,
	refreshes []alignedcmd.ItemSlotRefresh,
) error {
	list7 := make([]int16, 0, len(refreshes))
	seen := make(map[int16]struct{}, len(refreshes))
	for _, refresh := range refreshes {
		if refresh.ListType != currentPetInventoryListType {
			// 只有宠物容器走本函数；list 3 的宠物槽由
			// sendSelectedEquippedCreatureItemRefresh（槽 26 的 op14 权威行）负责，
			// 见 equipped_creature_item_refresh.go 与 scene_creature_state.go。
			continue
		}
		if refresh.SlotIndex < 0 {
			return fmt.Errorf("pet move slot refresh has invalid slot %d", refresh.SlotIndex)
		}
		if _, duplicate := seen[refresh.SlotIndex]; duplicate {
			continue
		}
		seen[refresh.SlotIndex] = struct{}{}
		list7 = append(list7, refresh.SlotIndex)
	}
	if len(list7) == 0 {
		return nil
	}
	if session == nil || session.selectedCharacterID == 0 {
		return fmt.Errorf("pet move slot refresh requires an active character")
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Inventory == nil {
		return fmt.Errorf("pet move slot refresh requires inventory repository")
	}

	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	inventory, found, err := repositories.Inventory.Load(ctx, characterID)
	if err != nil {
		return fmt.Errorf("pet move slot refresh load character %s: %w", characterID, err)
	}
	if !found {
		return fmt.Errorf("pet move slot refresh inventory %s not found", characterID)
	}

	entries := make([]currentItemListEntry, 0, len(list7))
	for _, slot := range list7 {
		stack, occupied := inventory.Slots[fmt.Sprintf("%d:%d", currentPetInventoryListType, slot)]
		if occupied && stack.ItemID > 0 && stack.Count >= 0 {
			entries = append(entries, currentItemListEntryFromStack(currentPetInventoryListType, slot, stack))
			continue
		}
		var entry currentItemListEntry
		entry.patchCore(slot, math.MaxUint32, 0)
		entries = append(entries, entry)
	}
	body := buildCurrentItemUpdateBody(currentPetInventoryListType, entries)
	s.logPacketEvent("game-aligned-command-item-slot-refresh-send",
		"conn_id", session.connID,
		"operation", operation,
		"char_id", session.selectedCharacterID,
		"msg_id", uint16(dnfenum.CmdPacketWalkoutPartyMember),
		"classification", 0,
		"list_type", currentPetInventoryListType,
		"slots", list7,
		"entry_count", len(entries),
		"body_len", len(body),
		"sequence", "pet_equipment_move_then_repository_backed_list7_changed_rows_without_container_replay",
		"evidence", "vendor_petinventorymovecoordinator_sendpetitemrefresh_petcreaturerefreshslots_only")
	return s.sendGameUpperRawClass(
		session,
		uint16(dnfenum.CmdPacketWalkoutPartyMember),
		body,
		0,
	)
}
