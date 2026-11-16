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
	actor, err := s.currentGuildOwnedSelectedIdentity(session)
	if err != nil {
		return fail(err)
	}
	view, err := s.readCurrentGuildRankingView(ctx)
	if err != nil {
		return fail(err)
	}
	rules, err := s.currentGuildRankingRules()
	if err != nil {
		return fail(err)
	}
	points, err := view.MemberPoints(r.GuildOwner{AccountID: actor.AccountID, CharacterID: actor.CharacterID}, binary.LittleEndian.Uint32(request), request[4], name, rules.MemberRanking, s.gameplayNow())
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
