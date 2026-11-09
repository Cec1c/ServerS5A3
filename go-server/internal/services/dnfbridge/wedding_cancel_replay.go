package dnfbridge

import (
	"context"
	"errors"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

type currentWeddingCancellationOutcome struct {
	done      chan struct{}
	committed bool
	resolved  bool
}

func (o *currentWeddingCancellationOutcome) resolve(committed bool) {
	if !o.resolved {
		o.committed, o.resolved = committed, true
		close(o.done)
	}
}

// Reserve every other live transport before changing the database. Its event
// waits without any wedding, dungeon, asset or database lock. It therefore
// follows an old quest ACK/mark and precedes the next request on that socket.
// A full queue rejects cancellation before the fee or either relationship moves.
func (s *Service) reserveCurrentWeddingCancellationDeliveries(source *gameSession, outcome *currentWeddingCancellationOutcome, characters ...r.CharacterRecord) ([]currentWeddingClearTransport, error) {
	var immediate []currentWeddingClearTransport
	for _, character := range characters {
		numericID, err := wedding.NumericID(character.CharacterID)
		if err != nil {
			return nil, err
		}
		id := uint16(numericID)
		s.mu.Lock()
		candidates := map[*gameSession]bool{}
		if current := s.gameSessions[id]; current != nil {
			candidates[current] = true
		}
		for _, session := range s.liveGameSessions {
			if session != nil && session.selectedCharacterID == id {
				candidates[session] = true
			}
		}
		s.mu.Unlock()
		for session := range candidates {
			pin, valid := s.currentWeddingClearTransport(session, character.AccountID)
			if !valid || pin.transport.character != id {
				continue
			}
			// Production transports always have an event loop. Direct-call
			// fixtures have none, and must not wait inline for their own commit.
			if session == source || session.events == nil || session.events.loop == nil {
				immediate = append(immediate, pin)
				continue
			}
			if err := s.tryPostGameSessionCharacterEvent(session, "game-wedding-cancellation-committed", pin.transport.character, pin.transport.generation, func() error {
				<-outcome.done
				if !outcome.committed {
					return nil
				}
				return s.deliverCurrentWeddingCancellation(pin)
			}); err != nil {
				return nil, err
			}
		}
	}
	return immediate, nil
}

func (s *Service) clearCurrentWeddingPreparationReplayForTransport(pin currentWeddingClearTransport) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	transport, canonical := pin.transport, pin.canonical
	if transport.session == nil || canonical.session == nil || transport.character == 0 || canonical.character != transport.character ||
		s.gameSessions[transport.character] != canonical.session ||
		canonical.session.selectedCharacterID != canonical.character || canonical.session.characterGeneration != canonical.generation ||
		transport.session.selectedCharacterID != transport.character || transport.session.characterGeneration != transport.generation ||
		transport.session.loginRevoked.Load() || canonical.session.loginRevoked.Load() ||
		s.accountIDForSession(transport.session) != pin.account || s.accountIDForSession(canonical.session) != pin.account ||
		(transport.session != canonical.session && s.liveGameSessions[transport.session.connID] != transport.session) {
		return false
	}
	// Identity stays pinned while the small replay maps are changed. The replay
	// helpers never acquire Service.mu or another replay-family mutex.
	for _, questID := range []uint16{20737, 20738} {
		transport.session.clearCurrentAcceptQuestReplay(currentAcceptQuestReplayKey{characterID: transport.character, questID: questID})
		transport.session.clearCurrentGiveUpQuestReplay(currentGiveUpQuestReplayKey{characterID: transport.character, questID: questID})
		transport.session.clearCurrentSetQuestTriggerReplayForQuest(transport.character, questID)
		transport.session.clearCurrentFinishQuestReplayForQuest(transport.character, questID)
	}
	return true
}

// wedding.mu is held, but no dungeon lock is acquired here. A later engagement
// may already have committed on another owner: send its fresh state and never
// apply the native Single reset to that newer relationship.
func (s *Service) deliverCurrentWeddingCancellationLocked(pin currentWeddingClearTransport) error {
	if !s.clearCurrentWeddingPreparationReplayForTransport(pin) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	fresh, err := s.currentWeddingCharacter(ctx, pin.canonical)
	if err != nil {
		return err
	}
	state, err := wedding.Read(fresh)
	if err != nil {
		return err
	}
	var published error
	if state.Phase == wedding.Single {
		published = s.sendGameUpperRawClass(pin.transport.session, 1002, []byte{5}, 0)
	}
	return errors.Join(published, s.sendCurrentWeddingInfoAndPartnerLocked(pin.transport.session, fresh))
}

func (s *Service) deliverCurrentWeddingCancellation(pin currentWeddingClearTransport) error {
	err := func() error {
		s.wedding.mu.Lock()
		defer s.wedding.mu.Unlock()
		return s.deliverCurrentWeddingCancellationLocked(pin)
	}()
	return errors.Join(err, s.deliverCurrentWeddingCancellationProjections(pin))
}

func (s *Service) deliverCurrentWeddingCancellationProjections(pin currentWeddingClearTransport) error {
	if !s.currentWeddingClearTransportCurrent(pin) {
		return nil
	}
	var published error
	if pin.transport == pin.canonical {
		receivers := s.currentWeddingPeerReceivers(pin.canonical)
		published = s.publishCurrentWeddingCancellationPeerState(pin.canonical, receivers)
	}
	return errors.Join(published, s.sendCurrentActiveQuestSnapshotForSession(pin.transport.session, "wedding_cancel_committed"), s.refreshCurrentWeddingQuests(pin.canonical))
}
