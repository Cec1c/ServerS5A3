package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"sort"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const currentGuildRankingInterval = 5 * time.Minute

func (s *Service) currentGuildRankingRules() (*guild.RankingRules, error) {
	s.guildRankingCatalogMu.Lock()
	defer s.guildRankingCatalogMu.Unlock()
	if s.guildRankingRules != nil {
		return s.guildRankingRules, nil
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return nil, err
	}
	rules, err := guild.LoadRankingRules(items.source)
	if err != nil {
		return nil, err
	}
	s.guildRankingRules = rules
	return rules, nil
}

func (s *Service) handleCurrentGuildRanking(session *gameSession, request []byte) error {
	fail := func(err error) error {
		s.logGameEvent(session, "game-guild-ranking-rejected", "error", err)
		return s.sendGameUpperFailure(session, currentGuildRankingOpcode, 1)
	}
	peer, bound := s.boundGameSessionCharacterSnapshot(session)
	if !s.options.guildRankingEnabled || !bound || peer.generation == 0 || len(request) != 2 || binary.LittleEndian.Uint16(request) != peer.character {
		return fail(guild.ErrCharacterNotFound)
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
	owner := r.GuildOwner{AccountID: actor.AccountID, CharacterID: actor.CharacterID}
	// Check membership before triggering the global sampler. Foreign or
	// departed characters cannot use this query to mutate ranking snapshots.
	_, reg, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return fail(err)
	}
	g, _, member := reg.GuildForOwner(owner)
	if !member {
		return fail(guild.ErrNotMember)
	}
	guildID := g.ID
	reg, err = s.refreshCurrentGuildRanking(ctx, owner)
	if err != nil {
		return fail(err)
	}
	g, _, member = reg.GuildForOwner(owner)
	if !member || g.ID != guildID || !s.boundGameSessionCharacterCurrent(peer) {
		return fail(guild.ErrNotMember)
	}
	if err := reg.Ranking.Validate(); err != nil {
		return fail(err)
	}
	body, err := buildCurrentGuildRankingBody(reg.Ranking.View(guildID, s.gameplayNow()))
	if err != nil {
		return fail(err)
	}
	return s.sendGameUpperSuccess(session, currentGuildRankingOpcode, body)
}

// The registry and result are published as one epoch. Character repositories
// are sampled without holding the global guild lock; source changes reject
// publication instead of overwriting concurrent membership or clear events.
func (s *Service) refreshCurrentGuildRanking(ctx context.Context, owner r.GuildOwner) (guild.Registry, error) {
	s.guildRankingMu.Lock()
	defer s.guildRankingMu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		repos, reg, err := s.loadCurrentGuildRegistry(ctx)
		if err != nil {
			return guild.Registry{}, err
		}
		now := s.gameplayNow()
		if reg.Ranking != nil && reg.Ranking.Current != nil {
			p := reg.Ranking.Current
			if p.SampledAt.After(now) {
				return guild.Registry{}, guild.ErrRankingSnapshotChanged
			}
			if p.Start.Equal(guild.RankingMonthStart(now)) && now.Sub(p.SampledAt) < currentGuildRankingInterval {
				return reg, reg.Ranking.Validate()
			}
		}
		if len(reg.Guilds) == 0 {
			return reg, nil
		}
		if owner.CharacterID == "" {
			ids := make([]uint32, 0, len(reg.Guilds))
			for id := range reg.Guilds {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			g := reg.Guilds[ids[0]]
			m := g.Members[g.MasterCharacterID]
			owner = r.GuildOwner{AccountID: m.AccountID, CharacterID: m.CharacterID}
		}
		if _, _, ok := reg.GuildForOwner(owner); !ok {
			return guild.Registry{}, guild.ErrNotMember
		}
		rules, err := s.currentGuildRankingRules()
		if err != nil {
			return guild.Registry{}, err
		}
		catalog, err := s.currentGuildDungeonCatalog()
		if err != nil {
			return guild.Registry{}, err
		}
		online := map[r.GuildOwner]bool{}
		for _, g := range reg.Guilds {
			for id := range s.currentGuildOnlineChannels(g, nil) {
				m := g.Members[id]
				online[r.GuildOwner{AccountID: m.AccountID, CharacterID: id}] = true
			}
		}
		facts, err := guild.SampleRankingMembers(ctx, repos.Character, reg, s.adventureGroupTable, defaultCharacterSlots, online, nil)
		if err != nil {
			return guild.Registry{}, err
		}
		period, err := guild.CalculateRankingPeriod(reg, facts, rules, catalog, now)
		if err != nil {
			return guild.Registry{}, err
		}
		digest, err := guild.RankingSourceDigest(reg)
		if err != nil {
			return guild.Registry{}, err
		}
		if err = guild.PublishRanking(ctx, repos.Guild, owner, digest, period); errors.Is(err, guild.ErrRankingSnapshotChanged) {
			continue
		} else if err != nil {
			return guild.Registry{}, err
		}
		return guild.Load(ctx, repos.Settings)
	}
	return guild.Registry{}, guild.ErrRankingSnapshotChanged
}

// Runs outside the login/event-loop and the shared gameplay timer callback.
// Startup does not wait for PVF or roster reads; shutdown joins this worker.
func (s *Service) startCurrentGuildRankingSampler(ctx context.Context) {
	if !s.options.guildRankingEnabled {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, err := s.refreshCurrentGuildRanking(callCtx, r.GuildOwner{})
			cancel()
			if err != nil && ctx.Err() == nil {
				s.logGameEvent(nil, "game-guild-ranking-sample-failed", "error", err)
			}
			now := s.gameplayNow()
			delay := currentGuildRankingInterval
			boundary := guild.RankingMonthStart(now).AddDate(0, 1, 0)
			if d := boundary.Sub(now); d > 0 && d < delay {
				delay = d
			}
			timer.Reset(delay)
		}
	}()
}

// Shared terminal facts survive a teammate returning to town before the other
// members claim their rewards. A later party or a replacement socket cannot
// join this already-completed run.
type currentGuildRankingRun struct {
	members    []r.GuildOwner
	identities map[uint16]boundGameSessionCharacter
	rules      *guild.RankingRules
}

// Called only from the authoritative completed-run barrier, before op31.
// Lock order is dungeon -> party manager -> instance; service identity reads
// happen without the instance lock, matching the existing entry transaction.
func (s *Service) freezeCurrentGuildRankingRun(session *gameSession, runtime *runtimeDungeonState) error {
	if !s.options.guildRankingEnabled || runtime == nil || runtime.partyInstance == nil || runtime.startedAt.IsZero() {
		return nil
	}
	instance := runtime.partyInstance
	instance.mu.Lock()
	frozen := instance.guildRankingRun != nil
	instance.mu.Unlock()
	if frozen {
		return nil
	}
	rules, err := s.currentGuildRankingRules()
	if err != nil {
		return err
	}
	run := &currentGuildRankingRun{rules: rules}
	publish := func() {
		instance.mu.Lock()
		defer instance.mu.Unlock()
		if instance.guildRankingRun == nil {
			instance.guildRankingRun = run
		}
	}
	if _, eligible := rules.PartyClears[uint32(runtime.Dungeon.ID)]; !eligible {
		publish()
		return nil
	}
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return guild.ErrCharacterNotFound
	}
	manager := s.runtimePartyManagerForService()
	snapshot, inParty := manager.SnapshotByUser(actor.character, actor.generation)
	if !inParty || snapshot.ID != instance.key.PartyID || len(snapshot.Members) < 2 || len(snapshot.Members) > 4 {
		publish()
		return nil
	}
	return manager.WithSnapshot(snapshot, func() error {
		identities := make(map[uint16]boundGameSessionCharacter, len(snapshot.Members))
		owners := make([]r.GuildOwner, 0, len(snapshot.Members))
		for _, member := range snapshot.Members {
			identity, ok := s.onlineGameSessionCharacterSnapshot(member.UserID)
			if !ok || identity.generation != member.SessionGeneration {
				publish()
				return nil
			}
			identities[member.UserID] = identity
			owners = append(owners, r.GuildOwner{AccountID: s.accountIDForSession(identity.session), CharacterID: strconv.Itoa(int(member.UserID))})
		}
		instance.mu.Lock()
		defer instance.mu.Unlock()
		if instance.guildRankingRun != nil {
			return nil
		}
		// All accepted party members must belong to this shared run. A stale
		// scene pointer alone is insufficient evidence of current membership.
		valid := len(instance.sessions) == len(identities)
		for id, identity := range identities {
			valid = valid && instance.sessions[id] == identity.session && instance.scenes[id] != nil
		}
		if valid {
			sort.Slice(owners, func(i, j int) bool { return owners[i].CharacterID < owners[j].CharacterID })
			run.members, run.identities = owners, identities
		}
		instance.guildRankingRun = run
		return nil
	})
}

func (s *Service) currentGuildRankingClear(session *gameSession, runtime *runtimeDungeonState) (*guild.RankingPartyClear, error) {
	if !s.options.guildRankingEnabled || runtime == nil || runtime.partyInstance == nil {
		return nil, nil
	}
	instance := runtime.partyInstance
	instance.mu.Lock()
	run := instance.guildRankingRun
	instance.mu.Unlock()
	if run == nil || len(run.members) < 2 {
		return nil, nil
	}
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	frozen, eligible := run.identities[actor.character]
	if !bound || !eligible || actor.generation != frozen.generation || actor.session != frozen.session {
		return nil, guild.ErrCharacterNotFound
	}
	revalidate := func() bool {
		if !s.boundGameSessionCharacterCurrent(frozen) {
			return false
		}
		instance.mu.Lock()
		defer instance.mu.Unlock()
		return instance.guildRankingRun == run && instance.sessions[actor.character] == session && instance.scenes[actor.character] != nil
	}
	return &guild.RankingPartyClear{DungeonID: uint32(runtime.Dungeon.ID), StartedAt: runtime.startedAt, Members: append([]r.GuildOwner(nil), run.members...), Rules: run.rules, Revalidate: revalidate}, nil
}
