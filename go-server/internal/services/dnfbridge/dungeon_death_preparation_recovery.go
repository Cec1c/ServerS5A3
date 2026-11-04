package dnfbridge

import (
	"context"
	"errors"
	"sort"
	"time"
)

// This error is created only inside the callback invoked after the room has
// validated the live actor and its scene binding. Admission failures are never
// converted into deferred deaths.
type dungeonDeathPreparationError struct{ cause error }

func (e *dungeonDeathPreparationError) Error() string { return e.cause.Error() }
func (e *dungeonDeathPreparationError) Unwrap() error { return e.cause }

type pendingDungeonDeathPreparation struct {
	room       *runtimeDungeonRoom
	character  uint16
	generation uint64
	body       []byte
	sequence   *uint64
	attempts   uint
	next       time.Time
}

func (s *Service) deferDungeonDeathPreparation(p *gameSession, rt *runtimeDungeonState, key uint32, body []byte, sequence *uint64, err error) bool {
	var preparation *dungeonDeathPreparationError
	if !errors.As(err, &preparation) {
		return false
	}
	if rt.pendingDeathPreparations == nil {
		rt.pendingDeathPreparations = make(map[uint32]*pendingDungeonDeathPreparation)
	}
	pending := rt.pendingDeathPreparations[key]
	if pending == nil || pending.room != rt.Room {
		pending = &pendingDungeonDeathPreparation{room: rt.Room, character: p.selectedCharacterID, generation: p.characterGeneration, body: append([]byte(nil), body...)}
		if sequence != nil {
			value := *sequence
			pending.sequence = &value
		}
		rt.pendingDeathPreparations[key] = pending
	}
	pending.attempts++
	delay := 5 * time.Second << min(pending.attempts-1, 3)
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	pending.next = s.gameplayNow().Add(delay)
	if pending.attempts == 1 || pending.attempts&(pending.attempts-1) == 0 {
		reason := "preparation_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "preparation_deadline"
		}
		s.logGameEvent(p, "game-dungeon-death-preparation-deferred", "dungeon_id", rt.Dungeon.ID, "object_key", key, "reason", reason)
	}
	return true
}

// Called by the existing character-bound material recovery event, outside the
// dungeon mutex. One retry per tick and backoff prevent a busy database from
// receiving a new burst of every pending death. The original handler retains
// all actor/owner/party and durable reward checks.
func (s *Service) recoverDungeonDeathPreparation(p *gameSession) error {
	if p == nil || p.loginRevoked.Load() {
		return nil
	}
	p.dungeon.mu.Lock()
	rt := p.dungeon.runtime
	if rt == nil {
		p.dungeon.mu.Unlock()
		return nil
	}
	var keys []uint32
	for key, pending := range rt.pendingDeathPreparations {
		if pending.room != rt.Room || pending.character != p.selectedCharacterID || pending.generation != p.characterGeneration {
			delete(rt.pendingDeathPreparations, key)
		} else if !s.gameplayNow().Before(pending.next) {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return rt.pendingDeathPreparations[keys[i]].next.Before(rt.pendingDeathPreparations[keys[j]].next)
	})
	if len(keys) == 0 {
		p.dungeon.mu.Unlock()
		return nil
	}
	key := keys[0]
	pending := rt.pendingDeathPreparations[key]
	attempts := pending.attempts
	p.dungeon.mu.Unlock()
	err := s.handleDungeonMonsterDeathRequest(p, pending.body, pending.sequence)
	p.dungeon.mu.Lock()
	// A changed admission condition must not keep retrying an invalid request.
	if remaining := rt.pendingDeathPreparations[key]; remaining == pending && remaining.attempts == attempts {
		delete(rt.pendingDeathPreparations, key)
	}
	p.dungeon.mu.Unlock()
	return err
}
