package dnfbridge

import (
	"encoding/binary"
	"strconv"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/eventloop"
)

func (s *Service) currentGuildEssenceCatalog() (*guild.EssenceCatalog, error) {
	s.guildDungeonCatalogMu.Lock()
	defer s.guildDungeonCatalogMu.Unlock()
	if s.guildEssenceCatalog != nil {
		return s.guildEssenceCatalog, nil
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return nil, err
	}
	c, err := guild.LoadEssenceCatalog(items.source)
	if err != nil {
		return nil, err
	}
	s.guildEssenceCatalog = c
	return c, nil
}

// Constructor defaults for an empty guild, plus the two committed balances.
func buildCurrentGuildEssenceSnapshot(balance guild.EssenceBalance) []byte {
	body := make([]byte, 0x895)
	body[1] = 0xff
	binary.LittleEndian.PutUint16(body[0x876:], 1)
	binary.LittleEndian.PutUint16(body[0x878:], 1)
	binary.LittleEndian.PutUint32(body[0x17:], balance.AP)
	binary.LittleEndian.PutUint32(body[0x1b:], balance.GP)
	return body
}

// Income and reconnect project existing ownership rather than clearing it.
// This does not assign initial level/skills or acquire/select a guardian.
func buildCurrentGuildGuardianSnapshot(g *guild.Guild) ([]byte, error) {
	if g == nil {
		return buildCurrentGuildEssenceSnapshot(guild.EssenceBalance{}), nil
	}
	if err := g.Guardians.Validate(); err != nil {
		return nil, err
	}
	body := buildCurrentGuildEssenceSnapshot(g.Essence)
	if g.Guardians == nil {
		return body, nil
	}
	binary.LittleEndian.PutUint32(body[0x44:], g.Guardians.ActiveID)
	for i, row := range g.Guardians.Owned {
		at := 0x7f8 + 18*i
		binary.LittleEndian.PutUint32(body[at:], row.ID)
		body[at+4] = row.Level
		copy(body[at+5:at+10], row.Skills[:])
		binary.LittleEndian.PutUint32(body[at+10:], row.APProgress)
		binary.LittleEndian.PutUint32(body[at+14:], row.GPProgress)
	}
	return body, nil
}

func buildCurrentGuildEssenceNotice(ap uint32) []byte {
	body := make([]byte, 8)
	binary.LittleEndian.PutUint32(body, ap)
	return body
}

func (s *Service) writeCurrentGuildEssenceSnapshot(session *gameSession) error {
	if !s.options.guildEssenceEnabled {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		return guild.ErrRepositoryUnavailable
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		return err
	}
	g, _, _ := reg.GuildForOwner(r.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(session.selectedCharacterID))})
	body, err := buildCurrentGuildGuardianSnapshot(g)
	if err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, 1297, body, 0)
}

// Caller has the login town lock. Asset reservation protects the canonical
// generation through SQL commit. No peer lock is acquired during this write.
func (s *Service) grantCurrentGuildEssenceLogin(session *gameSession) error {
	if !s.options.guildEssenceEnabled {
		return nil
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return guild.ErrCharacterNotFound
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return err
	}
	defer release()
	repos, ok := s.repositoryGroup()
	if !ok {
		return guild.ErrRepositoryUnavailable
	}
	catalog, err := s.currentGuildEssenceCatalog()
	if err != nil {
		return err
	}
	result, err := guild.GrantEssenceLogin(ctx, repos.Guild, r.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(identity.character))}, catalog, s.gameplayNow(), func() bool { return s.boundGameSessionCharacterCurrent(identity) })
	if err != nil {
		return err
	}
	return s.projectCurrentGuildEssenceIncome(session, result, false)
}

func (s *Service) projectCurrentGuildEssenceIncome(session *gameSession, income guild.EssenceIncome, replayed bool) error {
	if !s.options.guildEssenceEnabled {
		return nil
	}
	if err := s.writeCurrentGuildEssenceSnapshot(session); err != nil {
		return err
	}
	if replayed || income.AP == 0 {
		return nil
	}
	s.queueCurrentGuildEssenceRefresh(income.GuildID, session.selectedCharacterID)
	return s.sendGameUpperRawClass(session, 1284, buildCurrentGuildEssenceNotice(income.AP), 0)
}

// Same commit may occur under a dungeon or town lock: use peer event queues,
// recheck their owner/generation/membership, and never wait on a peer here.
func (s *Service) queueCurrentGuildEssenceRefresh(guildID uint32, skip uint16) {
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	_, reg, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		s.logInfo("guild essence peer refresh deferred", "error", err)
		return
	}
	g := reg.Guilds[guildID]
	if g == nil {
		return
	}
	for id, member := range g.Members {
		numeric, err := strconv.ParseUint(id, 10, 16)
		if err != nil || uint16(numeric) == skip {
			continue
		}
		peer, ok := s.onlineGameSessionCharacterSnapshot(uint16(numeric))
		if !ok || s.accountIDForSession(peer.session) != member.AccountID || peer.session.events == nil || peer.session.events.loop == nil {
			continue
		}
		const source = "guild-essence-refresh"
		err = peer.session.events.loop.Submit(eventloop.Event{Type: source, Priority: eventloop.PriorityMedium, Data: gameSessionEventTask{source: source, characterID: peer.character, characterGeneration: peer.generation, async: true, run: func() error {
			if !s.boundGameSessionCharacterCurrent(peer) {
				return nil
			}
			g, err := s.currentGuildForSelectedCharacter(peer.session)
			if err != nil {
				return err
			}
			if g == nil || g.ID != guildID {
				return nil
			}
			return s.writeCurrentGuildEssenceSnapshot(peer.session)
		}}})
		if err != nil {
			s.logGameEvent(peer.session, "game-guild-essence-peer-refresh-deferred", "error", err)
		}
	}
}
