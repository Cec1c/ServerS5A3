package dnfbridge

const (
	currentRaidChatType       byte = 55
	currentRaidLeaderChatType byte = 56
)

// Public v95 NOTI118 / 0200C9A0 reads type, server, reserved, channel,
// sender, name DSTR, message DSTR and group DSTR. Its native context checks
// compare the server and channel separately before 02A00710 constructs the
// information-panel record. This route carries the authoritative name even
// when another raid member is in a different scene.
func buildCurrentRaidChatNoticeBody(messageType, server, channel byte, sender uint16, name, message []byte) []byte {
	var w packetWriter
	w.writeByte(messageType)
	w.writeByte(server)
	w.writeByte(0)
	w.writeByte(channel)
	w.writeUint16(sender)
	w.writeRawDstr(name)
	w.writeRawDstr(message)
	w.writeRawDstr(nil)
	return w.bytes()
}

func (s *Service) handleCurrentRaidChat(session *gameSession, op uint16, request currentPeerChatRequest) error {
	source := selectedCharacterID(session)
	state, found := s.runtimeRaidForMember(source)
	if !found || !s.currentRaidInfoVisible(session, state) || request.TargetID != source || request.Unknown != 0 ||
		len(request.TargetName) != 0 || request.HasTrailingValue || len(request.Message) == 0 || len(request.Message) > 512 ||
		(request.MessageType != currentRaidChatType && request.MessageType != currentRaidLeaderChatType) ||
		(request.MessageType == currentRaidLeaderChatType && state.LeaderID != source) {
		s.logGameEvent(session, "game-current-raid-chat-rejected", "source_char_id", source,
			"message_type", request.MessageType, "reason", "authenticated_current_member_or_leader_required")
		return s.sendGameUpperFailure(session, op, 1)
	}
	index := runtimeRaidMemberIndex(state, source)
	if index < 0 || state.Members[index].Name == "" {
		return s.sendGameUpperFailure(session, op, 1)
	}
	body := buildCurrentRaidChatNoticeBody(request.MessageType, byte(state.RaidKey>>24), byte(state.RaidKey>>16),
		source, rosterNameBytes(state.Members[index].Name), request.Message)
	if err := s.sendGameUpperSuccess(session, op, nil); err != nil {
		return err
	}
	sent := 0
	for _, member := range state.Members {
		peer, online := s.onlineGameSession(member.CharID)
		if !online || peer.channel.ID != session.channel.ID {
			continue
		}
		identity, bound := s.boundGameSessionCharacterSnapshot(peer)
		current, belongs := s.runtimeRaidForMember(member.CharID)
		if !bound || identity.character != member.CharID || !s.boundGameSessionCharacterCurrent(identity) ||
			!belongs || current.RaidKey != state.RaidKey || !s.currentRaidInfoVisible(peer, current) {
			continue
		}
		if err := s.sendGameUpperRawClassCodec(peer, currentPeerChatNotifyOpcode, body, 0, true); err != nil {
			if peer == session {
				return err
			}
			s.logWarn("dnfbridge raid chat peer send failed", "source_char_id", source, "target_char_id", member.CharID, "error", err)
			continue
		}
		sent++
	}
	s.logGameEvent(session, "game-current-raid-chat-forwarded", "raid_key", state.RaidKey,
		"source_char_id", source, "message_type", request.MessageType, "message_len", len(request.Message),
		"recipients", sent, "body_source", "public_v95_noti118_server_channel_name_message_three_dstr")
	return nil
}
