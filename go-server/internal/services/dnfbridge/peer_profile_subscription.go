package dnfbridge

import (
	"context"
	"sort"
	"strconv"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// One latest inspection per live viewer. This grants no new visibility: every
// refresh rechecks scene/party/raid permission and both connection generations.
// The native receiver refreshes an existing matching window only; it does not
// reopen a closed window. No per-viewer timers or durable subscriptions exist.
type currentPeerProfileSubscription struct {
	viewer        boundGameSessionCharacter
	actor         boundGameSessionCharacter
	owner         byte
	viewerAccount string
	actorAccount  string
	channel       int
	route         byte
}

// s.mu is held by the binding lifecycle. Release old sockets retained by the
// single current subscription when either side disconnects or changes roles.
func (s *Service) clearCurrentPeerProfileReferencesLocked(session *gameSession) {
	session.peerProfileSubscription = currentPeerProfileSubscription{}
	for _, viewer := range s.gameSessions {
		if viewer.peerProfileSubscription.actor.session == session {
			viewer.peerProfileSubscription = currentPeerProfileSubscription{}
		}
	}
}

func (s *Service) rememberCurrentPeerProfile(v, a boundGameSessionCharacter, owner byte) {
	if s.options.currentClientVersion != "official-24.9" {
		return
	}
	if v.session == nil || a.session == nil || v.character == 0 || a.character == 0 || v.character == a.character {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Keep the identities captured before repository reads and socket writes.
	// A response that raced a rebind must never subscribe the new character.
	for _, identity := range []boundGameSessionCharacter{v, a} {
		if s.gameSessions[identity.character] != identity.session || identity.session.selectedCharacterID != identity.character || identity.session.characterGeneration != identity.generation {
			return
		}
	}
	v.session.peerProfileSubscription = currentPeerProfileSubscription{viewer: v, actor: a, owner: owner, viewerAccount: v.session.accountID, actorAccount: a.session.accountID, channel: currentTownPresenceChannelID(v.session), route: s.currentSceneActorRoute()}
}

func (s *Service) currentPeerProfileSceneAllowed(ctx context.Context, viewer, actor *gameSession, viewerID, actorID uint16) bool {
	if currentTownPresenceChannelID(viewer) == 0 || currentTownPresenceChannelID(viewer) != currentTownPresenceChannelID(actor) {
		return false
	}
	if s.sessionHasActiveDungeonRuntime(viewer) || s.sessionHasActiveDungeonRuntime(actor) {
		// Dungeon peers already use the central party/raid profile broadcast.
		return false
	}
	return s.onlinePlayers != nil && s.onlinePlayers.PeerInSameArea(viewerID, actorID)
}

func (s *Service) broadcastCurrentInspectedProfile(ctx context.Context, actor *gameSession) {
	identity, ok := s.boundGameSessionCharacterSnapshot(actor)
	if !ok {
		return
	}
	s.mu.Lock()
	var viewers []currentPeerProfileSubscription
	for _, viewer := range s.gameSessions {
		if sub := viewer.peerProfileSubscription; sub.actor == identity {
			viewers = append(viewers, sub)
		}
	}
	s.mu.Unlock()
	sort.Slice(viewers, func(i, j int) bool { return viewers[i].viewer.character < viewers[j].viewer.character })
	for _, sub := range viewers {
		if err := s.sendCurrentInspectedProfile(ctx, sub); err != nil {
			s.logGameEvent(sub.viewer.session, "game-peer-inspection-refresh-failed", "target_char_id", identity.character, "error_kind", currentCrossSceneProfileErrorKind(err))
		}
	}
}

func (s *Service) sendCurrentInspectedProfile(ctx context.Context, sub currentPeerProfileSubscription) error {
	v, a := sub.viewer, sub.actor
	if !s.boundGameSessionCharacterCurrent(v) || !s.boundGameSessionCharacterCurrent(a) {
		return nil
	}
	if sub.viewerAccount == "" || sub.actorAccount == "" || sub.viewerAccount != v.session.accountID || sub.actorAccount != a.session.accountID || sub.channel != currentTownPresenceChannelID(v.session) || sub.route != s.currentSceneActorRoute() {
		return nil
	}
	// Existing party/raid broadcasts already carry exactly this quiet frame.
	if s.currentCrossSceneProfileMembership(v.session, a.session, v.character, a.character) {
		return nil
	}
	if !s.currentPeerProfileSceneAllowed(ctx, v.session, a.session, v.character, a.character) {
		return nil
	}
	owner := sub.owner
	if owner != currentTownRemoteActorOwnerContext(v.session, a.character) {
		return nil
	}
	revision := currentCrossSceneAppearanceRevision(a.session)
	id, name, character, found := s.characterForEnter(ctx, a.session, a.character)
	if !found || id != a.character || character.CharacterID != strconv.Itoa(int(id)) || character.AccountID != sub.actorAccount {
		return nil
	}
	mode0, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(ctx, a.session, id, name, character, true, owner)
	if err != nil {
		return err
	}
	var legacy dnfrepo.LegacyUserInfoRepository
	if repos, ok := s.repositoryGroupFor(ctx); ok && repos.Character != nil {
		legacy = repos.LegacyUserInfo
		viewerRecord, found, err := repos.Character.Load(ctx, strconv.Itoa(int(v.character)))
		if err != nil {
			return err
		}
		if !found || viewerRecord.CharacterID != strconv.Itoa(int(v.character)) || viewerRecord.AccountID != sub.viewerAccount {
			return nil
		}
	} else {
		return nil
	}
	ctx = context.WithValue(ctx, nativePeerProfileSkillsKey{}, strconv.Itoa(int(id)))
	if s.buffSwapReapplyEnabled() {
		ctx = context.WithValue(ctx, nativePeerBuffInspectKey{}, strconv.Itoa(int(id)))
	}
	mode3 := s.buildCurrentSelectedUserInfoMode3BodyInContext(ctx, a.session, legacy, character, true, id, owner)
	body, err := buildCurrentCrossSceneUserInfoBody(mode0, mode3)
	if err != nil {
		return err
	}
	_, err = s.sendCurrentCrossSceneProfileFrame(ctx, v.session, a.session, body, func() bool {
		s.mu.Lock()
		current := v.session.peerProfileSubscription == sub
		s.mu.Unlock()
		return current && revision == a.session.crossSceneAppearanceRevision && sub.route == s.currentSceneActorRoute() &&
			s.boundGameSessionCharacterCurrent(v) && s.boundGameSessionCharacterCurrent(a) &&
			sub.viewerAccount == v.session.accountID && sub.actorAccount == a.session.accountID &&
			sub.channel == currentTownPresenceChannelID(v.session) &&
			owner == currentTownRemoteActorOwnerContext(v.session, a.character) &&
			s.currentPeerProfileSceneAllowed(ctx, v.session, a.session, v.character, a.character)
	})
	return err
}
