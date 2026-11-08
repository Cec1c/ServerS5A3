package dnfbridge

import "time"

func (s *Service) currentRaidFailurePresentationNow() time.Time {
	if s.gameplayTimers != nil {
		return s.gameplayTimers.Now()
	}
	return time.Now()
}

// The active dungeon return must not immediately replace the failure movie.
// Client completion can release it early; missing completion uses the actual
// configured movie window, measured from this connection's successful result.
func (s *Service) currentRaidFailurePresentationReady(p *gameSession, state runtimeRaidState) bool {
	b := state.Battle
	if b == nil || !b.TimedOut {
		return true
	}
	identity := currentRaidProjectionIdentity{InstanceID: b.InstanceID, Phase: b.Phase, CharID: p.selectedCharacterID, Generation: p.characterGeneration}
	o := &p.raidProjection
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.resultSent || o.resultIdentity != identity || o.failed || o.failureResultAt.IsZero() {
		return false
	}
	if o.movieCompleted && o.movieIdentity == identity {
		return true
	}
	var seconds uint32
	for _, n := range b.AntonFlowConfig.MovieEndSeconds {
		seconds = max(seconds, n)
	}
	if b.FiendWarConfig != nil {
		for _, n := range b.FiendWarConfig.Battle.Raid.MovieEndSeconds {
			seconds = max(seconds, uint32(n))
		}
	}
	return !s.currentRaidFailurePresentationNow().Before(o.failureResultAt.Add(time.Duration(seconds) * time.Second))
}

// Failure movie requests own no reward eligibility. The normal success/card
// owner remains unchanged; no clear counters or reward-ready bits are set.
func (s *Service) handleCurrentRaidFailureMovieCompletion(p *gameSession, op uint16, completed bool) (bool, error) {
	if !s.currentRaidAuthenticated(p) {
		return false, nil
	}
	s.raidMu.Lock()
	live := s.runtimeRaidByMemberLocked(p.selectedCharacterID)
	var state runtimeRaidState
	if live != nil && live.Battle != nil && live.Battle.TimedOut && !live.Battle.PhaseCompleted {
		state = cloneRuntimeRaidState(*live)
	}
	s.raidMu.Unlock()
	b := state.Battle
	if b == nil {
		return false, nil
	}
	if b.ChannelID != p.channel.ID || b.ChannelType != p.channel.Type || b.MemberAccounts[p.selectedCharacterID] != s.accountIDForSession(p) || !currentAntonResultDelivered(p, state) {
		return true, s.sendRaidCommandFailure(p, op)
	}
	if !completed {
		return true, s.sendCurrentRaidMovieSkipPrompt(p, state, op)
	}
	identity := currentRaidProjectionIdentity{InstanceID: b.InstanceID, Phase: b.Phase, CharID: p.selectedCharacterID, Generation: p.characterGeneration}
	o := &p.raidProjection
	o.mu.Lock()
	done := o.movieCompleted && o.movieIdentity == identity
	o.mu.Unlock()
	packets := []currentAntonRewardPacket{{opcode: op, class: 1, body: []byte{1}}}
	if !done {
		packets = append(packets, currentAntonRewardPacket{opcode: 587, class: 0, body: []byte{4, 1}})
	}
	if err := s.sendCurrentAntonRewardPackets(p, state, packets, false); err != nil {
		return true, err
	}
	o.mu.Lock()
	o.movieIdentity = identity
	o.movieCompleted = true
	o.mu.Unlock()
	return true, nil
}
