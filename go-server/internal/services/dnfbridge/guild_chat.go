package dnfbridge

import (
	"bytes"
	"context"
	"strconv"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/guild"
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
	return s.handleCurrentGuildChatMessage(session, op, request, nil)
}

// Text and item links share authoritative membership, contribution and bounded
// delivery. Item rows have already passed the op424 fixed-size decoder.
func (s *Service) handleCurrentGuildChatMessage(session *gameSession, op uint16, request currentPeerChatRequest, items [][]byte) error {
	reject := func(reason string, cause error) error {
		connID := ""
		if session != nil {
			connID = session.connID
		}
		s.logWarn("dnfbridge guild chat rejected", "conn_id", connID, "reason", reason, "error", cause)
		ctx, cancel := s.currentGuildContext()
		defer cancel()
		return s.sendCurrentGuildChatFrame(ctx, session, op, []byte{0, 1}, 1)
	}
	if request.MessageType != currentGuildChatType || request.Unknown != 0 ||
		len(request.TargetName) != 0 || request.HasTrailingValue ||
		len(request.Message) == 0 || len(request.Message) > 255 || bytes.IndexByte(request.Message, 0) >= 0 {
		return reject("invalid_request", nil)
	}
	source, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return reject("sender_binding", nil)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	repos, registry, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil || repos.Character == nil {
		return reject("guild_snapshot", err)
	}
	sourceID := strconv.Itoa(int(source.character))
	g, _, found := registry.GuildForCharacter(sourceID)
	if !found {
		return reject("sender_not_member", nil)
	}
	// Only the sender's transaction and echo need this reservation. Peer
	// delivery runs on each recipient's bounded session queue after release.
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{source})
	if err != nil {
		return reject("sender_reservation", err)
	}
	defer release()
	character, err := s.currentGuildOwnedSelectedCharacter(session)
	if err != nil {
		return reject("sender_ownership", err)
	}
	_, registry, err = s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return reject("guild_revalidation", err)
	}
	current, member, found := registry.GuildForCharacter(sourceID)
	if !found || current.ID != g.ID || member.AccountID != character.AccountID {
		return reject("sender_membership_changed", nil)
	}
	name, encodeErr := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(character.Name))
	channel, channelOK := currentSessionOwnerChannelField(session)
	if encodeErr != nil || len(name) == 0 || len(name) > 30 || bytes.IndexByte(name, 0) >= 0 || !channelOK {
		return reject("sender_display", encodeErr)
	}
	// The request's u16 comes from a client-side actor/target hint. It is not
	// authority for sender identity or recipients; use the bound session only.
	var recipients []currentGuildChatRecipient
	for id, member := range current.Members {
		value, parseErr := strconv.ParseUint(id, 10, 16)
		if parseErr != nil || value == 0 || uint16(value) == source.character {
			continue
		}
		if identity, online := s.onlineGameSessionCharacterSnapshot(uint16(value)); online {
			recipients = append(recipients, currentGuildChatRecipient{identity: identity, account: member.AccountID, joinedAt: member.JoinedAt})
		}
	}
	income, contributionErr := s.grantCurrentGuildContributionReserved(ctx, session, source, guild.ContributionChat)
	if contributionErr != nil {
		s.logGameEvent(session, "game-guild-contribution-chat-deferred", "error", contributionErr)
	}
	writeCtx, writeCancel := guildChatContext(session)
	defer writeCancel()
	if err := s.sendCurrentGuildChatFrame(writeCtx, session, op, []byte{1}, 1); err != nil {
		return err
	}
	notice := buildCurrentGuildChatBody(channel, name, request.Message)
	noticeOpcode := currentOfficialWhisperNotifyOpcode
	if len(items) > 0 {
		noticeOpcode = currentGuildItemHyperlinkNotifyOpcode
		notice = buildCurrentGuildItemHyperlinkNoticeBody(channel, name, request.Message, items)
	}
	if err := s.sendCurrentGuildChatFrame(writeCtx, session, noticeOpcode, notice, 0); err != nil {
		return err
	}
	release()
	for _, recipient := range recipients {
		if err := s.queueCurrentGuildChatNotice(recipient, current.ID, noticeOpcode, notice); err != nil {
			s.logWarn("dnfbridge guild chat recipient unavailable", "source_char_id", source.character,
				"target_char_id", recipient.identity.character, "stage", "enqueue", "error", err)
		}
	}
	s.logGameEvent(session, "game-guild-chat-forwarded", "guild_id", current.ID,
		"source_char_id", source.character, "message_len", len(request.Message), "recipient_candidates", len(recipients)+1)
	if contributionErr == nil {
		if err := s.projectCurrentGuildChatContribution(source, income); err != nil {
			s.logGameEvent(session, "game-guild-contribution-chat-projection-deferred", "error", err)
		}
	}
	return nil
}

// Each local delivery has its own budget and follows connection cancellation.
func guildChatContext(session *gameSession) (context.Context, context.CancelFunc) {
	ctx := session.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, createWriteTimeout)
}
