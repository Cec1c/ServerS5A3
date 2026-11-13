package dnfbridge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/platform/eventloop"
)

// Each socket has one queued/running refresh and three small dirty targets. It
// never retains an aggregate snapshot or creates a goroutine per broadcast.
// No storage, projection lock, transport or queue wait holds this mutex.
type currentRaidRefreshQueue struct {
	mu      sync.Mutex
	pending *currentRaidRefresh
	closed  bool
}

type currentRaidRefreshTarget struct {
	aggregate *runtimeRaidState
	key       uint32
	instance  string
	phase     byte
	version   uint64
	revision  uint64
}

type currentRaidRefresh struct {
	peer                       boundGameSessionCharacter
	presence, roster, progress *currentRaidRefreshTarget
	queued, running            bool
	timer                      *time.Timer
	stopContext                func() bool
}

func clearCurrentRaidRefreshLocked(q *currentRaidRefreshQueue) {
	if p := q.pending; p != nil {
		if p.timer != nil {
			p.timer.Stop()
		}
		if p.stopContext != nil {
			p.stopContext()
		}
	}
	q.pending = nil
}

func resetCurrentRaidRefresh(session *gameSession, closed bool) {
	if session == nil {
		return
	}
	q := &session.raidRefresh
	q.mu.Lock()
	clearCurrentRaidRefreshLocked(q)
	q.closed = closed
	q.mu.Unlock()
}

func (s *Service) discardCurrentRaidRefresh(p *currentRaidRefresh) {
	q := &p.peer.session.raidRefresh
	q.mu.Lock()
	if q.pending == p {
		clearCurrentRaidRefreshLocked(q)
	}
	q.mu.Unlock()
}

func (s *Service) requestCurrentRaidRefresh(peer boundGameSessionCharacter, state runtimeRaidState, progress bool) {
	kind := 0
	if progress {
		kind = 2
	}
	s.requestCurrentRaidRefreshKind(peer, state, kind)
}

// kind 0 refreshes presence, 1 only map/roster progress, 2 includes settlement.
func (s *Service) requestCurrentRaidRefreshKind(peer boundGameSessionCharacter, state runtimeRaidState, kind int) {
	if peer.session == nil || !s.boundGameSessionCharacterCurrent(peer) {
		return
	}
	s.raidMu.Lock()
	live := s.raids[state.RaidKey]
	valid := live != nil && runtimeRaidMemberIndex(*live, peer.character) >= 0
	target := &currentRaidRefreshTarget{aggregate: live, key: state.RaidKey}
	if state.Battle != nil {
		target.instance, target.phase, target.version = state.Battle.InstanceID, state.Battle.Phase, state.Battle.ProgressVersion
		valid = valid && live.Battle != nil && live.Battle.InstanceID == target.instance && live.Battle.Phase == target.phase
	} else {
		valid = valid && live.Battle == nil
	}
	s.currentRaidRefreshRevision++
	target.revision = s.currentRaidRefreshRevision
	s.raidMu.Unlock()
	if !valid || (kind != 0 && state.Battle == nil) {
		return
	}
	session := peer.session
	q := &session.raidRefresh
	s.mu.Lock()
	if s.gameSessions[peer.character] != session || session.selectedCharacterID != peer.character || session.characterGeneration != peer.generation {
		s.mu.Unlock()
		return
	}
	q.mu.Lock()
	s.mu.Unlock()
	if q.closed || session.loginRevoked.Load() || (session.events != nil && !session.events.active.Load()) || (session.ctx != nil && session.ctx.Err() != nil) {
		q.mu.Unlock()
		return
	}
	p := q.pending
	if p == nil || p.peer != peer {
		clearCurrentRaidRefreshLocked(q)
		p = &currentRaidRefresh{peer: peer}
		q.pending = p
		if session.ctx != nil {
			p.stopContext = context.AfterFunc(session.ctx, func() { s.discardCurrentRaidRefresh(p) })
		}
	}
	if kind == 2 {
		if p.progress == nil || p.progress.revision < target.revision {
			p.progress = target
		}
	} else if kind == 1 {
		if p.roster == nil || p.roster.revision < target.revision {
			p.roster = target
		}
	} else {
		if p.presence == nil || p.presence.revision < target.revision {
			p.presence = target
		}
	}
	if p.queued || p.running || p.timer != nil {
		q.mu.Unlock()
		return
	}
	p.queued = true
	q.mu.Unlock()
	s.submitCurrentRaidRefresh(p)
}

func (s *Service) submitCurrentRaidRefresh(p *currentRaidRefresh) {
	if !s.boundGameSessionCharacterCurrent(p.peer) {
		s.discardCurrentRaidRefresh(p)
		return
	}
	session := p.peer.session
	// Real connections install their event loop before character binding. Keep
	// the eventless embedding/test path bounded too; never run IO on a caller's
	// shared timequeue through tryPost's inline fallback.
	if session.events == nil || session.events.loop == nil {
		go func() { _ = s.runCurrentRaidRefresh(p) }()
		return
	}
	err := s.tryPostGameSessionCharacterEventWithDiscard(session, "raid-owned-refresh", p.peer.character, p.peer.generation,
		func() error { return s.runCurrentRaidRefresh(p) }, func() { s.discardCurrentRaidRefresh(p) })
	if err == nil {
		return
	}
	q := &session.raidRefresh
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending != p {
		return
	}
	p.queued = false
	if !errors.Is(err, eventloop.ErrQueueFull) {
		clearCurrentRaidRefreshLocked(q)
		return
	}
	// A full owner queue must not lose a one-off seat change or terminal result.
	// One timer per dirty owner retries admission only, never reads or writes.
	p.timer = time.AfterFunc(250*time.Millisecond, func() {
		q.mu.Lock()
		if q.pending != p || q.closed {
			q.mu.Unlock()
			return
		}
		p.timer = nil
		p.queued = true
		q.mu.Unlock()
		s.submitCurrentRaidRefresh(p)
	})
}

func (s *Service) currentRaidRefreshSnapshot(peer boundGameSessionCharacter, target *currentRaidRefreshTarget, progress bool) (runtimeRaidState, bool) {
	if target == nil || !s.boundGameSessionCharacterCurrent(peer) {
		return runtimeRaidState{}, false
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	live := s.runtimeRaidByMemberLocked(peer.character)
	if live == nil || live != target.aggregate || live.RaidKey != target.key {
		return runtimeRaidState{}, false
	}
	if b := live.Battle; b != nil {
		if b.InstanceID != target.instance || b.Phase != target.phase || b.ProgressVersion < target.version {
			return runtimeRaidState{}, false
		}
	} else if target.instance != "" || progress {
		return runtimeRaidState{}, false
	}
	if progress && (!live.Started || live.Battle.MemberAccounts[peer.character] != s.accountIDForSession(peer.session) || live.Battle.ChannelID != peer.session.channel.ID || live.Battle.ChannelType != peer.session.channel.Type) {
		return runtimeRaidState{}, false
	}
	return cloneRuntimeRaidState(*live), true
}

func (s *Service) runCurrentRaidRefresh(p *currentRaidRefresh) error {
	session := p.peer.session
	q := &session.raidRefresh
	q.mu.Lock()
	if q.pending != p {
		q.mu.Unlock()
		return nil
	}
	p.queued = false
	p.running = true
	presence, roster, progress := p.presence, p.roster, p.progress
	p.presence = nil
	p.roster = nil
	p.progress = nil
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		if q.pending != p {
			q.mu.Unlock()
			return
		}
		p.running = false
		followup := p.presence != nil || p.roster != nil || p.progress != nil
		if followup {
			p.queued = true
		} else {
			clearCurrentRaidRefreshLocked(q)
		}
		q.mu.Unlock()
		if followup {
			s.submitCurrentRaidRefresh(p)
		}
	}()
	var result error
	if latest, ok := s.currentRaidRefreshSnapshot(p.peer, presence, false); ok {
		if s.currentRaidInfoVisible(session, latest) && s.raidPresenceContext(session, latest, strings.TrimSpace(session.accountID)) {
			err := s.withGameWire(session, func() error {
				// Waiting for this recipient's wire must not publish an obsolete
				// roster after a concurrent seat expiry, rejoin or phase change.
				current, present := s.currentRaidRefreshSnapshot(p.peer, presence, false)
				if !present || !s.raidPresenceContext(session, current, strings.TrimSpace(session.accountID)) {
					return nil
				}
				body, err := s.buildRuntimeRaidRosterBody(current)
				if err != nil {
					return err
				}
				return s.sendGameUpperRawClassCodecLocked(session, raid.RaidMemberRefreshMsgID, body, 0, true)
			})
			if err != nil {
				s.logGameEvent(session, "game-raid-presence-refresh-failed", "raid_key", latest.RaidKey, "error", err)
				result = errors.Join(result, err)
			}
		}
	}
	if latest, ok := s.currentRaidRefreshSnapshot(p.peer, roster, true); ok && s.currentRaidAuthenticated(session) {
		if err := s.sendCurrentRaidProgressProjection(session, latest, false, false); err != nil {
			s.logGameEvent(session, "game-raid-roster-progress-send-failed", "raid_key", latest.RaidKey, "error", err)
			result = errors.Join(result, err)
		}
	}
	latest, ok := s.currentRaidRefreshSnapshot(p.peer, progress, true)
	if !ok || !s.currentRaidAuthenticated(session) {
		return result
	}
	err := s.sendCurrentRaidQueuedProgress(session, latest)
	if err != nil {
		s.logGameEvent(session, "game-raid-dungeon-progress-send-failed", "raid_key", latest.RaidKey, "error", err)
	}
	return errors.Join(result, err)
}

func (s *Service) sendCurrentRaidQueuedProgress(peer *gameSession, latest runtimeRaidState) error {
	if latest.Battle.Phase == 1 && !latest.Battle.PhaseCompleted && !latest.Battle.TimedOut {
		if err := s.sendCurrentRaidBattleState(peer, latest); err != nil {
			return err
		}
	} else if err := s.sendCurrentRaidProgressProjection(peer, latest, false, false); err != nil {
		return err
	}
	if err := s.sendCurrentAntonTerminalResult(peer, latest); err != nil {
		return err
	}
	if err := s.sendCurrentAntonRewardFlowProjection(peer, latest); err != nil {
		return err
	}
	return s.sendCurrentAntonRewardReturn(peer, latest)
}
