package dnfbridge

import dnfrepo "longheng.io/server/internal/modules/dnf/repository"

const currentOfficialWhisperNotifyOpcode = uint16(65)

func isCurrentPrivateWhisperType(messageType byte) bool {
	return messageType == 0x24 || messageType == 0x28
}

// NOTI65 / 201B510 reads type, result, sender DSTR, sender channel,
// message DSTR, auxiliary DSTR, then the conversation peer DSTR for type 0x24.
// 29FFFA0 -> 348B520 uses the last name to find/create the conversation window;
// it must name the other participant from the receiving client's perspective.
// A zero result takes 201C076 -> 201C22B, the actual message display path.
// NOTI17 is STATE_ITEMTRADE in this EXE and must not be emitted for whispers.
func buildCurrentOfficialWhisperBody(channel byte, sender, peer string, message []byte) []byte {
	var writer packetWriter
	writer.writeByte(0x24)
	writer.writeByte(0)
	writer.writeRawDstr(rosterNameBytes(sender))
	writer.writeByte(channel)
	writer.writeRawDstr(message)
	writer.writeRawDstr(nil)
	writer.writeRawDstr(rosterNameBytes(peer))
	return writer.bytes()
}

func (s *Service) sendCurrentOfficialWhisper(sourceSession, targetSession *gameSession, opcode uint16,
	request currentPeerChatRequest, source, target dnfrepo.CharacterRecord) error {
	if !isCurrentPrivateWhisperType(request.MessageType) {
		return s.sendGameUpperFailure(sourceSession, opcode, 1)
	}
	if err := s.sendGameUpperSuccess(sourceSession, opcode, nil); err != nil {
		return err
	}
	channel, _ := currentSessionOwnerChannelField(sourceSession)
	incoming := buildCurrentOfficialWhisperBody(channel, source.Name, source.Name, request.Message)
	if err := s.sendGameUpperRawClassCodec(targetSession, currentOfficialWhisperNotifyOpcode, incoming, 0, true); err != nil {
		return err
	}
	echo := buildCurrentOfficialWhisperBody(channel, source.Name, target.Name, request.Message)
	if err := s.sendGameUpperRawClassCodec(sourceSession, currentOfficialWhisperNotifyOpcode, echo, 0, true); err != nil {
		return err
	}
	s.logGameEvent(sourceSession, "game-official-whisper-forwarded", "source_char_id", selectedCharacterID(sourceSession),
		"target_char_id", selectedCharacterID(targetSession), "message_type", request.MessageType,
		"message_len", len(request.Message), "msg_id", currentOfficialWhisperNotifyOpcode,
		"body_source", "current_exe_noti65_success_two_names_four_dstr_no_trade_state")
	return nil
}
