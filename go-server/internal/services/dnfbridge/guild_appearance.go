package dnfbridge

import (
	"encoding/binary"
	"errors"
	"strconv"

	guild "longheng.io/server/internal/modules/dnf/guild"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const currentGuildMarkOpcode uint16 = 787
const currentGuildAuraOpcode uint16 = 832

func (s *Service) handleCurrentGuildMark(session *gameSession, body []byte) error {
	return s.handleCurrentGuildAppearance(session, currentGuildMarkOpcode, body)
}

func (s *Service) handleCurrentGuildAura(session *gameSession, body []byte) error {
	return s.handleCurrentGuildAppearance(session, currentGuildAuraOpcode, body)
}

func (s *Service) handleCurrentGuildAppearance(session *gameSession, op uint16, body []byte) error {
	// Class0/832 is a different native notification. Do not use it as an ACK.
	fail := func(code byte) error {
		if op == currentGuildMarkOpcode {
			return s.sendGameUpperFailure(session, op, code)
		}
		s.logGameEvent(session, "game-guild-appearance-rejected", "opcode", op, "code", code)
		return nil
	}
	expected := 1
	if op == currentGuildAuraOpcode {
		expected = 8
	}
	if len(body) != expected {
		return fail(34)
	}
	source, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return fail(34)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return fail(34)
	}
	appearance, err := guild.LoadAppearanceCatalog(catalog.source)
	if err != nil {
		return fail(34)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{source})
	if err != nil {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		release()
		return fail(34)
	}
	change := guild.AppearanceChange{Owner: repo.GuildOwner{CharacterID: strconv.Itoa(int(source.character)), AccountID: s.accountIDForSession(session)}, Mark: body[0], Aura: op == currentGuildAuraOpcode, Revalidate: func() bool { return s.boundGameSessionCharacterCurrent(source) }}
	if change.Aura {
		change.AuraMark, change.AuraFrame = binary.LittleEndian.Uint32(body), binary.LittleEndian.Uint32(body[4:])
	}
	guildID, err := guild.ChangeAppearance(ctx, repos.Guild, change, appearance, s.gameplayNow())
	release()
	if op == currentGuildMarkOpcode {
		code := currentGuildTextError(err)
		if errors.Is(err, guild.ErrInsufficientMileage) {
			code = 22
		}
		return s.finishCurrentGuildEdit(ctx, source, guildID, op, true, err, code)
	}
	if err != nil {
		return fail(currentGuildTextError(err))
	}
	// Native 832 prompts locally before sending; NOTI70 replaces both cached
	// indices from committed state. No independent class1/832 callback is used.
	s.refreshCurrentGuildText(ctx, guildID, source, true)
	return nil
}
