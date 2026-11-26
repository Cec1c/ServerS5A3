package dnfbridge

import (
	"context"
	"errors"
	"sync"
	"time"
)

const partyLoadingDeadline = 25 * time.Second

func partyLoadingWatchdogDelay(cycle *partyDungeonLoadingCycle) time.Duration {
	if cycle.started.IsZero() {
		return partyDungeonLoadingBarrierTimeout
	}
	age := time.Since(cycle.started)
	target := partyLoadingDeadline
	if age < 10*time.Second {
		target = 10 * time.Second
	}
	// After retiring a stalled peer, give the surviving owners one bounded
	// dispatch turn to prepare/activate; a zero-delay watchdog would retire
	// them before their already-admitted work can run.
	return min(partyDungeonLoadingBarrierTimeout, max(time.Second, target-age))
}

// partyLoadingPreparationWatchdogDelay wakes the preparation watchdog based on
// actual preparation age (instead of firing immediately when op37 loading took
// ~9-10s), while still honoring the shared 25-second cycle deadline.
func partyLoadingPreparationWatchdogDelay(cycle *partyDungeonLoadingCycle) time.Duration {
	if cycle == nil {
		return partyDungeonLoadingBarrierTimeout
	}
	if !cycle.prepareStarted.IsZero() {
		delay := partyDungeonLoadingBarrierTimeout
		if !cycle.prepareWarned {
			delay = min(delay, max(time.Second, partyLoadingPreparationSlowThreshold-time.Since(cycle.prepareStarted)))
		}
		if !cycle.started.IsZero() {
			delay = min(delay, max(time.Second, partyLoadingDeadline-time.Since(cycle.started)))
		}
		return delay
	}
	return partyLoadingWatchdogDelay(cycle)
}

// Called outside loadingMu. Readiness is frozen only while choosing the
// timeout action; normal member retirement remains the authority for removal.
func (s *Service) expirePartyLoadingCycle(partyID uint16, cycle *partyDungeonLoadingCycle, preparing bool) bool {
	s.partyDungeonLoadingMu.Lock()
	if cycle == nil || s.partyDungeonLoadingCycles[partyID] != cycle || cycle.expiring || cycle.started.IsZero() || time.Since(cycle.started) < partyLoadingDeadline {
		s.partyDungeonLoadingMu.Unlock()
		return false
	}
	if preparing && cycle.activationScheduled || !preparing && cycle.Released {
		s.partyDungeonLoadingMu.Unlock()
		return false
	}
	missing := make(map[uint16]partyDungeonLoadingOwner)
	for _, id := range cycle.membersAt {
		ready := cycle.Ready[id]
		if preparing {
			p := cycle.preparing[id]
			ready = p != nil && p.ready
		}
		if !ready {
			missing[id] = cycle.owners[id]
		}
	}
	if len(missing) == 0 {
		s.partyDungeonLoadingMu.Unlock()
		return false
	}
	// Only one stalled member can be isolated unambiguously. A broader
	// failure returns the whole group rather than activating a minority.
	if len(missing) != 1 {
		for id, owner := range cycle.owners {
			missing[id] = owner
		}
	}
	cycle.expiring = true
	if cycle.Timer != nil {
		cycle.Timer.Stop()
		cycle.Timer = nil
	}
	s.partyDungeonLoadingMu.Unlock()
	var wg sync.WaitGroup
	for id, owner := range missing {
		if owner.session == nil {
			continue
		}
		wg.Add(1)
		go func(id uint16, owner partyDungeonLoadingOwner) {
			defer wg.Done()
			p := owner.session
			identity, ok := s.boundGameSessionCharacterSnapshot(p)
			if !ok {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			defer cancel()
			err := s.callGameSession(ctx, p, "party-loading-deadline", func() error {
				if !s.boundGameSessionCharacterCurrent(identity) || snapshotPartyDungeonLoadingOwner(p) != owner {
					return nil
				}
				s.partyDungeonLoadingMu.Lock()
				current := s.partyDungeonLoadingCycles[partyID] == cycle && cycle.expiring
				s.partyDungeonLoadingMu.Unlock()
				if !current || uint16(s.runtimePartyStateForSession(p).PartyID) != partyID {
					return nil
				}
				s.logGameEvent(p, "game-party-loading-deadline", "party_id", partyID, "cycle", cycle.Cycle, "char_id", id, "preparing", preparing, "returning_members", len(missing))
				if owner.runtime == nil || owner.runtime.partyInstance == nil {
					return s.handleOnlineLeaveParty(p, 13)
				}
				removed, err := s.leaveRuntimePartyDungeonMember(p, owner.runtime.partyInstance, "loading_deadline", true)
				if !removed {
					return err
				}
				p.pendingPostStartMapMu.Lock()
				if p.pendingPostStartMapPlacement != nil && p.pendingPostStartMapPlacement.Runtime == owner.runtime {
					p.pendingPostStartMapPlacement = nil
				}
				p.pendingPostStartMapMu.Unlock()
				return errors.Join(err, s.handleDungeonBackToVillage(p, nil))
			})
			if err != nil {
				s.logGameEvent(p, "game-party-loading-deadline-failed", "party_id", partyID, "char_id", id, "error", err)
			}
		}(id, owner)
	}
	wg.Wait()
	s.partyDungeonLoadingMu.Lock()
	current := s.partyDungeonLoadingCycles[partyID] == cycle
	cycle.expiring = false
	s.partyDungeonLoadingMu.Unlock()
	if current {
		// Re-evaluate the authoritative membership after retirement. Do not
		// remove an unresponsive or replaced owner merely from a stale snapshot.
		if preparing {
			s.checkPartyLoadingPreparation(partyID, cycle, "deadline_retired")
		} else {
			s.forceReleasePartyDungeonLoadingCycle(partyID, cycle, "deadline_retired")
		}
	}
	return true
}
