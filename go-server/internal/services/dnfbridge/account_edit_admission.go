package dnfbridge

import (
	"context"
	"errors"
	"strings"

	"longheng.io/server/internal/modules/dnf/adminapi"
	"longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) accountEditFence() (repository.AccountEditFence, bool) {
	if s == nil || !s.options.playerStrict {
		return nil, false
	}
	g, ok := s.repositoryGroup()
	if !ok {
		return nil, false
	}
	fence, ok := g.Account.(repository.AccountEditFence)
	return fence, ok
}

// The marker precedes the database wait. Once a GM transaction owns the row,
// it either sees this marker and rolls back, or commits before login can load
// player state. The marker survives unbind/handover and shutdown timeouts.
func (s *Service) admitAccountForEdits(session *gameSession, account string) error {
	if s == nil || !s.options.playerStrict {
		return nil
	}
	fence, ok := s.accountEditFence()
	if !ok {
		return errors.New("account edit protection unavailable")
	}
	if session == nil || account == "" || len(account) > 128 || strings.TrimSpace(account) != account {
		return errors.New("invalid account admission")
	}
	s.mu.Lock()
	if s.accountEditAdmissions == nil {
		s.accountEditAdmissions = make(map[*gameSession]map[string]bool)
	}
	accounts := s.accountEditAdmissions[session]
	if accounts == nil {
		accounts = make(map[string]bool)
		s.accountEditAdmissions[session] = accounts
	}
	accounts[account] = true
	s.mu.Unlock()
	ctx := session.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, createWriteTimeout)
	defer cancel()
	return fence.WaitForAccountEdits(ctx, account)
}

// Call only after event-loop Stop completed and the cleanup callback finished.
func (s *Service) releaseAccountEditAdmission(session *gameSession) {
	s.mu.Lock()
	delete(s.accountEditAdmissions, session)
	delete(s.characterEditAdmissions, session)
	s.mu.Unlock()
}

func (s *Service) administrativeAccountEditStatus(account string) (*adminapi.AccountEditStatus, error) {
	if _, ok := s.accountEditFence(); !ok {
		return nil, errors.New("account edit protection unavailable")
	}
	if account == "" || len(account) > 128 || strings.TrimSpace(account) != account {
		return nil, errors.New("invalid account identity")
	}
	out := &adminapi.AccountEditStatus{Protocol: adminapi.AccountEditProtocol, AccountID: account}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, accounts := range s.accountEditAdmissions {
		if accounts[account] {
			out.Busy = true
			break
		}
	}
	return out, nil
}
