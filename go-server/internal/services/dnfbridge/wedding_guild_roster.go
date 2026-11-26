package dnfbridge

import (
	"context"
	"errors"
	"strconv"
	"time"

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
	return s.primeCurrentWeddingGuildRosterForBoundWithBudget(ctx, ctx, bound, g)
}

func (s *Service) primeCurrentWeddingGuildRosterForBoundWithBudget(primeCtx, writeCtx context.Context, bound boundGameSessionCharacter, g *guild.Guild) error {
	if g == nil || s.options.currentClientVersion != "official-24.9" || !s.boundGameSessionCharacterCurrent(bound) {
		return nil
	}
	if primeCtx != nil && primeCtx.Err() != nil {
		return primeCtx.Err()
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return wedding.ErrUnavailable
	}
	snapshot, found, err := r.LoadCharacterWedding(primeCtx, repos.Character, strconv.Itoa(int(bound.character)))
	viewer := r.CharacterRecord{CharacterID: snapshot.CharacterID, AccountID: snapshot.AccountID, Stats: snapshot.Stats}
	if err != nil {
		return err
	}
	if !found || viewer.AccountID != s.accountIDForSession(bound.session) || !s.boundGameSessionCharacterCurrent(bound) {
		return wedding.ErrOwnership
	}
	return s.primeCurrentWeddingGuildRosterWithBudget(primeCtx, writeCtx, bound.session, viewer, g)
}

func (s *Service) primeCurrentWeddingGuildRoster(ctx context.Context, session *gameSession, viewer r.CharacterRecord, g *guild.Guild) error {
	return s.primeCurrentWeddingGuildRosterWithBudget(ctx, ctx, session, viewer, g)
}

func (s *Service) primeCurrentWeddingGuildRosterWithBudget(primeCtx, writeCtx context.Context, session *gameSession, viewer r.CharacterRecord, g *guild.Guild) error {
	state, e := wedding.Read(viewer)
	if e != nil || state.Phase != wedding.Reserved || g == nil || s.options.currentClientVersion != "official-24.9" {
		return nil
	}
	if primeCtx != nil && primeCtx.Err() != nil {
		return primeCtx.Err()
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || viewer.CharacterID != strconv.Itoa(int(identity.character)) || viewer.AccountID != s.accountIDForSession(session) {
		return wedding.ErrOwnership
	}
	viewerChannel := currentTownPresenceChannelID(session)
	if viewerChannel <= 0 || viewerChannel >= 255 {
		return nil
	}
	viewerMember, hasViewer := g.Members[viewer.CharacterID]
	if !hasViewer || viewerMember.AccountID != viewer.AccountID {
		return nil
	}
	route := s.currentSceneActorRoute()
	var published error
	for _, member := range guild.SortedMembers(g) {
		if primeCtx != nil && primeCtx.Err() != nil {
			published = errors.Join(published, primeCtx.Err())
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
			directory:     route,
			guildID:       g.ID,
			appearanceRev: currentCrossSceneAppearanceRevision(peer.session),
		}
		now := s.guildMode8Now()
		session.guild.mu.Lock()
		alreadyPrimed := session.guild.weddingPrimedPeers[peerCharID].matches(expectedPrime, now)
		session.guild.mu.Unlock()
		if alreadyPrimed {
			continue
		}
		access := currentGuildMemberProfileAccess{
			viewer:        identity,
			actor:         peer,
			viewerAccount: viewer.AccountID,
			actorAccount:  member.AccountID,
			viewerChannel: viewerChannel,
			request: currentGuildMemberProfileRequest{
				channel:   byte(channel),
				character: peerCharID,
				directory: route,
			},
			guildID:      g.ID,
			viewerJoined: viewerMember.JoinedAt,
			actorJoined:  member.JoinedAt,
		}
		if !s.currentGuildMemberProfileIdentities(access) {
			continue
		}
		peerCtx, peerCancel := guildSameChannelPerPeerContext(primeCtx, guildSameChannelPrimePerPeerMaxDuration)
		delivered, writtenRev, transportErr, sendErr := s.sendCurrentGuildRosterPrimeMode8DeliveredWithWriteCtx(peerCtx, writeCtx, access, g)
		peerCancel()
		if sendErr != nil {
			published = errors.Join(published, sendErr)
			if transportErr || (primeCtx != nil && primeCtx.Err() != nil) {
				break
			}
			continue
		}
		if delivered && s.boundGameSessionCharacterCurrent(identity) {
			recorded := weddingPrimedPeer{
				viewerChar:    identity.character,
				viewerGen:     identity.generation,
				actorSession:  access.actor.session,
				actorGen:      access.actor.generation,
				actorChannel:  int(access.request.channel),
				directory:     access.request.directory,
				guildID:       access.guildID,
				appearanceRev: writtenRev,
				expiresAt:     guildPrimedPeerExpiry(s.guildMode8Now(), time.Time{}),
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
