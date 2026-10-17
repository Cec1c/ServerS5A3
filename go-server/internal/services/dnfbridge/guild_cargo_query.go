package dnfbridge

import (
	"errors"
	"strconv"

	guild "longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const currentGuildCargoOpcode uint16 = 261
const currentGuildCargoHistoryOpcode uint16 = 262

// Approved server policy: new/legacy missing warehouses start with eight slots;
// purchases extend them up to the limit read from the active PVF.
const currentGuildCargoInitialCapacity uint32 = 8

func currentGuildCargoQueryError(op uint16, err error) byte {
	switch {
	case errors.Is(err, guild.ErrNotMember):
		return 100
	case errors.Is(err, guild.ErrPermissionDenied):
		return 36
	case op == currentGuildCargoOpcode && errors.Is(err, guild.ErrCargoUninitialized):
		return 196
	default:
		return 8 // Both callbacks recognize this generic failure branch.
	}
}

func (s *Service) handleCurrentGuildCargo(session *gameSession, body []byte) error {
	return s.handleCurrentGuildCargoQuery(session, currentGuildCargoOpcode, body)
}
func (s *Service) handleCurrentGuildCargoHistory(session *gameSession, body []byte) error {
	return s.handleCurrentGuildCargoQuery(session, currentGuildCargoHistoryOpcode, body)
}

func (s *Service) handleCurrentGuildCargoQuery(session *gameSession, op uint16, request []byte) error {
	return s.handleCurrentGuildCargoQueryForGuild(session, op, request, 0)
}

func (s *Service) handleCurrentGuildCargoQueryForGuild(session *gameSession, op uint16, request []byte, expectedGuild uint32) error {
	s.guildCargoMu.Lock()
	defer s.guildCargoMu.Unlock()
	return s.handleCurrentGuildCargoQueryLocked(session, op, request, expectedGuild)
}

func (s *Service) handleCurrentGuildCargoQueryLocked(session *gameSession, op uint16, request []byte, expectedGuild uint32) error {
	fail := func(code byte) error {
		if expectedGuild != 0 {
			return nil
		}
		return s.sendGameUpperFailure(session, op, code)
	}
	if len(request) != 0 {
		return fail(8)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || identity.generation == 0 {
		return fail(8)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return fail(8)
	}
	defer release()
	if !session.townMu.TryLock() {
		return fail(8)
	}
	defer session.townMu.Unlock()
	revalidate := func() bool {
		if !s.boundGameSessionCharacterCurrent(identity) || session.townSceneReadyCharacterID != identity.character || !session.townPositionSnapshot.PositionValid {
			return false
		}
		if !session.dungeon.mu.TryLock() {
			return false
		}
		inDungeon := session.dungeon.runtime != nil
		session.dungeon.mu.Unlock()
		return !inDungeon
	}
	if !revalidate() {
		return fail(8)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return fail(8)
	}
	position := session.townPositionSnapshot
	area, found := s.townArea(int64(position.TownID), int64(position.AreaID))
	if !found || !townMapPathLooksLikeGuildHall(area.MapPath) {
		return fail(8)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return fail(8)
	}
	limit, err := currentGuildCargoSceneLimit(catalog.source, area.MapPath)
	if err != nil {
		return fail(8)
	}
	query := guild.CargoQuery{Owner: r.GuildOwner{CharacterID: strconv.Itoa(int(identity.character)), AccountID: s.accountIDForSession(session)}, Revalidate: revalidate}
	var view guild.CargoView
	if expectedGuild != 0 {
		view, err = guild.ReadCargo(ctx, repos.Guild, query)
	} else {
		view, err = guild.OpenCargo(ctx, repos.Guild, query, guild.CargoInitialization{InitialCapacity: currentGuildCargoInitialCapacity, MaxCapacity: limit, Now: s.gameplayNow()})
	}
	if err != nil {
		return fail(currentGuildCargoQueryError(op, err))
	}
	if expectedGuild != 0 && view.GuildID != expectedGuild {
		return nil
	}
	var body []byte
	if op == currentGuildCargoOpcode {
		body, err = buildCurrentGuildCargoSnapshotBody(view.Cargo, limit, catalog)
	} else {
		for _, entry := range view.Cargo.History {
			if _, resolveErr := catalog.ResolveItem(uint32(entry.Item.ItemID)); resolveErr != nil {
				return fail(8)
			}
		}
		body, err = buildCurrentGuildCargoHistoryBody(view.Cargo.History)
	}
	if err != nil {
		return fail(currentGuildCargoQueryError(op, err))
	}
	if !revalidate() {
		return fail(8)
	}
	if err := s.sendGameUpperSuccess(session, op, body); err != nil {
		return err
	}
	if op == currentGuildCargoOpcode {
		session.guild.mu.Lock()
		session.guild.cargoLoadedGuild, session.guild.cargoLoadedGeneration = view.GuildID, identity.generation
		session.guild.mu.Unlock()
	}
	return nil
}
