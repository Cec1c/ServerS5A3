package dnfbridge

import (
	"context"
	"errors"
	"time"

	"longheng.io/server/internal/platform/eventloop"
)

const (
	guildRefreshRequested = iota
	guildRefreshCoalesced
	guildRefreshQueued
	guildRefreshExecuted
	guildRefreshStale
	guildRefreshDeferred
	guildRefreshFailed
	guildSummaryMembers
	guildSummaryRows
	guildSummaryFallback
	guildSummaryFailed
	guildSummaryQueries
	guildSummaryFullLoads
	guildRosterCounterCount
)

var guildRosterCounterNames = [guildRosterCounterCount]string{
	"refresh_requested", "refresh_coalesced", "refresh_queued", "refresh_executed",
	"refresh_stale_dropped", "refresh_deferred", "refresh_failed",
	"summary_members", "summary_rows", "summary_fallback", "summary_failed",
	"summary_queries", "summary_full_loads",
}

func (s *Service) guildRosterCounter(kind int, n int64) {
	if s != nil && s.performance != nil {
		s.performance.guildRoster[kind].Add(n)
	}
}

// One token belongs to one socket/character/generation/guild. All mutable
// fields use guild.mu; no storage, socket write or queue submission holds it.
type currentGuildPresenceRefresh struct {
	peer            boundGameSessionCharacter
	guildID         uint32
	revision        uint64
	queued, running bool
	retries         int
	timer           *time.Timer
	stopContext     func() bool
}

func clearCurrentGuildPresenceRefreshLocked(session *gameSession) {
	if p := session.guild.presenceRefresh; p != nil {
		if p.timer != nil {
			p.timer.Stop()
		}
		if p.stopContext != nil {
			p.stopContext()
		}
	}
	session.guild.presenceRefresh = nil
}

func resetCurrentGuildPresenceRefresh(session *gameSession, closed bool) {
	if session == nil {
		return
	}
	session.guild.mu.Lock()
	clearCurrentGuildPresenceRefreshLocked(session)
	session.guild.presenceRefreshClosed = closed
	session.guild.mu.Unlock()
}

func (s *Service) discardCurrentGuildPresenceRefresh(p *currentGuildPresenceRefresh) {
	session := p.peer.session
	session.guild.mu.Lock()
	if session.guild.presenceRefresh == p {
		clearCurrentGuildPresenceRefreshLocked(session)
		s.guildRosterCounter(guildRefreshStale, 1)
	}
	session.guild.mu.Unlock()
}

func (s *Service) requestCurrentGuildPresenceRefresh(peer boundGameSessionCharacter, guildID uint32) {
	s.guildRosterCounter(guildRefreshRequested, 1)
	if peer.session == nil {
		return
	}
	s.mu.Lock()
	if s.gameSessions[peer.character] != peer.session || peer.session.selectedCharacterID != peer.character || peer.session.characterGeneration != peer.generation {
		s.mu.Unlock()
		s.guildRosterCounter(guildRefreshStale, 1)
		return
	}
	session := peer.session
	session.guild.mu.Lock()
	s.mu.Unlock()
	if session.guild.presenceRefreshClosed || session.loginRevoked.Load() ||
		(session.events != nil && !session.events.active.Load()) ||
		(session.ctx != nil && session.ctx.Err() != nil) {
		session.guild.mu.Unlock()
		s.guildRosterCounter(guildRefreshStale, 1)
		return
	}
	p := session.guild.presenceRefresh
	if p == nil || p.peer != peer || p.guildID != guildID {
		clearCurrentGuildPresenceRefreshLocked(session)
		p = &currentGuildPresenceRefresh{peer: peer, guildID: guildID}
		session.guild.presenceRefresh = p
		if session.ctx != nil {
			p.stopContext = context.AfterFunc(session.ctx, func() { s.discardCurrentGuildPresenceRefresh(p) })
		}
	}
	p.revision++
	if p.queued || p.running || p.timer != nil {
		session.guild.mu.Unlock()
		s.guildRosterCounter(guildRefreshCoalesced, 1)
		return
	}
	p.retries, p.queued = 0, true
	session.guild.mu.Unlock()
	s.submitCurrentGuildPresenceRefresh(p)
}

func (s *Service) submitCurrentGuildPresenceRefresh(p *currentGuildPresenceRefresh) {
	if !s.boundGameSessionCharacterCurrent(p.peer) {
		s.discardCurrentGuildPresenceRefresh(p)
		return
	}
	session := p.peer.session
	err := s.tryPostGameSessionCharacterEventWithDiscard(session, "guild-presence-refresh", p.peer.character, p.peer.generation,
		func() error { return s.runCurrentGuildPresenceRefresh(p) },
		func() { s.discardCurrentGuildPresenceRefresh(p) })
	if err == nil {
		s.guildRosterCounter(guildRefreshQueued, 1)
		return
	}
	session.guild.mu.Lock()
	defer session.guild.mu.Unlock()
	if session.guild.presenceRefresh != p {
		return
	}
	p.queued = false
	s.guildRosterCounter(guildRefreshDeferred, 1)
	if !errors.Is(err, eventloop.ErrQueueFull) {
		clearCurrentGuildPresenceRefreshLocked(session)
		return
	}
	// At most three delayed retries per burst; a later observation can retry an
	// exhausted dirty token. The timer does no IO and never waits for capacity.
	if p.retries >= 3 {
		return
	}
	delay := 100 * time.Millisecond * time.Duration(1<<p.retries)
	p.retries++
	p.timer = time.AfterFunc(delay, func() {
		session.guild.mu.Lock()
		if session.guild.presenceRefresh != p {
			session.guild.mu.Unlock()
			return
		}
		p.timer, p.queued = nil, true
		session.guild.mu.Unlock()
		s.submitCurrentGuildPresenceRefresh(p)
	})
}

func (s *Service) runCurrentGuildPresenceRefresh(p *currentGuildPresenceRefresh) error {
	session := p.peer.session
	session.guild.mu.Lock()
	if session.guild.presenceRefresh != p {
		session.guild.mu.Unlock()
		return nil
	}
	p.queued, p.running = false, true
	revision := p.revision
	session.guild.mu.Unlock()
	s.guildRosterCounter(guildRefreshExecuted, 1)
	defer func() {
		session.guild.mu.Lock()
		if session.guild.presenceRefresh != p {
			session.guild.mu.Unlock()
			return
		}
		p.running = false
		followup := p.revision != revision
		if followup {
			p.queued, p.retries = true, 0
		} else {
			clearCurrentGuildPresenceRefreshLocked(session)
		}
		session.guild.mu.Unlock()
		// One followup goes to the tail; never loop and starve ordinary events.
		if followup {
			s.submitCurrentGuildPresenceRefresh(p)
		}
	}()
	// Presence is already durable. A background management/roster snapshot
	// must not occupy the combat packet queue or rebuild guild actors mid-run.
	// Check at execution time: the player may have entered after enqueueing.
	// Explicit window requests and later town refreshes read current storage.
	session.dungeon.mu.Lock()
	inDungeon := session.dungeon.runtime != nil
	session.dungeon.mu.Unlock()
	if inDungeon {
		s.guildRosterCounter(guildRefreshDeferred, 1)
		return nil
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	for _, notice := range []bool{true, false} {
		if err := s.refreshCurrentGuildTextPeer(ctx, p.guildID, p.peer, boundGameSessionCharacter{}, notice); err != nil {
			s.guildRosterCounter(guildRefreshFailed, 1)
			return nil // optional refresh failure does not revoke a healthy socket
		}
	}
	return nil
}
