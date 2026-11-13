package dnfbridge

import "time"

// Mutated only on the bound session event loop. The cycle coordinator only
// reads ready under loadingMu after preparation publishes its immutable data.
type partyLoadingPreparation struct {
	owner      partyDungeonLoadingOwner
	wantTail   bool
	ready      bool
	activation []func() error
	after      []func() error
	finishBody []byte
}

func (p *partyLoadingPreparation) finishAfterActivation() error {
	work := p.after
	p.after = nil
	for _, run := range work {
		if err := run(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) markPartyLoadingPrepared(partyID uint16, cycle *partyDungeonLoadingCycle, id uint16, plan *partyLoadingPreparation, trigger string) {
	s.partyDungeonLoadingMu.Lock()
	if s.partyDungeonLoadingCycles[partyID] != cycle || cycle.preparing[id] != plan {
		s.partyDungeonLoadingMu.Unlock()
		return
	}
	plan.ready = true
	ready := !cycle.activationScheduled
	for _, p := range cycle.preparing {
		if !p.ready {
			ready = false
		}
	}
	type activationTarget struct {
		owner partyDungeonLoadingOwner
		tail  bool
	}
	var plans map[uint16]activationTarget
	if ready {
		cycle.activationScheduled = true
		if cycle.Timer != nil {
			cycle.Timer.Stop()
			cycle.Timer = nil
		}
		plans = make(map[uint16]activationTarget, len(cycle.preparing))
		for k, p := range cycle.preparing {
			plans[k] = activationTarget{p.owner, p.wantTail}
		}
	}
	s.partyDungeonLoadingMu.Unlock()
	s.logGameEvent(plan.owner.session, "game-party-dungeon-loading-prepared", "party_id", partyID, "char_id", id, "cycle", cycle.Cycle, "all_prepared", ready)
	if !ready {
		return
	}
	// Do not wait in a session event; a peer may still be completing its own
	// preparation callback. The existing dispatch retains owner/generation and
	// queue-capacity guards for each activation as well.
	for char, p := range plans {
		if p.owner.session.events == nil || p.owner.session.events.loop == nil {
			s.releasePartyDungeonLoadingMemberPhase(partyID, cycle, char, p.owner, p.tail, trigger, 1)
		} else {
			go s.releasePartyDungeonLoadingMemberPhase(partyID, cycle, char, p.owner, p.tail, trigger, 1)
		}
	}
}

// Snapshot liveness outside loadingMu. A slow connected member never expires;
// only a departed member can be removed from the preparation set. Retry failed
// preparation on its own event loop, with the original scene/binding guards.
func (s *Service) checkPartyLoadingPreparation(partyID uint16, cycle *partyDungeonLoadingCycle, trigger string) {
	s.partyDungeonLoadingMu.Lock()
	if s.partyDungeonLoadingCycles[partyID] != cycle || cycle.activationScheduled {
		s.partyDungeonLoadingMu.Unlock()
		return
	}
	plans := make(map[uint16]*partyLoadingPreparation, len(cycle.preparing))
	for id, plan := range cycle.preparing {
		plans[id] = plan
	}
	s.partyDungeonLoadingMu.Unlock()
	departed := make(map[uint16]bool)
	for id, plan := range plans {
		peer, online := s.onlineGameSession(id)
		owner := plan.owner.session
		departed[id] = !online || peer == nil || owner.ctx != nil && owner.ctx.Err() != nil || uint16(s.runtimePartyStateForSession(owner).PartyID) != partyID
	}
	s.partyDungeonLoadingMu.Lock()
	if s.partyDungeonLoadingCycles[partyID] != cycle || cycle.activationScheduled {
		s.partyDungeonLoadingMu.Unlock()
		return
	}
	if cycle.Timer != nil {
		cycle.Timer.Stop()
	}
	var readyID uint16
	var readyPlan *partyLoadingPreparation
	type retryTarget struct {
		owner partyDungeonLoadingOwner
		tail  bool
	}
	retry := make(map[uint16]retryTarget)
	for id, plan := range plans {
		if cycle.preparing[id] != plan {
			continue
		}
		if departed[id] {
			delete(cycle.preparing, id)
			continue
		}
		if plan.ready {
			readyID, readyPlan = id, plan
		} else {
			retry[id] = retryTarget{plan.owner, plan.wantTail}
		}
	}
	if len(cycle.preparing) == 0 {
		cycle.Timer = nil
		cycle.activationScheduled = true
		s.partyDungeonLoadingMu.Unlock()
		return
	}
	cycle.Timer = time.AfterFunc(partyDungeonLoadingBarrierTimeout, func() { s.checkPartyLoadingPreparation(partyID, cycle, "prepare_watchdog") })
	s.partyDungeonLoadingMu.Unlock()
	if readyPlan != nil {
		s.markPartyLoadingPrepared(partyID, cycle, readyID, readyPlan, trigger)
	}
	for id, plan := range retry {
		go s.releasePartyDungeonLoadingMemberPhase(partyID, cycle, id, plan.owner, plan.tail, trigger, 0)
	}
}
