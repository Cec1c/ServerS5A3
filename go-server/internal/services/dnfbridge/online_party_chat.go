package dnfbridge

import (
	"bytes"
	"context"
	"strconv"
	"time"

	"longheng.io/server/internal/platform/eventloop"
)

const currentPartyChatType byte = 2

// V179 NOTI65 names the sender without requiring a scene actor. Unlike guild
// type 6, ordinary party type 2 has no two decoration bytes after the result.
func buildCurrentPartyChatBody(channel byte, name, message []byte) []byte {
	var w packetWriter
	w.writeBytes([]byte{currentPartyChatType, 0})
	w.writeRawDstr(name)
	w.writeByte(channel)
	w.writeRawDstr(message)
	w.writeRawDstr(nil)
	return w.bytes()
}

func (s *Service) handleCurrentPartyChat(session *gameSession, op uint16, request currentPeerChatRequest) error {
	ctx, cancel := guildChatContext(session)
	defer cancel()
	reject := func() error { return s.sendCurrentGuildChatFrame(ctx, session, op, []byte{0, 1}, 1) }
	if request.MessageType != currentPartyChatType || request.Unknown != 0 || len(request.TargetName) != 0 ||
		request.HasTrailingValue || len(request.Message) == 0 || len(request.Message) > 255 || bytes.IndexByte(request.Message, 0) >= 0 {
		return reject()
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return reject()
	}
	account := s.accountIDForSession(session)
	reservation, err := s.reserveCurrentCharacterAssets(ctx, identity, account)
	if err != nil {
		return reject()
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	repos, available := s.repositoryGroup()
	if !available || repos.Character == nil {
		return reject()
	}
	actor, found, err := repos.Character.Load(ctx, strconv.Itoa(int(identity.character)))
	if err != nil || !found || account == "" || actor.AccountID != account {
		return reject()
	}
	manager := s.runtimePartyManagerForService()
	state, found := manager.SnapshotByUser(identity.character, 0)
	if !found || len(state.Members) < 2 {
		return reject()
	}
	name := rosterNameBytes(actor.Name)
	channel, validChannel := currentSessionOwnerChannelField(session)
	if !validChannel || len(name) == 0 || len(name) > 30 || bytes.IndexByte(name, 0) >= 0 {
		return reject()
	}
	validate := func() error {
		current, belongs := manager.SnapshotByUser(identity.character, 0)
		if !belongs || current.ID != state.ID || len(current.Members) < 2 || !s.boundGameSessionCharacterCurrent(identity) {
			return errInvalidGameSessionEvent
		}
		return nil
	}
	if err := s.sendCurrentGuildChatFrameChecked(ctx, session, op, []byte{1}, 1, validate); err != nil {
		return err
	}
	body := buildCurrentPartyChatBody(channel, name, request.Message)
	if err := s.sendCurrentGuildChatFrameChecked(ctx, session, currentOfficialWhisperNotifyOpcode, body, 0, validate); err != nil {
		return err
	}
	s.releaseCurrentCharacterAssets(reservation)
	for _, member := range state.Members {
		if member.UserID == identity.character {
			continue
		}
		peer, online := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !online || peer.session.channel.ID != session.channel.ID {
			continue
		}
		if err := s.queueCurrentPartyChat(peer, state.ID, body); err != nil {
			s.logWarn("dnfbridge party chat recipient unavailable", "party_id", state.ID, "target_char_id", member.UserID, "error", err)
		}
	}
	s.logGameEvent(session, "game-party-chat-forwarded", "party_id", state.ID, "source_char_id", identity.character,
		"message_len", len(request.Message), "recipient_candidates", len(state.Members))
	return nil
}

func (s *Service) queueCurrentPartyChat(peer boundGameSessionCharacter, partyID uint16, body []byte) error {
	p := peer.session
	if p == nil || p.loginRevoked.Load() {
		return errInvalidGameSessionEvent
	}
	body = append([]byte(nil), body...)
	expires := time.Now().Add(createWriteTimeout)
	account := s.accountIDForSession(p)
	run := func() error {
		ctx, cancel := guildChatContext(p)
		defer cancel()
		ctx, expireCancel := context.WithDeadline(ctx, expires)
		defer expireCancel()
		reservation, err := s.reserveCurrentCharacterAssets(ctx, peer, account)
		if err != nil {
			return nil
		}
		defer s.releaseCurrentCharacterAssets(reservation)
		validate := func() error {
			current, belongs := s.runtimePartyManagerForService().SnapshotByUser(peer.character, 0)
			if !belongs || current.ID != partyID || !s.boundGameSessionCharacterCurrent(peer) {
				return errInvalidGameSessionEvent
			}
			return nil
		}
		if err := s.sendCurrentGuildChatFrameChecked(ctx, p, currentOfficialWhisperNotifyOpcode, body, 0, validate); err != nil {
			s.logWarn("dnfbridge party chat recipient unavailable", "party_id", partyID, "target_char_id", peer.character, "error", err)
		}
		return nil
	}
	if p.events == nil || p.events.loop == nil {
		return run()
	}
	if !p.events.active.Load() {
		return errInvalidGameSessionEvent
	}
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return p.events.loop.Submit(eventloop.Event{
		Type: "game-party-chat-delivery", Priority: eventloop.PriorityMedium, Context: ctx,
		Data: gameSessionEventTask{source: "game-party-chat-delivery", characterID: peer.character, characterGeneration: peer.generation, async: true, run: run},
	})
}
