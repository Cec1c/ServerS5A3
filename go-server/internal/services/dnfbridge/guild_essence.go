package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) currentGuildEssenceCatalog() (*guild.EssenceCatalog, error) {
	s.guildDungeonCatalogMu.Lock()
	defer s.guildDungeonCatalogMu.Unlock()
	if s.guildEssenceCatalog != nil {
		return s.guildEssenceCatalog, nil
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return nil, err
	}
	c, err := guild.LoadEssenceCatalog(items.source)
	if err != nil {
		return nil, err
	}
	s.guildEssenceCatalog = c
	return c, nil
}

// Constructor defaults for an empty guild, plus the two committed balances.
func buildCurrentGuildEssenceSnapshot(balance guild.EssenceBalance) []byte {
	body := make([]byte, 0x895)
	body[1] = 0xff
	binary.LittleEndian.PutUint16(body[0x876:], 1)
	binary.LittleEndian.PutUint16(body[0x878:], 1)
	binary.LittleEndian.PutUint32(body[0x17:], balance.AP)
	binary.LittleEndian.PutUint32(body[0x1b:], balance.GP)
	return body
}

// Income and reconnect project existing ownership rather than clearing it.
// This does not assign initial level/skills or acquire/select a guardian.
func buildCurrentGuildGuardianSnapshot(g *guild.Guild) ([]byte, error) {
	if g == nil {
		return buildCurrentGuildEssenceSnapshot(guild.EssenceBalance{}), nil
	}
	if err := g.Guardians.Validate(); err != nil {
		return nil, err
	}
	body := buildCurrentGuildEssenceSnapshot(g.Essence)
	if err := g.Placements.Validate(); err != nil {
		return nil, err
	}
	for i, cost := range currentGuildPlacementCosts(g) {
		binary.LittleEndian.PutUint32(body[0x852+4*i:], cost)
	}
	for index := uint8(0); index < 2; index++ {
		level, err := guild.MerchantLevel(g, index)
		if err != nil {
			return nil, err
		}
		binary.LittleEndian.PutUint16(body[0x876+2*int(index):], uint16(level))
	}
	if g.Guardians == nil {
		return body, nil
	}
	binary.LittleEndian.PutUint32(body[0x44:], g.Guardians.ActiveID)
	for i, row := range g.Guardians.Owned {
		at := 0x7f8 + 18*i
		binary.LittleEndian.PutUint32(body[at:], row.ID)
		body[at+4] = row.Level
		copy(body[at+5:at+10], row.Skills[:])
		binary.LittleEndian.PutUint32(body[at+10:], row.APProgress)
		binary.LittleEndian.PutUint32(body[at+14:], row.GPProgress)
	}
	return body, nil
}

func buildCurrentGuildEssenceNotice(ap uint32) []byte {
	body := make([]byte, 8)
	binary.LittleEndian.PutUint32(body, ap)
	return body
}

func appendUniqueGuildID(ids []uint32, guildID uint32) []uint32 {
	if guildID == 0 {
		return ids
	}
	for _, id := range ids {
		if id == guildID {
			return ids
		}
	}
	return append(ids, guildID)
}

func guildIDInSlice(guildID uint32, expected []uint32) bool {
	if guildID == 0 {
		return false
	}
	for _, id := range expected {
		if id == guildID {
			return true
		}
	}
	return false
}

func (s *Service) writeCurrentGuildEssenceSnapshot(session *gameSession) error {
	return s.writeCurrentGuildEssenceSnapshotForGuilds(session, nil)
}

func (s *Service) writeCurrentGuildEssenceSnapshotForGuild(session *gameSession, expectedGuildID uint32) error {
	if expectedGuildID == 0 {
		return s.writeCurrentGuildEssenceSnapshotForGuilds(session, nil)
	}
	return s.writeCurrentGuildEssenceSnapshotForGuilds(session, []uint32{expectedGuildID})
}

func (s *Service) writeCurrentGuildEssenceSnapshotForGuilds(session *gameSession, expectedGuildIDs []uint32) error {
	if !s.options.guildEssenceEnabled {
		return nil
	}
	// A practice's native local actors use its frozen 191 table. Publish later
	// guild changes when the character returns, instead of changing that table
	// while the server still owns the original combat snapshot.
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if len(expectedGuildIDs) > 0 {
		if !bound || session.loginRevoked.Load() || (session.ctx != nil && session.ctx.Err() != nil) {
			return nil
		}
		session.guild.placement.mu.Lock()
		defer session.guild.placement.mu.Unlock()
		if p := &session.guild.placement; p.frozenPractice != (boundGameSessionCharacter{}) {
			if p.frozenPractice == identity {
				return nil
			}
			p.frozenPractice = boundGameSessionCharacter{}
			p.identity = boundGameSessionCharacter{}
		}
		repos, ok := s.repositoryGroup()
		if !ok || repos.Settings == nil {
			return guild.ErrRepositoryUnavailable
		}
		ctx, cancel := s.currentGuildContext()
		defer cancel()
		// Acquire wireMu before reading the authoritative guild view so a wire
		// wait overlapping a leave/kick/rebind observes the latest membership
		// and writes 1297 + 191/777 atomically without interleaving NOTI151.
		if hook := testBeforeGuildPeerWireLock; hook != nil {
			hook("essence")
		}
		if err := s.lockGameWire(session, ctx); err != nil {
			return err
		}
		defer session.wireMu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		if session.channelExitWireClosed || session.loginRevoked.Load() || !s.boundGameSessionCharacterCurrent(identity) {
			return nil
		}
		character, err := s.currentGuildOwnedSelectedIdentity(session)
		if err != nil {
			return err
		}
		view, err := guild.ReadHallView(ctx, repos.Settings)
		if err != nil {
			return err
		}
		owner := r.GuildOwner{AccountID: character.AccountID, CharacterID: character.CharacterID}
		projection, found := view.Projection(owner)
		if !found || !guildIDInSlice(projection.GuildID, expectedGuildIDs) {
			return nil
		}
		g := currentGuildHallDisplay(projection)
		body, err := buildCurrentGuildGuardianSnapshot(g)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if session.channelExitWireClosed || session.loginRevoked.Load() || !s.boundGameSessionCharacterCurrent(identity) {
			return nil
		}
		// Use an independent transport write budget (gameTransportWriteTimeout)
		// rather than the remaining slice of the 3s storage/prep context so slow
		// DB reads cannot prematurely close a healthy player's connection.
		finishWrite, err := beginGameWriteBudgetLocked(session, nil)
		if err != nil {
			return err
		}
		defer finishWrite()
		sendLocked := func(op uint16, payload []byte) error {
			if err := s.sendGameUpperRawClassCodecLocked(session, op, payload, 0, true); err != nil {
				session.closeGameTransport()
				return err
			}
			return nil
		}
		p := &session.guild.placement
		initial := g == nil || p.identity != identity || p.guildID != g.ID || p.rows == nil
		if initial {
			if err = sendLocked(1297, body); err != nil {
				return err
			}
			return s.writeCurrentGuildPlacementProjectionWithSendLocked(session, g, identity, sendLocked)
		}
		if err = s.writeCurrentGuildPlacementProjectionWithSendLocked(session, g, identity, sendLocked); err != nil {
			return err
		}
		return sendLocked(1297, body)
	}
	owner := r.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(session.selectedCharacterID))}
	session.guild.placement.mu.Lock()
	defer session.guild.placement.mu.Unlock()
	if p := &session.guild.placement; p.frozenPractice != (boundGameSessionCharacter{}) {
		if bound && p.frozenPractice == identity {
			return nil
		}
		p.frozenPractice = boundGameSessionCharacter{}
		p.identity = boundGameSessionCharacter{}
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		return guild.ErrRepositoryUnavailable
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	view, err := guild.ReadHallView(ctx, repos.Settings)
	if err != nil {
		return err
	}
	projection, found := view.Projection(owner)
	var g *guild.Guild
	if found {
		g = currentGuildHallDisplay(projection)
	}
	body, err := buildCurrentGuildGuardianSnapshot(g)
	if err != nil {
		return err
	}
	if bound && !s.boundGameSessionCharacterCurrent(identity) {
		return guild.ErrCharacterNotFound
	}
	if bound {
		p := &session.guild.placement
		// Initialize the guardian/merchant scene before 191 copies its tail.
		// Incremental 777 packets run first so maintenance-only changes do not
		// trigger a second full-base rebuild in 1297.
		initial := g == nil || p.identity != identity || p.guildID != g.ID || p.rows == nil
		if initial {
			if err = s.sendGameUpperRawClass(session, 1297, body, 0); err != nil {
				return err
			}
			return s.writeCurrentGuildPlacementProjectionLocked(session, g, identity)
		}
		if err = s.writeCurrentGuildPlacementProjectionLocked(session, g, identity); err != nil {
			return err
		}
	}
	return s.sendGameUpperRawClass(session, 1297, body, 0)
}

// Caller has the login town lock. Asset reservation protects the canonical
// generation through SQL commit. No peer lock is acquired during this write.
func (s *Service) grantCurrentGuildEssenceLogin(session *gameSession) error {
	if !s.options.guildEssenceEnabled {
		return nil
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return guild.ErrCharacterNotFound
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return err
	}
	defer release()
	return s.grantCurrentGuildEssenceLoginReserved(ctx, session, identity)
}

// The caller owns both the town lock and this identity's asset reservation.
// In particular op339 must not recursively reserve the same character.
func (s *Service) grantCurrentGuildEssenceLoginReserved(ctx context.Context, session *gameSession, identity boundGameSessionCharacter) error {
	if identity.session != session || !s.boundGameSessionCharacterCurrent(identity) {
		return guild.ErrCharacterNotFound
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return guild.ErrRepositoryUnavailable
	}
	catalog, err := s.currentGuildEssenceCatalog()
	if err != nil {
		return err
	}
	result, err := guild.GrantEssenceLogin(ctx, repos.Guild, r.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(identity.character))}, catalog, s.gameplayNow(), func() bool { return s.boundGameSessionCharacterCurrent(identity) })
	if err != nil {
		return err
	}
	return s.projectCurrentGuildEssenceIncome(session, result, false)
}

// Optional login income must not return a storage/projection failure to the
// scene-ready owner: the game packet loop closes the connection on that error.
// Keep one generation-bound retry for the next native guild-window refresh.
func (s *Service) projectOptionalGuildEssenceLoginLocked(session *gameSession) {
	identity, _ := s.boundGameSessionCharacterSnapshot(session)
	if err := s.grantCurrentGuildEssenceLogin(session); err != nil {
		session.guildEssenceLoginRetry = identity
		s.logGameEvent(session, "game-guild-essence-login-deferred", "error", err)
		return
	}
	session.guildEssenceLoginRetry = boundGameSessionCharacter{}
}

func (s *Service) refreshOptionalGuildEssenceLocked(ctx context.Context, session *gameSession, identity boundGameSessionCharacter) {
	if !s.options.guildEssenceEnabled {
		return
	}
	if session.guildEssenceLoginRetry == identity {
		if err := s.grantCurrentGuildEssenceLoginReserved(ctx, session, identity); err != nil {
			s.logGameEvent(session, "game-guild-essence-login-retry-deferred", "error", err)
			return
		}
		session.guildEssenceLoginRetry = boundGameSessionCharacter{}
		return
	}
	// Selection/reconnect invalidates another generation's pending login.
	session.guildEssenceLoginRetry = boundGameSessionCharacter{}
	s.projectOptionalGuildEssenceSnapshot(session)
}

func (s *Service) projectOptionalGuildEssenceSnapshot(session *gameSession) {
	if err := s.writeCurrentGuildEssenceSnapshot(session); err != nil {
		s.logGameEvent(session, "game-guild-essence-projection-deferred", "error", err)
	}
}

func (s *Service) projectCurrentGuildEssenceIncome(session *gameSession, income guild.EssenceIncome, replayed bool) error {
	if !s.options.guildEssenceEnabled {
		return nil
	}
	if err := s.writeCurrentGuildEssenceSnapshot(session); err != nil {
		return err
	}
	if replayed || income.AP == 0 {
		return nil
	}
	s.queueCurrentGuildEssenceRefresh(income.GuildID, session.selectedCharacterID)
	return s.sendGameUpperRawClass(session, 1284, buildCurrentGuildEssenceNotice(income.AP), 0)
}

// Bounded per-session coalesce token for peer essence/placement refreshes.
// All mutable fields are guarded by session.guild.mu; no IO or queue submit holds it.
type currentGuildEssenceRefresh struct {
	peer            boundGameSessionCharacter
	guildIDs        []uint32
	revision        uint64
	queued, running bool
	coalesceTimer   *time.Timer
	stopContext     func() bool
}

func clearCurrentGuildEssenceRefreshLocked(session *gameSession) {
	if p := session.guild.essenceRefresh; p != nil {
		if p.coalesceTimer != nil {
			p.coalesceTimer.Stop()
		}
		if p.stopContext != nil {
			p.stopContext()
		}
	}
	session.guild.essenceRefresh = nil
}

func (s *Service) discardCurrentGuildEssenceRefresh(p *currentGuildEssenceRefresh) {
	session := p.peer.session
	session.guild.mu.Lock()
	if session.guild.essenceRefresh == p {
		clearCurrentGuildEssenceRefreshLocked(session)
	}
	session.guild.mu.Unlock()
}

func (s *Service) requestCurrentGuildEssenceRefresh(peer boundGameSessionCharacter, guildID uint32) {
	if peer.session == nil || peer.session.events == nil || peer.session.events.loop == nil {
		return
	}
	s.mu.Lock()
	if s.gameSessions[peer.character] != peer.session || peer.session.selectedCharacterID != peer.character || peer.session.characterGeneration != peer.generation {
		s.mu.Unlock()
		return
	}
	session := peer.session
	session.guild.mu.Lock()
	s.mu.Unlock()
	if session.guild.presenceRefreshClosed || session.loginRevoked.Load() ||
		!session.events.active.Load() ||
		(session.ctx != nil && session.ctx.Err() != nil) {
		session.guild.mu.Unlock()
		return
	}
	p := session.guild.essenceRefresh
	if p == nil || p.peer != peer {
		clearCurrentGuildEssenceRefreshLocked(session)
		p = &currentGuildEssenceRefresh{peer: peer}
		session.guild.essenceRefresh = p
		if session.ctx != nil {
			p.stopContext = context.AfterFunc(session.ctx, func() { s.discardCurrentGuildEssenceRefresh(p) })
		}
	}
	p.guildIDs = appendUniqueGuildID(p.guildIDs, guildID)
	p.revision++
	if p.queued || p.running || p.coalesceTimer != nil {
		session.guild.mu.Unlock()
		return
	}
	if window := s.guildPresenceCoalesceWindow(); window > 0 && !session.guild.lastEssenceRefreshAt.IsZero() {
		if elapsed := s.guildRosterNow().Sub(session.guild.lastEssenceRefreshAt); elapsed >= 0 && elapsed < window {
			p.coalesceTimer = s.armCurrentGuildEssenceCoalesceTimerLocked(session, p, window-elapsed)
			session.guild.mu.Unlock()
			return
		}
	}
	p.queued = true
	session.guild.mu.Unlock()
	s.submitCurrentGuildEssenceRefresh(p)
}

func (s *Service) armCurrentGuildEssenceCoalesceTimerLocked(session *gameSession, p *currentGuildEssenceRefresh, delay time.Duration) *time.Timer {
	return time.AfterFunc(delay, func() {
		session.guild.mu.Lock()
		if session.guild.essenceRefresh != p || session.guild.presenceRefreshClosed ||
			session.loginRevoked.Load() ||
			(session.events != nil && !session.events.active.Load()) ||
			(session.ctx != nil && session.ctx.Err() != nil) {
			if session.guild.essenceRefresh == p {
				clearCurrentGuildEssenceRefreshLocked(session)
			}
			session.guild.mu.Unlock()
			return
		}
		p.coalesceTimer = nil
		if p.queued || p.running {
			session.guild.mu.Unlock()
			return
		}
		p.queued = true
		session.guild.mu.Unlock()
		s.submitCurrentGuildEssenceRefresh(p)
	})
}

func (s *Service) submitCurrentGuildEssenceRefresh(p *currentGuildEssenceRefresh) {
	if !s.boundGameSessionCharacterCurrent(p.peer) {
		s.discardCurrentGuildEssenceRefresh(p)
		return
	}
	session := p.peer.session
	if session.events == nil || session.events.loop == nil {
		s.discardCurrentGuildEssenceRefresh(p)
		return
	}
	const source = "guild-essence-refresh"
	err := s.tryPostGameSessionCharacterEventWithDiscard(session, source, p.peer.character, p.peer.generation,
		func() error { return s.runCurrentGuildEssenceRefresh(p) },
		func() { s.discardCurrentGuildEssenceRefresh(p) })
	if err == nil {
		return
	}
	session.guild.mu.Lock()
	if session.guild.essenceRefresh == p {
		p.queued = false
		if !p.running && p.coalesceTimer == nil {
			clearCurrentGuildEssenceRefreshLocked(session)
		}
	}
	session.guild.mu.Unlock()
	s.logGameEvent(session, "game-guild-essence-peer-refresh-deferred", "error", err)
}

func (s *Service) runCurrentGuildEssenceRefresh(p *currentGuildEssenceRefresh) error {
	session := p.peer.session
	session.guild.mu.Lock()
	if session.guild.essenceRefresh != p {
		session.guild.mu.Unlock()
		return nil
	}
	p.queued, p.running = false, true
	revision := p.revision
	guildIDs := append([]uint32(nil), p.guildIDs...)
	session.guild.mu.Unlock()
	executedRealRefresh := false
	defer func() {
		session.guild.mu.Lock()
		if session.guild.essenceRefresh != p {
			session.guild.mu.Unlock()
			return
		}
		p.running = false
		if executedRealRefresh {
			session.guild.lastEssenceRefreshAt = s.guildRosterNow()
		}
		followup := executedRealRefresh && p.revision != revision &&
			!session.guild.presenceRefreshClosed && !session.loginRevoked.Load() &&
			(session.events == nil || session.events.active.Load()) &&
			(session.ctx == nil || session.ctx.Err() == nil)
		submitNow := false
		if followup {
			if window := s.guildPresenceCoalesceWindow(); window > 0 && session.events != nil && session.events.loop != nil {
				p.coalesceTimer = s.armCurrentGuildEssenceCoalesceTimerLocked(session, p, window)
			} else {
				p.queued = true
				submitNow = true
			}
		} else {
			clearCurrentGuildEssenceRefreshLocked(session)
		}
		session.guild.mu.Unlock()
		if submitNow {
			s.submitCurrentGuildEssenceRefresh(p)
		}
	}()
	if !s.boundGameSessionCharacterCurrent(p.peer) {
		return nil
	}
	// Peer income is already committed. Do not rebuild guild placements or
	// perform repository reads while the peer owns a combat dungeon scene.
	// Town/member-window refresh reads the latest balance on return.
	if sessionInDungeon(session) {
		s.logGameEvent(session, "game-guild-essence-peer-refresh-deferred", "reason", "dungeon_scene_owned")
		return nil
	}
	executedRealRefresh = true
	return s.writeCurrentGuildEssenceSnapshotForGuilds(session, guildIDs)
}

// Same commit may occur under a dungeon or town lock: use peer event queues,
// recheck their owner/generation/membership, and never wait on a peer here.
func (s *Service) queueCurrentGuildEssenceRefresh(guildID uint32, skip uint16) {
	if !s.options.guildEssenceEnabled {
		return
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	_, reg, err := s.readCurrentGuildDirectory(ctx)
	if err != nil {
		s.logInfo("guild essence peer refresh deferred", "error", err)
		return
	}
	for _, member := range reg.Members(guildID) {
		id := member.CharacterID
		numeric, err := strconv.ParseUint(id, 10, 16)
		if err != nil || uint16(numeric) == skip {
			continue
		}
		peer, ok := s.onlineGameSessionCharacterSnapshot(uint16(numeric))
		if !ok || s.accountIDForSession(peer.session) != member.AccountID || peer.session.events == nil || peer.session.events.loop == nil {
			continue
		}
		s.requestCurrentGuildEssenceRefresh(peer, guildID)
	}
}
