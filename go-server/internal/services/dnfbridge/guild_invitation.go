package dnfbridge

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentGuildInvitationLifetime = 15 * time.Second

var (
	errCurrentGuildInvitationChannel = errors.New("guild invitation participants changed channels")
	errCurrentGuildInvitationExpired = errors.New("guild invitation expired before transaction")
)

// All ordinary and creation consent state is protected by guildCreateMu.
// Lock order: consent mutex, ordered character reservations, Guild UoW.
// Pending consent is process-local and is never restored after a restart.
type currentGuildInvitation struct {
	inviter, target           boundGameSessionCharacter
	inviterOwner, targetOwner dnfrepo.GuildOwner
	guildID                   uint32
	targetName                string
	expiresAt                 time.Time
	timerName                 string
	expired                   bool
}

func (s *Service) clearCurrentGuildInvitation(invite *currentGuildInvitation, keepExpired bool) {
	s.ensureGameplayTimeQueue().Cancel(invite.timerName)
	delete(invite.inviter.session.guild.outgoingInvitations, invite)
	if invite.target.session.guild.incomingInvitation == invite {
		if keepExpired {
			// There is no nonce in op152. Drain the old dialog's response before
			// offering a replacement on the same session after a server timeout.
			invite.expired = true
		} else {
			invite.target.session.guild.incomingInvitation = nil
		}
	}
}

// Call only after releasing any participant reservations.
func (s *Service) notifyCurrentGuildInvitation(invite *currentGuildInvitation, result byte) error {
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{invite.inviter})
	if err != nil {
		return nil
	}
	defer release()
	if s.accountIDForSession(invite.inviter.session) != invite.inviterOwner.AccountID {
		return nil
	}
	return s.sendGameUpperSecondaryRaw(invite.inviter.session, currentGuildInviteResult, buildCurrentGuildInvitationResult(result, invite.targetName))
}

func (s *Service) expireCurrentGuildInvitation(invite *currentGuildInvitation) {
	s.guildCreateMu.Lock()
	defer s.guildCreateMu.Unlock()
	if invite.target.session.guild.incomingInvitation != invite || invite.expired {
		return
	}
	s.clearCurrentGuildInvitation(invite, true)
	if err := s.notifyCurrentGuildInvitation(invite, 0); err != nil {
		s.logGameEvent(invite.inviter.session, "game-guild-invitation-expiry-notify-failed", "error", err)
	}
}

func (s *Service) closeCurrentGuildInvitationsLocked(session *gameSession) {
	if invite := session.guild.incomingInvitation; invite != nil {
		wasExpired := invite.expired
		s.clearCurrentGuildInvitation(invite, false)
		if !wasExpired {
			if err := s.notifyCurrentGuildInvitation(invite, 0); err != nil {
				s.logGameEvent(session, "game-guild-invitation-close-notify-failed", "error", err)
			}
		}
	}
	for invite := range session.guild.outgoingInvitations {
		// No verified cancellation notification exists for the target dialog.
		// Its next reply is consumed as stale; it cannot join via a new socket.
		s.clearCurrentGuildInvitation(invite, true)
	}
}

func (s *Service) handleCurrentGuildInvitation(session *gameSession, body []byte) error {
	s.guildCreateMu.Lock()
	defer s.guildCreateMu.Unlock()
	region, name, valid := parseCurrentGuildInvitation(body)
	if !valid || int(region) != s.channelAdvertiseID() {
		return s.sendGameUpperFailure(session, currentGuildInviteOpcode, 3)
	}
	source, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return s.sendGameUpperFailure(session, currentGuildInviteOpcode, 34)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	reject := func(code byte) error {
		release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{source})
		if err != nil {
			return nil
		}
		defer release()
		return s.sendGameUpperFailure(session, currentGuildInviteOpcode, code)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return reject(23)
	}
	id, found, err := repos.Character.FindIDByName(ctx, name)
	if err != nil || !found {
		return reject(23)
	}
	numericID, err := strconv.ParseUint(id, 10, 16)
	if err != nil || numericID == 0 || numericID == uint64(source.character) {
		return reject(23)
	}
	target, online := s.onlineGameSessionCharacterSnapshot(uint16(numericID))
	if !online {
		return reject(3)
	}
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{source, target})
	if err != nil {
		return nil
	}
	defer release()
	if session.channel.ID != target.session.channel.ID {
		return s.sendGameUpperFailure(session, currentGuildInviteOpcode, 3)
	}
	if session.guild.createInvitation != nil || target.session.guild.createInvitation != nil || target.session.guild.incomingInvitation != nil {
		return s.sendGameUpperFailure(session, currentGuildInviteOpcode, 23)
	}
	actorOwner := dnfrepo.GuildOwner{CharacterID: strconv.Itoa(int(source.character)), AccountID: s.accountIDForSession(session)}
	targetOwner := dnfrepo.GuildOwner{CharacterID: id, AccountID: s.accountIDForSession(target.session)}
	actor, actorFound, err := repos.Character.Load(ctx, actorOwner.CharacterID)
	if err != nil || !actorFound || actor.AccountID != actorOwner.AccountID {
		return s.sendGameUpperFailure(session, currentGuildInviteOpcode, 34)
	}
	character, found, err := repos.Character.Load(ctx, id)
	if err != nil || !found || character.CharacterID != id || character.AccountID != targetOwner.AccountID || character.Name != name {
		return s.sendGameUpperFailure(session, currentGuildInviteOpcode, 23)
	}
	r, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		return s.sendGameUpperFailure(session, currentGuildInviteOpcode, 23)
	}
	g, _, found := r.GuildForOwner(actorOwner)
	if !found {
		return s.sendGameUpperFailure(session, currentGuildInviteOpcode, 34)
	}
	g, err = r.CheckInvitation(g.ID, actorOwner, character)
	if err != nil {
		return s.sendGameUpperFailure(session, currentGuildInviteOpcode, currentGuildInvitationError(err))
	}
	payload, err := buildCurrentGuildInvitationBody(g, actor.Name)
	if err != nil {
		return s.sendGameUpperFailure(session, currentGuildInviteOpcode, 23)
	}
	invite := &currentGuildInvitation{inviter: source, target: target, inviterOwner: actorOwner, targetOwner: targetOwner, guildID: g.ID, targetName: name, expiresAt: s.gameplayNow().Add(currentGuildInvitationLifetime)}
	invite.timerName = fmt.Sprintf("guild-invitation:%p", invite)
	if session.guild.outgoingInvitations == nil {
		session.guild.outgoingInvitations = make(map[*currentGuildInvitation]struct{})
	}
	session.guild.outgoingInvitations[invite] = struct{}{}
	target.session.guild.incomingInvitation = invite
	if err := s.ensureGameplayTimeQueue().ScheduleAfter(invite.timerName, currentGuildInvitationLifetime, func(time.Time) { s.expireCurrentGuildInvitation(invite) }); err != nil {
		s.clearCurrentGuildInvitation(invite, false)
		return s.sendGameUpperFailure(session, currentGuildInviteOpcode, 23)
	}
	if err := s.sendGameUpperSuccess(session, currentGuildInviteOpcode, nil); err != nil {
		s.clearCurrentGuildInvitation(invite, false)
		return err
	}
	if err := s.sendGameUpperSecondaryRaw(target.session, currentGuildInviteNotification, payload); err != nil {
		s.clearCurrentGuildInvitation(invite, false)
		return err
	}
	return nil
}

func (s *Service) handleCurrentGuildInvitationReply(session *gameSession, body []byte) error {
	s.guildCreateMu.Lock()
	var joinedGuildID uint32
	var joinedSource boundGameSessionCharacter
	// Release consent/participant locks before reserving other guild members.
	defer func() {
		s.guildCreateMu.Unlock()
		if joinedGuildID != 0 {
			ctx, cancel := s.currentGuildContext()
			defer cancel()
			s.refreshCurrentGuildText(ctx, joinedGuildID, joinedSource, true)
			s.refreshCurrentGuildText(ctx, joinedGuildID, joinedSource, false)
		}
	}()
	if len(body) != 1 || body[0] > 1 {
		return s.sendGameUpperFailure(session, currentGuildInviteReplyOpcode, 103)
	}
	invite := session.guild.incomingInvitation
	if invite == nil {
		if body[0] == 0 {
			return nil
		}
		return s.sendGameUpperFailure(session, currentGuildInviteReplyOpcode, 103)
	}
	if !s.boundGameSessionCharacterCurrent(invite.target) {
		wasExpired := invite.expired
		s.clearCurrentGuildInvitation(invite, false)
		if wasExpired {
			return nil
		}
		return s.notifyCurrentGuildInvitation(invite, 0)
	}
	if invite.expired || !s.gameplayNow().Before(invite.expiresAt) || !s.boundGameSessionCharacterCurrent(invite.inviter) || !s.boundGameSessionCharacterCurrent(invite.target) {
		alreadyExpired := invite.expired
		s.clearCurrentGuildInvitation(invite, false)
		if !alreadyExpired {
			_ = s.notifyCurrentGuildInvitation(invite, 0)
		}
		if body[0] == 0 {
			return nil
		}
		return s.sendGameUpperFailure(session, currentGuildInviteReplyOpcode, 103)
	}
	if body[0] == 0 {
		s.clearCurrentGuildInvitation(invite, false)
		// Native success152 says "joined". A refusal has no success ACK.
		return s.notifyCurrentGuildInvitation(invite, 0)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	var committed *dnfguild.Guild
	var sendErr error
	err := func() error {
		release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{invite.inviter, invite.target})
		if err != nil {
			return err
		}
		defer release()
		repos, ok := s.repositoryGroup()
		if !ok {
			return dnfguild.ErrRepositoryUnavailable
		}
		err = dnfguild.Update(ctx, repos.Guild, []dnfrepo.GuildOwner{invite.inviterOwner, invite.targetOwner}, s.gameplayNow(), func(tx dnfrepo.Group, r *dnfguild.Registry) error {
			if !s.boundGameSessionCharacterCurrent(invite.inviter) || !s.boundGameSessionCharacterCurrent(invite.target) {
				return dnfguild.ErrCharacterNotFound
			}
			if !s.gameplayNow().Before(invite.expiresAt) {
				return errCurrentGuildInvitationExpired
			}
			if session.channel.ID != invite.inviter.session.channel.ID {
				return errCurrentGuildInvitationChannel
			}
			if s.accountIDForSession(invite.inviter.session) != invite.inviterOwner.AccountID || s.accountIDForSession(session) != invite.targetOwner.AccountID {
				return dnfguild.ErrCharacterNotFound
			}
			if session.guild.createInvitation != nil || invite.inviter.session.guild.createInvitation != nil {
				return dnfguild.ErrPermissionDenied
			}
			target, found, err := tx.Character.Load(ctx, invite.targetOwner.CharacterID)
			if err != nil {
				return err
			}
			if !found || target.AccountID != invite.targetOwner.AccountID || target.Name != invite.targetName {
				return dnfguild.ErrCharacterNotFound
			}
			committed, err = r.AcceptInvitation(invite.guildID, invite.inviterOwner, target, s.gameplayNow())
			return err
		})
		if err == nil {
			joinedGuildID, joinedSource = invite.guildID, invite.target
			sendErr = s.sendGameUpperSuccess(session, currentGuildInviteReplyOpcode, nil)
			if sendErr == nil {
				sendErr = s.refreshCurrentGuildMembership(ctx, session, committed)
			}
		} else {
			// 152 displays native generic string838 for codes without a specific
			// branch (including permission36); do not mislabel them "no guild".
			code := currentGuildInvitationError(err)
			sendErr = s.sendGameUpperFailure(session, currentGuildInviteReplyOpcode, code)
		}
		return err
	}()
	s.clearCurrentGuildInvitation(invite, false)
	if err != nil {
		s.logGameEvent(session, "game-guild-invitation-accept-rejected", "error", err)
		return errors.Join(sendErr, s.notifyCurrentGuildInvitation(invite, currentGuildInvitationError(err)))
	}
	return errors.Join(sendErr, s.notifyCurrentGuildInvitation(invite, 1))
}
