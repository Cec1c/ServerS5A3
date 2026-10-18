package dnfbridge

import (
	"encoding/binary"
	"errors"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func parseCurrentGuildSettings(body []byte) (dnfguild.SettingsChange, bool) {
	if len(body) < 14 {
		return dnfguild.SettingsChange{}, false
	}
	n := binary.LittleEndian.Uint32(body[4:8])
	if n > dnfguild.PromotionMaxBytes || uint64(n)+14 != uint64(len(body)) {
		return dnfguild.SettingsChange{}, false
	}
	end := 8 + int(n)
	promotion, ok := parseCurrentGuildText(body[4:end], dnfguild.PromotionMaxBytes)
	if !ok || body[end] > 1 {
		return dnfguild.SettingsChange{}, false
	}
	return dnfguild.SettingsChange{
		Mask: binary.LittleEndian.Uint32(body), Promotion: promotion,
		WebPromotion: body[end] == 1,
		Tags:         binary.LittleEndian.Uint32(body[end+1 : end+5]), JoinMode: body[end+5],
	}, true
}

func (s *Service) handleCurrentGuildSettings(session *gameSession, body []byte) error {
	op := uint16(dnfenum.CmdPacketModifyGuildPromoteMsg)
	change, ok := parseCurrentGuildSettings(body)
	if !ok {
		return s.sendGameUpperFailure(session, op, 108)
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
		g, err := r.SetSettings(dnfrepo.GuildOwner{CharacterID: actor.CharacterID, AccountID: actor.AccountID}, change, s.gameplayNow())
		if err == nil {
			guildID = g.ID
		}
		return err
	})
	code := currentGuildTextError(err)
	if errors.Is(err, dnfguild.ErrPermissionDenied) || errors.Is(err, dnfguild.ErrInvalidSettings) {
		code = 7
	}
	return s.finishCurrentGuildEdit(ctx, source, guildID, op, true, err, code)
}
