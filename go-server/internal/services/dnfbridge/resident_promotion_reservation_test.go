package dnfbridge

import (
	"context"
	"testing"
	"time"
)

func TestResidentPromotionKeepsOwnedAssetReservation(t *testing.T) {
	owner := &gameSession{accountID: "owner", selectedCharacterID: 2572, characterGeneration: 7}
	s := &Service{gameSessions: map[uint16]*gameSession{2572: owner}}
	identity := boundGameSessionCharacter{session: owner, character: 2572, generation: 7}
	reservation, err := s.reserveCurrentCharacterAssets(context.Background(), identity, "owner")
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	done := make(chan struct{})
	go func() { s.promoteResidentGameSession(owner, 2572); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		// Let an unfixed implementation finish before failing this regression.
		s.releaseCurrentCharacterAssets(reservation)
		<-done
		t.Fatal("canonical town projection waited for its own asset reservation")
	}
	s.mu.Lock()
	valid := s.currentCharacterAssetReservationValidLocked(reservation)
	s.mu.Unlock()
	if !valid {
		t.Fatal("read-only promotion released or invalidated the paid reservation")
	}
	select {
	case <-reservation.done:
		t.Fatal("read-only promotion unblocked competing binding writers")
	default:
	}
}

func TestResidentReplacementStillWaitsForAssetReservation(t *testing.T) {
	owner := &gameSession{accountID: "owner", selectedCharacterID: 2572, characterGeneration: 7}
	replacement := &gameSession{accountID: "owner", selectedCharacterID: 2572, characterGeneration: 8}
	s := &Service{gameSessions: map[uint16]*gameSession{2572: owner}}
	identity := boundGameSessionCharacter{session: owner, character: 2572, generation: 7}
	reservation, err := s.reserveCurrentCharacterAssets(context.Background(), identity, "owner")
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	started, done := make(chan struct{}), make(chan struct{})
	go func() { close(started); s.promoteResidentGameSession(replacement, 2572); close(done) }()
	<-started
	select {
	case <-done:
		t.Fatal("replacement bypassed the current owner's paid reservation")
	case <-time.After(30 * time.Millisecond):
	}
	s.mu.Lock()
	valid := s.currentCharacterAssetReservationValidLocked(reservation)
	s.mu.Unlock()
	if !valid {
		t.Fatal("waiting replacement changed the canonical owner")
	}
	s.releaseCurrentCharacterAssets(reservation)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("replacement did not resume after the asset installation finished")
	}
	if current, ok := s.onlineGameSessionCharacterSnapshot(2572); !ok || current.session != replacement {
		t.Fatal("replacement did not become canonical after reservation release")
	}
}
