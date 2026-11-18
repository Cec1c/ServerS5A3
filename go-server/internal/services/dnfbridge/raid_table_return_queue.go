package dnfbridge

import (
	"context"
	"errors"
	"sync"
	"time"

	"longheng.io/server/internal/platform/eventloop"
)

const currentRaidTableReturnRetry = 25 * time.Millisecond

// Keep one accepted return per recipient while its ordinary owner queue is
// full. Repeated requests coalesce; no sender waits for another owner, and
// timers only retry enqueueing. Scene work still runs on the recipient owner.
type currentRaidTableReturnQueue struct {
	mu      sync.Mutex
	pending *currentRaidTableReturnDelivery
	closed  bool
}

type currentRaidTableReturnDelivery struct {
	peer        boundGameSessionCharacter
	runtime     *runtimeDungeonState
	owner       *currentRaidTablePartyReturn
	queued      bool
	timer       *time.Timer
	stopContext func() bool
}

func clearCurrentRaidTableReturnLocked(q *currentRaidTableReturnQueue) {
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

func resetCurrentRaidTableReturn(session *gameSession, closed bool) {
	if session == nil {
		return
	}
	q := &session.raidTableReturn
	q.mu.Lock()
	clearCurrentRaidTableReturnLocked(q)
	q.closed = closed
	q.mu.Unlock()
}

func (s *Service) discardCurrentRaidTableReturn(p *currentRaidTableReturnDelivery) {
	q := &p.peer.session.raidTableReturn
	q.mu.Lock()
	if q.pending == p {
		clearCurrentRaidTableReturnLocked(q)
	}
	q.mu.Unlock()
}

// Caller holds raidMu then Service.mu. This snapshot uses no recipient
// dungeon lock or repository call; the owner callback rechecks the full run.
func (s *Service) currentRaidTableReturnTargetLocked(peer boundGameSessionCharacter, expected *runtimeDungeonState, owner *currentRaidTablePartyReturn) bool {
	if expected == nil || owner == nil {
		return false
	}
	for _, identity := range []boundGameSessionCharacter{peer, owner.leader} {
		if identity.session == nil || identity.character == 0 ||
			s.gameSessions[identity.character] != identity.session ||
			identity.session.selectedCharacterID != identity.character ||
			identity.session.characterGeneration != identity.generation {
			return false
		}
	}
	live := s.raids[owner.entry.RaidKey]
	if live == nil || live.Battle == nil || runtimeRaidMemberIndex(*live, peer.character) < 0 {
		return false
	}
	b, e := live.Battle, owner.entry
	p, present := b.Participation[peer.character]
	return b.InstanceID == e.InstanceID && b.Phase == e.Phase && b.ChannelID == e.ChannelID && b.ChannelType == e.ChannelType &&
		present && p.Session == peer.session && p.Generation == peer.generation && p.Runtime == expected &&
		p.RunID == e.RunID && p.Phase == e.Phase && p.Attempt == e.Attempt
}

func (s *Service) requestCurrentRaidTableReturn(peer boundGameSessionCharacter, expected *runtimeDungeonState, owner *currentRaidTablePartyReturn) error {
	session := peer.session
	if session == nil || expected == nil || owner == nil {
		return nil
	}
	if session.events == nil || session.events.loop == nil {
		return s.sendCurrentRaidTablePartyReturnOnOwner(peer, expected, owner)
	}
	transport := session.gameTransportContext()
	q := &session.raidTableReturn
	// Preserve raidMu -> Service.mu -> mailbox order. A delayed producer may
	// not replace a newly admitted run's pending delivery with an old snapshot.
	s.raidMu.Lock()
	s.mu.Lock()
	if !s.currentRaidTableReturnTargetLocked(peer, expected, owner) {
		s.mu.Unlock()
		s.raidMu.Unlock()
		return nil
	}
	q.mu.Lock()
	s.mu.Unlock()
	s.raidMu.Unlock()
	if q.closed || session.loginRevoked.Load() || !session.events.active.Load() || transport.Err() != nil {
		q.mu.Unlock()
		return nil
	}
	if p := q.pending; p != nil && p.peer == peer && p.runtime == expected && p.owner == owner {
		q.mu.Unlock()
		return nil
	}
	clearCurrentRaidTableReturnLocked(q)
	p := &currentRaidTableReturnDelivery{peer: peer, runtime: expected, owner: owner, queued: true}
	q.pending = p
	p.stopContext = context.AfterFunc(transport, func() { s.discardCurrentRaidTableReturn(p) })
	q.mu.Unlock()
	s.submitCurrentRaidTableReturn(p)
	return nil
}

func (s *Service) submitCurrentRaidTableReturn(p *currentRaidTableReturnDelivery) {
	session := p.peer.session
	s.raidMu.Lock()
	s.mu.Lock()
	valid := s.currentRaidTableReturnTargetLocked(p.peer, p.runtime, p.owner)
	s.mu.Unlock()
	s.raidMu.Unlock()
	if !valid || session.loginRevoked.Load() || !session.events.active.Load() || session.gameTransportContext().Err() != nil {
		s.discardCurrentRaidTableReturn(p)
		return
	}
	err := s.tryPostGameSessionCharacterEventWithDiscard(session, "raid-table-party-return", p.peer.character, p.peer.generation,
		func() error {
			q := &session.raidTableReturn
			q.mu.Lock()
			current := q.pending == p && !q.closed
			q.mu.Unlock()
			if !current {
				return nil
			}
			defer s.discardCurrentRaidTableReturn(p)
			return s.sendCurrentRaidTablePartyReturnOnOwner(p.peer, p.runtime, p.owner)
		}, func() { s.discardCurrentRaidTableReturn(p) })
	if err == nil {
		return
	}
	q := &session.raidTableReturn
	q.mu.Lock()
	if q.pending != p || q.closed {
		q.mu.Unlock()
		return
	}
	p.queued = false
	if !errors.Is(err, eventloop.ErrQueueFull) {
		clearCurrentRaidTableReturnLocked(q)
		q.mu.Unlock()
		s.logGameEvent(session, "game-raid-table-party-return-queue-closed", "error", err)
		return
	}
	p.timer = time.AfterFunc(currentRaidTableReturnRetry, func() {
		q.mu.Lock()
		if q.pending != p || q.closed {
			q.mu.Unlock()
			return
		}
		p.timer = nil
		p.queued = true
		q.mu.Unlock()
		s.submitCurrentRaidTableReturn(p)
	})
	q.mu.Unlock()
}
