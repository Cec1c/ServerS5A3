package dnfbridge

import (
	"context"
	"fmt"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

const currentRaidClockName = "raid:active-instances"

// Use the process gameplay time queue so shutdown cancels the scheduler.
// No goroutine/timer is created per dungeon or per participant.
func (s *Service) startCurrentRaidClock(ctx context.Context) {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return
	}
	if err := s.scheduleCurrentRaidClock(ctx); err != nil {
		s.logWarn("schedule raid clock failed", "error", err)
	}
}

func (s *Service) scheduleCurrentRaidClock(ctx context.Context) error {
	queue := s.ensureGameplayTimeQueue()
	if queue == nil {
		return errGameplayTimeQueueUnavailable
	}
	return queue.ScheduleAfter(currentRaidClockName, time.Second, func(_ time.Time) {
		if ctx != nil && ctx.Err() != nil {
			return
		}
		// Read now, rather than the scheduled due time: a delayed callback must
		// replay all elapsed deadlines through the domain reducer.
		s.expireCurrentRaidDisconnectSeats(queue.Now())
		states, errors := s.advanceCurrentRaidClocks(queue.Now())
		s.queueCurrentRaidWeeklyClears()
		s.queueCurrentAntonRewardFlows()
		for _, err := range errors {
			s.logWarn("raid clock deferred", "error", err)
		}
		for _, state := range states {
			s.queueCurrentRaidDungeonProgress(state)
		}
		// Retry only exact timed-out active owners; scene confirmation owns retirement.
		s.queueCurrentRaidDeadlineReturns(queue.Now())
		if ctx == nil || ctx.Err() == nil {
			if err := s.scheduleCurrentRaidClock(ctx); err != nil {
				s.logWarn("reschedule raid clock failed", "error", err)
			}
		}
	})
}

func (s *Service) advanceCurrentRaidClocks(now time.Time) ([]runtimeRaidState, []error) {
	if s == nil || s.options.currentClientVersion != "official-24.9" || now.IsZero() {
		return nil, nil
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	var states []runtimeRaidState
	var errors []error
	for _, state := range s.raids {
		if state == nil || !state.Started || state.Battle == nil {
			continue
		}
		b := state.Battle
		if b.PhaseCompleted {
			if !b.Single && b.Phase == 0 && b.RewardFlow[0] != nil && !b.RewardFlow[0].BreakUntil.IsZero() && !now.Before(b.RewardFlow[0].BreakUntil) {
				started, err := s.beginCurrentAntonNextPhaseLocked(state, now)
				if err != nil {
					errors = append(errors, err)
				}
				if started {
					states = append(states, cloneRuntimeRaidState(*state))
					continue
				}
			}
			if err := s.freezeCurrentAntonRewardFlowLocked(*state); err != nil {
				errors = append(errors, err)
			}
			if b.Single || b.Phase == 1 {
				if err := s.freezeCurrentRaidFinalWeeklyIntentLocked(*state); err != nil {
					errors = append(errors, fmt.Errorf("raid %d final weekly entitlement: %w", state.RaidKey, err))
				}
			}
			continue
		}
		if b.TimedOut || now.Before(b.EnterAfter) {
			continue
		}
		if !now.Before(b.Deadline) {
			// Expiration is a failure, never a successful phase completion. It
			// closes admission without creating a clear/reward/weekly record.
			if b.Mode == 2 && b.Phase == 0 && currentRaidHasTwoPhaseProgram(b) {
				next, err := (&raid.LukeProgression{AntonProgression: b.Anton}).EndPhase(b.Deadline)
				if err != nil {
					errors = append(errors, fmt.Errorf("Luke deadline rule: %w", err))
					continue
				}
				if !next.PhaseFailed() {
					errors = append(errors, fmt.Errorf("Luke deadline did not execute FAIL PHASE"))
					continue
				}
				b.Anton = next.AntonProgression
			}
			b.TimedOut = true
			for i := range b.Dungeons {
				b.Dungeons[i].State = 2
			}
			b.ProgressVersion++
			states = append(states, cloneRuntimeRaidState(*state))
			continue
		}
		if b.Single || b.Anton == nil {
			continue
		}
		if now.Before(b.Anton.LastTime()) {
			continue
		}
		next, err := b.Anton.Tick(now)
		if err != nil {
			errors = append(errors, fmt.Errorf("raid %d clock: %w", state.RaidKey, err))
			continue
		}
		changed := !b.Anton.SameProjection(next)
		b.Anton, b.Dungeons, b.PhaseCompleted = next, next.Dungeons(), next.PhaseCleared()
		if changed {
			b.ProgressVersion++
			states = append(states, cloneRuntimeRaidState(*state))
		}
	}
	return states, errors
}
