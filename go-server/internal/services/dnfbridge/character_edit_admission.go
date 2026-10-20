package dnfbridge

import (
	"context"
	"errors"
	"strings"

	"longheng.io/server/internal/modules/dnf/adminapi"
)

// Mark before waiting, and reload the selected record after the database fence.
// Another character on this account is not an occupant of this character.
func (s *Service) admitCharacterForEdits(ctx context.Context, session *gameSession, account string, character uint16) error {
	if !s.options.playerStrict {
		return nil
	}
	fence, ok := s.accountEditFence()
	if !ok || session == nil || character == 0 || account == "" || strings.TrimSpace(account) != account || len(account) > 128 {
		return errors.New("character edit protection unavailable")
	}
	s.mu.Lock()
	if s.characterEditAdmissions == nil {
		s.characterEditAdmissions = make(map[*gameSession]map[uint16]string)
	}
	if s.characterEditAdmissions[session] == nil {
		s.characterEditAdmissions[session] = make(map[uint16]string)
	}
	s.characterEditAdmissions[session][character] = account
	s.mu.Unlock()
	// Keep a failed admission occupied until explicit return or completed
	// connection cleanup; a timed-out request must not grant edit permission.
	return fence.WaitForAccountEdits(ctx, account)
}

func (s *Service) releaseCharacterEditAdmission(session *gameSession) {
	s.mu.Lock()
	delete(s.characterEditAdmissions, session)
	s.mu.Unlock()
}

func (s *Service) administrativeCharacterEditStatus(account string, character uint16) (*adminapi.CharacterEditStatus, error) {
	if _, ok := s.accountEditFence(); !ok || character == 0 || account == "" || len(account) > 128 || strings.TrimSpace(account) != account {
		return nil, errors.New("character edit protection unavailable")
	}
	out := &adminapi.CharacterEditStatus{Protocol: adminapi.CharacterEditProtocol, AccountID: account, CharacterID: character}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, characters := range s.characterEditAdmissions {
		if _, exists := characters[character]; exists {
			out.Busy = true
			break
		}
	}
	// Include bindings from compatibility paths, even if no admission exists.
	if s.gameSessions[character] != nil {
		out.Busy = true
	}
	for _, session := range s.liveGameSessions {
		if session != nil && session.selectedCharacterID == character {
			out.Busy = true
		}
	}
	return out, nil
}
