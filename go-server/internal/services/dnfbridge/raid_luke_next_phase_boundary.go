package dnfbridge

import "time"

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
