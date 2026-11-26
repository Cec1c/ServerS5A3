package dnfbridge

import (
	"context"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	guildSameChannelPrimeMaxDuration        = 1500 * time.Millisecond
	guildSameChannelPrimeMinCtxHeadway      = 1500 * time.Millisecond
	guildSameChannelPrimePerPeerMaxDuration = 500 * time.Millisecond
	guildSameChannelPrimeFailBackoff        = 5 * time.Second
)

const guildSameChannelPrimeRetryMaxEntries = 256

func guildSameChannelPrimeBudgetExhausted(ctx context.Context, started time.Time, maxDuration, minHeadway time.Duration) bool {
	if ctx != nil {
		if ctx.Err() != nil {
			return true
		}
		if minHeadway > 0 {
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < minHeadway {
				return true
			}
		}
	}
	return maxDuration > 0 && time.Since(started) >= maxDuration
}

// newGuildRosterPrimeContext derives a shared absolute deadline for optional
// guild roster priming (wedding + same-channel peers), taking the earlier of
// started+maxDuration and parentDeadline-minHeadway. When the budget is already
// exhausted, ok is false and the returned context is already canceled so no
// priming work starts and the parent context keeps its roster send headway.
func newGuildRosterPrimeContext(
	parent context.Context,
	started time.Time,
	maxDuration time.Duration,
	minHeadway time.Duration,
) (context.Context, context.CancelFunc, bool) {
	if parent == nil {
		parent = context.Background()
	}
	if parent.Err() != nil {
		return parent, func() {}, false
	}
	var deadline time.Time
	hasDeadline := false
	if maxDuration > 0 {
		deadline = started.Add(maxDuration)
		hasDeadline = true
	}
	if parentDeadline, ok := parent.Deadline(); ok {
		headwayLimit := parentDeadline
		if minHeadway > 0 {
			headwayLimit = parentDeadline.Add(-minHeadway)
		}
		if !hasDeadline || headwayLimit.Before(deadline) {
			deadline = headwayLimit
			hasDeadline = true
		}
	}
	if !hasDeadline {
		return parent, func() {}, true
	}
	if !deadline.After(time.Now()) {
		c, cancel := context.WithCancel(parent)
		cancel()
		return c, func() {}, false
	}
	c, cancel := context.WithDeadline(parent, deadline)
	return c, cancel, true
}

func guildSameChannelPerPeerContext(primeCtx context.Context, perPeerMax time.Duration) (context.Context, context.CancelFunc) {
	if primeCtx == nil {
		primeCtx = context.Background()
	}
	if perPeerMax <= 0 {
		return primeCtx, func() {}
	}
	peerDeadline := time.Now().Add(perPeerMax)
	if parentDeadline, ok := primeCtx.Deadline(); ok && !peerDeadline.Before(parentDeadline) {
		return primeCtx, func() {}
	}
	return context.WithDeadline(primeCtx, peerDeadline)
}

func (s *Service) hasIncompleteGuildRosterPrime(bound boundGameSessionCharacter, g *dnfguild.Guild) bool {
	if s == nil || g == nil || bound.session == nil || s.options.currentClientVersion != "official-24.9" {
		return false
	}
	session := bound.session
	session.guild.mu.Lock()
	defer session.guild.mu.Unlock()
	if session.guild.presenceRefreshClosed {
		return false
	}
	sameScope := session.guild.sameChannelPrimeGuildID == g.ID &&
		session.guild.sameChannelPrimeViewerChar == bound.character &&
		session.guild.sameChannelPrimeViewerGen == bound.generation
	return sameScope && (session.guild.sameChannelPrimeIncomplete || session.guild.weddingPrimeIncomplete)
}

func (s *Service) setWeddingGuildRosterPrimeIncomplete(bound boundGameSessionCharacter, g *dnfguild.Guild, incomplete bool) {
	if g == nil || bound.session == nil {
		return
	}
	session := bound.session
	session.guild.mu.Lock()
	if !session.guild.presenceRefreshClosed {
		if session.guild.sameChannelPrimeGuildID != g.ID ||
			session.guild.sameChannelPrimeViewerChar != bound.character ||
			session.guild.sameChannelPrimeViewerGen != bound.generation {
			session.guild.sameChannelPrimeGuildID = g.ID
			session.guild.sameChannelPrimeViewerChar = bound.character
			session.guild.sameChannelPrimeViewerGen = bound.generation
			session.guild.sameChannelPrimeCursor = ""
			session.guild.sameChannelPrimeIncomplete = false
			session.guild.sameChannelPrimeRetryAfter = nil
		}
		session.guild.weddingPrimeIncomplete = incomplete
	}
	session.guild.mu.Unlock()
}

func (s *Service) markSameChannelGuildPrimeIncomplete(bound boundGameSessionCharacter, g *dnfguild.Guild) {
	if g == nil || bound.session == nil {
		return
	}
	session := bound.session
	session.guild.mu.Lock()
	if !session.guild.presenceRefreshClosed {
		if session.guild.sameChannelPrimeGuildID != g.ID ||
			session.guild.sameChannelPrimeViewerChar != bound.character ||
			session.guild.sameChannelPrimeViewerGen != bound.generation {
			session.guild.sameChannelPrimeGuildID = g.ID
			session.guild.sameChannelPrimeViewerChar = bound.character
			session.guild.sameChannelPrimeViewerGen = bound.generation
			session.guild.sameChannelPrimeCursor = ""
			session.guild.weddingPrimeIncomplete = false
			session.guild.sameChannelPrimeRetryAfter = nil
		}
		session.guild.sameChannelPrimeIncomplete = true
	}
	session.guild.mu.Unlock()
}

// canCurrentCrossAreaPartyPeerRequest allows ordinary same-channel party
// invite/join requests (mode 0/12) across different town areas (for example,
// guild-list invites between Hendon Myre and Seria's room). Cross-channel,
// active-dungeon, unpublished-presence, and isolated raid-waiting/guild-hall
// pairs remain strictly blocked.
func (s *Service) canCurrentCrossAreaPartyPeerRequest(
	source, target *gameSession,
	sourceID, targetID uint16,
	mode byte,
) bool {
	if s == nil || s.onlinePlayers == nil || s.options.currentClientVersion != "official-24.9" {
		return false
	}
	if mode != 0 && mode != 12 {
		return false
	}
	if source == nil || target == nil || sourceID == 0 || targetID == 0 || sourceID == targetID {
		return false
	}
	if s.partyChannelMismatch(source, target) {
		return false
	}
	if s.sessionHasActiveDungeonRuntime(source) || s.sessionHasActiveDungeonRuntime(target) {
		return false
	}
	sourceInfo, sourceFound := s.onlinePlayers.PlayerForCharacter(sourceID)
	targetInfo, targetFound := s.onlinePlayers.PlayerForCharacter(targetID)
	if !sourceFound || !targetFound || sourceInfo.Session != source || targetInfo.Session != target {
		return false
	}
	if sourceInfo.ChannelID != targetInfo.ChannelID {
		return false
	}
	if s.currentAntonWaitingArea(sourceInfo.TownID, sourceInfo.AreaID) &&
		s.currentAntonWaitingArea(targetInfo.TownID, targetInfo.AreaID) &&
		(sourceInfo.Instance != targetInfo.Instance || sourceInfo.RaidBattleInstance != targetInfo.RaidBattleInstance) {
		return false
	}
	if s.currentGuildHallArea(sourceInfo.TownID, sourceInfo.AreaID) &&
		s.currentGuildHallArea(targetInfo.TownID, targetInfo.AreaID) &&
		(sourceInfo.Instance == 0 || sourceInfo.Instance != targetInfo.Instance) {
		return false
	}
	return true
}

// primeCurrentSameChannelGuildPartyPeers silently primes online same-channel
// guild members into the viewer's auxiliary actor cache (category 4 via mode8)
// before guild roster frames are delivered, so cross-area guild-list party
// invites find the peer without spawning visible ghost actors in the viewer's
// local town area. Deduplicated via session.guild.weddingPrimedPeers, bounded
// by an absolute priming deadline, and resumable across subsequent roster
// requests when truncated.
func (s *Service) primeCurrentSameChannelGuildPartyPeers(
	ctx context.Context,
	bound boundGameSessionCharacter,
	g *dnfguild.Guild,
) {
	primeCtx, primeCancel, allowed := newGuildRosterPrimeContext(ctx, time.Now(), guildSameChannelPrimeMaxDuration, guildSameChannelPrimeMinCtxHeadway)
	defer primeCancel()
	if !allowed {
		s.markSameChannelGuildPrimeIncomplete(bound, g)
		return
	}
	writeCtx := ctx
	if bound.session != nil {
		writeCtx = bound.session.gameTransportContext()
	}
	s.primeCurrentSameChannelGuildPartyPeersWithBudget(primeCtx, writeCtx, bound, g)
}

func (s *Service) primeCurrentSameChannelGuildPartyPeersWithBudget(
	primeCtx context.Context,
	writeCtx context.Context,
	bound boundGameSessionCharacter,
	g *dnfguild.Guild,
) {
	if s == nil || g == nil || s.options.currentClientVersion != "official-24.9" ||
		!s.boundGameSessionCharacterCurrent(bound) {
		return
	}
	session := bound.session
	if session == nil {
		return
	}
	if primeCtx != nil && primeCtx.Err() != nil {
		s.markSameChannelGuildPrimeIncomplete(bound, g)
		return
	}
	viewerChannel := currentTownPresenceChannelID(session)
	if viewerChannel <= 0 || viewerChannel >= 255 {
		return
	}
	viewerIDStr := strconv.Itoa(int(bound.character))
	viewerAccount := s.accountIDForSession(session)
	viewerMember, hasViewer := g.Members[viewerIDStr]
	if !hasViewer || viewerAccount == "" || viewerMember.AccountID != viewerAccount {
		return
	}
	route := s.currentSceneActorRoute()
	members := dnfguild.SortedMembers(g)
	if len(members) == 0 {
		return
	}

	var resumeFrom string
	var retrySnapshot map[uint16]time.Time
	now := time.Now()
	session.guild.mu.Lock()
	sameScope := session.guild.sameChannelPrimeGuildID == g.ID &&
		session.guild.sameChannelPrimeViewerChar == bound.character &&
		session.guild.sameChannelPrimeViewerGen == bound.generation
	if sameScope {
		resumeFrom = session.guild.sameChannelPrimeCursor
		if len(session.guild.sameChannelPrimeRetryAfter) > 0 {
			retrySnapshot = make(map[uint16]time.Time, len(session.guild.sameChannelPrimeRetryAfter))
			for id, until := range session.guild.sameChannelPrimeRetryAfter {
				if now.Before(until) {
					retrySnapshot[id] = until
				}
			}
		}
	}
	session.guild.mu.Unlock()

	startIdx := 0
	if resumeFrom != "" {
		for i, m := range members {
			if m.CharacterID == resumeFrom {
				startIdx = i
				break
			}
		}
	}

	var (
		incomplete          bool
		resumeCursor        string
		sawDeferred         bool
		firstDeferredCursor string
		failedPeers         map[uint16]time.Time
		clearedPeers        []uint16
		viewerValidated     bool
	)
	recordFailure := func(peerCharID uint16, memberCharID string) {
		sawDeferred = true
		if firstDeferredCursor == "" {
			firstDeferredCursor = memberCharID
		}
		if guildSameChannelPrimeFailBackoff > 0 {
			if failedPeers == nil {
				failedPeers = make(map[uint16]time.Time, 4)
			}
			failedPeers[peerCharID] = time.Now().Add(guildSameChannelPrimeFailBackoff)
		}
	}

	for offset := 0; offset < len(members); offset++ {
		idx := (startIdx + offset) % len(members)
		member := members[idx]
		if primeCtx != nil && primeCtx.Err() != nil {
			incomplete = true
			resumeCursor = member.CharacterID
			break
		}
		if member.CharacterID == viewerIDStr {
			continue
		}
		parsedID, err := strconv.ParseUint(member.CharacterID, 10, 16)
		if err != nil || parsedID == 0 {
			continue
		}
		peerCharID := uint16(parsedID)
		peer, online := s.onlineGameSessionCharacterSnapshot(peerCharID)
		if !online || peer.session == nil || peer.session == session ||
			s.accountIDForSession(peer.session) != member.AccountID {
			continue
		}
		peerChannel := currentTownPresenceChannelID(peer.session)
		if peerChannel != viewerChannel {
			continue
		}
		expectedPrime := weddingPrimedPeer{
			viewerChar:    bound.character,
			viewerGen:     bound.generation,
			actorSession:  peer.session,
			actorGen:      peer.generation,
			actorChannel:  peerChannel,
			directory:     route,
			guildID:       g.ID,
			appearanceRev: currentCrossSceneAppearanceRevision(peer.session),
		}
		primeCheckNow := s.guildMode8Now()
		session.guild.mu.Lock()
		alreadyPrimed := session.guild.weddingPrimedPeers[peerCharID].matches(expectedPrime, primeCheckNow)
		session.guild.mu.Unlock()
		if alreadyPrimed {
			continue
		}
		if retryUntil, deferred := retrySnapshot[peerCharID]; deferred && time.Now().Before(retryUntil) {
			sawDeferred = true
			if firstDeferredCursor == "" {
				firstDeferredCursor = member.CharacterID
			}
			continue
		}
		if primeCtx != nil && primeCtx.Err() != nil {
			incomplete = true
			resumeCursor = member.CharacterID
			break
		}
		// Validate the viewer once per roster sweep (only when there is at least
		// one unprimed peer to send) rather than repeating 2x viewer queries per peer.
		if !viewerValidated {
			repos, ok := s.repositoryGroupFor(primeCtx)
			if !ok || repos.Character == nil {
				return
			}
			vc, found, verr := dnfrepo.LoadCharacterMirrorStats(primeCtx, repos.Character, viewerIDStr, "delete_flag")
			if verr != nil {
				if primeCtx != nil && primeCtx.Err() != nil {
					incomplete = true
					resumeCursor = member.CharacterID
					break
				}
				return
			}
			if !found || vc.CharacterID != viewerIDStr || vc.AccountID != viewerAccount || vc.Stats["delete_flag"] != 0 {
				return
			}
			viewerValidated = true
		}
		access := currentGuildMemberProfileAccess{
			viewer:        bound,
			actor:         peer,
			viewerAccount: viewerAccount,
			actorAccount:  member.AccountID,
			viewerChannel: viewerChannel,
			request: currentGuildMemberProfileRequest{
				channel:   byte(peerChannel),
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
		if !s.boundGameSessionCharacterCurrent(bound) {
			return
		}
		if transportErr {
			incomplete = true
			resumeCursor = member.CharacterID
			break
		}
		if sendErr != nil || !delivered {
			recordFailure(peerCharID, member.CharacterID)
			if primeCtx != nil && primeCtx.Err() != nil {
				incomplete = true
				nextIdx := (idx + 1) % len(members)
				resumeCursor = members[nextIdx].CharacterID
				break
			}
			continue
		}
		clearedPeers = append(clearedPeers, peerCharID)
		recorded := weddingPrimedPeer{
			viewerChar:    bound.character,
			viewerGen:     bound.generation,
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

	saveNow := time.Now()
	session.guild.mu.Lock()
	if !session.guild.presenceRefreshClosed {
		if session.guild.sameChannelPrimeGuildID != g.ID ||
			session.guild.sameChannelPrimeViewerChar != bound.character ||
			session.guild.sameChannelPrimeViewerGen != bound.generation {
			session.guild.sameChannelPrimeGuildID = g.ID
			session.guild.sameChannelPrimeViewerChar = bound.character
			session.guild.sameChannelPrimeViewerGen = bound.generation
			session.guild.sameChannelPrimeRetryAfter = nil
		}
		if incomplete {
			session.guild.sameChannelPrimeIncomplete = true
			session.guild.sameChannelPrimeCursor = resumeCursor
		} else if sawDeferred {
			session.guild.sameChannelPrimeIncomplete = true
			session.guild.sameChannelPrimeCursor = firstDeferredCursor
		} else {
			session.guild.sameChannelPrimeIncomplete = false
			session.guild.sameChannelPrimeCursor = ""
		}
		if len(session.guild.sameChannelPrimeRetryAfter) > 0 {
			for _, id := range clearedPeers {
				delete(session.guild.sameChannelPrimeRetryAfter, id)
			}
			for id, until := range session.guild.sameChannelPrimeRetryAfter {
				if !saveNow.Before(until) {
					delete(session.guild.sameChannelPrimeRetryAfter, id)
				}
			}
		}
		if len(failedPeers) > 0 {
			if session.guild.sameChannelPrimeRetryAfter == nil ||
				len(session.guild.sameChannelPrimeRetryAfter)+len(failedPeers) > guildSameChannelPrimeRetryMaxEntries {
				session.guild.sameChannelPrimeRetryAfter = make(map[uint16]time.Time, len(failedPeers))
			}
			for id, until := range failedPeers {
				session.guild.sameChannelPrimeRetryAfter[id] = until
			}
		}
	}
	session.guild.mu.Unlock()
}

// primeCurrentCrossAreaPartyInviteActor sends a single mode8 cross-scene
// USERINFO priming frame so the invitee's auxiliary actor cache (category 4)
// has the inviter's real name, level, and server group before NOTI7 arrives,
// without spawning a ghost character in the invitee's local town scene.
func (s *Service) primeCurrentCrossAreaPartyInviteActor(receiver, inviter *gameSession, inviterID uint16) {
	if s == nil || receiver == nil || inviter == nil || inviterID == 0 ||
		s.options.currentClientVersion != "official-24.9" {
		return
	}
	owner := currentConnectionTownActorOwnerContext(receiver)
	if owner == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	loadedID, name, character, found := s.characterForEnter(ctx, inviter, inviterID)
	if !found || loadedID != inviterID || character.CharacterID != strconv.Itoa(int(inviterID)) || name == "" {
		return
	}
	buildCtx := ctx
	if s.equipMode0RepositoryMemoEnabled() {
		buildCtx, _ = withEquipMode0RepositoryMemo(ctx)
	}
	mode0, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(buildCtx, inviter, inviterID, name, character, true, owner)
	if err != nil {
		s.logGameEvent(receiver, "game-party-cross-area-invite-actor-prime-failed",
			"inviter_char_id", inviterID, "stage", "mode0", "error", err)
		return
	}
	var legacyRepo dnfrepo.LegacyUserInfoRepository
	if repositories, ok := s.repositoryGroupFor(buildCtx); ok {
		legacyRepo = repositories.LegacyUserInfo
	}
	mode3 := s.buildCurrentSelectedUserInfoMode3BodyInContext(buildCtx, inviter, legacyRepo, character, true, inviterID, owner)
	priming, err := buildCurrentCrossSceneUserInfoBody(mode0, mode3)
	if err != nil {
		s.logGameEvent(receiver, "game-party-cross-area-invite-actor-prime-failed",
			"inviter_char_id", inviterID, "stage", "mode8", "error", err)
		return
	}
	if err := s.sendGameUpperRawClassCodec(receiver, uint16(dnfenum.CmdPacketSetUDPIPPort), priming, 0, true); err != nil {
		s.logGameEvent(receiver, "game-party-cross-area-invite-actor-prime-failed",
			"inviter_char_id", inviterID, "stage", "send", "error", err)
		return
	}
	s.logGameEvent(receiver, "game-party-cross-area-invite-actor-primed",
		"inviter_char_id", inviterID,
		"receiver_char_id", selectedCharacterID(receiver),
		"owner_context", owner,
		"body_len", len(priming))
}
