package dnfbridge

import (
	"context"
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// One refresh owns one batch. It is synchronous and is never retained in a
// session or shared across refreshes. Expensive typed actor records are built
// once per actor/connection namespace, not once per recipient.
type currentCrossSceneProfileBatch struct {
	service *Service
	ctx     context.Context
	route   byte
	peers   map[uint16]*currentCrossSceneProfilePeer
	bodies  map[currentCrossSceneProfileKey]currentCrossSceneProfileBody
	sent    map[currentCrossSceneProfileDelivery]bool
}

type currentCrossSceneProfilePeer struct {
	bound     boundGameSessionCharacter
	account   string
	channel   int
	character dnfrepo.CharacterRecord
}

type currentCrossSceneProfileKey struct {
	actor uint16
	owner byte
}

type currentCrossSceneProfileBody struct {
	data     []byte
	revision uint64
}

func currentCrossSceneAppearanceRevision(session *gameSession) uint64 {
	session.crossSceneAppearanceMu.Lock()
	defer session.crossSceneAppearanceMu.Unlock()
	return session.crossSceneAppearanceRevision
}

func invalidateCurrentCrossSceneAppearance(session *gameSession) {
	session.crossSceneAppearanceMu.Lock()
	session.crossSceneAppearanceRevision++
	session.crossSceneAppearanceMu.Unlock()
}

type currentCrossSceneProfileDelivery struct {
	viewer uint16
	currentCrossSceneProfileKey
}

func newCurrentCrossSceneProfileBatch(s *Service, ctx context.Context) *currentCrossSceneProfileBatch {
	// The existing read memo is limited to this refresh and invalidates on
	// writes. Authentication captures the exact returned character/account;
	// every delivery still checks live bindings and central membership.
	ctx, _ = withEquipMode0RepositoryMemo(ctx)
	return &currentCrossSceneProfileBatch{
		service: s, ctx: ctx, route: s.currentSceneActorRoute(),
		peers:  make(map[uint16]*currentCrossSceneProfilePeer),
		bodies: make(map[currentCrossSceneProfileKey]currentCrossSceneProfileBody),
		sent:   make(map[currentCrossSceneProfileDelivery]bool),
	}
}

func (p *currentCrossSceneProfilePeer) current(s *Service) bool {
	return p != nil && s.boundGameSessionCharacterCurrent(p.bound) &&
		p.bound.session.accountID == p.account &&
		currentTownPresenceChannelID(p.bound.session) == p.channel
}

func (b *currentCrossSceneProfileBatch) peer(session *gameSession) (*currentCrossSceneProfilePeer, error) {
	s := b.service
	bound, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return nil, nil
	}
	if cached, ok := b.peers[bound.character]; ok {
		if cached.bound == bound && cached.current(s) {
			return cached, nil
		}
		// A refresh never migrates its cached ownership to a replacement socket.
		return nil, nil
	}
	account, channel := session.accountID, currentTownPresenceChannelID(session)
	if strings.TrimSpace(account) == "" || channel == 0 {
		return nil, nil
	}
	repos, ok := s.repositoryGroupFor(b.ctx)
	if !ok || repos.Character == nil {
		return nil, nil
	}
	key := strconv.Itoa(int(bound.character))
	character, found, err := repos.Character.Load(b.ctx, key)
	if err != nil {
		return nil, err
	}
	if !found || character.CharacterID != key || character.AccountID != account {
		return nil, nil
	}
	peer := &currentCrossSceneProfilePeer{bound: bound, account: account, channel: channel, character: character}
	if !peer.current(s) {
		return nil, nil
	}
	b.peers[bound.character] = peer
	return peer, nil
}

func (b *currentCrossSceneProfileBatch) send(viewer, actor *gameSession, owner byte) error {
	s := b.service
	viewerID, actorID := selectedCharacterID(viewer), selectedCharacterID(actor)
	if owner == 0 || owner != currentConnectionTownActorOwnerContext(viewer) ||
		b.route != s.currentSceneActorRoute() ||
		!s.currentCrossSceneProfileMembership(viewer, actor, viewerID, actorID) {
		return nil
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}
	v, err := b.peer(viewer)
	if err != nil || v == nil {
		return err
	}
	a, err := b.peer(actor)
	if err != nil || a == nil || v.channel != a.channel {
		return err
	}
	key := currentCrossSceneProfileKey{actor: actorID, owner: owner}
	delivery := currentCrossSceneProfileDelivery{viewer: viewerID, currentCrossSceneProfileKey: key}
	if b.sent[delivery] {
		return nil
	}
	body, ready := b.bodies[key]
	if !ready {
		body.revision = currentCrossSceneAppearanceRevision(actor)
		if a.character.Name == "" {
			return nil
		}
		mode0, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(b.ctx, actor, actorID, a.character.Name, a.character, true, owner)
		if err != nil {
			return err
		}
		var legacyRepo dnfrepo.LegacyUserInfoRepository
		if repos, ok := s.repositoryGroupFor(b.ctx); ok {
			legacyRepo = repos.LegacyUserInfo
		}
		detailCtx := b.ctx
		if buffSwapReapplyEnabled() {
			detailCtx = context.WithValue(detailCtx, nativePeerBuffInspectKey{}, strconv.Itoa(int(actorID)))
		}
		mode3 := s.buildCurrentSelectedUserInfoMode3BodyInContext(detailCtx, actor, legacyRepo, a.character, true, actorID, owner)
		body.data, err = buildCurrentCrossSceneUserInfoBody(mode0, mode3)
		if err != nil {
			return err
		}
		b.bodies[key] = body
	}
	deliveryBody, err := s.currentTownPartyProfileWithHP(viewer, actor, body.data)
	if err != nil {
		return err
	}
	delivered, err := s.sendCurrentCrossSceneProfileFrame(b.ctx, viewer, actor, deliveryBody, func() bool {
		return body.revision == actor.crossSceneAppearanceRevision &&
			v.current(s) && a.current(s) && b.route == s.currentSceneActorRoute() &&
			owner == currentConnectionTownActorOwnerContext(viewer) &&
			s.currentCrossSceneProfileMembership(viewer, actor, viewerID, actorID)
	})
	if err != nil || !delivered {
		return err
	}
	b.sent[delivery] = true
	s.logGameEvent(viewer, "game-peer-userinfo-profile-refresh-sent",
		"source_char_id", viewerID, "target_char_id", actorID,
		"owner_context", owner, "body_len", len(body.data))
	return nil
}
