package dnfbridge

import (
	"errors"
	"fmt"
	"time"
)

// Schedule after activation, on the same character owner. Old-room and
// replacement-session callbacks cannot publish notifications into a new scene.
func (s *Service) deferPartyLoadingSocial(session *gameSession, source string) {
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return
	}
	owner := snapshotPartyDungeonLoadingOwner(session)
	name := fmt.Sprintf("party-loading-social:%p:%d", session, identity.generation)
	err := s.ensureGameplayTimeQueue().ScheduleAfter(name, time.Second, func(time.Time) {
		err := s.tryPostGameSessionCharacterEvent(session, "party-loading-social", identity.character, identity.generation, func() error {
			if !s.boundGameSessionCharacterCurrent(identity) || snapshotPartyDungeonLoadingOwner(session) != owner {
				return nil
			}
			return s.sendLoadingSocial(session, source)
		})
		if err != nil {
			s.logGameEvent(session, "game-party-loading-social-deferred", "error", err)
		}
	})
	if err != nil {
		s.logGameEvent(session, "game-party-loading-social-schedule-failed", "error", err)
	}
}

func (s *Service) sendLoadingSocial(session *gameSession, source string) error {
	if err := s.sendCurrentCharacterFriendList(session, source); err != nil {
		return err
	}
	if err := s.sendCurrentAccountFriendLists(session, source); err != nil {
		return err
	}
	s.refreshCurrentAccountFriendPresence(session, source)
	return errors.Join(s.queueCurrentWeddingBenefitsReplay(session, source), s.queueCurrentWeddingCeremonyRecovery(session))
}
