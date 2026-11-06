package dnfbridge

import (
	"context"
	"errors"

	"longheng.io/server/internal/modules/dnf/wedding"
)

// Scene/creature owners may hold town, dungeon or asset locks. Re-read only
// after that event finishes; never take wedding/asset locks inside those owners.
func (s *Service) queueCurrentWeddingBenefitsReplay(session *gameSession, source string) error {
	if s.options.currentClientVersion != "official-24.9" {
		return nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || session.events == nil || session.events.loop == nil {
		return nil
	}
	err := s.tryPostGameSessionCharacterEvent(session, "game-wedding-benefits-replay", identity.character, identity.generation, func() error {
		return s.replayCurrentWeddingBenefits(identity, source)
	})
	if err != nil {
		s.logInfo("婚姻属性状态回放待恢复", "event", "game-wedding-benefits-replay-deferred", "char_id", identity.character, "source", source, "error", err)
	}
	// The originating scene/item mutation has already committed.
	return nil
}

func (s *Service) replayCurrentWeddingBenefits(identity boundGameSessionCharacter, source string) error {
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	if !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return err
	}
	defer release()
	fresh, err := s.currentWeddingCharacter(ctx, identity)
	if err != nil {
		return err
	}
	state, err := wedding.Read(fresh)
	if err != nil {
		return err
	}
	if state.Phase != wedding.Married {
		// A clear projection is not a minimum-tier marriage: Info uses -1
		// for the ring and the room has no installed furniture. Never copy
		// the 86 server's unrelated custom attributes to an unmarried actor.
		return errors.Join(s.sendCurrentWeddingInfoAndPartnerLocked(identity.session, fresh), s.writeCurrentWeddingRoom(identity.session, wedding.Room{}))
	}
	if err = s.sendCurrentWeddingInfoAndPartnerLocked(identity.session, fresh); err != nil {
		return err
	}
	return s.sendCurrentWeddingRoomSnapshot(identity.session, fresh)
}
