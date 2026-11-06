package dnfbridge

import (
	"encoding/binary"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentSceneBootstrapObjectKey uint16 = 0x0191

const (
	currentSceneMenuAdventureNameOffset = 0x32
	currentSceneMenuAdventureNameEnd    = 0x43
)

const (
	// The current client resolves UINT32_MAX to runtime creature type 8.  A
	// live mode0 trace then shows the empty overhead marker.  Zero is the
	// C# no-equipped-creature value and must be kept for an absent pet.
	currentSceneNoCreatureTemplateID   uint32 = 0
	currentSceneNoAttachedUIResourceID uint32 = 0xffffffff
	// sub_2009160 passes this field to the selected actor's 17/6599 state
	// event.  It is an initialization marker, not a character statistic: the
	// same-version DOVE entry carries one and the current EXE always dispatches
	// the event for the local actor.  Leaving it at zero suppresses that state
	// initialization path.
	currentSceneActorStateEventArg uint32 = 1
	// Current EXE sub_20042F0 applies this normal-town visual-state mask to
	// the selected actor. The same-version DOVE scene entry carries 0x06;
	// zero is not the neutral state and leaves its visual refresh path unset.
	currentSceneNormalTownVisualState byte = 0x06
	// V195 mode0 finalizer 22C628B -> 2961D30 sets actor+0x3ac. The Empire
	// roster UI reads that identity at 129A44D; it is independent of the
	// advertised route in the mode0 prefix and remains the local roster 0.
	currentSceneActorLocalRosterServer byte = 0
)

func currentSceneActorObjectKey(characterID uint16) uint16 {
	if characterID != 0 {
		return characterID
	}
	return currentSceneBootstrapObjectKey
}

func buildCurrentSceneObjectListBody(sceneObjectKey uint16, character dnfrepo.CharacterRecord, hasCharacter bool, fallbackName string) []byte {
	return buildCurrentSceneObjectListBodyInContext(sceneObjectKey, character, hasCharacter, fallbackName, currentSceneObjectContext)
}

func buildCurrentSceneObjectListBodyInContext(sceneObjectKey uint16, character dnfrepo.CharacterRecord, hasCharacter bool, fallbackName string, ownerChannel byte) []byte {
	return buildCurrentSceneObjectListBodyWithCreatureInContext(sceneObjectKey, character, hasCharacter, fallbackName, currentEquippedCreatureSnapshot{}, ownerChannel)
}

func buildCurrentSceneObjectListBodyWithCreature(sceneObjectKey uint16, character dnfrepo.CharacterRecord, hasCharacter bool, fallbackName string, creature currentEquippedCreatureSnapshot) []byte {
	return buildCurrentSceneObjectListBodyWithCreatureInContext(sceneObjectKey, character, hasCharacter, fallbackName, creature, currentSceneObjectContext)
}

func buildCurrentSceneObjectListBodyWithCreatureInContext(sceneObjectKey uint16, character dnfrepo.CharacterRecord, hasCharacter bool, fallbackName string, creature currentEquippedCreatureSnapshot, ownerChannel byte) []byte {
	if sceneObjectKey == 0 {
		sceneObjectKey = currentSceneBootstrapObjectKey
	}
	if fallbackName == "" && hasCharacter {
		fallbackName = character.Name
	}

	var writer packetWriter
	writer.writeByte(0)
	writer.writeUint16(1)
	writer.writeByte(currentSceneObjectRoute)
	writer.writeByte(ownerChannel)
	writeCurrentSceneObjectEntryWithCreature(&writer, sceneObjectKey, character, hasCharacter, fallbackName, creature)
	return writer.bytes()
}

// buildCurrentSceneObjectListBodyWithCreatureAndAdventureNameInContext writes
// the account's durable adventure-group name through the current EXE's native
// organization-name display field. This display projection does not create a
// guild or alter social state. An optional authoritative guild projection
// replaces that display field for actual members.
func buildCurrentSceneObjectListBodyWithCreatureAndAdventureNameInContext(sceneObjectKey uint16, character dnfrepo.CharacterRecord, hasCharacter bool, fallbackName string, creature currentEquippedCreatureSnapshot, adventureName string, ownerChannel, serverRoute byte, guild ...currentSceneGuildProjection) []byte {
	return buildSceneObjectListBodyForProfile(sceneObjectKey, character, hasCharacter, fallbackName, creature, adventureName, ownerChannel, serverRoute, false, guild...)
}

func buildSceneObjectListBodyForProfile(sceneObjectKey uint16, character dnfrepo.CharacterRecord, hasCharacter bool, fallbackName string, creature currentEquippedCreatureSnapshot, adventureName string, ownerChannel, serverRoute byte, native90 bool, guild ...currentSceneGuildProjection) []byte {
	if sceneObjectKey == 0 {
		sceneObjectKey = currentSceneBootstrapObjectKey
	}
	name := fallbackName
	if name == "" && hasCharacter {
		name = character.Name
	}

	var writer packetWriter
	writer.writeByte(0)
	writer.writeUint16(1)
	// The paired client's local-actor branch requires the announced server as
	// well as the owner channel; a hardcoded server 0 selects a foreign actor.
	writer.writeByte(serverRoute)
	writer.writeByte(ownerChannel)
	if !native90 {
		writeCurrentSceneObjectEntryWithCreatureAndAdventureName(&writer, sceneObjectKey, character, hasCharacter, name, creature, adventureName, guild...)
	} else {
		if raw, ok := buildCurrentSceneObjectRawState(character, hasCharacter, name); ok {
			writer.writeBytes(raw)
		} else {
			writer.writeZero(0x47)
		}
		writer.writeUint16(sceneObjectKey)
		writeCurrentSceneObjectName(&writer, name)
		writeSceneObjectEntryTailForProfile(&writer, character, hasCharacter, creature, adventureName, true, guild...)
	}
	return writer.bytes()
}

func writeCurrentSceneObjectEntry(writer *packetWriter, sceneObjectKey uint16, character dnfrepo.CharacterRecord, hasCharacter bool, fallbackName string) {
	writeCurrentSceneObjectEntryWithCreature(writer, sceneObjectKey, character, hasCharacter, fallbackName, currentEquippedCreatureSnapshot{})
}

func writeCurrentSceneObjectEntryWithCreature(writer *packetWriter, sceneObjectKey uint16, character dnfrepo.CharacterRecord, hasCharacter bool, fallbackName string, creature currentEquippedCreatureSnapshot) {
	writeCurrentSceneObjectEntryWithCreatureAndAdventureName(writer, sceneObjectKey, character, hasCharacter, fallbackName, creature, "")
}

func writeCurrentSceneObjectEntryWithCreatureAndAdventureName(writer *packetWriter, sceneObjectKey uint16, character dnfrepo.CharacterRecord, hasCharacter bool, fallbackName string, creature currentEquippedCreatureSnapshot, adventureName string, guild ...currentSceneGuildProjection) {
	name := fallbackName
	if name == "" && hasCharacter {
		name = character.Name
	}

	if rawState, ok := buildCurrentSceneObjectRawState(character, hasCharacter, name); ok {
		// 现用EXE的22C5622把记录头+0x32的17字节名称写入人物资料+0x41C，
		// 人物菜单353FC86读取这一份；独立冒险团资料包不会补写此字段。
		// 沿用冒险团名的16字节编码限制，保留末尾NUL及+0x43的图标字段。
		if encoded, err := encodeRepresentAccountName(adventureName); err == nil {
			copy(rawState[currentSceneMenuAdventureNameOffset:currentSceneMenuAdventureNameEnd], encoded)
		}
		// MCP/live hook：sub_34DBD90 会校验这段对象状态的编码值；全 0 会跳到 0x18181818。
		writer.writeBytes(rawState)
	} else {
		writer.writeZero(0x47)
	}
	writer.writeUint16(sceneObjectKey)
	writeCurrentSceneObjectName(writer, name)
	writer.writeBytes(buildCurrentSceneObjectEntryTailForCurrentExeWithCreatureAndAdventureName(character, hasCharacter, creature, adventureName, guild...))
}

func buildCurrentSceneObjectRawState(character dnfrepo.CharacterRecord, hasCharacter bool, fallbackName string) ([]byte, bool) {
	// Current EXE sub_2009160 reads this as a fixed 0x47-byte actor-state
	// record before the separately typed key/name/job/grow fields.  It is not a
	// USERINFO subtype and must never be copied from a captured scene packet.
	//
	// The fields which have a durable DB/PVF source are emitted later in this
	// entry (name/job/grow/level/equipment) or in the authoritative mode1/3
	// refresh.  Only the offsets below are proven to be consumed by the current
	// EXE (see .ida-mcp/scan_raw47_20260718): offset 0x07 is the required
	// boolean read by sub_26908A0 marking the newly-created scene actor ready;
	// offset 0x0A is the actor level read by sub_2059450/sub_269E330; offset
	// 0x2C keeps the actor's own list/overhead entries visible in
	// sub_11B6FF0/sub_11B7C90; offset 0x43 is the overhead icon record id that
	// sub_1F60CE0 skips only when -1.
	// +0x32到+0x42另由入场入口填入当前账号的冒险团名；其余未知字段保留零，
	// 不使用抓包中的账号私有状态。
	_ = fallbackName
	const rawLen = 0x47
	const actorReadyOffset = 0x07
	const actorLevelOffset = 0x0A
	const actorSelfVisibleOffset = 0x2C
	const actorOverheadIconOffset = 0x43
	raw := make([]byte, rawLen)
	raw[actorReadyOffset] = 1
	if hasCharacter {
		raw[actorLevelOffset] = rosterLevel(character)
	}
	raw[actorSelfVisibleOffset] = 1
	binary.LittleEndian.PutUint32(raw[actorOverheadIconOffset:actorOverheadIconOffset+4], 0xFFFFFFFF)
	return raw, true
}

func buildCurrentSceneObjectEntryTail(character dnfrepo.CharacterRecord, hasCharacter bool) []byte {
	var writer packetWriter
	writeCurrentSceneObjectEntryTailWithCreature(&writer, character, hasCharacter, currentEquippedCreatureSnapshot{})
	return writer.bytes()
}

func buildCurrentSceneObjectEntryTailForCurrentExe(character dnfrepo.CharacterRecord, hasCharacter bool) []byte {
	return buildCurrentSceneObjectEntryTailForCurrentExeWithCreature(character, hasCharacter, currentEquippedCreatureSnapshot{})
}

func buildCurrentSceneObjectEntryTailForCurrentExeWithCreature(character dnfrepo.CharacterRecord, hasCharacter bool, creature currentEquippedCreatureSnapshot) []byte {
	return buildCurrentSceneObjectEntryTailForCurrentExeWithCreatureAndAdventureName(character, hasCharacter, creature, "")
}

func buildCurrentSceneObjectEntryTailForCurrentExeWithCreatureAndAdventureName(character dnfrepo.CharacterRecord, hasCharacter bool, creature currentEquippedCreatureSnapshot, adventureName string, guild ...currentSceneGuildProjection) []byte {
	var writer packetWriter
	writeCurrentSceneObjectEntryTailWithCreatureAndAdventureName(&writer, character, hasCharacter, creature, adventureName, guild...)
	tail := writer.bytes()
	if len(tail) < currentSceneObjectPostNameTailLength() {
		padded := make([]byte, currentSceneObjectPostNameTailLength())
		copy(padded, tail)
		return padded
	}
	return tail
}

func patchCurrentSceneObjectTailTransientItemRaw(tail []byte) bool {
	const equipOffset = 6
	equipEnd, ok := currentSceneObjectEquipSummaryEnd(tail, equipOffset)
	if !ok {
		return false
	}
	rawOffset := equipEnd + 8
	if rawOffset < 0 || rawOffset+8 > len(tail) {
		return false
	}
	for idx := 0; idx < 8; idx++ {
		tail[rawOffset+idx] = 0
	}
	return true
}
