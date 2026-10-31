package dnfbridge

import (
	"bytes"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// V179 menu key 8 targets the main input; key 22 targets a detached window.
// The former sends type 1, and NOTI65 type 1 reaches the main chat UI without
// the extra conversation-name DSTR consumed by detached-window type 0x24.
func (s *Service) sendCurrentMainWhisper(sourceSession, targetSession *gameSession, opcode uint16,
	request currentPeerChatRequest, source dnfrepo.CharacterRecord) error {
	if request.MessageType != 1 || len(request.Message) > 255 || bytes.IndexByte(request.Message, 0) >= 0 {
		return s.sendGameUpperFailure(sourceSession, opcode, 1)
	}
	if err := s.sendGameUpperSuccess(sourceSession, opcode, nil); err != nil {
		return err
	}
	var w packetWriter
	w.writeBytes([]byte{1, 0})
	w.writeRawDstr(rosterNameBytes(source.Name))
	w.writeByte(currentAdventureCharacterServerGroup(source))
	w.writeRawDstr(request.Message)
	w.writeRawDstr(nil)
	if err := s.sendGameUpperRawClassCodec(targetSession, currentOfficialWhisperNotifyOpcode, w.bytes(), 0, true); err != nil {
		return err
	}
	s.logGameEvent(sourceSession, "game-main-whisper-forwarded", "source_char_id", selectedCharacterID(sourceSession),
		"target_char_id", selectedCharacterID(targetSession), "message_len", len(request.Message),
		"body_source", "v179_menu8_main_input_type1_noti65_main_chat")
	return nil
}
