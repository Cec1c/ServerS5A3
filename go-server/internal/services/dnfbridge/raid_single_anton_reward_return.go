package dnfbridge

import (
	"context"
	"crypto/sha256"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

// The completion worker calls this after projecting the committed card list.
// A raid result page does not send the ordinary card-menu return request.
func (s *Service) sendCurrentSingleAntonRewardReturn(session *gameSession, state runtimeRaidState) error {
	if session == nil || state.Battle == nil || !s.currentRaidAuthenticated(session) {
		return nil
	}
	b := state.Battle
	f := b.RewardFlow[b.Phase]
	if b.Mode != 1 || !b.Single || !b.PhaseCompleted || b.TimedOut || b.Phase != 0 ||
		f == nil || !f.Single || f.Mode != 1 || f.Phase != b.Phase || f.InstanceID != b.InstanceID ||
		f.RaidKey != state.RaidKey || f.FinishedAt.IsZero() || f.SquadShownAt.IsZero() ||
		f.FinishedAt.Before(f.SquadShownAt.Add(currentAntonMovieWindow(f))) {
		return nil
	}
	// Only a successfully displayed, committed final list authorizes the
	// automatic transition. Never grant or reroll an item from a return path.
	var cards []raid.RewardDisplayCard
	for _, recipient := range f.Recipients {
		p, q := f.Party[recipient.CharacterID], f.Squad[recipient.CharacterID]
		if p == nil || !p.Committed || q == nil || !q.Committed {
			return nil
		}
		cards = append(cards, raid.RewardDisplayCard{CharacterID: recipient.CharacterID, CardType: 1, Items: currentAntonPartyDisplayItems(q.Plan)})
	}
	if len(cards) != 1 || cards[0].CharacterID != session.selectedCharacterID ||
		f.Recipients[0].AccountID != s.accountIDForSession(session) {
		return nil
	}
	body, err := raid.BuildRewardListBody(3, cards)
	if err != nil {
		return err
	}
	identity := currentRaidProjectionIdentity{InstanceID: b.InstanceID, Phase: b.Phase, CharID: session.selectedCharacterID, Generation: session.characterGeneration}
	o := &session.raidProjection
	o.mu.Lock()
	displayed := !o.failed && o.resultSent && o.resultIdentity == identity &&
		o.rewardDisplayIdentity == identity && o.rewardDisplaySignature == sha256.Sum256(body) && !o.rewardDisplayShownAt.IsZero()
	if displayed {
		deadline := o.rewardDisplayShownAt.Add(currentAntonMovieWindow(f))
		if wait := time.Until(deadline); wait > 0 {
			// A late subscriber still gets the full display window. One bounded
			// callback re-enters its session queue; it cannot return a replacement
			// character, connection or run, and has no reward-grant operation.
			if o.singleAntonReturnTimer == nil || o.singleAntonReturnAt != deadline {
				if o.singleAntonReturnTimer != nil {
					o.singleAntonReturnTimer.Stop()
				}
				o.singleAntonReturnAt = deadline
				frozen := cloneRuntimeRaidState(state)
				o.singleAntonReturnTimer = time.AfterFunc(wait, func() {
					o.mu.Lock()
					if o.singleAntonReturnAt == deadline {
						o.singleAntonReturnTimer = nil
					}
					o.mu.Unlock()
					ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
					defer cancel()
					if err := s.callGameSession(ctx, session, "single-anton-reward-return", func() error {
						return s.sendCurrentSingleAntonRewardReturn(session, frozen)
					}); err != nil {
						s.logGameEvent(session, "game-single-anton-reward-return-deferred", "error", err)
					}
				})
			}
			o.mu.Unlock()
			return nil
		}
		if o.singleAntonReturnTimer != nil {
			o.singleAntonReturnTimer.Stop()
			o.singleAntonReturnTimer = nil
		}
	}
	o.mu.Unlock()
	if !displayed {
		return nil
	}
	session.dungeon.mu.Lock()
	r := session.dungeon.runtime
	ready := s.currentSingleAntonRewardReturnReady(session, r, state)
	session.dungeon.mu.Unlock()
	if !ready {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	transition, err := s.prepareCurrentDungeonTownTransitionForSession(ctx, session, session.selectedCharacterID)
	if err != nil {
		return err
	}
	session.dungeon.mu.Lock()
	ready = s.currentSingleAntonRewardReturnReady(session, r, state)
	session.dungeon.mu.Unlock()
	if !ready {
		return nil
	}
	return s.sendCurrentCompletedDungeonReturnToTown(session, r, transition, 0, "single_anton_committed_reward_display_finished")
}

// Caller holds dungeon.mu; the owning session event loop serializes the
// subsequent actor route. Use the actual clear journal and frozen entry,
// independently of the ordinary result/card UI flags that this raid omits.
func (s *Service) currentSingleAntonRewardReturnReady(session *gameSession, r *runtimeDungeonState, captured runtimeRaidState) bool {
	if r == nil || r.raidEntry == nil || !r.raidEntry.singleAntonMode ||
		session.dungeon.runtime != r || r.townReturnOp24Sent ||
		!s.currentRaidCompletedMapExitReceiptReady(session, r, true, false, 1) {
		return false
	}
	e := r.raidEntry
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	live := s.raids[e.RaidKey]
	if live == nil || live.Battle == nil || live.RaidKey != captured.RaidKey {
		return false
	}
	b := live.Battle
	f := b.RewardFlow[b.Phase]
	p, participating := b.Participation[session.selectedCharacterID]
	return b.InstanceID == captured.Battle.InstanceID && b.Mode == 1 && b.Single && b.Phase == 0 &&
		b.PhaseCompleted && !b.TimedOut && len(b.SingleSequence) > 0 &&
		b.SingleSequence[len(b.SingleSequence)-1] == uint32(r.Dungeon.ID) &&
		f != nil && f.FinishedAt == captured.Battle.RewardFlow[0].FinishedAt &&
		s.currentAntonFlowWeeklyCommittedLocked(f) && participating &&
		p.Session == session && p.Runtime == r && p.Generation == session.characterGeneration &&
		p.RunID == e.RunID && p.Phase == e.Phase && p.Attempt == e.Attempt && p.DungeonID == uint32(r.Dungeon.ID)
}
