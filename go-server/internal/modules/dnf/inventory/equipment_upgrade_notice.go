package inventory

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 普通强化、增幅及对应券共用86的成功新等级/失败原等级门槛。
func equipmentUpgradeAnnouncement(cmd Command, mode string, itemID int32, oldLevel, newLevel byte, succeeded bool, stack dnfrepo.ItemStack) *alignedcmd.EquipmentUpgradeNotice {
	level := newLevel
	if !succeeded {
		level = oldLevel
	}
	if cmd.UpgradeNoticeLevel < 0 || int(level) < cmd.UpgradeNoticeLevel {
		return nil
	}
	body, err := buildEquipmentUpgradeAnnouncementBody(1, succeeded, cmd.SelectedCharacterID, itemID, level, 0, stack)
	if err != nil {
		return nil
	}
	return &alignedcmd.EquipmentUpgradeNotice{Kind: mode, ItemID: int64(itemID), Level: level, Succeeded: succeeded, Body: body}
}

// 86的1/14分别对应强化和锻造。现用客户端2027cc0也消费相同字段，
// 并在固定字段之后经1F0EEC0读取动态属性，不能只发旧的9字节前缀。
func buildEquipmentUpgradeAnnouncementBody(kind byte, success bool, userID uint16, itemID int32, level, forgeLevel byte, stack dnfrepo.ItemStack) ([]byte, error) {
	if (kind != 1 && kind != 14) || itemID <= 0 || stack.ItemID != int64(itemID) || stack.Count <= 0 {
		return nil, fmt.Errorf("装备播报快照与目标不符")
	}
	if len(stack.RawEntry) != 0 && len(stack.RawEntry) != currentItemListEntrySize {
		return nil, fmt.Errorf("装备播报物品行长度无效")
	}
	raw := currentRawEntryForStack(0, stack)
	count := int(raw[randomOptionCountOffset])
	if count > randomOptionMaximumCount {
		return nil, fmt.Errorf("装备播报动态属性数量无效")
	}
	var w packetWriter
	w.writeByte(kind)
	if success {
		w.writeByte(1)
	} else {
		w.writeByte(0)
	}
	w.writeUint16(userID)
	w.writeInt32(itemID)
	w.writeByte(level)
	if kind == 14 {
		w.writeByte(forgeLevel)
	}
	w.writeByte(byte(count))
	for i := 0; i < count; i++ {
		w.writeByte(raw[randomOptionTypeOffset+i])
		w.writeByte(raw[randomOptionValue1Offset+i])
		w.writeByte(raw[randomOptionValue2Offset+i])
	}
	if count > 0 {
		w.writeByte(raw[randomOptionStateOffset])
		w.writeByte(raw[randomOptionChangedIndexOffset])
		if raw[randomOptionChangedIndexOffset] != 0xff {
			w.writeBytes(raw[randomOptionCandidateOffset : randomOptionCandidateOffset+4])
		}
	}
	return w.bytes(), nil
}
