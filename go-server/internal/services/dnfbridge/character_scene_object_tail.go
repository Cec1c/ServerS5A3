package dnfbridge

import (
	"encoding/binary"
	"unicode/utf16"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func writeCurrentSceneObjectEntryTail(writer *packetWriter, character dnfrepo.CharacterRecord, hasCharacter bool) {
	writeCurrentSceneObjectEntryTailWithCreature(writer, character, hasCharacter, currentEquippedCreatureSnapshot{})
}

func writeCurrentSceneObjectEntryTailWithCreature(writer *packetWriter, character dnfrepo.CharacterRecord, hasCharacter bool, creature currentEquippedCreatureSnapshot) {
	writeCurrentSceneObjectEntryTailWithCreatureAndAdventureName(writer, character, hasCharacter, creature, "")
}

func writeCurrentSceneObjectEntryTailWithCreatureAndAdventureName(writer *packetWriter, character dnfrepo.CharacterRecord, hasCharacter bool, creature currentEquippedCreatureSnapshot, adventureName string, guild ...currentSceneGuildProjection) {
	var organization currentSceneGuildProjection
	if len(guild) != 0 {
		organization = guild[0]
	}
	job := byte(0)
	grow := byte(0)
	level := byte(0)
	if hasCharacter {
		job = byte(numericCharacterStat(character.Job))
		grow = characterGrowTypeByte(character)
		level = rosterLevel(character)
	}
	writer.writeByte(job)
	// 持久化的 grow_type 是打包值：低四位=转职分支（PVF `[growtype N]` 的 N-1），
	// 高四位=觉醒次数（1=一觉 / 2=二觉）。当前客户端在这个"名字后第二个 u8"
	// 上按同样两个分量读取（MCP sub_2009160 拆成 low4=grow、high3=subgrow），
	// 所以这里必须把高四位一并带上；原先写 packCurrentSceneObjectGrow(grow, 0)
	// 等于把觉醒次数丢掉，actor 会被重建成"未觉醒"。
	//
	// 实证（香港 2026-09-25，packet_log，账号 dnf:10001 / 角色 3 test2 job=14
	// grow_type=0x21，conn game-000017 03:52 选角后）：
	//   - 选角列表（同一账号 bootstrap roster，msg_id=2）该行是 `0e 21 5a`
	//     （job=14 grow=0x21 level=90）⇒ 客户端按 PVF 显示二觉名"炽天使"；
	//   - 选角后 op2 mode0 actor entry（msg_id=2 body_len=468，object_tail_start=87）
	//     同一行是 `0e 01 5a`（grow 只剩低四位）⇒ 进场后客户端按未觉醒的
	//     一转分支名"圣骑士"显示，两条链路对同一个权威字段给出两个不同的职业名。
	// 真档 PVF character/Priest/ATPriest.chr（job 14）：
	//   `[growtype name]` = 圣职者 / 圣骑士 / 异端审判者 / 巫女 / 诱魔者；
	//   `[growtype 2]`（=分支 1=圣骑士）的 `[awakening name]` = 福音传道者 / 炽天使。
	// 因此 grow_type=0x01→圣骑士、0x11→福音传道者、0x21→炽天使。
	writer.writeByte(packCurrentSceneObjectGrow(grow&0x0f, (grow>>4)&0x07))
	// Current EXE proof: sub_2003810 stores this byte at template+20. sub_24DC280
	// copies it to actor ability context 0, and the recompute chain copies contexts
	// 0 -> 1 -> 2 -> 3. The level getter sub_2416DB0 reads context 3 offset 0.
	writer.writeByte(level)
	// 下面严格对应 sub_2009160 在 selector 之后的所有 upper_pkt_read_*。
	// 旧 DOVE entry 在这里带版本相关资源号；当前 NoPack 先用安全空状态创建对象，再让后续 359/124 刷新。
	writer.writeByte(0) // sub_20038C0
	writer.writeByte(0) // sub_2003970
	writer.writeByte(0) // v112[1]
	writeCurrentSceneObjectEquipSummary(writer, currentSceneObjectMode0EquipSummary(character, hasCharacter))
	writer.writeUint32(0) // v113: 对象资源/动作参数，随后传入对象虚表 +0x10。
	writer.writeByte(0)   // v114
	writer.writeByte(0)   // v115
	writer.writeByte(0)   // v104
	writer.writeByte(0)   // v97
	// Current EXE ReadAndApplyActorNameTagState (sub_2008D80) reads this exact
	// pair and resolves worn endpoint 30. Keep the existing empty sentinel when
	// no name tag is equipped; otherwise project the durable item and expiry.
	nameTagItemID, nameTagExpireTime := currentSceneObjectNameTagState(character, hasCharacter)
	writer.writeUint32(nameTagItemID)
	writer.writeUint32(nameTagExpireTime)
	writer.writeByte(0)                 // sub_2003A20
	writer.writeUint32(organization.id) // native actor guild identifier
	writer.writeByte(0)                 // v116
	// Current EXE sub_20028B0 reads the equipped creature template ID here.
	// A live trace proved that UINT32_MAX resolves to runtime type 8 and leaves
	// the empty green overhead marker.  The C# no-equipped-creature path writes
	// zero, so do not reuse the DOVE template sentinel for this field.
	creatureItemID := currentSceneNoCreatureTemplateID
	var creatureName []byte
	creatureAliveState := byte(0)
	if creature.valid() {
		creatureItemID = creature.itemID
		creatureName = rosterNameBytes(creature.name)
		creatureAliveState = creature.aliveState
	}
	writer.writeUint32(creatureItemID)
	writer.writeRawDstr(creatureName)
	writer.writeByte(creatureAliveState) // sub_20028B0 equipped-creature alive state
	writer.writeByte(0)                  // sub_2003B80
	// Native 1C03C90 reads the organization level BEFORE v120; 1C03D40
	// starts directly with a GBK DSTR and then chaos point. An extra byte
	// before the DSTR makes its length exceed the native 64-byte buffer;
	// the reader consumes only the length, shifting disguise/EXP/visibility.
	organizationName := adventureName
	if organization.id != 0 {
		organizationName = organization.name
	}
	adventureNameBytes := rosterNameBytes(organizationName)
	organizationLevel := byte(0)
	if len(adventureNameBytes) != 0 {
		organizationLevel = 1
	}
	writer.writeByte(organizationLevel)   // sub_2003C90
	writer.writeUint32(organization.mark) // native guild mark
	writer.writeRawDstr(adventureNameBytes)
	writer.writeUint32(0) // sub_2003D40 chaos point
	// Latest v56 22BF580 reads DSTR, u32, then u16 (37E8350).
	// Keep the matched client's trailing u16 even when projecting a guild.
	// Omitting the u16 shifts 22C1650 onto profession/EXP and locks the menu
	// as disguised; test1 type=3/EXP=36 reproduced this in the live cache.
	writer.writeUint16(0)
	writer.writeUint32(currentSceneActorStateEventArg) // v121 -> selected actor event 17/6599
	writer.writeByte(0)                                // v122
	// 1C05B70 is the disguise pair, not profession. 1C03F20 reads the
	// following {u8 profession, u32 experience}, stores template+368/+36C,
	// and compares profession levels through 229E530.
	expertType := byte(0)
	if hasCharacter && character.Stats["expert_job_type"] >= 1 && character.Stats["expert_job_type"] <= 4 {
		expertType = byte(character.Stats["expert_job_type"])
	}
	writer.writeByte(0)                                         // sub_2005B70 disguise kind
	writer.writeByte(0)                                         // sub_2005B70 disguise flag
	writer.writeByte(expertType)                                // sub_2003F20 profession
	writer.writeUint32(statU32(character, "expert_job_exp", 0)) // sub_2003F20 experience
	writer.writeByte(0)                                         // v125
	writer.writeUint32(0)                                       // v126
	writer.writeUint16(0)                                       // v128
	writer.writeByte(0)                                         // sub_2004E00
	writer.writeByte(0)                                         // sub_2004E00
	writer.writeUint16(0)                                       // sub_2004E00
	writeCurrentHonorExpertState(writer, character)             // sub_2003150 HonorExpert {u32 level, u64 progress EXP}
	writer.writeByte(currentSceneNormalTownVisualState)         // sub_20042F0 normal-town actor visual state
	writer.writeUint32(0)                                       // sub_20042F0 后的对象 flag dword
	writer.writeByte(0)                                         // internal field +197
	writer.writeUint16(0)                                       // sub_2691CF0/sub_2691B70
	writer.writeByte(0)                                         // sub_34AEA80
	writer.writeUint16(0)                                       // sub_20029A0
	writer.writeByte(0)                                         // sub_20029A0
	writer.writeUint16(0)                                       // sub_2002A20
	writer.writeByte(0)                                         // sub_2004510
	writer.writeByte(0)                                         // sub_2690AF0
	writer.writeUint32(0)                                       // object internal +267
}

func currentSceneObjectNameTagState(character dnfrepo.CharacterRecord, hasCharacter bool) (uint32, uint32) {
	if !hasCharacter {
		return currentSceneNoAttachedUIResourceID, currentSceneNoAttachedUIResourceID
	}
	itemID := statU32(character, "name_tag_item_id", 0)
	if itemID == 0 {
		return currentSceneNoAttachedUIResourceID, currentSceneNoAttachedUIResourceID
	}
	return itemID, statU32(character, "name_tag_expire_time", 0)
}

func currentSceneObjectMode0EquipSummary(character dnfrepo.CharacterRecord, hasCharacter bool) []dnfrepo.CharacterRosterEquipSummary {
	return currentSceneObjectEquipSummary(character, hasCharacter)
}

func rewriteCurrentSceneObjectTailEquipSummary(tail []byte, rows []dnfrepo.CharacterRosterEquipSummary) ([]byte, bool) {
	const equipOffset = 6
	oldEnd, ok := currentSceneObjectEquipSummaryEnd(tail, equipOffset)
	if !ok {
		return nil, false
	}
	rows = cloneRosterEquipSummary(rows)
	sortRosterEquipSummary(rows)
	var writer packetWriter
	writeCurrentSceneObjectEquipSummary(&writer, rows)
	replacement := writer.bytes()
	out := make([]byte, 0, len(tail)-oldEnd+equipOffset+len(replacement))
	out = append(out, tail[:equipOffset]...)
	out = append(out, replacement...)
	out = append(out, tail[oldEnd:]...)
	return out, true
}

const currentSceneObjectEquipSummaryRowBytes = 19

func currentSceneObjectEquipSummaryEnd(tail []byte, offset int) (int, bool) {
	if offset < 0 || len(tail) <= offset {
		return 0, false
	}
	pos := offset + 1
	count := int(tail[offset])
	for idx := 0; idx < count; idx++ {
		if pos+5 > len(tail) {
			return 0, false
		}
		pos += 5
		if pos+4 > len(tail) {
			return 0, false
		}
		rawLen := int(binary.LittleEndian.Uint32(tail[pos : pos+4]))
		pos += 4
		if rawLen < 0 || pos+rawLen > len(tail) {
			return 0, false
		}
		pos += rawLen
		if pos+10 > len(tail) {
			return 0, false
		}
		pos += 10
	}
	return pos, true
}

func writeCurrentSceneObjectEquipSummary(writer *packetWriter, rows []dnfrepo.CharacterRosterEquipSummary) {
	count := len(rows)
	if count > 0xff {
		count = 0xff
	}
	writer.writeByte(byte(count))
	for idx := 0; idx < count; idx++ {
		row := rows[idx]
		writer.writeByte(rosterByteValue(row.Slot, 0))
		writer.writeUint32(rosterUint32Value(row.ItemIDOrIcon, 0))
		writer.writeUint32(0) // sub_225C9B0(type=44) -> sub_3457C50 u32 len + raw; legacy InvenItem raw is not this current block.
		writer.writeByte(rosterByteValue(row.PackedFlags, 0))
		writer.writeUint32(rosterUint32Value(row.OptionalIDOrExpire, 0))
		writer.writeUint32(rosterUint32Value(row.AuxValue, 0))
		writer.writeByte(rosterByteValue(row.AuxFlag, 0))
	}
}

func packCurrentSceneObjectGrow(grow byte, subGrow byte) byte {
	// MCP sub_2009160：name 后第二个 u8 直接拆成 low4=grow、high3=subgrow；
	// 这里不能复用 roster 的 job|grow 打包，否则会把 job 写进动作/状态链并跳到 0x1818181C。
	return (grow & 0x0f) | ((subGrow & 0x07) << 4)
}

func writeCurrentSceneObjectName(writer *packetWriter, value string) {
	// MCP sub_2009160 -> upper_pkt_read_wstr -> sub_3457C80：
	// 字符串实际是 u32 字节长度 + raw name，再由客户端转宽字；不能写固定 UTF-16，
	// 否则名字长度和后续 job/grow/resource selector 会整体错位，城镇对象会变成“没有名字”。
	writer.writeRawDstr(rosterNameBytes(value))
}

func writeFixedUTF16(writer *packetWriter, value string, units int) {
	encoded := utf16.Encode([]rune(value))
	for idx := 0; idx < units; idx++ {
		var unit uint16
		if idx < len(encoded) {
			unit = encoded[idx]
		}
		writer.writeUint16(unit)
	}
}

func writeNullTerminatedUTF16(writer *packetWriter, value string, maxUnits int) {
	encoded := utf16.Encode([]rune(value))
	if maxUnits > 0 && len(encoded) >= maxUnits {
		encoded = encoded[:maxUnits-1]
	}
	for _, unit := range encoded {
		writer.writeUint16(unit)
	}
	writer.writeUint16(0)
}
