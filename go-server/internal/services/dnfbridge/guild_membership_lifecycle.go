package dnfbridge

import (
	"context"
	"errors"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func currentGuildDepartureError(err error) byte {
	switch {
	case errors.Is(err, dnfguild.ErrDisbandHasMembers), errors.Is(err, dnfguild.ErrMemberRemovalMaster):
		return 87
	case errors.Is(err, dnfguild.ErrPermissionDenied):
		return 36
	case errors.Is(err, dnfguild.ErrNotMember):
		return 34
	default:
		return 3
	}
}

func (s *Service) handleCurrentGuildSecede(session *gameSession, body []byte) error {
	op := uint16(dnfenum.CmdPacketReqGuildSecede)
	request, valid := parseCurrentGuildSecedeRequest(body)
	if !valid {
		return s.sendGameUpperFailure(session, op, 3)
	}
	source, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return s.sendGameUpperFailure(session, op, 34)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	var removed dnfguild.MemberRemoval
	err := func() error {
		var target *dnfrepo.GuildOwner
		if request.Name != "" {
			repos, ok := s.repositoryGroup()
			if !ok {
				return dnfguild.ErrRepositoryUnavailable
			}
			id, found, err := repos.Character.FindIDByName(ctx, request.Name)
			if err != nil {
				return err
			}
			if !found {
				return dnfguild.ErrCharacterNotFound
			}
			c, found, err := repos.Character.Load(ctx, id)
			if err != nil {
				return err
			}
			if !found {
				return dnfguild.ErrCharacterNotFound
			}
			target = &dnfrepo.GuildOwner{CharacterID: id, AccountID: c.AccountID}
		}
		return s.mutateCurrentGuildForSession(ctx, session, target, func(tx dnfrepo.Group, reg *dnfguild.Registry, actor dnfrepo.CharacterRecord) error {
			if !s.boundGameSessionCharacterCurrent(source) {
				return dnfguild.ErrCharacterNotFound
			}
			owner := dnfrepo.GuildOwner{CharacterID: actor.CharacterID, AccountID: actor.AccountID}
			selected, c := owner, actor
			if target != nil {
				selected = *target
				var found bool
				var err error
				c, found, err = tx.Character.Load(ctx, target.CharacterID)
				if err != nil {
					return err
				}
				if !found || c.CharacterID != selected.CharacterID || c.AccountID != selected.AccountID || c.Name != request.Name {
					return dnfguild.ErrCharacterNotFound
				}
			}
			group, err := dnfguild.CharacterServerGroup(c)
			_, member, found := reg.GuildForOwner(selected)
			if err != nil || !found || member.ServerGroup != group || member.Name != c.Name {
				return dnfguild.ErrNotMember
			}
			if request.Region != group && !(request.Name == "" && request.Region == 0) {
				return dnfguild.ErrCharacterNotFound
			}
			removed, err = reg.DepartMember(owner, selected, s.gameplayNow())
			return err
		})
	}()
	return s.finishCurrentGuildDeparture(ctx, source, op, removed, err)
}

func (s *Service) handleCurrentGuildDisband(session *gameSession, body []byte) error {
	op := uint16(dnfenum.CmdPacketBreakGuild)
	if len(body) != 0 {
		return s.sendGameUpperFailure(session, op, 3)
	}
	source, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return s.sendGameUpperFailure(session, op, 34)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	var removed dnfguild.MemberRemoval
	err := s.mutateCurrentGuildForSession(ctx, session, nil, func(_ dnfrepo.Group, reg *dnfguild.Registry, actor dnfrepo.CharacterRecord) error {
		if !s.boundGameSessionCharacterCurrent(source) {
			return dnfguild.ErrCharacterNotFound
		}
		var err error
		removed, err = reg.Disband(dnfrepo.GuildOwner{CharacterID: actor.CharacterID, AccountID: actor.AccountID}, s.gameplayNow())
		return err
	})
	return s.finishCurrentGuildDeparture(ctx, source, op, removed, err)
}

func (s *Service) finishCurrentGuildDeparture(ctx context.Context, source boundGameSessionCharacter, op uint16, removed dnfguild.MemberRemoval, mutationErr error) error {
	var sendErr error
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{source})
	if err == nil {
		if mutationErr != nil {
			sendErr = s.sendGameUpperFailure(source.session, op, currentGuildDepartureError(mutationErr))
		} else {
			var body []byte
			if op == uint16(dnfenum.CmdPacketReqGuildSecede) {
				body = buildCurrentGuildSecedeSuccessBody(removed)
			}
			sendErr = s.sendGameUpperSuccess(source.session, op, body)
		}
		release()
	}
	if mutationErr != nil {
		return sendErr
	}
	// The transaction is committed even if the sender disconnected. Notify the
	// current target independently, without holding the sender's reservation.
	if err := s.clearDepartedGuildSession(ctx, removed); err != nil {
		s.logGameEvent(source.session, "game-guild-departure-refresh-failed", "error", err)
		sendErr = errors.Join(sendErr, err)
	}
	s.refreshCurrentGuildText(ctx, removed.GuildID, source, true)
	s.refreshCurrentGuildText(ctx, removed.GuildID, source, false)
	return sendErr
}

func (s *Service) clearDepartedGuildSession(ctx context.Context, removed dnfguild.MemberRemoval) error {
	id, err := strconv.ParseUint(removed.Member.CharacterID, 10, 16)
	if err != nil || id == 0 {
		return nil
	}
	peer, online := s.onlineGameSessionCharacterSnapshot(uint16(id))
	if !online || s.accountIDForSession(peer.session) != removed.Member.AccountID {
		return nil
	}
	// Preserve the creation/invitation lock order: consent, reservation, storage.
	s.guildCreateMu.Lock()
	defer s.guildCreateMu.Unlock()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{peer})
	if err != nil {
		return nil
	}
	defer release()
	_, registry, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return err
	}
	if _, _, joined := registry.GuildForCharacter(removed.Member.CharacterID); joined {
		return nil
	}
	session := peer.session
	s.closeCurrentGuildInvitationsLocked(session)
	if invite := session.guild.createInvitation; invite != nil {
		if err := s.cancelCurrentGuildCreateInvitation(invite, currentGuildCreateAuthResetValue, true); err != nil {
			return err
		}
	}
	session.guild.mu.Lock()
	session.guild.pendingName, session.guild.promotion = "", ""
	session.guild.nameChecked, session.guild.createCommitted, session.guild.memberListRequested = false, false, false
	session.guild.lastCreate, session.guild.soloOperationID = nil, ""
	session.guild.cargoLoadedGuild, session.guild.cargoLoadedGeneration, session.guild.cargoPending = 0, 0, 0
	session.guild.mu.Unlock()
	s.retireCurrentGuildHallPresence(session)
	if err := s.sendGameUpperRawClass(session, 151, nil, 0); err != nil {
		return err
	}
	// NOTI151 clears the native model; the ordinary projection also clears the
	// guild name, grade and cached buff. It never republishes a hall presence.
	return s.refreshCurrentGuildMembership(ctx, session, nil)
}
