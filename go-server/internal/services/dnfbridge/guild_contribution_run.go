package dnfbridge

import (
	"strconv"

	"longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// Independent of ranking's limited dungeon catalog. Freeze all terminal
// participants once; ordinary/recommended eligibility is checked at settlement.
type currentGuildContributionRun struct {
	members    []r.GuildOwner
	guilds     map[string]guild.ContributionMembership
	identities map[uint16]boundGameSessionCharacter
}

func (s *Service) freezeCurrentGuildContributionRun(session *gameSession, runtime *runtimeDungeonState) error {
	if !s.guildContributionEnabled() || runtime == nil || runtime.partyInstance == nil || runtime.startedAt.IsZero() {
		return nil
	}
	instance := runtime.partyInstance
	instance.mu.Lock()
	frozen := instance.guildContributionRun != nil
	instance.mu.Unlock()
	if frozen {
		return nil
	}
	run := &currentGuildContributionRun{}
	publishEmpty := func() {
		instance.mu.Lock()
		defer instance.mu.Unlock()
		if instance.guildContributionRun == nil {
			instance.guildContributionRun = run
		}
	}
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return guild.ErrCharacterNotFound
	}
	manager := s.runtimePartyManagerForService()
	snapshot, inParty := manager.SnapshotByUser(actor.character, actor.generation)
	if !inParty || snapshot.ID != instance.key.PartyID || len(snapshot.Members) < 2 || len(snapshot.Members) > 4 {
		publishEmpty()
		return nil
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	_, registry, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return err
	}
	return manager.WithSnapshot(snapshot, func() error {
		identities := map[uint16]boundGameSessionCharacter{}
		owners := make([]r.GuildOwner, 0, len(snapshot.Members))
		guilds := map[string]guild.ContributionMembership{}
		for _, member := range snapshot.Members {
			identity, ok := s.onlineGameSessionCharacterSnapshot(member.UserID)
			if !ok || identity.generation != member.SessionGeneration {
				publishEmpty()
				return nil
			}
			owner := r.GuildOwner{AccountID: s.accountIDForSession(identity.session), CharacterID: strconv.Itoa(int(member.UserID))}
			identities[member.UserID] = identity
			owners = append(owners, owner)
			guilds[owner.CharacterID] = guild.ContributionMembership{}
			if g, m, found := registry.GuildForOwner(owner); found {
				guilds[owner.CharacterID] = guild.ContributionMembership{GuildID: g.ID, JoinedAt: m.JoinedAt}
			}
		}
		instance.mu.Lock()
		defer instance.mu.Unlock()
		if instance.guildContributionRun != nil {
			return nil
		}
		valid := len(instance.sessions) == len(identities)
		for id, identity := range identities {
			valid = valid && instance.sessions[id] == identity.session && instance.scenes[id] != nil
		}
		if valid {
			run.members, run.identities, run.guilds = owners, identities, guilds
		}
		instance.guildContributionRun = run
		return nil
	})
}

func (s *Service) currentGuildContributionClear(session *gameSession, runtime *runtimeDungeonState, completion string) (*dungeon.GuildContributionClear, error) {
	if !s.guildContributionEnabled() || runtime == nil {
		return nil, nil
	}
	activity := guild.ContributionActivity{CompletionKey: completion}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return nil, guild.ErrCharacterNotFound
	}
	current := func() bool { return s.boundGameSessionCharacterCurrent(identity) }
	if runtime.guildDungeonClear != nil {
		activity.Event = guild.ContributionGuildClear
	} else {
		if !currentDungeonEarnsGuildMileage(runtime) || runtime.partyInstance == nil {
			return nil, nil
		}
		instance := runtime.partyInstance
		instance.mu.Lock()
		run := instance.guildContributionRun
		instance.mu.Unlock()
		if run == nil || len(run.members) < 2 {
			return nil, nil
		}
		frozen, found := run.identities[identity.character]
		if !found || frozen != identity {
			return nil, guild.ErrCharacterNotFound
		}
		activity.Event = guild.ContributionPartyClear
		activity.PartyMembers = append([]r.GuildOwner(nil), run.members...)
		activity.FrozenGuilds = make(map[string]guild.ContributionMembership, len(run.guilds))
		for id, g := range run.guilds {
			activity.FrozenGuilds[id] = g
		}
		current = func() bool {
			if !s.boundGameSessionCharacterCurrent(frozen) {
				return false
			}
			instance.mu.Lock()
			defer instance.mu.Unlock()
			return instance.guildContributionRun == run && instance.sessions[identity.character] == session && instance.scenes[identity.character] != nil
		}
	}
	contract, err := s.currentGuildContributionContract()
	if err != nil {
		return nil, err
	}
	return &dungeon.GuildContributionClear{Contract: contract, Activity: activity, Revalidate: current}, nil
}
