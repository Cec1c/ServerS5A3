package dnfbridge

import (
	"context"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func (s *Service) sendCurrentSocketMutationRefresh(session *gameSession, result currentSocketMutationResult, source string) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	if result.TargetEquipped || result.Target.ListType == currentSocketListEquipment {
		if err := s.sendCurrentSocketEquipmentSlotUpdate(session, result.Target.Slot, source+"_target"); err != nil {
			return err
		}
	} else if result.Target.ListType != 0 || result.Target.Slot != 0 {
		if err := s.sendCurrentSocketInventorySlotUpdate(session, result.Target.ListType, result.Target.Slot, source+"_target"); err != nil {
			return err
		}
	}
	for _, changed := range result.Consumed {
		if err := s.sendCurrentSocketInventorySlotUpdate(session, changed.ListType, changed.Slot, source+"_consume"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) sendCurrentSocketInventorySlotUpdate(session *gameSession, listType byte, slot int16, source string) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Inventory == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	inventory, found, err := repositories.Inventory.Load(ctx, characterID)
	if err != nil {
		return err
	}
	var entry currentItemListEntry
	if found {
		if stack, ok := inventory.Slots[currentSocketInventoryKey(listType, slot)]; ok && stack.ItemID > 0 {
			entry = currentItemListEntryFromStack(listType, slot, stack)
		} else {
			// 客户端指名的这一格在权威状态里是空的 ⇒ 必须发**已验证的空行哨兵**
			// （itemID=0xFFFFFFFF）。旧写法 `patchCore(slot, 0, 0)` 写的是 itemID=0，
			// 而运行时 PVF 的 item 0 是 `stackable/gold.stk`（金币）：客户端会把它
			// 实例化到本地那一格上，玩家刚脱到该格的装备/武器行因此被整体覆盖
			// （+0x0B..0x0C 耐久字一起变 0），只有换场景/重登的全量重放才纠正得回来。
			// 现场归因：本函数是线上 105 行错误空行的**唯一**来源。详见
			// empty_slot_row_encoding.go。
			patchEmptySlotRow(&entry, slot)
		}
	} else {
		patchEmptySlotRow(&entry, slot)
	}
	body := buildCurrentItemUpdateBody(listType, []currentItemListEntry{entry})
	s.logGameEvent(session, "game-current-socket-inventory-slot-refresh",
		"source", source,
		"msg_id", uint16(dnfenum.CmdPacketWalkoutPartyMember),
		"list_type", listType,
		"slot", slot,
		"empty_row_item_id", emptySlotItemID(),
		"body_len", len(body))
	return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), body, 0)
}

func (s *Service) sendCurrentSocketEquipmentSlotUpdate(session *gameSession, slot int16, source string) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Equipment == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	equipment, found, err := repositories.Equipment.Load(ctx, characterID)
	if err != nil {
		return err
	}
	var entry currentItemListEntry
	// 空行编码与上面 list 0/1 那条**同一个**：非 0 槽的空行必须写哨兵 0xFFFFFFFF。
	//
	// 这里必须说清一条边界：**list 3 的空行形状在现场没有直接字节证据**
	// （`login_equipment_container.go:341-346` 因此选择"该槽为空时干脆不发"）。
	// 我们仍然把 itemID=0 换成哨兵，理由是：① 客户端的 0x77 行 reader 对
	// list 0/1/2/3/7/38 是同一套，而 item 0 = `stackable/gold.stk`（金币）这件事
	// 已被 `quick_slot_gold_row_test.go` 现场证过；② 哨兵已被逐字节验证为"空行"
	// （`inventory/body.go:186-193`），并且 dnfbridge 在 list 7
	// （`pet_move_slot_refresh.go:77`）上用的就是它。也就是说：我们是用
	// "已验证的空行"替换"已知会被画成金币的坏编码"，而不是发明一种新形状。
	// 本分支在本份线上日志里是 **0 行**（没被触发过）；一键回退
	// `DNFBRIDGE_EMPTY_SLOT_ROW_ENCODING=legacy` 把它逐字节还原成 itemID=0。
	patchEmptySlotRow(&entry, slot)
	if found {
		for _, equipped := range equipment.Entries {
			if equipped.SlotIndex == slot {
				if row, ok := currentItemListEntryFromEquipment(equipped); ok {
					entry = row
				}
				break
			}
		}
	}
	body := buildCurrentItemUpdateBody(currentSocketListEquipment, []currentItemListEntry{entry})
	s.logGameEvent(session, "game-current-socket-equipment-slot-refresh",
		"source", source,
		"msg_id", uint16(dnfenum.CmdPacketWalkoutPartyMember),
		"list_type", currentSocketListEquipment,
		"slot", slot,
		"body_len", len(body))
	return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), body, 0)
}
