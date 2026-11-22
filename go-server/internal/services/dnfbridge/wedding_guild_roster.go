package dnfbridge

import (
	"context"
	"errors"
	"strconv"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

// The current native invitation list skips a guild record whose +3c general
// character-info link is missing. Roster140 stores metadata but does not create
// that object. Prime real online guild members with the established silent
// mode8 path before the roster is decoded and linked. Offline membership is
// retained in the roster; it is never fabricated into an online game session.
func (s *Service) primeCurrentWeddingGuildRosterForBound(ctx context.Context, bound boundGameSessionCharacter, g *guild.Guild) error {
	if g == nil || s.options.currentClientVersion != "official-24.9" || !s.boundGameSessionCharacterCurrent(bound) {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return wedding.ErrUnavailable
	}
	snapshot, found, err := r.LoadCharacterWedding(ctx, repos.Character, strconv.Itoa(int(bound.character)))
	viewer := r.CharacterRecord{CharacterID: snapshot.CharacterID, AccountID: snapshot.AccountID, Stats: snapshot.Stats}
	if err != nil {
		return err
	}
	if !found || viewer.AccountID != s.accountIDForSession(bound.session) || !s.boundGameSessionCharacterCurrent(bound) {
		return wedding.ErrOwnership
	}
	return s.primeCurrentWeddingGuildRoster(ctx, bound.session, viewer, g)
}

func (s *Service) primeCurrentWeddingGuildRoster(ctx context.Context, session *gameSession, viewer r.CharacterRecord, g *guild.Guild) error {
	state, e := wedding.Read(viewer)
	if e != nil || state.Phase != wedding.Reserved || g == nil || s.options.currentClientVersion != "official-24.9" {
		return nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || viewer.CharacterID != strconv.Itoa(int(identity.character)) || viewer.AccountID != s.accountIDForSession(session) {
		return wedding.ErrOwnership
	}
	var published error
	for _, member := range guild.SortedMembers(g) {
		if ctx != nil && ctx.Err() != nil {
			published = errors.Join(published, ctx.Err())
			break
		}
		if member.CharacterID == viewer.CharacterID || member.CharacterID == strconv.FormatInt(state.PartnerID, 10) {
			continue
		}
		n, e := wedding.NumericID(member.CharacterID)
		if e != nil {
			published = errors.Join(published, e)
			continue
		}
		peerCharID := uint16(n)
		peer, online := s.onlineGameSessionCharacterSnapshot(peerCharID)
		if !online || s.accountIDForSession(peer.session) != member.AccountID {
			continue
		}
		channel := currentTownPresenceChannelID(peer.session)
		if channel <= 0 || channel >= 255 {
			continue
		}
		expectedPrime := weddingPrimedPeer{
			viewerChar:    identity.character,
			viewerGen:     identity.generation,
			actorSession:  peer.session,
			actorGen:      peer.generation,
			actorChannel:  channel,
			guildID:       g.ID,
			appearanceRev: currentCrossSceneAppearanceRevision(peer.session),
		}
		session.guild.mu.Lock()
		alreadyPrimed := session.guild.weddingPrimedPeers[peerCharID] == expectedPrime
		session.guild.mu.Unlock()
		if alreadyPrimed {
			continue
		}
		access, e := s.loadCurrentGuildMemberProfileAccess(ctx, session, currentGuildMemberProfileRequest{channel: byte(channel), character: peerCharID, directory: s.currentSceneActorRoute()})
		if e != nil {
			published = errors.Join(published, e)
			continue
		}
		if access.guildID != g.ID || !s.boundGameSessionCharacterCurrent(identity) {
			continue
		}
		delivered, writtenRev, sendErr := s.sendCurrentGuildMemberProfileWithPolicyDelivered(ctx, access, true)
		if sendErr != nil {
			published = errors.Join(published, sendErr)
			continue
		}
		if delivered && s.boundGameSessionCharacterCurrent(identity) {
			recorded := weddingPrimedPeer{
				viewerChar:    identity.character,
				viewerGen:     identity.generation,
				actorSession:  access.actor.session,
				actorGen:      access.actor.generation,
				actorChannel:  int(access.request.channel),
				guildID:       access.guildID,
				appearanceRev: writtenRev,
			}
			session.guild.mu.Lock()
			if !session.guild.presenceRefreshClosed {
				if session.guild.weddingPrimedPeers == nil || len(session.guild.weddingPrimedPeers) >= 1024 {
					session.guild.weddingPrimedPeers = make(map[uint16]weddingPrimedPeer, 32)
				}
				session.guild.weddingPrimedPeers[peerCharID] = recorded
			}
			session.guild.mu.Unlock()
		}
	}
	return published
}
