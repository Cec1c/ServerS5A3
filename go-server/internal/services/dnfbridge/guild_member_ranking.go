package dnfbridge

import (
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	"math"
)

const currentGuildMemberRankingOpcode uint16 = 1505

func (s *Service) handleCurrentGuildMemberRanking(session *gameSession, request []byte) error {
	fail := func(err error) error {
		s.logGameEvent(session, "game-guild-member-ranking-rejected", "error", err)
		return s.sendGameUpperFailure(session, currentGuildMemberRankingOpcode, 100)
	}
	peer, bound := s.boundGameSessionCharacterSnapshot(session)
	if !s.options.guildRankingEnabled || !bound || peer.generation == 0 || len(request) < 7 {
		return fail(guild.ErrCharacterNotFound)
	}
	name, end, ok := parseCurrentGuildDSTR(request, 5)
	if !ok || end != len(request) || name == "" {
		return fail(guild.ErrInvalidName)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{peer})
	if err != nil {
		return fail(err)
	}
	defer release()
	actor, err := s.currentGuildOwnedSelectedCharacter(session)
	if err != nil {
		return fail(err)
	}
	_, reg, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return fail(err)
	}
	g, _, found := reg.GuildForOwner(r.GuildOwner{AccountID: actor.AccountID, CharacterID: actor.CharacterID})
	if !found || g.ID != binary.LittleEndian.Uint32(request) {
		return fail(guild.ErrNotMember)
	}
	var target guild.Member
	for _, m := range g.Members {
		if m.ServerGroup == request[4] && m.Name == name {
			if target.CharacterID != "" {
				return fail(guild.ErrInvalidRegistry)
			}
			target = m
		}
	}
	if target.CharacterID == "" {
		return fail(guild.ErrCharacterNotFound)
	}
	rules, err := s.currentGuildRankingRules()
	if err != nil {
		return fail(err)
	}
	points, err := guild.MemberRankingPoints(target, rules.MemberRanking, s.gameplayNow())
	if err != nil {
		return fail(err)
	}
	if !s.boundGameSessionCharacterCurrent(peer) {
		return fail(guild.ErrCharacterNotFound)
	}
	body := make([]byte, 16)
	for i, value := range points {
		binary.LittleEndian.PutUint32(body[4+4*i:], math.Float32bits(value))
	}
	return s.sendGameUpperSuccess(session, currentGuildMemberRankingOpcode, body)
}
