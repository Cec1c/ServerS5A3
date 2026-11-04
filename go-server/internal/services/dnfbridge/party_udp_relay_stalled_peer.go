package dnfbridge

import "time"

// A learned endpoint proves only that a member sent a packet in the past.
// Keep the existing never-learned bootstrap separate from a bounded recovery
// for a member whose previously established outgoing paths have all stopped.
type partyUDPRelayStallTiming struct {
	idle, confirm, poll time.Duration
}

func (r *currentPartyUDPRelay) stalledPeerTiming() partyUDPRelayStallTiming {
	t := r.stallTiming
	if t.idle <= 0 {
		t.idle = 15 * time.Second
	}
	if t.confirm <= 0 {
		t.confirm = 15 * time.Second
	}
	if t.poll <= 0 {
		t.poll = time.Second
	}
	return t
}

// The service supplies scene/generation guards. It never executes under the
// relay topology lock, and only the affected member receives a handshake.
type partyUDPRelayRecoveryPlan struct {
	current func() bool
	deliver func(func() bool) bool
}

type partyUDPRelayStalledLeg struct {
	leg, reverse              *currentPartyUDPRelayLeg
	received, reverseReceived uint64
}

func (r *currentPartyUDPRelay) watchStalledPartyMember(incoming, target *currentPartyUDPRelayLeg) {
	if r.prepareStalledPeer == nil || incoming.key.pvp || target.key.pvp {
		return
	}
	now, timing := time.Now(), r.stalledPeerTiming()
	// The normal packet path does one small per-leg check. Scan the bounded
	// topology only once a learned reverse leg has actually gone idle.
	target.mu.Lock()
	eligible := target.received > 0 && !target.lastReceivedAt.IsZero() &&
		now.Sub(target.lastReceivedAt) >= timing.idle && !target.stallWatching &&
		!target.stallRecoveryUsed && !now.Before(target.stallRetryAfter)
	target.mu.Unlock()
	if !eligible {
		return
	}
	r.mu.Lock()
	if r.done == nil || !r.pairCurrentLocked(incoming, target) {
		r.mu.Unlock()
		return
	}
	var legs []partyUDPRelayStalledLeg
	for key, leg := range r.legs {
		if key.pvp || key.partyID != target.key.partyID || key.ownerID != target.key.ownerID {
			continue
		}
		leg.mu.Lock()
		ok := leg.binding == target.binding && leg.received > 0 && leg.learnedEndpoint != nil &&
			!leg.lastReceivedAt.IsZero() && now.Sub(leg.lastReceivedAt) >= timing.idle &&
			!leg.stallWatching && !leg.stallRecoveryUsed && !now.Before(leg.stallRetryAfter)
		count := leg.received
		leg.mu.Unlock()
		reverse := r.legs[currentPartyUDPRelayLegKey{partyID: key.partyID, ownerID: key.peerID, peerID: key.ownerID}]
		if !ok || !r.pairCurrentLocked(leg, reverse) {
			r.mu.Unlock()
			return
		}
		reverse.mu.Lock()
		active, reverseCount := now.Sub(reverse.lastReceivedAt) < timing.idle, reverse.received
		reverse.mu.Unlock()
		if !active {
			r.mu.Unlock()
			return
		}
		legs = append(legs, partyUDPRelayStalledLeg{leg, reverse, count, reverseCount})
	}
	if len(legs) == 0 {
		r.mu.Unlock()
		return
	}
	for _, pair := range legs {
		pair.leg.mu.Lock()
		pair.leg.stallWatching = true
		pair.leg.mu.Unlock()
	}
	stop := r.done
	r.wg.Add(1)
	r.mu.Unlock()
	go r.confirmStalledPartyMember(stop, target, legs, timing)
}

func (r *currentPartyUDPRelay) stalledMemberCurrent(legs []partyUDPRelayStalledLeg, timing partyUDPRelayStallTiming, enoughTraffic bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(legs) == 0 {
		return false
	}
	member := legs[0].leg.key
	count := 0
	for key := range r.legs {
		if key.pvp == member.pvp && key.partyID == member.partyID && key.ownerID == member.ownerID {
			count++
		}
	}
	if count != len(legs) {
		return false
	}
	for _, pair := range legs {
		if !r.pairCurrentLocked(pair.leg, pair.reverse) {
			return false
		}
		pair.leg.mu.Lock()
		unchanged := pair.leg.received == pair.received
		pair.leg.mu.Unlock()
		pair.reverse.mu.Lock()
		active := time.Since(pair.reverse.lastReceivedAt) < timing.idle
		progress := pair.reverse.received >= pair.reverseReceived+32
		pair.reverse.mu.Unlock()
		if !unchanged || !active || (enoughTraffic && !progress) {
			return false
		}
	}
	return true
}

func (r *currentPartyUDPRelay) confirmStalledPartyMember(stop <-chan struct{}, target *currentPartyUDPRelayLeg, legs []partyUDPRelayStalledLeg, timing partyUDPRelayStallTiming) {
	defer r.wg.Done()
	defer func() {
		for _, pair := range legs {
			pair.leg.mu.Lock()
			pair.leg.stallWatching = false
			pair.leg.stallRetryAfter = time.Now().Add(timing.idle)
			pair.leg.mu.Unlock()
		}
	}()
	plan := r.prepareStalledPeer(target.key.partyID, target.binding)
	if plan == nil || plan.current == nil || plan.deliver == nil {
		return
	}
	current := func() bool { return r.stalledMemberCurrent(legs, timing, false) && plan.current() }
	if !current() {
		return
	}
	timer := time.NewTimer(timing.confirm)
	ticker := time.NewTicker(timing.poll)
	defer timer.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if !current() {
				return
			}
		case <-timer.C:
			if !current() || !r.stalledMemberCurrent(legs, timing, true) {
				return
			}
			// Spend the lifecycle budget before requesting a wire write. Errors
			// never start an unbounded loop of handshakes against the client.
			for _, pair := range legs {
				pair.leg.mu.Lock()
				pair.leg.stallRecoveryUsed = true
				pair.leg.mu.Unlock()
			}
			delivered := plan.deliver(func() bool { return current() && r.stalledMemberCurrent(legs, timing, true) })
			if delivered {
				for _, pair := range legs {
					pair.leg.mu.Lock()
					pair.leg.stallRecoverySent = true
					pair.leg.mu.Unlock()
				}
			}
			r.log("party UDP relay stalled member recovery requested", "party_id", target.key.partyID,
				"member_id", target.key.ownerID, "generation", target.binding.generation,
				"peer_count", len(legs), "delivered", delivered,
				"meaning", "one_endpoint_refresh_requested_not_proof_of_client_recovery")
			return
		}
	}
}

func (r *currentPartyUDPRelay) logStalledMemberTrafficResumed(leg *currentPartyUDPRelayLeg) {
	if r.prepareStalledPeer == nil {
		return
	}
	leg.mu.Lock()
	observed := leg.stallRecoverySent && !leg.stallRecoveryObserved
	if observed {
		leg.stallRecoveryObserved = true
	}
	leg.mu.Unlock()
	if observed {
		r.log("party UDP relay inbound observed after recovery request", "party_id", leg.key.partyID,
			"member_id", leg.key.ownerID, "peer_id", leg.key.peerID,
			"meaning", "server_inbound_resumed_not_proof_of_client_action_application_or_causality")
	}
}
