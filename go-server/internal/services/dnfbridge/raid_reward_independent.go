package dnfbridge

import (
	"crypto/sha256"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

// These are presentation/claim boundaries for one frozen owner. FinishedAt and
// BreakUntil remain whole-raid boundaries and must never be used to gate a
// different owner's card page.
func currentRaidOwnerRewardsCommitted(f *currentAntonRewardFlow, character uint16) bool {
	if f == nil {
		return false
	}
	for _, owner := range f.Recipients {
		if owner.CharacterID == character {
			p, q := f.Party[character], f.Squad[character]
			return p != nil && p.Committed && (!f.squadRequired() || q != nil && q.Committed)
		}
	}
	return false
}

func currentRaidOwnerPartySelectAt(f *currentAntonRewardFlow, character uint16) time.Time {
	if shown := f.PartySelectAfter[character]; !shown.IsZero() {
		return shown
	}
	if f.Mode == 1 && f.Phase == 0 && !f.Single {
		// Preserve the unattended first-phase fallback, without borrowing
		// another connection's screen delivery time.
		return f.AutoSelectAt.Add(currentAntonMovieWindow(f))
	}
	return f.AutoSelectAt
}

func currentRaidOwnerSquadReady(f *currentAntonRewardFlow, character uint16, now time.Time) bool {
	p := f.Party[character]
	if p == nil || !p.Committed {
		return false
	}
	if f.TeamDisplayReady[character] {
		return true
	}
	if f.Mode == 1 && f.Phase == 0 && !f.Single && !p.Manual {
		return !p.CommittedAt.IsZero() && !now.Before(p.CommittedAt.Add(currentRaidNativeCardChoiceWindow))
	}
	return true
}

// Team rewards have no personal choice. Once one participant reaches the team
// page, commit the frozen team pool for every eligible owner independently.
// Their personal party selection and movie clocks remain untouched.
func currentRaidTeamRewardGrantReady(f *currentAntonRewardFlow, now time.Time) bool {
	for _, owner := range f.Recipients {
		if currentRaidOwnerSquadReady(f, owner.CharacterID, now) {
			return true
		}
	}
	return false
}

// Return ownership is bound to the viewer's actually delivered team row, not
// the changing whole-raid display. A peer retry must not restart this clock.
func currentRaidOwnerSquadDisplaySignature(f *currentAntonRewardFlow, character uint16) [32]byte {
	if f == nil || f.Squad[character] == nil || !f.Squad[character].Committed {
		return [32]byte{}
	}
	body, err := raid.BuildRewardListBody(3, []raid.RewardDisplayCard{{CharacterID: character, CardType: 1, Items: currentAntonPartyDisplayItems(f.Squad[character].Plan)}})
	if err != nil {
		return [32]byte{}
	}
	return sha256.Sum256(body)
}

func markCurrentRaidCardCommitted(card *currentAntonRewardCard, at time.Time, manual bool) {
	if !card.Committed {
		card.Committed, card.CommittedAt, card.Manual = true, at, manual
	}
	card.Manual = card.Manual || manual
}

// Fiend War maintenance already runs periodically. Its final return waits for
// this connection's actual display window, including a late reconnect, rather
// than the last raid member's global completion timestamp.
func currentRaidOwnerSquadDisplayFinished(session *gameSession, state runtimeRaidState, now time.Time) bool {
	if session == nil || state.Battle == nil || state.Battle.Phase > 1 {
		return false
	}
	b := state.Battle
	f := b.RewardFlow[b.Phase]
	if !currentRaidOwnerRewardsCommitted(f, session.selectedCharacterID) || !f.squadRequired() {
		return false
	}
	q := f.Squad[session.selectedCharacterID]
	body, err := raid.BuildRewardListBody(3, []raid.RewardDisplayCard{{CharacterID: session.selectedCharacterID, CardType: 1, Items: currentAntonPartyDisplayItems(q.Plan)}})
	if err != nil {
		return false
	}
	identity := currentRaidProjectionIdentity{InstanceID: b.InstanceID, Phase: b.Phase, CharID: session.selectedCharacterID, Generation: session.characterGeneration}
	o := &session.raidProjection
	o.mu.Lock()
	defer o.mu.Unlock()
	return !o.failed && o.resultSent && o.resultIdentity == identity && o.rewardSquadDisplayIdentity == identity &&
		o.rewardSquadDisplaySignature == sha256.Sum256(body) && !o.rewardSquadDisplayShownAt.IsZero() &&
		!now.Before(o.rewardSquadDisplayShownAt.Add(currentAntonMovieWindow(f)))
}

func deferCurrentRaidRewardOwner(f *currentAntonRewardFlow, character uint16, now time.Time) {
	if f.OwnerNextAttempt == nil {
		f.OwnerNextAttempt = make(map[uint16]time.Time)
	}
	f.OwnerNextAttempt[character] = now.Add(2 * time.Second)
}
