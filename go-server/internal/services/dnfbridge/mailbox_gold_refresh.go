package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
)

// Reload after the transaction and ACK: never reuse an amount from before the
// claim, or rebuild the full item container while the mail detail is open.
func (s *Service) sendCommittedCharacterGold(session *gameSession) error {
	identity, current := s.boundGameSessionCharacterSnapshot(session)
	if !current {
		return nil
	}
	r, ok := s.repositoryGroup()
	if !ok || r.Character == nil {
		return fmt.Errorf("character gold repository unavailable")
	}
	ctx, cancel := context.WithTimeout(session.gameTransportContext(), createWriteTimeout)
	defer cancel()
	character, found, err := r.Character.Load(ctx, strconv.Itoa(int(identity.character)))
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("character gold owner missing")
	}
	body, err := s.prepareCurrentPremiumContractItemUpdate(session, buildCurrentGoldStateBody(character.Stats["gold"]))
	if err != nil {
		return err
	}
	defer s.rearmCurrentRelativeExpirationAfterItems(session, body)
	return s.withGameWire(session, func() error {
		if !s.boundGameSessionCharacterCurrent(identity) {
			return nil
		}
		return s.sendGameUpperRawClassCodecLocked(session, 14, body, 0, true)
	})
}

// postCommittedCharacterGoldRefresh hands the authoritative gold republish to
// the bound session event loop. Callers hold their own asset lock (auctionMu
// for a released bid), so this only snapshots the recipient and submits; a
// blocked or full destination queue must never delay the committed
// transaction. The amount is reloaded inside the event task so a later claim
// or purchase is not repainted with the transaction-time balance.
func (s *Service) postCommittedCharacterGoldRefresh(characterID uint16, source string) error {
	identity, online := s.onlineGameSessionCharacterSnapshot(characterID)
	if !online {
		return nil
	}
	return s.tryPostGameSessionCharacterEvent(identity.session, source, identity.character, identity.generation, func() error {
		if !s.boundGameSessionCharacterCurrent(identity) {
			return nil
		}
		return s.sendCommittedCharacterGold(identity.session)
	})
}
