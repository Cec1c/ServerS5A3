package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
)

const (
	currentItemHyperlinkNotifyOpcode = uint16(370)
	currentItemHyperlinkItemSize     = 148
	currentItemHyperlinkItemLimit    = 5
	// The current class0/370 DSTR reader has capacity 256 including its terminator.
	currentItemHyperlinkMessageLimit = 255
)

var errCurrentItemHyperlinkBodyInvalid = errors.New("current item hyperlink body is invalid")

type currentItemHyperlinkRequest struct {
	MessageType byte
	Hint        uint16
	Reserved    uint32
	Message     []byte
	Items       [][]byte
}

// Current op424 request layout:
//
//	u8 type, u16 hint, u32 reserved, u32 message length,
//	message bytes, u8 item count, count * 148 opaque item bytes.
//
// The hint is metadata from the client and is never used as sender identity.
func decodeCurrentItemHyperlinkRequest(body []byte) (currentItemHyperlinkRequest, error) {
	if len(body) < 12 {
		return currentItemHyperlinkRequest{}, errCurrentItemHyperlinkBodyInvalid
	}
	request := currentItemHyperlinkRequest{
		MessageType: body[0],
		Hint:        binary.LittleEndian.Uint16(body[1:3]),
		Reserved:    binary.LittleEndian.Uint32(body[3:7]),
	}
	messageLength := binary.LittleEndian.Uint32(body[7:11])
	if (request.MessageType != currentAreaChatMessageType && request.MessageType != currentGuildChatType) || request.Reserved != 0 ||
		messageLength == 0 || messageLength > currentItemHyperlinkMessageLimit {
		return currentItemHyperlinkRequest{}, errCurrentItemHyperlinkBodyInvalid
	}
	messageEnd := 11 + int(messageLength)
	if messageEnd >= len(body) {
		return currentItemHyperlinkRequest{}, errCurrentItemHyperlinkBodyInvalid
	}
	request.Message = append([]byte(nil), body[11:messageEnd]...)
	if bytes.IndexByte(request.Message, 0) >= 0 {
		return currentItemHyperlinkRequest{}, errCurrentItemHyperlinkBodyInvalid
	}
	count := int(body[messageEnd])
	if count == 0 || count > currentItemHyperlinkItemLimit {
		return currentItemHyperlinkRequest{}, errCurrentItemHyperlinkBodyInvalid
	}
	itemsStart := messageEnd + 1
	itemsEnd := itemsStart + count*currentItemHyperlinkItemSize
	if itemsEnd < itemsStart || itemsEnd != len(body) {
		return currentItemHyperlinkRequest{}, errCurrentItemHyperlinkBodyInvalid
	}
	request.Items = make([][]byte, 0, count)
	for offset := itemsStart; offset < itemsEnd; offset += currentItemHyperlinkItemSize {
		request.Items = append(request.Items, append([]byte(nil), body[offset:offset+currentItemHyperlinkItemSize]...))
	}
	return request, nil
}

// Current class0/370 callback 0x2A042F0 reads type(u8), sender(u16),
// channel(u8), the original message DSTR, count(u8), N*148 item bytes, then
// an empty DSTR. 0x29EFE20 consumes the fixed item rows.
func buildCurrentItemHyperlinkNoticeBody(channel byte, senderID uint16, message []byte, items [][]byte) []byte {
	var writer packetWriter
	writer.writeByte(currentAreaChatMessageType)
	writer.writeUint16(senderID)
	writer.writeByte(channel)
	writer.writeRawDstr(message)
	writer.writeByte(byte(len(items)))
	for _, item := range items {
		writer.writeBytes(item)
	}
	writer.writeRawDstr(nil)
	return writer.bytes()
}

func (s *Service) handleCurrentItemHyperlinkMessage(session *gameSession, typ uint16, body []byte) error {
	request, err := decodeCurrentItemHyperlinkRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-item-hyperlink-request-rejected", "body_len", len(body), "error", err)
		return s.sendGameUpperFailure(session, typ, 1)
	}
	if request.MessageType == currentGuildChatType {
		return s.handleCurrentGuildChatMessage(session, typ, currentPeerChatRequest{
			MessageType: currentGuildChatType, Message: request.Message,
		}, request.Items)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		s.logGameEvent(session, "game-item-hyperlink-request-rejected", "reason", "current_bound_character_required")
		return s.sendGameUpperFailure(session, typ, 1)
	}
	sourceID := identity.character
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repositories, available := s.repositoryGroup()
	if !available || repositories.Character == nil || strings.TrimSpace(s.accountIDForSession(session)) == "" {
		return s.sendGameUpperFailure(session, typ, 1)
	}
	character, found, err := repositories.Character.Load(ctx, strconv.Itoa(int(sourceID)))
	if err != nil || !found || strings.TrimSpace(character.AccountID) != strings.TrimSpace(s.accountIDForSession(session)) ||
		!s.boundGameSessionCharacterCurrent(identity) {
		s.logGameEvent(session, "game-item-hyperlink-request-rejected", "source_char_id", sourceID,
			"reason", "bound_character_owner_mismatch")
		return s.sendGameUpperFailure(session, typ, 1)
	}
	if err := s.sendGameUpperSuccess(session, typ, nil); err != nil {
		return err
	}
	recipients := []*gameSession{session}
	recipients = append(recipients, s.currentAreaChatPeerSessions(session, sourceID)...)
	sent := 0
	for _, recipient := range recipients {
		recipientIdentity, current := s.boundGameSessionCharacterSnapshot(recipient)
		if !current || recipientIdentity.character != selectedCharacterID(recipient) {
			continue
		}
		channel, channelOK := currentSessionOwnerChannelField(recipient)
		if !channelOK {
			continue
		}
		notice := buildCurrentItemHyperlinkNoticeBody(channel, sourceID, request.Message, request.Items)
		if err := s.sendGameUpperRawClassCodec(recipient, currentItemHyperlinkNotifyOpcode, notice, 0, true); err != nil {
			if recipient == session {
				return err
			}
			s.logWarn("dnfbridge item hyperlink peer send failed", "source_char_id", sourceID,
				"target_char_id", recipientIdentity.character, "error", err)
			continue
		}
		sent++
	}
	s.logGameEvent(session, "game-item-hyperlink-forwarded", "source_char_id", sourceID,
		"hint_ignored", request.Hint, "message_len", len(request.Message), "item_count", len(request.Items),
		"recipients", sent, "body_source", "current_exe_cmd424_noti370_type_sender_channel_message_items_empty_dstr")
	return nil
}
