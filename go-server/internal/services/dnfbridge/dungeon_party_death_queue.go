package dnfbridge

import (
	"context"
	"errors"
	"sync"
	"time"

	"longheng.io/server/internal/platform/eventloop"
)

const (
	partyDeathPendingLimit = 512
	partyDeathPendingBytes = 1 << 20
	partyDeathQueueRetry   = 25 * time.Millisecond
)

// One bounded mailbox per recipient. The normal event queue may be briefly
// full: retain every distinct death and retry one wakeup, without waiting in
// another player's owner or spawning a goroutine per death. Visuals use a
// bounded burst between owner events; progression consumes one FIFO entry
// per wakeup, leaving ordinary requests room to run.
type partyDungeonDeathQueue struct {
	mu       sync.Mutex
	watch    sync.Once
	items    []partyDungeonDeathDelivery
	bytes    int
	queued   bool
	closed   bool
	timer    *time.Timer
	identity boundGameSessionCharacter
	version  uint64
}

func (q *partyDungeonDeathQueue) stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.items = nil
	q.bytes = 0
	if q.timer != nil {
		q.timer.Stop()
		q.timer = nil
	}
}

func (s *Service) postPartyDungeonDeath(d partyDungeonDeathDelivery) bool {
	peer := d.identity.session
	if peer.events == nil || peer.events.loop == nil {
		return s.deliverPartyDungeonDeath(d) == nil
	}
	q := &peer.partyDeaths
	q.watch.Do(func() { context.AfterFunc(peer.gameTransportContext(), q.stop) })
	q.mu.Lock()
	if q.closed || peer.gameTransportContext().Err() != nil || !peer.events.active.Load() {
		q.mu.Unlock()
		return false
	}
	// No path acquires this mailbox while holding Service.mu. Bind validation
	// under the mailbox lock keeps a delayed old producer from replacing a
	// new character's pending queue. An in-flight old delivery keeps its own
	// immutable copy and cannot remove a newer queue entry on completion.
	if !s.boundGameSessionCharacterCurrent(d.identity) {
		q.mu.Unlock()
		return false
	}
	if q.identity != d.identity {
		q.items = nil
		q.bytes = 0
		q.identity = d.identity
		q.version++
	}
	overflow := len(q.items) >= partyDeathPendingLimit || len(d.body) > partyDeathPendingBytes-q.bytes
	if !overflow {
		q.items = append(q.items, d)
		q.bytes += len(d.body)
	}
	q.mu.Unlock()
	if overflow {
		// Sustained critical backlog has a hard memory bound. Retire only the
		// incomplete recipient stream; do not roll back accepted gameplay or
		// grow an unlimited buffer. A merely full ordinary queue is retried.
		s.logGameEvent(peer, "game-dungeon-party-death-backlog-exhausted", "object_key", d.objectKey, "from_char_id", d.from)
		if s.boundGameSessionCharacterCurrent(d.identity) {
			peer.closeGameTransport()
		}
		return false
	}
	s.submitPartyDeathQueue(peer)
	return true
}

func (s *Service) submitPartyDeathQueue(peer *gameSession) {
	q := &peer.partyDeaths
	if peer.gameTransportContext().Err() != nil || peer.loginRevoked.Load() || !peer.events.active.Load() {
		q.stop()
		return
	}
	q.mu.Lock()
	if q.closed || q.queued || q.timer != nil || len(q.items) == 0 {
		q.mu.Unlock()
		return
	}
	q.queued = true
	q.mu.Unlock()
	// The wakeup itself spans character changes. Each immutable delivery
	// validates its own binding and room; a skipped stale wakeup must not
	// strand newer deliveries. No gameplay mutation is authorized by this 0.
	err := s.tryPostGameSessionCharacterEvent(peer, "party-death-notification", 0, 0, func() error {
		s.runPartyDeathQueue(peer)
		return nil
	})
	if err == nil {
		return
	}
	q.mu.Lock()
	q.queued = false
	if !q.closed && errors.Is(err, eventloop.ErrQueueFull) {
		q.timer = time.AfterFunc(partyDeathQueueRetry, func() {
			q.mu.Lock()
			q.timer = nil
			q.mu.Unlock()
			s.submitPartyDeathQueue(peer)
		})
		q.mu.Unlock()
		return
	}
	q.mu.Unlock()
	q.stop()
	s.logGameEvent(peer, "game-dungeon-party-death-queue-closed", "error", err)
}

func (s *Service) runPartyDeathQueue(peer *gameSession) {
	s.drainPartyDeathVisuals(peer)
	q := &peer.partyDeaths
	q.mu.Lock()
	if q.closed || len(q.items) == 0 {
		q.queued = false
		q.mu.Unlock()
		return
	}
	d := q.items[0]
	version := q.version
	q.mu.Unlock()
	_ = s.deliverPartyDungeonDeath(d) // owns write-failure logging and transport semantics
	q.mu.Lock()
	if !q.closed && q.version == version {
		q.bytes -= len(d.body)
		q.items[0] = partyDungeonDeathDelivery{}
		q.items = q.items[1:]
		if len(q.items) == 0 {
			q.items = nil
		}
	}
	q.queued = false
	q.mu.Unlock()
	s.submitPartyDeathQueue(peer)
}

// Called only on the recipient's session owner, between complete events.
// A visual notice has no experience, quest or room-transition side effects.
// Do not make it wait for one mailbox wakeup per older progression event.
// State deliveries keep their FIFO and still follow the committed reward.
func (s *Service) drainPartyDeathVisuals(peer *gameSession) {
	q := &peer.partyDeaths
	for sent := 0; sent < 32; sent++ {
		q.mu.Lock()
		at := -1
		if !q.closed {
			for i := range q.items {
				if q.items[i].phase == partyDeathVisualOnly {
					at = i
					break
				}
			}
		}
		if at < 0 {
			q.mu.Unlock()
			return
		}
		d := q.items[at]
		q.bytes -= len(d.body)
		copy(q.items[at:], q.items[at+1:])
		q.items[len(q.items)-1] = partyDungeonDeathDelivery{}
		q.items = q.items[:len(q.items)-1]
		if len(q.items) == 0 {
			q.items = nil
		}
		q.mu.Unlock()
		// Rechecks character generation, scene, membership and transport under
		// the recipient lock. No foreign owner or direct producer writes.
		_ = s.deliverPartyDungeonDeath(d)
	}
}
