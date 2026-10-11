package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentSceneOp9ActorDisplayKind byte   = 0
	currentSceneOp9ActorRemoveKind  byte   = 3
	currentSceneOp9StableSceneValue uint16 = 9999
)

func buildCurrentSceneOp9NoopBody() []byte {
	var writer packetWriter
	writer.writeUint16(0)
	writer.writeUint16(currentSceneOp9StableSceneValue)
	return writer.bytes()
}

func buildCurrentSceneOp9ActorRemovalBody(sceneObjectKey uint16) []byte {
	return buildCurrentSceneOp9ActorRemovalBodyInContext(
		sceneObjectKey,
		currentSceneObjectContext,
	)
}

func buildCurrentSceneOp9ActorRemovalBodyInContext(
	sceneObjectKey uint16,
	ownerChannel byte,
) []byte {
	var writer packetWriter
	writer.writeUint16(1)
	writer.writeUint16(currentSceneOp9StableSceneValue)
	writer.writeUint16(sceneObjectKey)
	writer.writeByte(0)
	writer.writeByte(ownerChannel)
	writer.writeByte(0)
	writer.writeByte(currentSceneOp9ActorRemoveKind)
	return writer.bytes()
}

func (s *Service) sendCurrentSceneOp9PreviewActorRemovalOnce(session *gameSession, source string) error {
	if session == nil || !session.selectPreviewObjectStateSent || session.selectPreviewActorRemoved {
		return nil
	}
	objectKey := currentSceneActorObjectKey(session.selectedCharacterID)
	if objectKey == 0 {
		return nil
	}
	body := buildCurrentSceneOp9ActorRemovalBodyInContext(
		objectKey,
		session.selectPreviewActorOwnerChannel,
	)
	s.logGameEvent(session, "game-upper-current-scene-op9-preview-actor-remove-send",
		"source", source,
		"char_id", session.selectedCharacterID,
		"object_key", objectKey,
		"owner_channel", session.selectPreviewActorOwnerChannel,
		"msg_id", uint16(dnfenum.CmdPacketRecoverStamina),
		"classification", 0,
		"record_kind", currentSceneOp9ActorRemoveKind,
		"body_len", len(body),
		"body_source", "current_exe_sub_1D64CA0_kind3_clear_slots_and_remove_target",
		"reason", "evict_transition_preview_before_final_actor_mode0_mode1_bind")
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketRecoverStamina), body, 0); err != nil {
		return err
	}
	session.selectPreviewActorRemoved = true
	session.currentSceneObjectListSent = false
	return nil
}

func buildCurrentSceneOp9ActorDisplayBody(sceneObjectKey uint16, character dnfrepo.CharacterRecord, hasCharacter bool, fallbackName string) []byte {
	return buildCurrentSceneOp9ActorDisplayBodyInContext(sceneObjectKey, character, hasCharacter, fallbackName, currentSceneObjectContext)
}

func buildCurrentSceneOp9ActorDisplayBodyInContext(sceneObjectKey uint16, character dnfrepo.CharacterRecord, hasCharacter bool, fallbackName string, ownerChannel byte) []byte {
	return buildCurrentSceneOp9ActorPartyDisplayBodyInContext(
		sceneObjectKey,
		character,
		hasCharacter,
		fallbackName,
		ownerChannel,
		ownerChannel,
		nil,
		0,
	)
}

type currentSceneOp9PartyMemberProjection struct {
	State alignedcmd.PartyMemberState
	Name  string
	Job   byte
	Level byte
	Grow  byte
}

// buildCurrentSceneOp9ActorPartyDisplayBodyInContext 构造 class0/op9 kind0 的
// "场景 actor + 队伍槽位表"记录。尾部 4 字节里的第二个字节（partyLeaderSlot）是
// **当前 EXE 用来在队伍框上画队长标记的槽位**（IDA：`sub_1D64CA0` 把它存进该 actor
// 的队伍记录并交给 `sub_26A0300`）。它必须等于**权威队长所在的行**，不能写成
// "本条记录描述的 actor 自己的槽位"——那会让被邀请方自己的记录说"队长在第 2 行"，
// 客户端于是把队长标记画在被邀请人那一行（并且被邀请方因此自认队长、能点"委任队长"）。
func buildCurrentSceneOp9ActorPartyDisplayBodyInContext(
	sceneObjectKey uint16,
	character dnfrepo.CharacterRecord,
	hasCharacter bool,
	fallbackName string,
	ownerChannel byte,
	localChannel byte,
	members []currentSceneOp9PartyMemberProjection,
	partyLeaderSlot byte,
) []byte {
	return buildCurrentSceneOp9ActorPartyDisplayBodyInContextWithSelectedChannel(
		sceneObjectKey,
		character,
		hasCharacter,
		fallbackName,
		ownerChannel,
		int(localChannel),
		members,
		partyLeaderSlot,
	)
}

// buildCurrentSceneOp9ActorPartyDisplayBodyInContextWithSelectedChannel 与
// buildCurrentSceneOp9ActorPartyDisplayBodyInContext 逐字节同形，唯一的差别是
// **身份块判据用未截断的 int 频道号**。
//
// 为什么需要它：读端（`sub_1D64CA0`/`sub_269EE50`）拿记录里的 owner 字节与客户端
// **自己的场景频道**比较，不一致就按"跨频道成员"读内联身份块。客户端场景频道可以是
// 508 这种 >255 的值（本地记忆/目录选择决定，服务端改不动），此时任何 u8 记录字节都
// 不可能等于它 ⇒ 这些行必须一律带身份块。用 `byte(508)` = 252 去比会让服务端在这个
// 判据上蒙对/蒙错，蒙错就是"客户端按跨频道读身份块、nameLength 越界"的闪退
// （2026-09-24 双开进图 50% 闪退的同一机理）。
//
// ≤255 时会话调用方传入的值与 byte 版完全相同 ⇒ 默认档逐字节不变。
func buildCurrentSceneOp9ActorPartyDisplayBodyInContextWithSelectedChannel(
	sceneObjectKey uint16,
	character dnfrepo.CharacterRecord,
	hasCharacter bool,
	fallbackName string,
	ownerChannel byte,
	localChannel int,
	members []currentSceneOp9PartyMemberProjection,
	partyLeaderSlot byte,
) []byte {
	return buildCurrentSceneOp9ActorPartyDisplayBodyForProfile(sceneObjectKey, character, hasCharacter, fallbackName, ownerChannel, localChannel, members, partyLeaderSlot, false)
}

func (s *Service) buildCurrentSceneOp9ActorDisplayBodyInContext(sceneObjectKey uint16, character dnfrepo.CharacterRecord, hasCharacter bool, fallbackName string, ownerChannel byte) []byte {
	return buildCurrentSceneOp9ActorPartyDisplayBodyForProfile(sceneObjectKey, character, hasCharacter, fallbackName, ownerChannel, int(ownerChannel), nil, 0, s.options.currentClientVersion == "official-24.9")
}

// Official 24.9 NOTI9 (2016350) reads one additional actor byte at
// 2016913 before the party count at 2016995. Its cross-context member
// identity also reads five bytes, two DSTRs and a suffix byte (2016BC1..C85).
// Earlier clients retain their original wire layout.
func buildCurrentSceneOp9ActorPartyDisplayBodyForProfile(
	sceneObjectKey uint16, character dnfrepo.CharacterRecord, hasCharacter bool,
	fallbackName string, ownerChannel byte, localChannel int,
	members []currentSceneOp9PartyMemberProjection, partyLeaderSlot byte,
	official249 bool,
) []byte {
	if sceneObjectKey == 0 {
		sceneObjectKey = currentSceneBootstrapObjectKey
	}
	name := fallbackName
	if name == "" && hasCharacter {
		name = character.Name
	}
	level := byte(1)
	job := byte(0)
	grow := byte(0)
	if hasCharacter {
		if character.Level > 0 && character.Level < 256 {
			level = byte(character.Level)
		}
		job = byte(numericCharacterStat(character.Job))
		grow = characterGrowTypeByte(character)
	}

	var writer packetWriter
	writer.writeUint16(1)
	writer.writeUint16(currentSceneOp9StableSceneValue)

	writer.writeUint16(sceneObjectKey)
	writer.writeByte(0)
	writer.writeByte(ownerChannel)
	writer.writeByte(0)
	writer.writeByte(currentSceneOp9ActorDisplayKind)

	writer.writeByte(currentSceneObjectRoute)
	// sub_1D64CA0 uses this nested byte as the inline-identity discriminator,
	// not as the actor-owner key. Zero is required for the following DSTR name
	// branch; the real actor owner is already carried by the outer record.
	writer.writeByte(currentSceneObjectContext)
	writer.writeRawDstr(rosterNameBytes(name))
	writer.writeByte(job)
	writer.writeByte(level)
	writer.writeUint32(0)
	writer.writeByte(grow)
	writer.writeUint16(0)
	writer.writeByte(0)
	writer.writeByte(0)
	writer.writeUint16(0)
	if official249 {
		writer.writeByte(0)
	} // native actor metadata +0x55, before member count

	members = currentSceneOp9PartyMembers(members)
	writer.writeByte(byte(len(members)))
	for slot, member := range members {
		writer.writeByte(byte(slot))
		writer.writeUint16(member.State.UserID)
		// 判据是"记录 owner 字节 vs 客户端当前频道"（int 比较；会话频道 > 255 时任何
		// u8 都不可能相等 ⇒ 一律写身份块）。见函数头注释。
		if int(ownerChannel) != localChannel {
			writer.writeByte(member.Job)
			writer.writeByte(member.Level)
			writer.writeByte(member.Grow)
			writer.writeByte(0)
			if official249 {
				writer.writeByte(0)
			}
			writer.writeRawDstr(rosterNameBytes(member.Name))
			if official249 {
				writer.writeRawDstr(nil) // optional secondary identity string
				writer.writeByte(0)      // optional secondary identity flag
			}
		}
		writer.writeByte(0)
		writer.writeByte(currentSceneOp9PartyMemberState(member.State.UserState))
		writer.writeByte(0)
	}
	writer.writeByte(0)
	writer.writeByte(partyLeaderSlot)
	writer.writeByte(0)
	writer.writeByte(0)
	return writer.bytes()
}

// currentSceneOp9PartyMemberSlot 解析某个成员在队伍槽位表里的行号（0 基）。
func currentSceneOp9PartyMemberSlot(members []currentSceneOp9PartyMemberProjection, memberID uint16) (byte, bool) {
	if memberID == 0 {
		return 0, false
	}
	for slot, member := range members {
		if member.State.UserID == memberID {
			return byte(slot), true
		}
	}
	return 0, false
}

// currentSceneOp9PartyLeaderSlot 给出队长所在的行号。权威名单永远把队长排在 0 号位
// （`runtimePartyMembers` 的既有不变量），所以找不到队长时回落到 0：它一定是真实占用的行。
func currentSceneOp9PartyLeaderSlot(members []currentSceneOp9PartyMemberProjection, leaderID uint16) byte {
	if slot, ok := currentSceneOp9PartyMemberSlot(members, leaderID); ok {
		return slot
	}
	return 0
}

func currentSceneOp9PartyMembers(members []currentSceneOp9PartyMemberProjection) []currentSceneOp9PartyMemberProjection {
	const maxCurrentScenePartySlots = 8
	capacity := len(members)
	if capacity > maxCurrentScenePartySlots {
		capacity = maxCurrentScenePartySlots
	}
	filtered := make([]currentSceneOp9PartyMemberProjection, 0, capacity)
	for _, member := range members {
		if member.State.UserID == 0 {
			continue
		}
		duplicate := false
		for _, existing := range filtered {
			if existing.State.UserID == member.State.UserID {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		filtered = append(filtered, member)
		if len(filtered) == maxCurrentScenePartySlots {
			break
		}
	}
	return filtered
}

func currentSceneOp9PartyMemberState(value byte) byte {
	if value == 0 {
		return 1
	}
	return value
}
