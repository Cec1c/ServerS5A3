package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

const (
	currentPeerChatStateOpcode  = uint16(17)
	currentPeerChatNotifyOpcode = uint16(118)
	currentFriendNotifyOpcode   = uint16(274)
	currentFriendAddOpcode      = uint16(290)
	currentDstrMaximumLength    = 4096
)

var errCurrentSocialBodyInvalid = errors.New("current social command body is invalid")

type currentPeerChatRequest struct {
	MessageType      byte
	TargetID         uint16
	Unknown          uint32
	Message          []byte
	TargetName       []byte
	TrailingValue    byte
	HasTrailingValue bool
}

type currentFriendAddRequest struct {
	TargetID   uint16
	TargetName []byte
}

func (s *Service) handleOnlineSocialCommand(session *gameSession, typ uint16, body []byte) (bool, error) {
	switch typ {
	case currentPeerChatStateOpcode:
		return true, s.handleCurrentPeerChat(session, typ, body)
	case uint16(dnfenum.CmdPacketItemHyperlinkMessage):
		return true, s.handleCurrentItemHyperlinkMessage(session, typ, body)
	case currentFriendAddOpcode:
		if s.options.currentClientVersion == "official-24.9" {
			return true, s.handleCurrentOfficialCharacterFriendMutation(session, typ, body)
		}
		return true, s.handleCurrentFriendAdd(session, typ, body)
	case uint16(291):
		if s.options.currentClientVersion == "official-24.9" {
			return true, s.handleCurrentOfficialCharacterFriendMutation(session, typ, body)
		}
		return false, nil
	case uint16(dnfenum.CmdPacketRegisiterToBlacklist), uint16(dnfenum.CmdPacketDeleteToBlacklist):
		return true, s.handleCurrentBlacklistMutation(session, typ, body)
	default:
		return false, nil
	}
}

func (s *Service) handleCurrentPeerChat(session *gameSession, typ uint16, body []byte) error {
	request, err := decodeCurrentPeerChatRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-peer-chat-request-rejected", "body_len", len(body), "error", err)
		if len(body) > 0 && (body[0] == currentGuildChatType || body[0] == currentPartyChatType) {
			return s.sendGameUpperFailure(session, typ, 1)
		}
		return nil
	}
	if request.MessageType == currentGuildChatType {
		return s.handleCurrentGuildChat(session, typ, request)
	}
	if request.MessageType == currentPartyChatType {
		return s.handleCurrentPartyChat(session, typ, request)
	}
	if len(request.TargetName) == 0 {
		if request.MessageType == currentRaidChatType || request.MessageType == currentRaidLeaderChatType {
			return s.handleCurrentRaidChat(session, typ, request)
		}
		return s.handleCurrentAreaChat(session, typ, request)
	}
	sourceID := selectedCharacterID(session)
	if request.HasTrailingValue && !isCurrentPrivateWhisperType(request.MessageType) {
		// Group/party variants need their own authenticated recipient owner.
		// Never project their private text to a guessed town or single peer.
		s.logGameEvent(session, "game-peer-chat-request-rejected", "message_type", request.MessageType,
			"reason", "native_group_route_not_yet_verified")
		return s.sendGameUpperFailure(session, typ, 1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repositories, available := s.repositoryGroup()
	if !available || repositories.Character == nil || sourceID == 0 || strings.TrimSpace(s.accountIDForSession(session)) == "" {
		return s.sendGameUpperFailure(session, typ, 1)
	}
	// Current EXE 0x201BFF7 writes ID zero and a target name. Resolve the
	// repository identity before selecting an online recipient.
	if isCurrentPrivateWhisperType(request.MessageType) && request.TargetID == 0 && request.HasTrailingValue {
		name, decodeErr := decodeCharacterName(request.TargetName)
		if decodeErr != nil {
			return s.sendGameUpperFailure(session, typ, 1)
		}
		id, found, findErr := repositories.Character.FindIDByName(ctx, name)
		value, parseErr := strconv.ParseUint(id, 10, 16)
		if findErr != nil || !found || parseErr != nil || value == 0 {
			return s.sendGameUpperFailure(session, typ, 1)
		}
		request.TargetID = uint16(value)
	}
	privateWhisper := isCurrentPrivateWhisperType(request.MessageType) && request.Unknown == 0
	if request.TargetID == 0 || request.TargetID == sourceID ||
		(!privateWhisper && !s.currentSocialPeerAvailable(sourceID, request.TargetID)) {
		s.logGameEvent(session, "game-peer-chat-request-rejected",
			"source_char_id", sourceID,
			"target_char_id", request.TargetID,
			"message_type", request.MessageType,
			"reason", "target_unavailable_for_message_route")
		return nil
	}
	targetSession, online := s.onlineGameSession(request.TargetID)
	if !online || selectedCharacterID(targetSession) != request.TargetID {
		return s.sendGameUpperFailure(session, typ, 1)
	}
	// Preserve the existing reply contract for profiles outside the verified
	// official client. Their NOTI118 route still carries an owner channel.
	if s.options.currentClientVersion != "official-24.9" && request.MessageType == 0x28 {
		channel, channelOK := currentSessionOwnerChannelField(targetSession)
		if !privateWhisper || !request.HasTrailingValue || !channelOK || request.TrailingValue != channel {
			s.logGameEvent(session, "game-peer-chat-request-rejected", "message_type", request.MessageType,
				"reason", "incoming_whisper_peer_channel_mismatch")
			return s.sendGameUpperFailure(session, typ, 1)
		}
	}
	sourceIdentity, sourceBound := s.boundGameSessionCharacterSnapshot(session)
	targetIdentity, targetBound := s.boundGameSessionCharacterSnapshot(targetSession)
	source, sourceFound, sourceErr := repositories.Character.Load(ctx, strconv.Itoa(int(sourceID)))
	target, targetFound, targetErr := repositories.Character.Load(ctx, strconv.Itoa(int(request.TargetID)))
	targetName, nameErr := decodeCharacterName(request.TargetName)
	if !sourceBound || !targetBound || sourceErr != nil || targetErr != nil || !sourceFound || !targetFound ||
		strings.TrimSpace(source.AccountID) != strings.TrimSpace(s.accountIDForSession(session)) ||
		strings.TrimSpace(target.AccountID) != strings.TrimSpace(s.accountIDForSession(targetSession)) ||
		nameErr != nil || targetName != target.Name || len(request.Message) > 512 ||
		!s.boundGameSessionCharacterCurrent(sourceIdentity) || !s.boundGameSessionCharacterCurrent(targetIdentity) {
		s.logGameEvent(session, "game-peer-chat-request-rejected",
			"source_char_id", sourceID,
			"target_char_id", request.TargetID,
			"reason", "repository_identity_unavailable")
		return nil
	}
	sourceName := source.Name
	if len(request.Message) == 0 {
		return nil
	}
	if s.options.currentClientVersion == "official-24.9" {
		// V179 348F607 reads actor+3AC (character server group), and 348F6F7
		// writes the conversation's same group, not an owner-channel byte.
		// Typed-name 201BFD8/201C05D uses the local character group. This
		// repository only resolves its own group; never alias foreign names.
		sourceGroup, targetGroup := source.Stats["server_group_id"], target.Stats["server_group_id"]
		if !privateWhisper || !request.HasTrailingValue || sourceGroup < 0 || sourceGroup >= 250 ||
			targetGroup != sourceGroup || int64(request.TrailingValue) != targetGroup {
			s.logGameEvent(session, "game-peer-chat-request-rejected", "message_type", request.MessageType,
				"reason", "whisper_character_group_mismatch")
			return s.sendGameUpperFailure(session, typ, 1)
		}
		return s.sendCurrentOfficialWhisper(session, targetSession, typ, request, source, target)
	}
	if err := s.sendGameUpperSuccess(session, typ, nil); err != nil {
		return err
	}
	peerState := buildCurrentPeerChatStateBody(request.TargetID, request.MessageType)
	if err := s.sendGameUpperRawClassCodec(session, currentPeerChatStateOpcode, peerState, 0, true); err != nil {
		return err
	}
	incomingState := buildCurrentPeerChatStateBody(sourceID, request.MessageType)
	if err := s.sendGameUpperRawClassCodec(targetSession, currentPeerChatStateOpcode, incomingState, 0, true); err != nil {
		return err
	}
	// 频道字节是 u8：这里原来写 `byte(targetSession.channel.ID)`，会话频道 > 255 时会
	// 截断成**另一个真实频道号**（508 → 252）。改成"装得下就用真实频道，装不下就用
	// catalog 落点频道（恒 ≤255）"，两条路都写得出具名事件。
	targetOwnerChannel, targetOwnerOK := currentSessionOwnerChannelField(targetSession)
	notice := buildCurrentPeerChatNoticeBody(
		request.MessageType,
		targetOwnerChannel,
		sourceID,
		rosterNameBytes(sourceName),
		request.Message,
	)
	if err := s.sendGameUpperRawClassCodec(targetSession, currentPeerChatNotifyOpcode, notice, 0, true); err != nil {
		return err
	}
	chatFields := []any{
		"source_char_id", sourceID,
		"target_char_id", request.TargetID,
		"target_name", targetName,
		"message_type", request.MessageType,
		"message_len", len(request.Message),
		"target_owner_channel", targetOwnerChannel,
		"target_connected_channel", targetSession.channel.ID,
		"body_source", "current_exe_class1_op17_ack_class0_op17_state_class0_op118_message",
	}
	if currentOwnerChannelOversized(targetSession.channel.ID) {
		if targetOwnerOK {
			chatFields = append(chatFields,
				"owner_channel_source", "catalog_resident_landing",
				"owner_channel_reason", currentOwnerChannelSubstitutionReason)
			s.logOwnerChannelSubstitution(targetSession, "game_peer_chat_notice", int(targetOwnerChannel))
		} else {
			chatFields = append(chatFields,
				"owner_channel_source", "session_owner_context_zero",
				"owner_channel_reason", currentOwnerChannelNoLandingReason)
			s.logOwnerChannelUnrepresentable(targetSession, "game_peer_chat_notice", "session_owner_context_zero")
		}
	}
	s.logGameEvent(session, "game-peer-chat-forwarded", chatFields...)
	return nil
}

func (s *Service) handleCurrentFriendAdd(session *gameSession, typ uint16, body []byte) error {
	request, err := decodeCurrentFriendAddRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-friend-add-request-rejected", "body_len", len(body), "error", err)
		return nil
	}
	sourceID := selectedCharacterID(session)
	if !s.currentSocialPeerAvailable(sourceID, request.TargetID) {
		s.logGameEvent(session, "game-friend-add-request-rejected",
			"source_char_id", sourceID,
			"target_char_id", request.TargetID,
			"reason", "target_not_online_in_same_area")
		return nil
	}
	targetSession, online := s.onlineGameSession(request.TargetID)
	if !online {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	loadedID, targetName, _, found := s.characterForEnter(ctx, targetSession, request.TargetID)
	if !found || loadedID != request.TargetID {
		return nil
	}
	if err := s.sendGameUpperSuccess(session, typ, nil); err != nil {
		return err
	}
	notice := buildCurrentFriendAddNoticeBody(request.TargetID, rosterNameBytes(targetName), true)
	if err := s.sendGameUpperRawClassCodec(session, currentFriendNotifyOpcode, notice, 0, true); err != nil {
		return err
	}
	s.logGameEvent(session, "game-friend-add-response-send",
		"source_char_id", sourceID,
		"target_char_id", request.TargetID,
		"target_name", targetName,
		"body_source", "current_exe_class1_op290_ack_class0_op274_friend_row")
	return nil
}

func (s *Service) handleCurrentBlacklistMutation(session *gameSession, typ uint16, body []byte) error {
	name, next, ok := readCurrentRawDstr(body, 0)
	if !ok || next != len(body) || len(name) == 0 {
		s.logGameEvent(session, "game-blacklist-mutation-rejected",
			"type", typ,
			"body_len", len(body),
			"reason", "exact_name_dstr_required")
		return nil
	}
	var response packetWriter
	response.writeRawDstr(name)
	s.logGameEvent(session, "game-blacklist-mutation-ack",
		"type", typ,
		"selected_character_id", selectedCharacterID(session),
		"name_len", len(name),
		"body_source", "current_exe_class1_callback_reads_echoed_name_dstr")
	return s.sendGameUpperSuccess(session, typ, response.bytes())
}

func (s *Service) currentSocialPeerAvailable(sourceID, targetID uint16) bool {
	return sourceID != 0 && targetID != 0 && sourceID != targetID &&
		s != nil && s.onlinePlayers != nil && s.onlinePlayers.PeerInSameArea(sourceID, targetID)
}

func decodeCurrentPeerChatRequest(body []byte) (currentPeerChatRequest, error) {
	if len(body) < 11 {
		return currentPeerChatRequest{}, errCurrentSocialBodyInvalid
	}
	request := currentPeerChatRequest{
		MessageType: body[0],
		TargetID:    binary.LittleEndian.Uint16(body[1:3]),
		Unknown:     binary.LittleEndian.Uint32(body[3:7]),
	}
	message, next, ok := readCurrentRawDstr(body, 7)
	if !ok {
		return currentPeerChatRequest{}, errCurrentSocialBodyInvalid
	}
	request.Message = message
	// The live normal-chat request ends after its message DSTR. Only a peer
	// request appends the target-name DSTR; requiring it rejects normal chat.
	if next == len(body) {
		return request, nil
	}
	targetName, next, ok := readCurrentRawDstr(body, next)
	if !ok || len(targetName) == 0 {
		return currentPeerChatRequest{}, errCurrentSocialBodyInvalid
	}
	// Current producers append exactly one u8 after the second DSTR
	// (0x201C05D / 0x348F6F7 / 0x2A04DB0). For private whispers it is the
	// character server group; other variants retain their separate routes.
	if next == len(body)-1 {
		request.TrailingValue = body[next]
		request.HasTrailingValue = true
		next++
	}
	if next != len(body) ||
		(request.MessageType == 0x28 && (!request.HasTrailingValue || request.Unknown != 0)) ||
		(request.TargetID == 0 && !(isCurrentPrivateWhisperType(request.MessageType) && request.HasTrailingValue)) {
		return currentPeerChatRequest{}, errCurrentSocialBodyInvalid
	}
	request.Message = message
	request.TargetName = targetName
	return request, nil
}

func decodeCurrentFriendAddRequest(body []byte) (currentFriendAddRequest, error) {
	if len(body) < 6 {
		return currentFriendAddRequest{}, errCurrentSocialBodyInvalid
	}
	request := currentFriendAddRequest{TargetID: binary.LittleEndian.Uint16(body[:2])}
	name, next, ok := readCurrentRawDstr(body, 2)
	if !ok || next != len(body) || request.TargetID == 0 || len(name) == 0 {
		return currentFriendAddRequest{}, errCurrentSocialBodyInvalid
	}
	request.TargetName = name
	return request, nil
}

func readCurrentRawDstr(body []byte, offset int) ([]byte, int, bool) {
	if offset < 0 || offset > len(body)-4 {
		return nil, offset, false
	}
	length := int32(binary.LittleEndian.Uint32(body[offset : offset+4]))
	if length < 0 || length > currentDstrMaximumLength {
		return nil, offset, false
	}
	start := offset + 4
	end := start + int(length)
	if end < start || end > len(body) {
		return nil, offset, false
	}
	return append([]byte(nil), body[start:end]...), end, true
}

func buildCurrentPeerChatStateBody(peerID uint16, messageType byte) []byte {
	var writer packetWriter
	writer.writeUint16(peerID)
	writer.writeByte(messageType)
	return writer.bytes()
}

func buildCurrentPeerChatNoticeBody(messageType, channelID byte, senderID uint16, senderName, message []byte) []byte {
	var writer packetWriter
	writer.writeByte(messageType)
	writer.writeByte(channelID)
	writer.writeByte(0)
	writer.writeByte(channelID)
	writer.writeUint16(senderID)
	writer.writeRawDstr(senderName)
	writer.writeRawDstr(message)
	// Current EXE 0x200C9A0 reads a third DSTR (max 17 wide characters) at
	// 0x200CAC2 before dispatching to the chat UI. Peer/normal chat has no group
	// name, but its empty length field must still be present.
	writer.writeRawDstr(nil)
	return writer.bytes()
}

func buildCurrentFriendAddNoticeBody(characterID uint16, name []byte, online bool) []byte {
	var writer packetWriter
	writer.writeUint16(characterID)
	writer.writeByte(0)
	writer.writeRawDstr(name)
	if online {
		writer.writeByte(1)
	} else {
		writer.writeByte(0)
	}
	return writer.bytes()
}
