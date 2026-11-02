package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"time"

	guild "longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) initializeGuildContributionProjection(ctx context.Context) error {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Guild == nil {
		return nil
	}
	reader, ok := repos.Guild.(r.GuildReadUnitOfWork)
	if !ok {
		return guild.ErrRepositoryUnavailable
	}
	started := time.Now()
	count, err := guild.InitializeContributionProjection(ctx, reader, s.gameplayNow())
	if err != nil {
		return fmt.Errorf("guild contribution startup projection: %w", err)
	}
	s.logInfo("dnfbridge guild contribution projection ready", "members_backfilled", count, "elapsed_ms", time.Since(started).Milliseconds())
	return nil
}

// Membership, current identities and contribution counters share one read
// view. No registry/character writer lock or complete asset record is needed.
func (s *Service) readCurrentGuildDisplay(ctx context.Context, source boundGameSessionCharacter, contribution, hydrateMembers bool) (*guild.Guild, error) {
	repos, ok := s.repositoryGroup()
	if !ok {
		return nil, guild.ErrRepositoryUnavailable
	}
	reader, ok := repos.Guild.(r.GuildReadUnitOfWork)
	if !ok {
		return nil, guild.ErrRepositoryUnavailable
	}
	owner := r.GuildOwner{CharacterID: strconv.Itoa(int(source.character)), AccountID: s.accountIDForSession(source.session)}
	var result *guild.Guild
	err := reader.WithinGuildRead(ctx, func(view r.GuildReadView) error {
		if !s.boundGameSessionCharacterCurrent(source) {
			return guild.ErrCharacterNotFound
		}
		registryStart := s.performanceStart()
		registry, err := guild.Load(ctx, view.Settings)
		s.performanceEnd(perfGuildRegistryRead, registryStart, err)
		if err != nil {
			return err
		}
		g, _, _ := registry.GuildForOwner(owner)
		ids := []string{owner.CharacterID}
		if g != nil && hydrateMembers {
			ids = make([]string, 0, len(g.Members))
			for id := range g.Members {
				ids = append(ids, id)
			}
		}
		s.guildRosterCounter(guildSummaryMembers, int64(len(ids)))
		summaryStart := s.performanceStart()
		readCtx, counts := r.WithGuildReadCounts(ctx)
		characters, err := view.Characters.LoadGuildCharacters(readCtx, ids)
		s.performanceEnd(perfGuildMemberSummaryRead, summaryStart, err)
		s.guildRosterCounter(guildSummaryQueries, counts.Queries)
		s.guildRosterCounter(guildSummaryRows, int64(len(characters)))
		if err != nil {
			s.guildRosterCounter(guildSummaryFailed, 1)
			return err
		}
		actor, found := characters[owner.CharacterID]
		if !found || actor.CharacterID != owner.CharacterID || actor.AccountID != owner.AccountID || actor.DeleteFlag != 0 {
			return guild.ErrCharacterNotFound
		}
		if g == nil {
			return nil
		}
		// Continuation frames contain no member rows. Validate only the requester
		// in the same snapshot and retain registry metadata without another batch.
		if !hydrateMembers {
			result = g
			return nil
		}
		members := guild.SortedMembers(g)
		if contribution {
			members, err = guild.HongbaoContributionMembers(ctx, view.Settings, g)
			if err != nil {
				return err
			}
		}
		g.Members = make(map[string]guild.Member, len(members))
		for _, m := range members {
			c, found := characters[m.CharacterID]
			if !found || c.AccountID != m.AccountID || c.DeleteFlag != 0 {
				continue
			}
			m.Name, m.Level, m.Job, m.GrowType = c.Name, c.Level, c.Job, strconv.FormatInt(c.GrowType, 10)
			g.Members[m.CharacterID] = m
		}
		result = g
		return nil
	})
	return result, err
}
