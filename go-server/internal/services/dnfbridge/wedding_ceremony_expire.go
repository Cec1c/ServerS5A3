package dnfbridge

import (
	"context"
	"errors"
	"strconv"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func (s *Service) currentWeddingCeremonyReturn(c r.CharacterRecord) (wedding.CeremonyReturnLocation, error) {
	id, err := wedding.NumericID(c.CharacterID)
	if err != nil {
		return wedding.CeremonyReturnLocation{}, err
	}
	if origin, ok := currentTownPrevVillageOriginFromPersistedStats(uint16(id), c); ok {
		if path, valid := s.currentTownPrevVillageOriginIsPersistable(origin); valid && !townMapPathLooksLikeWeddingChurch(path) && !townMapPathLooksLikeCoupleRoom(path) && !townMapPathLooksLikeGuildHall(path) {
			p := wedding.CeremonyReturnLocation{Town: int64(origin.TownID), Area: int64(origin.AreaID), X: int64(origin.PositionX), Y: int64(origin.PositionY), Direction: int64(origin.MovementCode)}
			if p.Valid() {
				return p, nil
			}
		}
	}
	// This existing login fallback resolves the current PVF area's real gate;
	// neither town coordinates nor a fictitious wedding return map are made up.
	location, err := s.currentInitialTownSeriaLoginLocation()
	if err != nil {
		return wedding.CeremonyReturnLocation{}, err
	}
	p := wedding.CeremonyReturnLocation{Town: int64(location.TownID), Area: int64(location.AreaID), X: location.PositionX, Y: location.PositionY, Direction: int64(location.Direction)}
	if !p.Valid() {
		return p, wedding.ErrState
	}
	return p, nil
}

// Caller holds wedding.mu, never a town or asset lock. Offline partners are
// included in the same durable pair transaction; only current online sockets
// acquire live participant reservations and receive scene projections.
func (s *Service) expireCurrentWeddingReservationLocked(ctx context.Context, owner wedding.Owner, ceremony *currentWeddingCeremony, pair wedding.PairResult) (wedding.PairResult, error) {
	returns := map[string]wedding.CeremonyReturnLocation{}
	var online []boundGameSessionCharacter
	for _, c := range []r.CharacterRecord{pair.Actor, pair.Partner} {
		state, err := wedding.Read(c)
		if err != nil {
			return wedding.PairResult{}, err
		}
		if c.Stats["town_id"] == 39 && c.Stats["area_id"] == state.CeremonyArea {
			p, err := s.currentWeddingCeremonyReturn(c)
			if err != nil {
				return wedding.PairResult{}, err
			}
			returns[c.CharacterID] = p
		}
		id, _ := wedding.NumericID(c.CharacterID)
		if identity, ok := s.onlineGameSessionCharacterSnapshot(uint16(id)); ok {
			if s.accountIDForSession(identity.session) != c.AccountID {
				return wedding.PairResult{}, wedding.ErrOwnership
			}
			online = append(online, identity)
		}
	}
	req := wedding.PairRequest{Actor: ceremony.first, Partner: ceremony.second, Now: s.gameplayNow(), Revalidate: func() bool {
		for _, identity := range online {
			if !s.boundGameSessionCharacterCurrent(identity) {
				return false
			}
		}
		return true
	}}
	release, err := s.reserveCurrentGuildParticipants(ctx, online)
	if err != nil {
		return wedding.PairResult{}, err
	}
	result, err := owner.ExpireReservation(ctx, req, ceremony.at, returns)
	release()
	if err != nil {
		return wedding.PairResult{}, err
	}
	for _, identity := range online {
		p, moved := returns[strconv.Itoa(int(identity.character))]
		if !moved {
			continue
		}
		location := guild.TeleportLocation{Town: p.Town, Area: p.Area, X: p.X, Y: p.Y, Direction: p.Direction}
		if err := s.queueCurrentWeddingCeremonyMove(identity, location); err != nil {
			s.logGameEvent(identity.session, "game-wedding-expired-return-deferred", "error", err)
		}
	}
	return result, nil
}

func (s *Service) queueCurrentWeddingCeremonyMove(identity boundGameSessionCharacter, location guild.TeleportLocation) error {
	if identity.session.events == nil || identity.session.events.loop == nil {
		return nil
	}
	return s.tryPostGameSessionCharacterEvent(identity.session, "game-wedding-ceremony-committed-move", identity.character, identity.generation, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
		defer cancel()
		c, err := s.currentWeddingCharacter(ctx, identity)
		if errors.Is(err, wedding.ErrOwnership) {
			return nil
		}
		if err != nil {
			return err
		}
		if guild.CharacterTeleportLocation(c) != location {
			return nil // A newer owned movement supersedes this projection.
		}
		body := buildCurrentSoloTeleportMoveBody(currentSoloTeleportRequest{TownID: byte(location.Town), AreaID: byte(location.Area), PositionX: int16(location.X), PositionY: int16(location.Y), Direction: byte(location.Direction)}, uint16(location.Town))
		return s.handleTownSetUserAreaWithCommit(identity.session, body, false, &location)
	})
}

// Reconnect may have projected the ordinary Seria login fallback. A durable
// ready admission can resume its original Church on its original channel;
// unattended/expired reservations never use this route.
func (s *Service) recoverCurrentWeddingReadyChurchLocked(ctx context.Context, identity boundGameSessionCharacter, owner wedding.Owner, pair wedding.PairResult) (wedding.PairResult, bool, error) {
	if s.wedding.ceremoniesStopped {
		return pair, false, wedding.ErrUnavailable
	}
	state, _, err := wedding.ValidatePair(pair.Actor, pair.Partner)
	if err != nil || state.CeremonyReadyAt == 0 || (state.Phase != wedding.Reserved && state.Phase != wedding.InCeremony) {
		return pair, false, err
	}
	if currentTownPresenceChannelID(identity.session) != int(state.CeremonyChannel) || !s.currentWeddingCeremonyEntryReady(identity) {
		return pair, false, nil
	}
	if p, found := s.onlinePlayers.PlayerForCharacter(identity.character); found && p.Session == identity.session && p.TownID == 39 && int64(p.AreaID) == state.CeremonyArea && p.Instance == weddingPairInstance(identity.character, uint16(state.PartnerID)) {
		return pair, true, nil
	}
	area, found := s.townArea(39, state.CeremonyArea)
	if !found || !townMapPathLooksLikeWeddingChurch(area.MapPath) {
		return pair, false, wedding.ErrUnavailable
	}
	position, err := currentWeddingCeremonyPosition(owner.Rules, pair.Actor)
	if err != nil {
		return pair, false, err
	}
	req := wedding.PairRequest{Actor: wedding.Actor{AccountID: pair.Actor.AccountID, CharacterID: pair.Actor.CharacterID}, Partner: wedding.Actor{AccountID: pair.Partner.AccountID, CharacterID: pair.Partner.CharacterID}, Now: s.gameplayNow(), Revalidate: func() bool { return s.currentWeddingCeremonyEntryPresence(identity) }}
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return pair, false, err
	}
	result, err := owner.EnterChurch(ctx, req, state.CeremonyAt, position)
	release()
	if err != nil {
		return pair, false, err
	}
	if err := s.queueCurrentWeddingCeremonyMove(identity, guild.CharacterTeleportLocation(result.Actor)); err != nil {
		return result, false, err
	}
	return result, true, nil
}
