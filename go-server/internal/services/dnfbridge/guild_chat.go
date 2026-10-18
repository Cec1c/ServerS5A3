package dnfbridge

import (
	"bytes"
	"strconv"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const currentGuildChatType byte = 6

// Matched NOTI65 / RVA 0x1c1b510 reads two additional display bytes for
// type 6, followed by sender DSTR, channel u8, message DSTR, auxiliary DSTR.
// No authoritative decoration awards exist yet: both display bytes stay zero.
// Unlike NOTI12, this route does not need the sender in the recipient's scene.
func buildCurrentGuildChatBody(channel byte, sender []byte, message []byte) []byte {
	var w packetWriter
	w.writeBytes([]byte{currentGuildChatType, 0, 0, 0})
	w.writeRawDstr(sender)
	w.writeByte(channel)
	w.writeRawDstr(message)
	w.writeRawDstr(nil)
	return w.bytes()
}

func (s *Service) handleCurrentGuildChat(session *gameSession, op uint16, request currentPeerChatRequest) error {
	reject := func() error { return s.sendGameUpperFailure(session, op, 1) }
	if request.MessageType != currentGuildChatType || request.Unknown != 0 ||
		len(request.TargetName) != 0 || request.HasTrailingValue ||
		len(request.Message) == 0 || len(request.Message) > 255 || bytes.IndexByte(request.Message, 0) >= 0 {
		return reject()
	}
	source, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return reject()
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	repos, registry, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil || repos.Character == nil {
		return reject()
	}
	sourceID := strconv.Itoa(int(source.character))
	g, _, found := registry.GuildForCharacter(sourceID)
	if !found {
		return reject()
	}
	identities := []boundGameSessionCharacter{source}
	for id := range g.Members {
		value, err := strconv.ParseUint(id, 10, 16)
		if err != nil || value == 0 || uint16(value) == source.character {
			continue
		}
		if identity, online := s.onlineGameSessionCharacterSnapshot(uint16(value)); online {
			identities = append(identities, identity)
		}
	}
	// Reserve the entire participant set in ascending character order once.
	// Acquiring a sender first and then its peers can deadlock simultaneous sends.
	// Reservations prevent reconnect/character selection during socket delivery.
	release, err := s.reserveCurrentGuildParticipants(ctx, identities)
	if err != nil {
		return reject()
	}
	defer release()
	character, err := s.currentGuildOwnedSelectedCharacter(session)
	if err != nil {
		return reject()
	}
	_, registry, err = s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return reject()
	}
	current, member, found := registry.GuildForCharacter(sourceID)
	if !found || current.ID != g.ID || member.AccountID != character.AccountID {
		return reject()
	}
	name, encodeErr := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(character.Name))
	channel, channelOK := currentSessionOwnerChannelField(session)
	if encodeErr != nil || len(name) == 0 || len(name) > 30 || bytes.IndexByte(name, 0) >= 0 || !channelOK {
		return reject()
	}
	// The request's u16 comes from a client-side actor/target hint. It is not
	// authority for sender identity or recipients; use the bound session only.
	recipients := []*gameSession{session}
	for _, identity := range identities[1:] {
		id := strconv.Itoa(int(identity.character))
		peerGuild, peerMember, belongs := registry.GuildForCharacter(id)
		if !belongs || peerGuild.ID != current.ID {
			continue
		}
		peer, exists, loadErr := repos.Character.Load(ctx, id)
		if loadErr != nil || !exists || peer.AccountID != peerMember.AccountID ||
			peer.AccountID != s.accountIDForSession(identity.session) || !s.boundGameSessionCharacterCurrent(identity) {
			continue
		}
		recipients = append(recipients, identity.session)
	}
	if err := s.sendGameUpperSuccess(session, op, nil); err != nil {
		return err
	}
	notice := buildCurrentGuildChatBody(channel, name, request.Message)
	for _, recipient := range recipients {
		if err := s.sendGameUpperRawClassCodec(recipient, currentOfficialWhisperNotifyOpcode, notice, 0, true); err != nil {
			if recipient == session {
				return err
			}
			s.logWarn("dnfbridge guild chat peer send failed", "source_char_id", source.character,
				"target_char_id", selectedCharacterID(recipient), "error", err)
		}
	}
	s.logGameEvent(session, "game-guild-chat-forwarded", "guild_id", current.ID,
		"source_char_id", source.character, "message_len", len(request.Message), "recipients", len(recipients))
	return nil
}
