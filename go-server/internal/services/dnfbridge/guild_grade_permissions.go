package dnfbridge

import (
	"encoding/binary"
	"errors"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) handleCurrentGuildGradePermissions(session *gameSession, body []byte) error {
	op := uint16(dnfenum.CmdPacketChangeGuildGrade)
	if len(body) != 5 {
		return s.sendGameUpperFailure(session, op, 48)
	}
	grade, mask := body[0], binary.LittleEndian.Uint32(body[1:])
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
		g, err := r.SetGradePermissions(dnfrepo.GuildOwner{CharacterID: actor.CharacterID, AccountID: actor.AccountID}, grade, mask, s.gameplayNow())
		if err == nil {
			guildID = g.ID
		}
		return err
	})
	code := currentGuildTextError(err)
	if errors.Is(err, dnfguild.ErrInvalidGradePermissions) {
		code = 48 // matched class1 callback recognizes this rejection
	}
	return s.finishCurrentGuildEdit(ctx, source, guildID, op, true, err, code)
}
