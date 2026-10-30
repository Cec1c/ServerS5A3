package dnfbridge

import (
	"context"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// sendCurrentCrossScenePeerProfile primes the auxiliary actor cache with a
// mode8 pair, then writes the ordinary mode3 inspection body. The client treats
// the mode8 frame as cache work and the separate mode3 frame as the explicit
// inspection UI request. Both records use the receiver's retained connection
// context. Ordinary scene mode0 and same-scene inspection stay on their
// existing paths.
func (s *Service) sendCurrentCrossScenePeerProfile(ctx context.Context, viewer, actor *gameSession, viewerID, actorID uint16) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	viewerBound, viewerOK := s.boundGameSessionCharacterSnapshot(viewer)
	actorBound, actorOK := s.boundGameSessionCharacterSnapshot(actor)
	if !viewerOK || !actorOK || viewerBound.character != viewerID || actorBound.character != actorID ||
		!s.currentCrossSceneProfileAllowed(ctx, viewer, actor, viewerID, actorID) {
		return nil
	}
	owner := currentConnectionTownActorOwnerContext(viewer)
	if owner == 0 {
		return nil
	}
	revision := currentCrossSceneAppearanceRevision(actor)
	route := s.currentSceneActorRoute()
	loadedID, name, character, found := s.characterForEnter(ctx, actor, actorID)
	if !found || loadedID != actorID || character.CharacterID != strconv.Itoa(int(actorID)) || name == "" {
		return nil
	}
	mode0, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(ctx, actor, actorID, name, character, true, owner)
	if err != nil {
		return err
	}
	var legacyRepo dnfrepo.LegacyUserInfoRepository
	if repositories, ok := s.repositoryGroupFor(ctx); ok {
		legacyRepo = repositories.LegacyUserInfo
	}
	ctx = context.WithValue(ctx, nativePeerProfileSkillsKey{}, strconv.Itoa(int(actorID)))
	if s.buffSwapReapplyEnabled() {
		ctx = context.WithValue(ctx, nativePeerBuffInspectKey{}, strconv.Itoa(int(actorID)))
	}
	mode3 := s.buildCurrentSelectedUserInfoMode3BodyInContext(ctx, actor, legacyRepo, character, true, actorID, owner)
	priming, err := buildCurrentCrossSceneUserInfoBody(mode0, mode3)
	if err != nil {
		return err
	}
	// Repository reads may yield to a disconnect, character switch or leave.
	// Revalidate both frozen socket identities and the authoritative relationship.
	if !s.boundGameSessionCharacterCurrent(viewerBound) || !s.boundGameSessionCharacterCurrent(actorBound) ||
		owner != currentConnectionTownActorOwnerContext(viewer) ||
		!s.currentCrossSceneProfileAllowed(ctx, viewer, actor, viewerID, actorID) {
		return nil
	}
	// Serialize both writes with wear invalidation, without holding this lock
	// during repository reads. An older inspection must not overwrite a newer
	// automatic appearance refresh, and the priming/inspection pair must not be
	// split by an appearance revision change.
	delivered, err := s.sendCurrentCrossSceneProfileFrames(ctx, viewer, actor, [][]byte{priming, mode3}, func() bool {
		return revision == actor.crossSceneAppearanceRevision && route == s.currentSceneActorRoute() &&
			s.boundGameSessionCharacterCurrent(viewerBound) && s.boundGameSessionCharacterCurrent(actorBound) &&
			owner == currentConnectionTownActorOwnerContext(viewer) &&
			s.currentCrossSceneProfileMembership(viewer, actor, viewerID, actorID)
	})
	if err != nil || !delivered {
		return err
	}
	s.rememberCurrentPeerProfile(viewerBound, actorBound, owner)
	s.logGameEvent(viewer, "game-peer-userinfo-profile-refresh-sent",
		"source_char_id", viewerID, "target_char_id", actorID,
		"mode", currentCrossSceneMode8Subtype, "msg_id", uint16(dnfenum.CmdPacketSetUDPIPPort),
		"classification", 0, "body_len", len(priming), "mode3_body_len", len(mode3), "owner_context", owner,
		"wire_order", "mode8_mode3",
		"body_source", "current_exe_mode8_cross_scene_profile")
	return nil
}
