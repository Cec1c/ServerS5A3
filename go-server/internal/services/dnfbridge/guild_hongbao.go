package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"strconv"

	guild "longheng.io/server/internal/modules/dnf/guild"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const currentGuildHongbaoListOpcode uint16 = 1300
const currentGuildHongbaoPointsOpcode uint16 = 1301

func (s *Service) handleCurrentGuildHongbaoList(session *gameSession, body []byte) error {
	if s.options.guildHongbaoEnabled {
		return s.handleCurrentGuildHongbaoLiveQuery(session, currentGuildHongbaoListOpcode, body)
	}
	return s.handleCurrentGuildHongbaoQuery(session, currentGuildHongbaoListOpcode, body)
}

func (s *Service) handleCurrentGuildHongbaoPoints(session *gameSession, body []byte) error {
	return s.handleCurrentGuildHongbaoQuery(session, currentGuildHongbaoPointsOpcode, body)
}

func (s *Service) sendCurrentGuildHongbaoFailure(session *gameSession, op uint16) error {
	if op == currentGuildHongbaoIssueOpcode || op == currentGuildHongbaoClaimOpcode {
		return s.sendGameUpperFailure(session, op, 34)
	}
	// Query callbacks consume an additional u32 after the normal failure header.
	return s.sendGameUpperRawClass(session, op, []byte{0, 34, 34, 0, 0, 0}, 1)
}

func (s *Service) handleCurrentGuildHongbaoQuery(session *gameSession, op uint16, body []byte) error {
	fail := func() error { return s.sendCurrentGuildHongbaoFailure(session, op) }
	if len(body) != 0 {
		return fail()
	}
	source, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return fail()
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{source})
	if err != nil {
		return nil
	}
	defer release()
	repos, ok := s.repositoryGroup()
	if !ok || repos.Guild == nil {
		return fail()
	}
	var members []guild.Member
	owner := repo.GuildOwner{CharacterID: strconv.Itoa(int(source.character)), AccountID: s.accountIDForSession(session)}
	// The read uses the same guild lock as edits, but never saves or grants.
	err = repos.Guild.WithinGuild(ctx, []repo.GuildOwner{owner}, func(tx repo.Group) error {
		if !s.boundGameSessionCharacterCurrent(source) {
			return guild.ErrCharacterNotFound
		}
		actor, found, e := tx.Character.Load(ctx, owner.CharacterID)
		if e != nil {
			return e
		}
		if !found || actor.CharacterID != owner.CharacterID || actor.AccountID != owner.AccountID {
			return guild.ErrCharacterNotFound
		}
		r, e := guild.Load(ctx, tx.Settings)
		if e != nil {
			return e
		}
		g, _, found := r.GuildForOwner(owner)
		if !found {
			return guild.ErrNotMember
		}
		members = guild.SortedMembers(g)
		if op == currentGuildHongbaoPointsOpcode {
			members, e = guild.HongbaoContributionMembers(ctx, tx.Settings, g)
			return e
		}
		return nil
	})
	if !s.boundGameSessionCharacterCurrent(source) {
		return nil
	}
	if err != nil {
		return fail()
	}
	var response []byte
	if op == currentGuildHongbaoPointsOpcode {
		// Membership stores the profile observed when joining. Project current
		// character data after releasing the guild transaction: its character
		// scope only contains the requester, not every listed member.
		current := members[:0]
		for _, member := range members {
			character, found, e := repos.Character.Load(ctx, member.CharacterID)
			if e != nil {
				return fail()
			}
			if !found || character.CharacterID != member.CharacterID || character.AccountID != member.AccountID || character.Stats["delete_flag"] != 0 {
				continue
			}
			member.Name, member.Level = character.Name, character.Level
			current = append(current, member)
		}
		members = current
		response, err = buildCurrentGuildHongbaoPointsBody(members)
		if err != nil {
			return fail()
		}
	} else {
		if len(members) > 65535 {
			return fail()
		}
		// This server has no hongbao issuance/claim producer yet. The collection
		// is empty; do not turn guild mileage into fictional spending or grants.
		var w packetWriter
		w.writeUint16(uint16(len(members)))
		w.writeUint32(0) // Issuance spending: no transactions exist in this implementation.
		w.writeUint32(0) // Claims: no transactions exist in this implementation.
		w.writeUint32(0) // Empty hongbao collection.
		response = w.bytes()
	}
	if !s.boundGameSessionCharacterCurrent(source) {
		return nil
	}
	return s.sendGameUpperSuccess(session, op, response)
}

func buildCurrentGuildHongbaoPointsBody(members []guild.Member) ([]byte, error) {
	// The native reader has a fixed 1000 * 40-byte stack buffer, no bound check.
	// Do not silently truncate a guild and omit the viewer's own score.
	if len(members) > 1000 {
		return nil, fmt.Errorf("hongbao roster exceeds native limit")
	}
	var w packetWriter
	w.writeUint32(uint32(len(members)))
	for _, m := range members {
		id, err := strconv.ParseUint(m.CharacterID, 10, 32)
		name := rosterNameBytes(m.Name)
		if err != nil || id == 0 || m.Level < 0 || m.Level > 255 || len(name) > 29 {
			return nil, fmt.Errorf("invalid hongbao member projection")
		}
		var row [40]byte
		binary.LittleEndian.PutUint32(row[:4], uint32(id))
		row[4] = byte(m.Level)
		copy(row[5:35], name) // 30-byte native name, including its NUL terminator.
		binary.LittleEndian.PutUint32(row[35:39], m.ContributionPoints)
		w.writeBytes(row[:])
	}
	return w.bytes(), nil
}
