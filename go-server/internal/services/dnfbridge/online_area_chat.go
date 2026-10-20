package dnfbridge

import (
	"context"
	"sort"
	"strconv"
	"strings"
)

// Type 3 and the message-only body are established by the current player's
// normal-chat op17 request. Other channels require their own recipient owner;
// they must not accidentally broadcast private/party/guild text to a town.
const (
	currentAreaChatMessageType  byte = 3
	currentAreaChatNotifyOpcode      = uint16(12)
	currentAreaChatMessageLimit      = 512
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
		len(request.Message) == 0 || len(request.Message) > currentAreaChatMessageLimit {
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
	recipients := []*gameSession{session}
	recipients = append(recipients, s.currentAreaChatPeerSessions(session, sourceID)...)
	for _, recipient := range recipients {
		id := selectedCharacterID(recipient)
		if id == 0 {
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

// currentAreaChatPeerSessions returns only current, canonically bound sessions
// in the source's exact town/area/channel/instance bucket. Self is handled by
// callers separately, so a stale socket can never be selected by character ID.
func (s *Service) currentAreaChatPeerSessions(source *gameSession, sourceID uint16) []*gameSession {
	if s == nil || s.onlinePlayers == nil || source == nil || sourceID == 0 {
		return nil
	}
	info, found := s.onlinePlayers.PlayerForCharacter(sourceID)
	if !found || info.Session != source {
		return nil
	}
	seen := make(map[uint16]struct{})
	peers := make([]*gameSession, 0, 4)
	for _, peer := range s.onlinePlayers.GetAreaScopePlayers(
		info.TownID, info.AreaID, info.ChannelID, info.Instance, info.RaidBattleInstance,
	) {
		if peer.CharacterID == sourceID || peer.Session == nil || !s.onlinePlayers.PeerInSameArea(sourceID, peer.CharacterID) {
			continue
		}
		identity, bound := s.onlineGameSessionCharacterSnapshot(peer.CharacterID)
		if !bound || identity.session != peer.Session || !s.boundGameSessionCharacterCurrent(identity) {
			continue
		}
		if _, duplicate := seen[peer.CharacterID]; duplicate {
			continue
		}
		seen[peer.CharacterID] = struct{}{}
		peers = append(peers, peer.Session)
	}
	sort.Slice(peers, func(i, j int) bool {
		return selectedCharacterID(peers[i]) < selectedCharacterID(peers[j])
	})
	return peers
}
