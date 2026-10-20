package dnfbridge

import (
	"context"
	"strings"
)

// Reservation fixes canonical ownership through a paid asset commit and runtime
// installation without holding service.mu while entering an asset transaction.
// It is per character and never blocks read-only binding validation.
type currentCharacterAssetReservation struct {
	identity boundGameSessionCharacter
	account  string
	done     chan struct{}
}

func (s *Service) reserveCurrentCharacterAssets(ctx context.Context, identity boundGameSessionCharacter, account string) (*currentCharacterAssetReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		s.mu.Lock()
		if identity.session == nil || identity.character == 0 || s.gameSessions[identity.character] != identity.session || identity.session.selectedCharacterID != identity.character || identity.session.characterGeneration != identity.generation || strings.TrimSpace(identity.session.accountID) != account {
			s.mu.Unlock()
			return nil, errDungeonWorldMapUnavailable
		}
		prior := s.characterAssetReservations[identity.character]
		if prior != nil {
			done := prior.done
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if s.characterAssetReservations == nil {
			s.characterAssetReservations = map[uint16]*currentCharacterAssetReservation{}
		}
		r := &currentCharacterAssetReservation{identity: identity, account: account, done: make(chan struct{})}
		s.characterAssetReservations[identity.character] = r
		s.mu.Unlock()
		return r, nil
	}
}
func (s *Service) currentCharacterAssetReservationValidLocked(r *currentCharacterAssetReservation) bool {
	if r == nil || r.identity.session == nil {
		return false
	}
	i := r.identity
	return s.characterAssetReservations[i.character] == r && s.gameSessions[i.character] == i.session && i.session.selectedCharacterID == i.character && i.session.characterGeneration == i.generation && strings.TrimSpace(i.session.accountID) == r.account
}
func (s *Service) releaseCurrentCharacterAssets(r *currentCharacterAssetReservation) {
	if r == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishCurrentCharacterAssetReservationLocked(r)
}

// Called at successful paid runtime installation, before any socket projection.
func (s *Service) finishCurrentCharacterAssetReservationLocked(r *currentCharacterAssetReservation) {
	if r != nil && s.characterAssetReservations[r.identity.character] == r {
		delete(s.characterAssetReservations, r.identity.character)
		close(r.done)
	}
}

// Returns with service.mu held, just like the canonical mutation's previous
// Lock. Waiters release service.mu; they cannot obstruct transaction Validate.
// Call only before a binding write, never from a repository/UOW callback.
func (s *Service) lockCurrentCharacterBindingMutation(session *gameSession, target uint16) {
	for {
		s.mu.Lock()
		var pending *currentCharacterAssetReservation
		for id, r := range s.characterAssetReservations {
			if id == target || session != nil && (id == session.selectedCharacterID || r.identity.session == session) {
				pending = r
				break
			}
		}
		if pending == nil {
			return
		}
		done := pending.done
		s.mu.Unlock()
		<-done
	}
}
