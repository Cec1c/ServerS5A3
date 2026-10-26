package dnfbridge

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// A practice uses the same guild projection for every participant until exit.
// Later guild purchases or construction apply on the next visit.
type currentGuildPracticeRun struct {
	GuildID        uint32
	DungeonID      uint32
	Generation     uint64
	Expansion      uint8
	Guardian       guild.PracticeGuardian
	Placements     []guild.Placement
	Finished       bool
	identity       boundGameSessionCharacter
	projection     []byte
	projectionSent bool
}

func currentFriendlyGuildPractice(runtime *runtimeDungeonState) bool {
	return runtime != nil && worldmap.IsFriendlyGuildPractice(runtime.Dungeon)
}

func validateCurrentGuildPracticeEntryRequest(runtime *runtimeDungeonState) error {
	if runtime == nil {
		return guild.ErrPermissionDenied
	}
	return validateCurrentGuildPracticeRequestOwner(runtime, runtime.guildPractice)
}

func validateCurrentGuildPracticeRequestOwner(runtime *runtimeDungeonState, p *currentGuildPracticeRun) error {
	if !currentFriendlyGuildPractice(runtime) || p == nil {
		return guild.ErrPermissionDenied
	}
	q := runtime.Request
	if p.GuildID == 0 || p.Generation == 0 || p.DungeonID != q.DungeonID || int64(p.DungeonID) != runtime.Dungeon.ID ||
		p.identity.character == 0 || runtime.Character.CharacterID != strconv.Itoa(int(p.identity.character)) ||
		q.Difficulty != 3 || q.EntryOption != 0 || q.SelectionMode != 0 || q.RuntimeState != 1 ||
		q.RuntimeToken != 0xffff || q.Reserved != 0 || q.PartyState != 0 || q.LeaderObjectKey != 0 ||
		q.SpecialMode != 0 || len(q.OpaqueTail) != 0 {
		return fmt.Errorf("%w: guild practice request or owner changed", errDungeonEntryModeUnsupported)
	}
	return nil
}

func (s *Service) currentGuildPracticeIdentity(session *gameSession, runtime *runtimeDungeonState) (boundGameSessionCharacter, error) {
	id, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok || id.generation == 0 || !dungeonRuntimeOwnsCharacter(runtime, id.character) || runtime.AccountID != s.accountIDForSession(session) {
		return boundGameSessionCharacter{}, guild.ErrCharacterNotFound
	}
	session.townMu.Lock()
	origin := session.townPositionSnapshot
	if session.townSelectorOriginBound {
		origin = session.townSelectorOriginSnapshot
	}
	session.townMu.Unlock()
	if origin.CharacterID != id.character || origin.TownID != 8 || !origin.PositionValid ||
		runtime.Character.Stats["town_id"] != int64(origin.TownID) || runtime.Character.Stats["area_id"] != int64(origin.AreaID) {
		return boundGameSessionCharacter{}, guild.ErrPermissionDenied
	}
	area, ok := s.townArea(int64(origin.TownID), int64(origin.AreaID))
	if !ok || !townMapPathLooksLikeGuildHall(area.MapPath) {
		return boundGameSessionCharacter{}, guild.ErrPermissionDenied
	}
	return id, nil
}

func (s *Service) freezeCurrentGuildPracticeGroup(ctx context.Context, session *gameSession, runtime *runtimeDungeonState, followers []*runtimePartyPreparedDungeonEntry) error {
	if !currentFriendlyGuildPractice(runtime) {
		return nil
	}
	if !s.options.guildEssenceEnabled {
		return guild.ErrPermissionDenied
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return err
	}
	catalog, err := guild.LoadPracticeCatalog(items.source)
	if err != nil {
		return err
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		return guild.ErrRepositoryUnavailable
	}
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		return err
	}
	sessions, runtimes := []*gameSession{session}, []*runtimeDungeonState{runtime}
	for _, peer := range followers {
		if peer == nil || peer.session == nil || peer.runtime == nil || !sameSelectDungeonRequest(peer.runtime.Request, runtime.Request) {
			return guild.ErrPermissionDenied
		}
		sessions, runtimes = append(sessions, peer.session), append(runtimes, peer.runtime)
	}
	owners := make([]*currentGuildPracticeRun, len(sessions))
	var g *guild.Guild
	var origin currentTownPositionSnapshot
	seen := make(map[uint16]bool, len(sessions))
	for i, peer := range sessions {
		identity, err := s.currentGuildPracticeIdentity(peer, runtimes[i])
		if err != nil {
			return err
		}
		if seen[identity.character] {
			return guild.ErrPermissionDenied
		}
		seen[identity.character] = true
		memberGuild, _, member := reg.GuildForOwner(r.GuildOwner{AccountID: runtimes[i].AccountID, CharacterID: runtimes[i].Character.CharacterID})
		if !member {
			return guild.ErrNotMember
		}
		if err := freezeCurrentDungeonTownReturnOrigin(peer, runtimes[i]); err != nil {
			return err
		}
		p := runtimes[i].townReturnOrigin
		if i == 0 {
			g, origin = memberGuild, p
		} else if memberGuild.ID != g.ID || peer.channel.ID != session.channel.ID || p.TownID != origin.TownID || p.AreaID != origin.AreaID {
			return guild.ErrPermissionDenied
		}
		owners[i] = &currentGuildPracticeRun{GuildID: g.ID, DungeonID: runtime.Request.DungeonID, Generation: identity.generation, identity: identity}
	}
	tail, err := buildCurrentGuildGuardianSnapshot(g)
	if err != nil {
		return err
	}
	// This is the same expansion byte sent by 1297 and copied into 191.
	expansion := tail[2]
	base, barricade, err := catalog.FriendlyDungeons(expansion)
	if err != nil || (runtime.Request.DungeonID != base && runtime.Request.DungeonID != barricade) {
		return guild.ErrPermissionDenied
	}
	guardian, err := catalog.GuardianProjection(g.Guardians)
	if err != nil {
		return err
	}
	projection, err := buildCurrentGuildPlacementSnapshot(g)
	if err != nil {
		return err
	}
	for i, owner := range owners {
		owner.Expansion, owner.Guardian = expansion, guardian
		owner.projection = slices.Clone(projection)
		if g.Placements != nil {
			owner.Placements = slices.Clone(g.Placements.Rows)
			for n := range owner.Placements {
				owner.Placements[n].Cells = slices.Clone(owner.Placements[n].Cells)
			}
		}
		// Validate before exposing any member's owner to the entry path.
		if err := validateCurrentGuildPracticeRequestOwner(runtimes[i], owner); err != nil {
			return err
		}
		if !s.boundGameSessionCharacterCurrent(owner.identity) {
			return guild.ErrCharacterNotFound
		}
	}
	for i, owner := range owners {
		runtimes[i].guildPractice = owner
	}
	return nil
}

func (s *Service) validateCurrentGuildPracticeEntry(session *gameSession, runtime *runtimeDungeonState) error {
	if !currentFriendlyGuildPractice(runtime) {
		return nil
	}
	if err := validateCurrentGuildPracticeEntryRequest(runtime); err != nil {
		return err
	}
	owner := runtime.guildPractice
	if owner.identity.session != session || owner.Generation != owner.identity.generation || !s.boundGameSessionCharacterCurrent(owner.identity) {
		return guild.ErrCharacterNotFound
	}
	return nil
}

func (s *Service) sendCurrentGuildPracticeSnapshot(session *gameSession, runtime *runtimeDungeonState) error {
	if !currentFriendlyGuildPractice(runtime) {
		return nil
	}
	if err := s.validateCurrentGuildPracticeEntry(session, runtime); err != nil {
		return err
	}
	p := runtime.guildPractice
	projection := &session.guild.placement
	projection.mu.Lock()
	defer projection.mu.Unlock()
	if p.projectionSent {
		return nil
	}
	if err := s.sendGameUpperRawClass(session, 191, p.projection, 0); err != nil {
		return err
	}
	p.projectionSent = true
	projection.frozenPractice = p.identity
	return nil
}

// Return paths release the frozen table before projecting the current town.
// Do not acquire dungeon.mu here: settlement callers may already own it.
func releaseCurrentGuildPracticeProjection(session *gameSession) {
	p := &session.guild.placement
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.frozenPractice != (boundGameSessionCharacter{}) {
		p.frozenPractice = boundGameSessionCharacter{}
		p.identity = boundGameSessionCharacter{}
	}
}
