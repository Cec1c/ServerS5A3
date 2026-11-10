package dnfbridge

import (
	"context"
	"strconv"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

// Native 1032/997 resolve the other player through the general character-info
// cache, not through the wedding preview record. Seed the two real bound
// spouses silently before opening either reservation dialog. Mode8 is the
// established cache-only frame; standalone mode0/mode3 are not sent here.
// Caller holds wedding.mu and the existing two-participant asset leases.
func (s *Service) primeCurrentWeddingReservationProfiles(ctx context.Context, from, to boundGameSessionCharacter, a, b r.CharacterRecord) error {
	if _, _, e := wedding.ValidatePair(a, b); e != nil {
		return e
	}
	repos, available := s.repositoryGroup()
	if !available {
		return wedding.ErrUnavailable
	}
	for _, side := range []struct {
		viewer, actor boundGameSessionCharacter
		character     r.CharacterRecord
	}{{from, to, b}, {to, from, a}} {
		if !s.boundGameSessionCharacterCurrent(side.viewer) || !s.boundGameSessionCharacterCurrent(side.actor) {
			return wedding.ErrOwnership
		}
		owner := currentConnectionTownActorOwnerContext(side.viewer.session)
		if owner == 0 {
			return wedding.ErrState
		}
		revision := currentCrossSceneAppearanceRevision(side.actor.session)
		route := s.currentSceneActorRoute()
		mode0, e := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(ctx, side.actor.session, side.actor.character, side.character.Name, side.character, true, owner)
		if e != nil {
			return e
		}
		detailCtx := context.WithValue(ctx, nativePeerProfileSkillsKey{}, strconv.Itoa(int(side.actor.character)))
		if s.buffSwapReapplyEnabled() {
			detailCtx = context.WithValue(detailCtx, nativePeerBuffInspectKey{}, side.character.CharacterID)
		}
		mode3 := s.buildCurrentSelectedUserInfoMode3BodyInContext(detailCtx, side.actor.session, repos.LegacyUserInfo, side.character, true, side.actor.character, owner)
		prime, e := buildCurrentCrossSceneUserInfoBody(mode0, mode3)
		if e != nil {
			return e
		}
		sent, e := s.sendCurrentCrossSceneProfileFrame(ctx, side.viewer.session, side.actor.session, prime, func() bool {
			return s.boundGameSessionCharacterCurrent(side.viewer) && s.boundGameSessionCharacterCurrent(side.actor) && revision == side.actor.session.crossSceneAppearanceRevision && route == s.currentSceneActorRoute() && owner == currentConnectionTownActorOwnerContext(side.viewer.session)
		})
		if e != nil {
			return e
		}
		if !sent {
			return wedding.ErrOwnership
		}
	}
	return nil
}
