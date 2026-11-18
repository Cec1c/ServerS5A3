package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/platform/eventloop"
)

const partyRoomMoveRetryInterval = 25 * time.Millisecond

const partyRoomMoveWriteRetryInterval = 250 * time.Millisecond

// Only an unsent start-map on a still-usable stream is replayable. The normal
// transport layer has already retired partial frames and expired writes.
type partyRoomMoveUnsentStartError struct{ cause error }

func (*partyRoomMoveUnsentStartError) Error() string {
	return "party room follow start-map was not written"
}
func (e *partyRoomMoveUnsentStartError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type partyRoomMoveTarget struct {
	identity boundGameSessionCharacter
	owner    partyDungeonLoadingOwner
	epoch    uint64
}

// Called while holding this target's dungeon lock during the existing move
// preflight. Queue submission never needs to reacquire a peer dungeon lock.
func snapshotPartyRoomMoveTargetLocked(identity boundGameSessionCharacter) partyRoomMoveTarget {
	p := identity.session
	o := partyDungeonLoadingOwner{session: p, runtime: p.dungeon.runtime, token: p.dungeon.runToken}
	if o.runtime != nil {
		o.room, o.visit = o.runtime.Room, o.runtime.loadingRoomVisit
	}
	return partyRoomMoveTarget{identity: identity, owner: o, epoch: p.dungeonPlacementEpoch.Load()}
}

// One immutable accepted follow and one admission retry per recipient. A full
// ordinary queue never blocks the leader or loses the only room transition.
type partyRoomMoveQueue struct {
	mu      sync.Mutex
	pending *partyRoomMoveDelivery
	closed  bool
}

type partyRoomMoveDelivery struct {
	target            partyRoomMoveTarget
	plan              runtimePartyDungeonMovePlan
	body              []byte
	retired           func(uint16) bool
	timer             *time.Timer
	stopContext       func() bool
	writeRetryStarted time.Time
	writeRetryCount   uint32
}

func clearPartyRoomMoveLocked(q *partyRoomMoveQueue) {
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

func resetPartyRoomMove(session *gameSession, closed bool) {
	if session == nil {
		return
	}
	q := &session.partyRoomMove
	q.mu.Lock()
	clearPartyRoomMoveLocked(q)
	q.closed = closed
	q.mu.Unlock()
}

func discardPartyRoomMove(p *partyRoomMoveDelivery) {
	q := &p.target.identity.session.partyRoomMove
	q.mu.Lock()
	if q.pending == p {
		clearPartyRoomMoveLocked(q)
	}
	q.mu.Unlock()
}

func samePartyRoomMovePlan(a, b runtimePartyDungeonMovePlan) bool {
	return a.target == b.target && a.seed == b.seed && a.instance == b.instance && a.room == b.room
}

func (s *Service) postPartyRoomMove(target partyRoomMoveTarget, body []byte, plan runtimePartyDungeonMovePlan, retired func(uint16) bool) error {
	if target.identity.session == nil || len(body) != dungeoncmd.MoveMapRequestSize {
		return errInvalidGameSessionEvent
	}
	p := &partyRoomMoveDelivery{target: target, body: append([]byte(nil), body...), plan: plan, retired: retired}
	session := target.identity.session
	if session.events == nil || session.events.loop == nil {
		return s.deliverPartyRoomMove(p)
	}
	q := &session.partyRoomMove
	transport := session.gameTransportContext()
	// Binding mutation uses the same Service.mu -> mailbox order. An old
	// preflight snapshot cannot replace work for a newer binding or room visit.
	s.mu.Lock()
	i := target.identity
	if s.gameSessions[i.character] != session || session.selectedCharacterID != i.character || session.characterGeneration != i.generation || session.dungeonPlacementEpoch.Load() != target.epoch {
		s.mu.Unlock()
		return nil
	}
	q.mu.Lock()
	s.mu.Unlock()
	if q.closed || session.loginRevoked.Load() || !session.events.active.Load() || transport.Err() != nil || session.ctx != nil && session.ctx.Err() != nil {
		q.mu.Unlock()
		return nil
	}
	if old := q.pending; old != nil {
		if old.target == target && samePartyRoomMovePlan(old.plan, plan) {
			q.mu.Unlock()
			return nil
		}
		if old.target.identity == i && old.target.epoch >= target.epoch {
			q.mu.Unlock()
			return fmt.Errorf("%w: conflicting follow for the same source room visit", errDungeonMoveRuntimeOwnerMismatch)
		}
	}
	clearPartyRoomMoveLocked(q)
	q.pending = p
	stopTransport := context.AfterFunc(transport, func() { discardPartyRoomMove(p) })
	if session.ctx != nil {
		stopOwner := context.AfterFunc(session.ctx, func() { discardPartyRoomMove(p) })
		p.stopContext = func() bool {
			a, b := stopTransport(), stopOwner()
			return a && b
		}
	} else {
		p.stopContext = stopTransport
	}
	q.mu.Unlock()
	s.submitPartyRoomMove(p)
	return nil
}

func (s *Service) submitPartyRoomMove(p *partyRoomMoveDelivery) {
	i := p.target.identity
	session := i.session
	if !s.boundGameSessionCharacterCurrent(i) || session.dungeonPlacementEpoch.Load() != p.target.epoch || session.loginRevoked.Load() || !session.events.active.Load() || session.gameTransportContext().Err() != nil || session.ctx != nil && session.ctx.Err() != nil || p.retired != nil && p.retired(i.character) {
		discardPartyRoomMove(p)
		return
	}
	q := &session.partyRoomMove
	q.mu.Lock()
	current := q.pending == p && !q.closed
	q.mu.Unlock()
	if !current {
		return
	}
	err := s.tryPostGameSessionCharacterEventWithDiscard(session, "runtime-party-dungeon-room-transition", i.character, i.generation, func() error {
		q.mu.Lock()
		current := q.pending == p && !q.closed
		expired := !p.writeRetryStarted.IsZero() && time.Since(p.writeRetryStarted) >= gameTransportWriteTimeout
		q.mu.Unlock()
		if !current {
			return nil
		}
		retained := false
		defer func() {
			if !retained {
				discardPartyRoomMove(p)
			}
		}()
		if expired {
			// Queue congestion can outlive a write retry budget. Check again
			// on the recipient owner before another socket write is attempted.
			if s.partyRoomMoveSourceCurrent(p) {
				session.closeGameTransport()
				return context.DeadlineExceeded
			}
			return nil
		}
		err := s.deliverPartyRoomMove(p)
		if err != nil {
			var attempt uint32
			retained, attempt = s.retainUnsentPartyRoomMove(p, err)
			if retained {
				if attempt == 1 {
					s.logGameEvent(session, "game-party-room-follow-unsent-retry", "char_id", i.character)
				}
				return nil
			}
			// After an accepted room transition, a non-replayable failure
			// cannot leave its recipient stream apparently healthy but split
			// from the party. Never retire a superseding run or return intent.
			if s.partyRoomMoveOwnsIncompleteStream(p) {
				session.closeGameTransport()
			}
			s.logGameEvent(session, "game-party-room-follow-failed", "char_id", i.character, "error", err)
		}
		return err
	}, func() { discardPartyRoomMove(p) })
	if err == nil {
		return
	}
	q.mu.Lock()
	if q.pending != p || q.closed {
		q.mu.Unlock()
		return
	}
	if !errors.Is(err, eventloop.ErrQueueFull) {
		clearPartyRoomMoveLocked(q)
		q.mu.Unlock()
		s.logGameEvent(session, "game-party-room-follow-queue-closed", "error", err)
		return
	}
	s.schedulePartyRoomMoveRetryLocked(q, p, partyRoomMoveRetryInterval)
	q.mu.Unlock()
}

func (s *Service) schedulePartyRoomMoveRetryLocked(q *partyRoomMoveQueue, p *partyRoomMoveDelivery, delay time.Duration) {
	p.timer = time.AfterFunc(delay, func() {
		q.mu.Lock()
		if q.pending != p || q.closed {
			q.mu.Unlock()
			return
		}
		p.timer = nil
		q.mu.Unlock()
		s.submitPartyRoomMove(p)
	})
}

func (s *Service) retainUnsentPartyRoomMove(p *partyRoomMoveDelivery, err error) (bool, uint32) {
	// This private marker is returned directly by the start-map write site.
	// Do not traverse arbitrary provider As/Unwrap methods to classify it.
	if _, ok := err.(*partyRoomMoveUnsentStartError); !ok {
		return false, 0
	}
	i := p.target.identity
	session := i.session
	if !s.partyRoomMoveSourceCurrent(p) || session.gameTransportContext().Err() != nil || session.ctx != nil && session.ctx.Err() != nil || session.loginRevoked.Load() || !session.events.active.Load() {
		return false, 0
	}
	q := &session.partyRoomMove
	q.mu.Lock()
	if q.pending != p || q.closed {
		q.mu.Unlock()
		return false, 0
	}
	now := time.Now()
	if p.writeRetryStarted.IsZero() {
		p.writeRetryStarted = now
	}
	if now.Sub(p.writeRetryStarted) >= gameTransportWriteTimeout {
		clearPartyRoomMoveLocked(q)
		q.mu.Unlock()
		// A persistent failure has the existing transport recovery budget.
		// Retire only this incomplete recipient, never the healthy party.
		if s.partyRoomMoveSourceCurrent(p) {
			session.closeGameTransport()
		}
		return false, 0
	}
	p.writeRetryCount++
	attempt := p.writeRetryCount
	s.schedulePartyRoomMoveRetryLocked(q, p, partyRoomMoveWriteRetryInterval)
	q.mu.Unlock()
	return true, attempt
}

// Recipient owner only, with no mailbox lock. Expiring old recovery work must
// not close a replacement run or a stream already accepting personal return.
func (s *Service) partyRoomMoveSourceCurrent(p *partyRoomMoveDelivery) bool {
	i := p.target.identity
	session := i.session
	if !s.boundGameSessionCharacterCurrent(i) || session.dungeonPlacementEpoch.Load() != p.target.epoch || p.retired != nil && p.retired(i.character) {
		return false
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	r := session.dungeon.runtime
	return r != nil && r.Session != nil && !r.townReturnPending && snapshotPartyRoomMoveTargetLocked(i) == p.target &&
		(p.plan.instance == nil || r.partyInstance == p.plan.instance)
}

func (s *Service) partyRoomMoveOwnsIncompleteStream(p *partyRoomMoveDelivery) bool {
	if s.partyRoomMoveSourceCurrent(p) {
		return true
	}
	i := p.target.identity
	session := i.session
	if !s.boundGameSessionCharacterCurrent(i) || p.retired != nil && p.retired(i.character) {
		return false
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	r := session.dungeon.runtime
	return r != nil && r == p.target.owner.runtime && session.dungeon.runToken == p.target.owner.token && !r.townReturnPending &&
		r.loadingRoomVisit == p.target.owner.visit+1 && runtimePartyMovePlanCommitted(r, p.plan)
}

// Recipient owner only. Internal fanout is not new client dungeon evidence and
// cannot cancel a personal op24 return. An own op45 that already committed the
// exact shared plan remains idempotent, with no second packet or fatigue debit.
func (s *Service) deliverPartyRoomMove(p *partyRoomMoveDelivery) error {
	i := p.target.identity
	session := i.session
	if !s.boundGameSessionCharacterCurrent(i) || p.retired != nil && p.retired(i.character) {
		return nil
	}
	session.dungeon.mu.Lock()
	r := session.dungeon.runtime
	if runtimePartyMovePlanCommitted(r, p.plan) {
		session.dungeon.mu.Unlock()
		return nil
	}
	if r == nil || r.Session == nil {
		session.dungeon.mu.Unlock()
		return errDungeonWorldMapUnavailable
	}
	current := snapshotPartyRoomMoveTargetLocked(i)
	valid := current == p.target && !r.townReturnPending
	session.dungeon.mu.Unlock()
	if !valid {
		return nil
	}
	if err := s.handleDungeonMoveMapPlanned(session, p.body, &p.plan, false); err != nil {
		return err
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	r = session.dungeon.runtime
	if r == nil || r.Session == nil {
		return errDungeonWorldMapUnavailable
	}
	scene, ok := r.Session.Scene()
	if !ok || runtimeDungeonRoomKeyFromScene(scene) != p.plan.target || r.Seed != p.plan.seed {
		return fmt.Errorf("%w: follower did not commit planned party room", errDungeonMoveRuntimeOwnerMismatch)
	}
	return nil
}
