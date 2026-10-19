package dnfbridge

import (
	"errors"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func currentGuildMemberGradeError(err error) byte {
	switch {
	case errors.Is(err, dnfguild.ErrPermissionDenied):
		return 83
	case errors.Is(err, dnfguild.ErrMemberGradeSelf):
		return 85
	case errors.Is(err, dnfguild.ErrMemberGradeMaster):
		return 87
	case errors.Is(err, dnfguild.ErrInvalidMemberGrade):
		return 88
	default:
		return 3
	}
}

func (s *Service) handleCurrentGuildMemberGrade(session *gameSession, body []byte) error {
	op := uint16(dnfenum.CmdPacketSetSubGuildMaster)
	if len(body) < 7 || body[0] == 0 || body[0] >= 250 {
		return s.sendGameUpperFailure(session, op, 3)
	}
	name, valid := parseCurrentGuildText(body[1:len(body)-1], rosterNameMaxBytes)
	if !valid || name == "" {
		return s.sendGameUpperFailure(session, op, 3)
	}
	region, grade := body[0], body[len(body)-1]
	source, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return s.sendGameUpperFailure(session, op, 3)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	var guildID uint32
	err := func() error {
		repos, ok := s.repositoryGroup()
		if !ok {
			return dnfguild.ErrCharacterNotFound
		}
		id, found, err := repos.Character.FindIDByName(ctx, name)
		if err != nil || !found {
			return dnfguild.ErrCharacterNotFound
		}
		target, found, err := repos.Character.Load(ctx, id)
		if err != nil || !found {
			return dnfguild.ErrCharacterNotFound
		}
		owner := dnfrepo.GuildOwner{CharacterID: id, AccountID: target.AccountID}
		return s.mutateCurrentGuildForSession(ctx, session, &owner, func(tx dnfrepo.Group, r *dnfguild.Registry, actor dnfrepo.CharacterRecord) error {
			if !s.boundGameSessionCharacterCurrent(source) {
				return dnfguild.ErrCharacterNotFound
			}
			current, exists, loadErr := tx.Character.Load(ctx, id)
			if loadErr != nil {
				return loadErr
			}
			if !exists || current.CharacterID != id || current.AccountID != owner.AccountID || current.Name != name {
				return dnfguild.ErrCharacterNotFound
			}
			group, groupErr := dnfguild.CharacterServerGroup(current)
			_, member, memberFound := r.GuildForOwner(owner)
			if groupErr != nil || group != region || !memberFound || member.ServerGroup != region || member.Name != name {
				return dnfguild.ErrCharacterNotFound
			}
			g, changeErr := r.ChangeMemberGrade(dnfrepo.GuildOwner{CharacterID: actor.CharacterID, AccountID: actor.AccountID}, owner, grade, s.gameplayNow())
			if changeErr == nil {
				guildID = g.ID
			}
			return changeErr
		})
	}()
	// Release the sender before refreshing peers, and never deliver an ACK to
	// a new character generation. The class1 callback consumes the extra byte.
	release, reserveErr := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{source})
	var sendErr error
	if reserveErr == nil {
		if err != nil {
			sendErr = s.sendGameUpperFailure(session, op, currentGuildMemberGradeError(err))
		} else {
			var w packetWriter
			w.writeByte(0)
			w.writeByte(region)
			writeClientNameDstr(&w, name)
			w.writeByte(grade)
			sendErr = s.sendGameUpperSuccess(session, op, w.bytes())
		}
		release()
	}
	if err == nil {
		s.refreshCurrentGuildText(ctx, guildID, source, true)
		s.refreshCurrentGuildText(ctx, guildID, source, false)
	}
	return sendErr
}
