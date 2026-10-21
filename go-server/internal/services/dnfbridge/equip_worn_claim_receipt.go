package dnfbridge

import (
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// A successful authoritative worn-slot noop is evidence of an absorbed claim,
// even when the optional login-window fallback is disabled. Keep its receipt
// scoped to this selected character generation and occurrence epoch. The epoch
// is not a timer (NOOP=false leaves the scene round at zero). This new receipt
// therefore has a fixed, non-sliding deadline using the existing window limit.
// The pre-existing optional login-window repeat ledger is unchanged.
type currentWornClaimReceipt struct {
	acceptedAt time.Time
	expiresAt  time.Time
	epoch      string
	statement  loginWearReassertStatement
}

func (s *Service) prepareCurrentWornClaimReceiptOwner(se *gameSession) {
	if s == nil || se == nil || s.options.currentClientVersion != "official-24.9" {
		return
	}
	if se.equipWornClaimReceiptOwner != se.selectedCharacterID || se.equipWornClaimReceiptGeneration != se.characterGeneration {
		se.equipWornClaimReceiptOwner = se.selectedCharacterID
		se.equipWornClaimReceiptGeneration = se.characterGeneration
		se.equipWornClaimReceipts = nil
		se.equipWornSlotClaimCounts = nil
	}
}

func (s *Service) currentWornClaimReceiptMatches(se *gameSession, slot int16, st loginWearReassertStatement) bool {
	if s == nil || se == nil || s.options.currentClientVersion != "official-24.9" || !loginWearReassertRepeatAbsorbEnabled() || !loginWearReassertWindowRepeatEnabled() {
		return false
	}
	r, ok := se.equipWornClaimReceipts[slot]
	now := s.gameplayNow()
	return ok && !now.Before(r.acceptedAt) && !now.After(r.expiresAt) && now.Sub(r.acceptedAt) <= s.loginWearReassertWindowDuration() && se.equipWornClaimReceiptOwner == se.selectedCharacterID && se.equipWornClaimReceiptGeneration == se.characterGeneration && r.epoch == wornSlotClaimEpochID(se) && r.statement == st
}

func (s *Service) noteCurrentWornClaimResult(se *gameSession, body []byte, fact *alignedcmd.EquipmentMoveFact, player bool) {
	if s == nil || se == nil || s.options.currentClientVersion != "official-24.9" || fact == nil {
		return
	}
	s.prepareCurrentWornClaimReceiptOwner(se)
	// A committed move ends the prior inventory statement. Rejected/failed moves
	// are deliberately not receipts; send failure after a successful noop is safe.
	if fact.Changed {
		se.equipWornClaimReceipts = nil
		return
	}
	if !loginWearReassertRepeatAbsorbEnabled() || !loginWearReassertWindowRepeatEnabled() || player || !fact.WornClaimNoop || se.selectedCharacterID == 0 || se.townSceneReadyCharacterID != se.selectedCharacterID || wornSlotClaimRetryTier() != wornSlotClaimTierRetry {
		return
	}
	slot, ok := wornSlotClaimShape(body)
	if !ok {
		return
	}
	st, ok := loginWearReassertStatementOf(body)
	if !ok {
		return
	}
	if se.equipWornClaimReceipts == nil {
		se.equipWornClaimReceipts = make(map[int16]currentWornClaimReceipt)
	}
	// Keep even an expired receipt as a tombstone until owner/epoch/move changes.
	// Repeated successful noops must neither slide nor reopen this deadline.
	epoch := wornSlotClaimEpochID(se)
	if previous, seen := se.equipWornClaimReceipts[slot]; seen && previous.epoch == epoch {
		return
	}
	now := s.gameplayNow()
	se.equipWornClaimReceipts[slot] = currentWornClaimReceipt{acceptedAt: now, expiresAt: now.Add(s.loginWearReassertWindowDuration()), epoch: epoch, statement: st}
}
