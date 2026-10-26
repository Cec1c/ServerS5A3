package dnfbridge

import (
	"encoding/binary"
	"math"

	"longheng.io/server/internal/modules/dnf/guild"
)

const currentGuildRankingBoardOpcode uint16 = 1504

func (s *Service) handleCurrentGuildRankingBoard(session *gameSession, request []byte) error {
	fail := func(err error) error {
		s.logGameEvent(session, "game-guild-ranking-board-rejected", "error", err)
		return s.sendGameUpperFailure(session, currentGuildRankingBoardOpcode, 1)
	}
	peer, bound := s.boundGameSessionCharacterSnapshot(session)
	// Only the verified default monthly list and hall initialization contract
	// are enabled. Other dropdown categories need their own score semantics.
	if !s.options.guildRankingEnabled || !bound || peer.generation == 0 || len(request) != 4 || request[0] > 1 || request[1] != 0 {
		return fail(guild.ErrRankingRules)
	}
	page := binary.LittleEndian.Uint16(request[2:])
	if page == 0 || request[0] == 1 && page != 1 {
		return fail(guild.ErrRankingRules)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{peer})
	if err != nil {
		return fail(err)
	}
	defer release()
	if _, err = s.currentGuildOwnedSelectedCharacter(session); err != nil {
		return fail(err)
	}
	_, reg, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return fail(err)
	}
	rows, err := reg.MonthlyRankingBoard(s.gameplayNow(), request[0] == 1, page)
	if err != nil {
		return fail(err)
	}
	if !s.boundGameSessionCharacterCurrent(peer) {
		return fail(guild.ErrCharacterNotFound)
	}
	body := buildCurrentGuildRankingBoardBody(request[0], rows)
	return s.sendGameUpperSuccess(session, currentGuildRankingBoardOpcode, body)
}

func buildCurrentGuildRankingBoardBody(kind byte, rows []guild.RankingBoardEntry) []byte {
	body := make([]byte, 5+72*len(rows))
	body[0] = kind
	// Native 01FB5A70 reads but never consumes the header WORD. Keep zero.
	binary.LittleEndian.PutUint16(body[3:], uint16(len(rows)))
	for i, entry := range rows {
		row := body[5+72*i : 5+72*(i+1)]
		g := entry.Guild
		binary.LittleEndian.PutUint32(row, g.ID)
		binary.LittleEndian.PutUint32(row[4:], entry.Rank)
		binary.LittleEndian.PutUint32(row[8:], math.Float32bits(entry.Score))
		// Bytes 12/13 and trailing 70/71 retain the native zero initializer;
		// no application/region policy is inferred from these opaque fields.
		row[14] = g.Mark
		writeGuildRankingBoardName(row[15:38], g.Name)
		writeGuildRankingBoardName(row[38:68], g.MasterName)
		binary.LittleEndian.PutUint16(row[68:], uint16(len(g.Members)))
	}
	return body
}

func writeGuildRankingBoardName(dst []byte, name string) {
	pos := 0
	for _, ch := range name {
		encoded := rosterNameBytes(string(ch))
		if ch == 0 || pos+len(encoded) >= len(dst) {
			break
		}
		copy(dst[pos:], encoded)
		pos += len(encoded)
	}
}
