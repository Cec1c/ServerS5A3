package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"longheng.io/server/internal/modules/dnf/characterfriend"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
)

const currentCharacterFriendListOpcode = uint16(273)

type currentCharacterFriendMutation struct {
	TargetID    uint16
	ServerGroup byte
	Name        string
}

// Actual shipped EXE 029C2EDE emits u16 actor UID, u8 server group, DSTR.
// 029C3088 deletes by u8 server group, DSTR, without an actor UID.
func decodeCurrentCharacterFriendMutation(opcode uint16, body []byte) (currentCharacterFriendMutation, error) {
	var request currentCharacterFriendMutation
	offset := 0
	if opcode == 290 {
		if len(body) < 7 {
			return request, errCurrentSocialBodyInvalid
		}
		request.TargetID = binary.LittleEndian.Uint16(body)
		offset = 2
	} else if opcode != 291 {
		return request, errCurrentSocialBodyInvalid
	}
	if len(body) < offset+5 {
		return request, errCurrentSocialBodyInvalid
	}
	request.ServerGroup = body[offset]
	raw, end, ok := readCurrentRawDstr(body, offset+1)
	if !ok || end != len(body) || len(raw) == 0 || request.ServerGroup >= 250 {
		return request, errCurrentSocialBodyInvalid
	}
	name, err := decodeCharacterName(raw)
	if err != nil {
		return request, errCurrentSocialBodyInvalid
	}
	request.Name = name
	return request, nil
}

func (s *Service) handleCurrentOfficialCharacterFriendMutation(session *gameSession, opcode uint16, body []byte) error {
	request, err := decodeCurrentCharacterFriendMutation(opcode, body)
	if err != nil {
		s.logGameEvent(session, "game-character-friend-rejected", "opcode", opcode, "reason", "native_request_shape")
		return s.sendGameUpperFailure(session, opcode, 0x15)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	repos, available := s.repositoryGroup()
	if !bound || !available || repos.Character == nil {
		return s.sendGameUpperFailure(session, opcode, 0x15)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	sourceID := strconv.Itoa(int(selectedCharacterID(session)))
	source, found, err := repos.Character.Load(ctx, sourceID)
	if err != nil || !found || source.AccountID != s.accountIDForSession(session) || !s.boundGameSessionCharacterCurrent(identity) {
		return s.sendGameUpperFailure(session, opcode, 0x15)
	}
	// This repository owns the local server group only. Cross-server identities
	// cannot be substituted with a local character having the same name.
	group := currentAdventureCharacterServerGroup(source)
	if request.ServerGroup != group {
		return s.sendGameUpperFailure(session, opcode, 0x15)
	}
	owner := characterfriend.NewOwner(repos)
	var target dnfrepo.CharacterRecord
	if opcode == 290 {
		id, exists, findErr := repos.Character.FindIDByName(ctx, request.Name)
		if findErr != nil || !exists {
			return s.sendGameUpperFailure(session, opcode, 0x15)
		}
		target, found, err = repos.Character.Load(ctx, id)
		n, parseErr := strconv.ParseUint(id, 10, 16)
		if err != nil || !found || parseErr != nil || n == 0 || target.Name != request.Name || currentAdventureCharacterServerGroup(target) != group || (request.TargetID != 0xffff && request.TargetID != 0 && request.TargetID != uint16(n)) {
			return s.sendGameUpperFailure(session, opcode, 0x15)
		}
		if target.CharacterID == sourceID {
			return s.sendGameUpperFailure(session, opcode, 1)
		}
		if !s.boundGameSessionCharacterCurrent(identity) {
			return s.sendGameUpperFailure(session, opcode, 0x15)
		}
		err = owner.Add(ctx, source.AccountID, sourceID, target.CharacterID)
	} else {
		rows, listErr := owner.List(ctx, source.AccountID, sourceID)
		if listErr != nil {
			return s.sendGameUpperFailure(session, opcode, 0x15)
		}
		for _, row := range rows {
			if row.Name == request.Name && currentAdventureCharacterServerGroup(row) == group {
				target = row
				break
			}
		}
		if target.CharacterID == "" {
			return s.sendGameUpperFailure(session, opcode, 0x15)
		}
		if !s.boundGameSessionCharacterCurrent(identity) {
			return s.sendGameUpperFailure(session, opcode, 0x15)
		}
		err = owner.Remove(ctx, source.AccountID, sourceID, target.CharacterID)
	}
	if err != nil {
		code := byte(0x15)
		if errors.Is(err, dnfrepo.ErrAccountMetadataLimitReached) {
			code = 4
		}
		s.logGameEvent(session, "game-character-friend-rejected", "opcode", opcode, "reason", "repository_mutation", "error", err)
		return s.sendGameUpperFailure(session, opcode, code)
	}
	if err := s.sendGameUpperSuccess(session, opcode, nil); err != nil {
		return err
	}
	if opcode == 291 {
		var w packetWriter
		w.writeUint32(2)
		w.writeUint32(1)
		w.writeByte(group)
		w.writeRawDstr(rosterNameBytes(request.Name))
		if err := s.sendGameUpperRawClassCodec(session, currentCharacterFriendListOpcode, w.bytes(), 0, true); err != nil {
			return err
		}
	}
	s.logGameEvent(session, "game-character-friend-persisted", "opcode", opcode, "source_char_id", sourceID, "target_char_id", target.CharacterID)
	return s.sendCurrentCharacterFriendList(session, "mutation")
}

func (s *Service) sendCurrentCharacterFriendList(session *gameSession, source string) error {
	if s.options.currentClientVersion != "official-24.9" || session == nil || selectedCharacterID(session) == 0 {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Account == nil || repos.Character == nil {
		return nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	rows, err := characterfriend.NewOwner(repos).List(ctx, s.accountIDForSession(session), strconv.Itoa(int(selectedCharacterID(session))))
	if err != nil {
		s.logGameEvent(session, "game-character-friend-list-deferred", "source", source, "error", err)
		return nil
	}
	body, err := s.buildCurrentCharacterFriendListBody(rows)
	if err != nil {
		return err
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	s.logGameEvent(session, "game-character-friend-list-send", "source", source, "count", len(rows), "body_len", len(body))
	return s.sendGameUpperRawClassCodec(session, currentCharacterFriendListOpcode, body, 0, true)
}

// 02006680 subcommand 0 -> 029C7F20 reset -> 029C74D0 full row insertion.
// 029C18D0 reads the extra trailing bool that the historical C# codec lacks.
func (s *Service) buildCurrentCharacterFriendListBody(rows []dnfrepo.CharacterRecord) ([]byte, error) {
	var w packetWriter
	w.writeUint32(0)
	w.writeUint32(uint32(len(rows)))
	for _, row := range rows {
		job := numericCharacterStat(row.Job)
		if row.Level < 0 || row.Level > 255 || job < 0 || job > 0xffffffff || len(rosterNameBytes(row.Name)) > 255 {
			return nil, errCurrentSocialBodyInvalid
		}
		channel := uint16(0)
		n, err := strconv.ParseUint(row.CharacterID, 10, 16)
		if err != nil || n == 0 {
			return nil, errCurrentSocialBodyInvalid
		}
		if peer, online := s.onlineGameSession(uint16(n)); online && s.accountIDForSession(peer) == row.AccountID {
			if value, ok := currentSessionOwnerChannelField(peer); ok {
				channel = uint16(value)
			}
		}
		w.writeByte(currentAdventureCharacterServerGroup(row))
		w.writeUint16(channel)
		w.writeRawDstr(rosterNameBytes(row.Name))
		w.writeByte(byte(row.Level))
		w.writeUint32(uint32(job))
		w.writeByte(characterGrowTypeByte(row))
		// Ordinary local character friends do not own special friend-state Y/Z
		// or the trailing cross-service flag. Keep their inactive values; do not
		// derive these fields from guessed account counters or actor IDs.
		w.writeByte(0)
		w.writeUint32(0)
		w.writeByte(0)
	}
	return w.bytes(), nil
}
