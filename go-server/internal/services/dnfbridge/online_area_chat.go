package dnfbridge

import (
	"context"
	"strconv"
	"strings"
)

// Type 3 and the message-only body are established by the current player's
// normal-chat op17 request. Other channels require their own recipient owner;
// they must not accidentally broadcast private/party/guild text to a town.
const (
	currentAreaChatMessageType  byte = 3
	currentAreaChatNotifyOpcode      = uint16(12)
)

// Current NOTI12 / 2035370 resolves the sender from the scene actor registry.
// It reads type(u8), sender(u16), channel(u8), message(DSTR), extra(DSTR).
// NOTI118 is a megaphone route and can discard ordinary type-3 messages.
func buildCurrentAreaChatNoticeBody(channelID byte, senderID uint16, message []byte) []byte {
	var writer packetWriter
	writer.writeByte(currentAreaChatMessageType)
	writer.writeUint16(senderID)
	writer.writeByte(channelID)
	writer.writeRawDstr(message)
	writer.writeRawDstr(nil)
	return writer.bytes()
}

func (s *Service) handleCurrentAreaChat(session *gameSession, typ uint16, request currentPeerChatRequest) error {
	sourceID := selectedCharacterID(session)
	if session == nil || sourceID == 0 || request.MessageType != currentAreaChatMessageType ||
		len(request.Message) == 0 || len(request.Message) > 512 {
		s.logGameEvent(session, "game-area-chat-request-rejected", "message_type", request.MessageType,
			"message_len", len(request.Message), "reason", "selected_normal_chat_required")
		return s.sendGameUpperFailure(session, typ, 1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repositories, available := s.repositoryGroup()
	if !available || repositories.Character == nil || strings.TrimSpace(s.accountIDForSession(session)) == "" {
		return s.sendGameUpperFailure(session, typ, 1)
	}
	character, found, err := repositories.Character.Load(ctx, strconv.Itoa(int(sourceID)))
	if err != nil || !found || strings.TrimSpace(character.AccountID) != strings.TrimSpace(s.accountIDForSession(session)) {
		return s.sendGameUpperFailure(session, typ, 1)
	}
	if err := s.sendGameUpperSuccess(session, typ, nil); err != nil {
		return err
	}
	// Always echo the authoritative sender name/message to its own information
	// panel, including private rooms where the shared-area roster is empty.
	recipients := map[uint16]*gameSession{sourceID: session}
	if s.onlinePlayers != nil {
		if source, ok := s.onlinePlayers.PlayerForCharacter(sourceID); ok {
			for _, peer := range s.onlinePlayers.GetAreaScopePlayers(source.TownID, source.AreaID, source.ChannelID, source.Instance, source.RaidBattleInstance) {
				if s.onlinePlayers.PeerInSameArea(sourceID, peer.CharacterID) && peer.Session != nil {
					recipients[peer.CharacterID] = peer.Session
				}
			}
		}
	}
	for id, recipient := range recipients {
		if selectedCharacterID(recipient) != id {
			continue
		}
		channel, _ := currentSessionOwnerChannelField(recipient)
		body := buildCurrentAreaChatNoticeBody(channel, sourceID, request.Message)
		if err := s.sendGameUpperRawClassCodec(recipient, currentAreaChatNotifyOpcode, body, 0, true); err != nil {
			if recipient == session {
				return err
			}
			s.logWarn("dnfbridge area chat peer send failed", "source_char_id", sourceID, "target_char_id", id, "error", err)
		}
	}
	s.logGameEvent(session, "game-area-chat-forwarded", "source_char_id", sourceID,
		"message_type", request.MessageType, "message_len", len(request.Message), "recipients", len(recipients),
		"body_source", "current_exe_cmd17_noti12_type_sender_channel_two_dstr")
	return nil
}
