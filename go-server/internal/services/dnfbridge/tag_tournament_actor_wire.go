package dnfbridge

import (
	"encoding/binary"
	"fmt"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// currentTagTournamentActorWire对应固定57d8fbfc客户端24F9150(full=1)。
// 1389外层负责主对象键、队员序号与姓名；这里没有第二个姓名或普通mode1外壳。
// base3A4/3A6/3A8的普通基类默认值由22BE850读取静态559371F/20/21，均为0；
// 新建资料的来源绑定见freshCurrentTagTournamentActor；此编码器本身不提供
// 默认角色或恢复战斗状态；实际入场必须完成逐角色与全部地图预检。
type currentTagTournamentActorWire struct {
	level, job, grow             byte
	base3A4, base3A6             byte
	base3A8                      uint32
	privateCharacter             bool
	stats                        []byte
	extraSlots                   byte
	extraSlotFlag                bool
	appearance                   []repo.CharacterRosterEquipSummary
	cloneTitle                   uint32
	base234, base238             byte
	ignored2034060, reserved     byte
	equipment                    []currentMode1EquipmentObjectRow
	equipmentFinalState          uint32
	skillObjectKey               uint16
	skillTree                    byte
	skills                       []currentTagTournamentSkillWire
	auxiliaryHandles             []uint32
	shieldDeck                   [5]uint32
	creatureItem, creatureHandle uint32
}

type currentTagTournamentSkillWire struct {
	slot  byte
	id    uint16
	level byte
}

// 仅编码已明确提供的原生字段，不读数据库、不构造缺失状态、不发送报文。
func buildCurrentTagTournamentActorWire(a currentTagTournamentActorWire) ([]byte, error) {
	if a.level == 0 || a.job > 15 || a.grow > 127 || len(a.stats) != currentMode1StatBlobWireSize || binary.LittleEndian.Uint32(a.stats[:4]) == 0 || a.skillTree > 1 {
		return nil, fmt.Errorf("帝国竞技场双角色基础资料、属性或技能页无效")
	}
	if len(a.appearance) > 33 || len(a.equipment) > 33 || len(a.skills) > 255 || len(a.auxiliaryHandles) > 255 || a.creatureItem == 0 && a.creatureHandle != 0 {
		return nil, fmt.Errorf("帝国竞技场双角色集合越界或宠物资料不完整")
	}
	seenAppearance := map[int64]bool{}
	for _, row := range a.appearance {
		if row.Slot < 0 || row.Slot > 32 || seenAppearance[row.Slot] || row.ItemIDOrIcon < 0 || row.ItemIDOrIcon > int64(^uint32(0)) || row.PackedFlags < 0 || row.PackedFlags > 255 || row.OptionalIDOrExpire < 0 || row.OptionalIDOrExpire > int64(^uint32(0)) || row.AuxValue < 0 || row.AuxValue > int64(^uint32(0)) || row.AuxFlag < 0 || row.AuxFlag > 255 {
			return nil, fmt.Errorf("帝国竞技场双角色外观资料越界或槽位重复")
		}
		seenAppearance[row.Slot] = true
	}
	seenEquipment := map[byte]bool{}
	for _, row := range a.equipment {
		slot, ok := currentMode1EquipmentActorSlot(row)
		if !ok || seenEquipment[slot] {
			return nil, fmt.Errorf("帝国竞技场装备创建槽位无效或重复")
		}
		seenEquipment[slot] = true
	}
	seenSkills := map[uint16]bool{}
	seenSlots := map[byte]bool{}
	for _, row := range a.skills {
		if row.id >= 512 || row.level == 0 || row.slot != 255 && row.slot >= 204 || seenSkills[row.id] || row.slot != 255 && seenSlots[row.slot] {
			return nil, fmt.Errorf("帝国竞技场技能等级、编号或槽位无效")
		}
		seenSkills[row.id] = true
		seenSlots[row.slot] = true
	}
	var w packetWriter
	w.writeByte(a.level)
	w.writeByte(a.job)
	w.writeByte(a.grow)
	w.writeByte(a.base3A4)
	w.writeByte(a.base3A6)
	w.writeUint32(a.base3A8)
	w.writeByte(boolByte(a.privateCharacter))
	w.writeUint32(uint32(len(a.stats)))
	w.writeBytes(a.stats)
	w.writeByte(a.extraSlots)
	w.writeByte(boolByte(a.extraSlotFlag))
	// 22BDE30 -> 251E2F0(type45)要求每行u32长度块，即使长度为0也不能省略。
	writeCurrentSceneObjectEquipSummary(&w, a.appearance)
	w.writeUint32(a.cloneTitle)
	w.writeByte(a.base234)
	w.writeByte(a.base238)
	w.writeByte(a.ignored2034060)
	w.writeByte(a.reserved)
	w.writeByte(byte(len(a.equipment)))
	for _, row := range a.equipment {
		writeCurrentMode1EquipmentCreateRow(&w, row)
	}
	w.writeUint32(a.equipmentFinalState)
	w.writeUint16(a.skillObjectKey)
	w.writeByte(byte(len(a.skills)))
	w.writeByte(a.skillTree)
	for _, row := range a.skills {
		w.writeByte(row.slot)
		w.writeUint16(row.id)
		w.writeByte(row.level)
	}
	// 22BE530读取的是对象句柄列表；写宠物itemID会被当作数量，导致后续越界。
	w.writeUint32(uint32(len(a.auxiliaryHandles)))
	for _, handle := range a.auxiliaryHandles {
		w.writeUint32(handle)
	}
	// 24F9424的五项交给守护者盾栏，与580相同，不是缔造者能量。
	for _, state := range a.shieldDeck {
		w.writeUint32(state)
	}
	// 22C32B0最后读取固定raw8，并在客户端对象槽30应用物品/句柄。
	w.writeUint32(a.creatureItem)
	w.writeUint32(a.creatureHandle)
	return w.bytes(), nil
}
