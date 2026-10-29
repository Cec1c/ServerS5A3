package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
)

// 现用EXE 6192dafe的class0/op14由0x2025a90处理：读取完整0x77行，
// list3经0x2025c8c的actor vtable+0xb54取得现有穿戴物品，再调用+0x15c更新。
// 附魔ACK 0x1fbc697使用同一getter，故必须先原位更新，再让弹窗持有该对象。
// 此处不重建list3容器，不发送被当前接收位屏蔽的op342，也不改变装备移动路径。
func (s *Service) sendWornEquipmentMutationSlots(session *gameSession, operation string, slots []int16) error {
	return s.sendWornEquipmentMutationSlotsWithPolicy(session, operation, slots, false)
}

// Repair can only update an existing item. An absent or unprojectable worn row
// must never be encoded as the empty-slot sentinel after a successful repair.
func (s *Service) sendRepairedWornEquipmentSlots(session *gameSession, operation string, slots []int16) error {
	return s.sendWornEquipmentMutationSlotsWithPolicy(session, operation, slots, true)
}

func (s *Service) sendWornEquipmentMutationSlotsWithPolicy(session *gameSession, operation string, slots []int16, requireOccupied bool) error {
	if len(slots) == 0 {
		return nil
	}
	if session == nil || session.selectedCharacterID == 0 {
		return fmt.Errorf("穿戴属性刷新缺少已选角色")
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Equipment == nil {
		return fmt.Errorf("穿戴属性刷新缺少装备仓储")
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	record, found, err := repos.Equipment.Load(ctx, characterID)
	if err != nil || !found || record.CharacterID != characterID {
		return fmt.Errorf("穿戴属性刷新读取角色%s失败：存在=%t，错误=%v", characterID, found, err)
	}
	bySlot := make(map[int16]currentItemListEntry)
	for _, equipped := range record.Entries {
		entry, valid := currentItemListEntryFromEquipment(equipped)
		if !valid {
			continue
		}
		slot := int16(binary.LittleEndian.Uint16(entry.data[:2]))
		if _, duplicate := bySlot[slot]; duplicate {
			return fmt.Errorf("穿戴属性刷新发现重复槽%d", slot)
		}
		bySlot[slot] = entry
	}
	entries := make([]currentItemListEntry, 0, len(slots))
	seen := make(map[int16]bool)
	for _, slot := range slots {
		if slot < 12 || slot > 25 {
			return fmt.Errorf("穿戴属性刷新不支持槽%d", slot)
		}
		if seen[slot] {
			continue
		}
		seen[slot] = true
		entry, occupied := bySlot[slot]
		if !occupied {
			if requireOccupied {
				return fmt.Errorf("repaired worn slot %d has no projectable committed row", slot)
			}
			// 精炼失败碎装：仅清除本次目标，现用op14原生清除actor该槽。
			entry.patchCore(slot, math.MaxUint32, 0)
		}
		entries = append(entries, entry)
	}
	if _, err := s.applyCurrentPVFUsePeriodsToEntriesWithLoadedCatalog(ctx, entries); err != nil {
		return fmt.Errorf("穿戴属性刷新投影期限失败：%w", err)
	}
	body := buildCurrentItemUpdateBody(3, entries)
	s.logPacketEvent("穿戴属性单件原位刷新", "conn_id", session.connID,
		"operation", operation, "char_id", session.selectedCharacterID,
		"msg_id", 14, "list_type", 3, "slots", slots, "entry_count", len(entries),
		"sequence", "事务提交后、结果弹窗前更新既有穿戴对象")
	return s.sendGameUpperRawClass(session, 14, body, 0)
}
