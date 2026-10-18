package dnfbridge

import (
	"errors"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) handleCurrentGuildGradeName(session *gameSession, body []byte) error {
	op := uint16(dnfenum.CmdPacketChangeGuildGradeName)
	if len(body) < 5 {
		return s.sendGameUpperFailure(session, op, 159)
	}
	name, ok := parseCurrentGuildText(body[1:], dnfguild.GradeNameMaxBytes)
	if !ok {
		return s.sendGameUpperFailure(session, op, 159)
	}
	source, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return s.sendGameUpperFailure(session, op, 34)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	var guildID uint32
	err := s.mutateCurrentGuildForSession(ctx, session, nil, func(_ dnfrepo.Group, r *dnfguild.Registry, actor dnfrepo.CharacterRecord) error {
		if !s.boundGameSessionCharacterCurrent(source) {
			return dnfguild.ErrCharacterNotFound
		}
		g, err := r.SetGradeName(dnfrepo.GuildOwner{CharacterID: actor.CharacterID, AccountID: actor.AccountID}, body[0], name, s.gameplayNow())
		if err == nil {
			guildID = g.ID
		}
		return err
	})
	code := currentGuildTextError(err)
	if errors.Is(err, dnfguild.ErrInvalidText) {
		code = 159 // class1/749 recognizes invalid text, not the memo error code
	}
	return s.finishCurrentGuildEdit(ctx, source, guildID, op, true, err, code)
}
