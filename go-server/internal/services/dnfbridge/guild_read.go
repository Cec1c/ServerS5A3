package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"sync"
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

func (s *Service) archiveExpiredGuildHistory(ctx context.Context) error {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Guild == nil {
		return nil
	}
	archiver, ok := repos.Guild.(r.GuildArchiveUnitOfWork)
	if !ok {
		return nil
	}
	started := time.Now()
	stats, err := guild.ArchiveExpiredHistory(ctx, archiver, s.gameplayNow())
	if err != nil {
		return fmt.Errorf("guild history startup archive: %w", err)
	}
	if stats.TotalArchived() > 0 {
		s.logInfo("dnfbridge guild history archive ready",
			"contract_claims", stats.ContractClaims,
			"teleports", stats.Teleports,
			"hongbao", stats.Hongbao,
			"departures", stats.Departures,
			"before_bytes", stats.BeforeBytes,
			"after_bytes", stats.AfterBytes,
			"elapsed_ms", time.Since(started).Milliseconds())
	}
	return nil
}

type guildPresenceRosterBodyKey struct {
	version   guild.GuildReadVersion
	guildID   uint32
	opcode    uint16
	full      bool
	cursor    bool
	region    byte
	sampledAt int64
}

// Membership, current identities and contribution counters share one read
// view. No registry/character writer lock or complete asset record is needed.
// Shared only by one presence broadcast. Consumers treat g, online and
// rosterChunks as immutable; each recipient still validates current membership
// and owner in its own RR view.
type guildPresenceReadCache struct {
	mu              sync.Mutex
	version         guild.GuildReadVersion
	g               *guild.Guild
	onlineVersion   guild.GuildReadVersion
	onlineGuildID   uint32
	online          map[string]byte
	onlineSampled   int64
	rosterChunksKey guildPresenceRosterBodyKey
	rosterChunks    []currentGuildPacketChunk
	rosterBuildDone chan struct{}
	// rosterChunksBuilder is a test seam; production leaves it nil and uses
	// buildCurrentGuildMemberListChunks directly.
	rosterChunksBuilder func(*guild.Guild, uint16, bool, bool, map[string]byte, time.Time, byte) []currentGuildPacketChunk

	// metaMu caches immutable unhydrated guild metadata and online counts for
	// non-window town recipients (hydrateMembers=false). It is NEVER held across
	// WithinGuildRead or repository IO so non-hydrated reads do not serialize.
	metaMu            sync.Mutex
	metaVersion       guild.GuildReadVersion
	metaGuild         *guild.Guild
	metaOnlineVersion guild.GuildReadVersion
	metaOnlineGuildID uint32
	metaOnlineCount   int
	metaOnlineSampled int64
	metaOnlineValid   bool
}

func (c *guildPresenceReadCache) sharedOnlineChannels(s *Service, g *guild.Guild) (map[string]byte, time.Time) {
	if c == nil || g == nil {
		return s.currentGuildOnlineChannels(g, nil), s.gameplayNow()
	}
	c.mu.Lock()
	if c.g != g {
		c.mu.Unlock()
		return s.currentGuildOnlineChannels(g, nil), s.gameplayNow()
	}
	version := c.version
	if c.online != nil && c.onlineGuildID == g.ID && c.onlineVersion == version {
		online := c.online
		sampled := time.Unix(0, c.onlineSampled).UTC()
		c.mu.Unlock()
		return online, sampled
	}
	c.mu.Unlock()
	now := s.gameplayNow()
	online := s.currentGuildOnlineChannels(g, nil)
	c.mu.Lock()
	if c.g == g && c.version == version {
		if c.online == nil || c.onlineGuildID != g.ID || c.onlineVersion != version {
			c.onlineVersion = version
			c.onlineGuildID = g.ID
			c.online = online
			c.onlineSampled = now.UnixNano()
		} else {
			online = c.online
			now = time.Unix(0, c.onlineSampled).UTC()
		}
	}
	c.mu.Unlock()
	return online, now
}

func (c *guildPresenceReadCache) sharedOnlineCount(s *Service, g *guild.Guild) (int, time.Time) {
	if c == nil || g == nil {
		return len(s.currentGuildOnlineChannels(g, nil)), s.gameplayNow()
	}
	c.metaMu.Lock()
	if c.metaGuild == g {
		version := c.metaVersion
		if c.metaOnlineValid && c.metaOnlineGuildID == g.ID && c.metaOnlineVersion == version {
			count := c.metaOnlineCount
			sampled := time.Unix(0, c.metaOnlineSampled).UTC()
			c.metaMu.Unlock()
			return count, sampled
		}
		c.metaMu.Unlock()
		now := s.gameplayNow()
		count := len(s.currentGuildOnlineChannels(g, nil))
		c.metaMu.Lock()
		if c.metaGuild == g && c.metaVersion == version {
			if !c.metaOnlineValid || c.metaOnlineGuildID != g.ID || c.metaOnlineVersion != version {
				c.metaOnlineVersion = version
				c.metaOnlineGuildID = g.ID
				c.metaOnlineCount = count
				c.metaOnlineSampled = now.UnixNano()
				c.metaOnlineValid = true
			} else {
				count = c.metaOnlineCount
				now = time.Unix(0, c.metaOnlineSampled).UTC()
			}
		}
		c.metaMu.Unlock()
		return count, now
	}
	c.metaMu.Unlock()
	online, sampled := c.sharedOnlineChannels(s, g)
	return len(online), sampled
}

func (c *guildPresenceReadCache) sharedRosterChunks(s *Service, g *guild.Guild, opcode uint16, full, cursor bool, online map[string]byte, sampledAt time.Time, region byte) []currentGuildPacketChunk {
	if c == nil || g == nil {
		return buildCurrentGuildMemberListChunks(g, opcode, full, cursor, online, sampledAt, region)
	}
	for {
		c.mu.Lock()
		if c.g != g {
			c.mu.Unlock()
			return buildCurrentGuildMemberListChunks(g, opcode, full, cursor, online, sampledAt, region)
		}
		key := guildPresenceRosterBodyKey{
			version:   c.version,
			guildID:   g.ID,
			opcode:    opcode,
			full:      full,
			cursor:    cursor,
			region:    region,
			sampledAt: sampledAt.UnixNano(),
		}
		if c.rosterChunks != nil && c.rosterChunksKey == key {
			chunks := c.rosterChunks
			c.mu.Unlock()
			return chunks
		}
		if c.rosterBuildDone != nil {
			// One broadcast must build the same immutable body once. Wait for
			// the in-flight build, then re-check the key because the registry
			// version may have advanced while it ran.
			done := c.rosterBuildDone
			c.mu.Unlock()
			<-done
			continue
		}
		done := make(chan struct{})
		c.rosterBuildDone = done
		builder := c.rosterChunksBuilder
		c.mu.Unlock()
		if builder == nil {
			builder = buildCurrentGuildMemberListChunks
		}
		chunks := builder(g, opcode, full, cursor, online, sampledAt, region)
		c.mu.Lock()
		if c.g == g && c.version == key.version {
			c.rosterChunksKey = key
			c.rosterChunks = chunks
		}
		c.rosterBuildDone = nil
		close(done)
		c.mu.Unlock()
		return chunks
	}
}

func (s *Service) readCurrentGuildDisplay(ctx context.Context, source boundGameSessionCharacter, contribution, hydrateMembers bool) (*guild.Guild, error) {
	return s.readCurrentGuildDisplayShared(ctx, source, contribution, hydrateMembers, nil)
}

func (s *Service) readCurrentGuildDisplayShared(ctx context.Context, source boundGameSessionCharacter, contribution, hydrateMembers bool, shared *guildPresenceReadCache) (*guild.Guild, error) {
	repos, ok := s.repositoryGroup()
	if !ok {
		return nil, guild.ErrRepositoryUnavailable
	}
	reader, ok := repos.Guild.(r.GuildReadUnitOfWork)
	if !ok {
		return nil, guild.ErrRepositoryUnavailable
	}
	owner := r.GuildOwner{CharacterID: strconv.Itoa(int(source.character)), AccountID: s.accountIDForSession(source.session)}
	serializeShared := shared != nil && !contribution && hydrateMembers
	if serializeShared {
		shared.mu.Lock()
		if err := ctx.Err(); err != nil {
			shared.mu.Unlock()
			return nil, err
		}
		defer shared.mu.Unlock()
	}
	var result *guild.Guild
	err := reader.WithinGuildRead(ctx, func(view r.GuildReadView) error {
		if !s.boundGameSessionCharacterCurrent(source) {
			return guild.ErrCharacterNotFound
		}
		registryStart := s.performanceStart()
		membership, _, err := guild.ReadGuildMembership(ctx, view.Settings, owner.CharacterID)
		if err != nil {
			return err
		}
		if serializeShared && membership != nil {
			if shared.g != nil && shared.version == membership.Version() {
				m, member := shared.g.Members[owner.CharacterID]
				if member && m.AccountID == owner.AccountID {
					current, err := view.Characters.LoadGuildCharacters(ctx, []string{owner.CharacterID})
					if err != nil {
						return err
					}
					c, found := current[owner.CharacterID]
					if !found || c.CharacterID != owner.CharacterID || c.AccountID != owner.AccountID || c.DeleteFlag != 0 {
						return guild.ErrCharacterNotFound
					}
					result = shared.g
					return nil
				}
			}
		}
		var g *guild.Guild
		metaHit := false
		if !hydrateMembers && !contribution && shared != nil && membership != nil {
			ver := membership.Version()
			shared.metaMu.Lock()
			if shared.metaGuild != nil && shared.metaVersion == ver {
				if m, member := shared.metaGuild.Members[owner.CharacterID]; member && m.AccountID == owner.AccountID {
					g = shared.metaGuild
					metaHit = true
				}
			}
			shared.metaMu.Unlock()
		}
		if !metaHit {
			g, err = membership.RosterProjection(owner)
			s.performanceEnd(perfGuildRegistryRead, registryStart, err)
			if err != nil {
				return err
			}
		} else {
			s.performanceEnd(perfGuildRegistryRead, registryStart, nil)
		}
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
			if !contribution && shared != nil && membership != nil && !metaHit {
				ver := membership.Version()
				shared.metaMu.Lock()
				if shared.metaVersion != ver || shared.metaGuild == nil || shared.metaGuild.ID != g.ID {
					shared.metaGuild = g
					shared.metaVersion = ver
					shared.metaOnlineGuildID = 0
					shared.metaOnlineCount = 0
					shared.metaOnlineSampled = 0
					shared.metaOnlineValid = false
				} else {
					// Adopt the already-published immutable metadata pointer for the same
					// (version, guildID) so concurrent cold-start readers share online counts.
					g = shared.metaGuild
				}
				shared.metaMu.Unlock()
			}
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
		if serializeShared && membership != nil {
			nextVersion := membership.Version()
			if shared.version != nextVersion || shared.g != g {
				shared.online = nil
				shared.rosterChunks = nil
			}
			shared.g, shared.version = g, nextVersion
		}
		return nil
	})
	return result, err
}
