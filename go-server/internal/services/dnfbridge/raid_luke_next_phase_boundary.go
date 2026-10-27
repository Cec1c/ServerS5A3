package dnfbridge

import "time"

// Local display completion is independent of the global phase boundary below.
// The visible qualified party must include the current reward owner and every
// one of its cards must have committed before native category2 can skip team.
func currentLukeLocalPartyDisplayCommitted(f *currentAntonRewardFlow, owner uint16, visible map[uint16]bool) bool {
	if f == nil || f.Mode != 2 || f.Phase != 0 || f.Single || !visible[owner] {
		return false
	}
	found := false
	for _, recipient := range f.Recipients {
		if !visible[recipient.CharacterID] {
			continue
		}
		card := f.Party[recipient.CharacterID]
		if card == nil || !card.Committed {
			return false
		}
		found = found || recipient.CharacterID == owner
	}
	return found
}

// Run after the manual reveal, within the recipient's own session event. A
// periodic projection also retries this, but waiting for that tick permits the
// native party page to enter its nonexistent team page first.
func (s *Service) sendCurrentLukeLocalPartyCompletion(session *gameSession, captured runtimeRaidState) error {
	if captured.Battle == nil || captured.Battle.Mode != 2 || captured.Battle.Phase != 0 || captured.Battle.Single {
		return nil
	}
	latest, err := s.latestCurrentRaidProjectionState(session, captured)
	if err != nil {
		return err
	}
	if !currentLukeLocalPartyDisplayCommitted(latest.Battle.RewardFlow[0], session.selectedCharacterID, s.currentRaidPartyRewardDisplayMembers(session, latest)) {
		return nil
	}
	return s.sendCurrentAntonRewardFlowProjection(session, latest)
}

// currentLukePhase0PartyOnlyRewardSettled reports that every eligible reward
// owner has committed the actual phase-zero party card. Luke's first phase has
// no team-card pool in the current PVF, so there is no later display stage to
// wait for. All reward groups still have to finish before this returns true.
func currentLukePhase0PartyOnlyRewardSettled(f *currentAntonRewardFlow) bool {
	if f == nil || f.Mode != 2 || f.Phase != 0 || f.Single || f.squadRequired() || len(f.Recipients) == 0 {
		return false
	}
	for _, owner := range f.Recipients {
		card := f.Party[owner.CharacterID]
		if card == nil || !card.Committed {
			return false
		}
	}
	return true
}

// currentRaidRewardFlowSettlementReady is the next-phase reward boundary.
// Normal flows retain both the completed display and break anchors. The stated
// Luke party-only exception skips only the impossible team-card display wait.
func currentRaidRewardFlowSettlementReady(f *currentAntonRewardFlow, now time.Time) bool {
	if f == nil {
		return false
	}
	if currentLukePhase0PartyOnlyRewardSettled(f) {
		return true
	}
	return !f.FinishedAt.IsZero() && !f.BreakUntil.IsZero() && !now.Before(f.FinishedAt)
}

func currentRaidRewardFlowSettlementCompleted(f *currentAntonRewardFlow) bool {
	return f != nil && (!f.FinishedAt.IsZero() || currentLukePhase0PartyOnlyRewardSettled(f))
}
