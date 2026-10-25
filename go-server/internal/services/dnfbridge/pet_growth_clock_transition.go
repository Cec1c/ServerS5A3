package dnfbridge

import (
	"errors"
	"fmt"
	"time"
)

// Transitions retain their original boundary if settling the preceding segment
// fails. A later retry must not apply the old mode to the entire elapsed time.
type currentPetGrowthClockTransition struct {
	mode        currentPetGrowthClockMode
	characterID uint16
	at          time.Time
	source      string
}

func queueCurrentPetGrowthClockTransition(session *gameSession, mode currentPetGrowthClockMode, at time.Time, source string) error {
	if session == nil || session.selectedCharacterID == 0 || mode == currentPetGrowthClockStopped {
		return nil
	}
	session.petGrowth.mu.Lock()
	defer session.petGrowth.mu.Unlock()
	clock := &session.petGrowth
	lastMode, lastCharacter, lastAt := clock.mode, clock.characterID, clock.anchor
	if len(clock.pending) != 0 {
		last := clock.pending[len(clock.pending)-1]
		lastMode, lastCharacter, lastAt = last.mode, last.characterID, last.at
	}
	if lastCharacter != 0 && lastCharacter != session.selectedCharacterID {
		return fmt.Errorf("pet growth transition character owner changed")
	}
	if at.IsZero() || (!lastAt.IsZero() && at.Before(lastAt)) {
		return fmt.Errorf("pet growth transition precedes the last clock boundary")
	}
	if lastMode == mode && lastCharacter == session.selectedCharacterID && !lastAt.IsZero() {
		return nil
	}
	clock.pending = append(clock.pending, currentPetGrowthClockTransition{
		mode: mode, characterID: session.selectedCharacterID, at: at, source: source,
	})
	return nil
}

func (s *Service) applyCurrentPetGrowthTransitionsLocked(session *gameSession) error {
	clock := &session.petGrowth
	for len(clock.pending) != 0 {
		next := clock.pending[0]
		if next.characterID != session.selectedCharacterID {
			return fmt.Errorf("pet growth pending transition character owner changed")
		}
		if clock.mode != currentPetGrowthClockStopped && !clock.anchor.IsZero() {
			if err := s.applyCurrentPetGrowthElapsedLocked(session, next.at, next.source+"_before_mode_switch"); err != nil {
				return err
			}
		}
		previous := clock.mode
		clock.mode, clock.characterID, clock.anchor = next.mode, next.characterID, next.at
		clock.generation++
		clock.pending[0] = currentPetGrowthClockTransition{}
		clock.pending = clock.pending[1:]
		if len(clock.pending) == 0 {
			clock.pending = nil
		}
		s.logGameEvent(session, "game-pet-growth-clock-switched",
			"char_id", clock.characterID, "previous_mode", previous.String(),
			"mode", clock.mode.String(), "source", next.source, "anchor_unix_ms", next.at.UnixMilli(),
			"timing_source", "existing_60s_decay_360s_recovery_runtime_constants")
	}
	return nil
}

// Call after releasing dungeon.mu. Only recording a boundary may nest
// dungeon.mu -> petGrowth.mu; repository I/O and scheduler work do not.
func (s *Service) flushCurrentPetGrowthClockTransitions(session *gameSession, source string) {
	if session == nil {
		return
	}
	session.petGrowth.mu.Lock()
	defer session.petGrowth.mu.Unlock()
	if len(session.petGrowth.pending) == 0 && session.petGrowth.timerName != "" {
		return
	}
	err := s.applyCurrentPetGrowthTransitionsLocked(session)
	// Even if settlement failed, retain the queue and arm a retry on the
	// existing shared timer. The original transition timestamps stay fixed.
	err = errors.Join(err, s.armCurrentPetGrowthTickLocked(session))
	if err != nil {
		s.logGameEvent(session, "game-pet-growth-clock-transition-deferred",
			"char_id", session.selectedCharacterID, "source", source,
			"pending", len(session.petGrowth.pending), "error", err)
	}
}

// The caller holds dungeon.mu and has installed this exact runtime.
func (s *Service) queueCurrentPetGrowthDungeonEntryClock(session *gameSession, runtime *runtimeDungeonState) {
	if session.dungeon.runtime != runtime || runtime.petGrowthEntryPending || !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		return
	}
	if err := queueCurrentPetGrowthClockTransition(session, currentPetGrowthClockDungeon, s.gameplayNow(), "dungeon_runtime_committed"); err != nil {
		s.logGameEvent(session, "game-pet-growth-clock-transition-rejected", "source", "dungeon_runtime_committed", "error", err)
	}
}

func (s *Service) activateCurrentPetGrowthDungeonClock(session *gameSession, runtime *runtimeDungeonState) {
	if session == nil || runtime == nil {
		return
	}
	session.dungeon.mu.Lock()
	if session.dungeon.runtime == runtime && dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		runtime.petGrowthEntryPending = false
	}
	s.queueCurrentPetGrowthDungeonEntryClock(session, runtime)
	session.dungeon.mu.Unlock()
	s.flushCurrentPetGrowthClockTransitions(session, "party_dungeon_group_committed")
}
