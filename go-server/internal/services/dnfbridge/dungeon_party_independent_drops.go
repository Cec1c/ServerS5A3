package dnfbridge

import "encoding/binary"

type dungeonDropRecipient struct {
	CharacterID uint16
	ActorKey    uint16
	AccountID   string
}

// Snapshot recipients without entering another session's dungeon lock. Accounts
// are fixed by login before dungeon entry. Dead members remain participants.
func partyDungeonDropRecipients(runtime *runtimeDungeonState, characterID uint16, actorKey uint16) []dungeonDropRecipient {
	self := dungeonDropRecipient{CharacterID: characterID, ActorKey: actorKey, AccountID: currentDungeonDropAccountID(runtime)}
	recipients := []dungeonDropRecipient{self}
	if runtime == nil || runtime.partyInstance == nil || !abyssPartyDropOwnerModeFromEnv().perMember() {
		return recipients
	}
	instance := runtime.lockPartyDungeonInstance()
	defer unlockPartyDungeonInstance(instance)
	for _, id := range partyDungeonMemberCharacterIDsLocked(runtime, characterID) {
		if id == characterID {
			continue
		}
		member := instance.sessions[id]
		if member == nil {
			continue
		}
		recipients = append(recipients, dungeonDropRecipient{CharacterID: id, ActorKey: currentSceneActorObjectKey(id), AccountID: member.accountID})
	}
	return recipients
}

// Each call consumes a new stretch of the room stream and runs the real PVF
// formula, rather than copying the killer's selected items for other members.
func (s *Service) planPartyDungeonMonsterDrops(session *gameSession, runtime *runtimeDungeonState, monster runtimeDungeonMonster, actorKey uint16) ([]currentDungeonDeathDropWire, error) {
	recipients := partyDungeonDropRecipients(runtime, session.selectedCharacterID, actorKey)
	if len(recipients) == 1 {
		return s.planCurrentDungeonMonsterDrops(runtime, monster, actorKey)
	}
	monsterCatalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		return nil, err
	}
	dimension, err := currentDungeonDimensionDrops(monsterCatalog)
	if err != nil {
		return nil, err
	}
	dimension, err = currentRuntimeDimensionDrops(dimension, runtime)
	if err != nil {
		return nil, err
	}
	_, dimensionMatched := currentDungeonDimensionTier(dimension, runtime.Dungeon.ID, runtime.Character.Level)
	var all []currentDungeonDeathDropWire
	for _, recipient := range recipients {
		view := *runtime
		if dimensionMatched && recipient.CharacterID != session.selectedCharacterID {
			character, err := s.loadDimensionDropCharacter(recipient.CharacterID)
			if err != nil {
				s.logGameEvent(session, "game-dungeon-party-member-drop-deferred", "recipient_char_id", recipient.CharacterID, "error", err)
				continue
			}
			view.Character = character
		}

		view.AccountID = recipient.AccountID
		// An unavailable peer account must not inherit the killer's contract.
		view.Character.AccountID = recipient.AccountID
		wires, err := s.planCurrentDungeonMonsterDrops(&view, monster, recipient.ActorKey)
		if err != nil {
			s.logGameEvent(session, "game-dungeon-party-member-drop-deferred", "monster_object_key", monster.ObjectKey, "recipient_char_id", recipient.CharacterID, "error", err)
			continue
		}
		runtime.DropOwner = view.DropOwner
		runtime.NextObjectKey = view.NextObjectKey
		all = append(all, wires...)
		s.logGameEvent(session, "game-dungeon-party-member-drop-rolled", "monster_object_key", monster.ObjectKey, "recipient_char_id", recipient.CharacterID, "drop_count", len(wires), "source", "independent_pvf_roll_per_member")
	}
	return all, nil
}

// Preserve the proven op38 trailing fields, including an optional story room.
// The cached authoritative body can contain everyone's drops; each receiver
// only gets its own objects, also when a duplicate death report is replayed.
func partyDungeonDeathBodyForRecipient(runtime *runtimeDungeonState, body []byte, actorKey uint16) []byte {
	if runtime == nil || runtime.partyInstance == nil || !abyssPartyDropOwnerModeFromEnv().perMember() || len(body) < currentDungeonZeroDropDeathBodySize {
		return body
	}
	count := int(body[2])
	end := 3 + count*currentDungeonDeathDropWireSize
	if end+4 > len(body) {
		return body
	}
	out := append([]byte(nil), body[:3]...)
	out[2] = 0
	for index := 0; index < count; index++ {
		row := body[3+index*currentDungeonDeathDropWireSize : 3+(index+1)*currentDungeonDeathDropWireSize]
		if binary.LittleEndian.Uint16(row[len(row)-2:]) != actorKey {
			continue
		}
		out = append(out, row...)
		out[2]++
	}
	return append(out, body[end:]...)
}
