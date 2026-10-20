package dnfbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"longheng.io/server/internal/modules/dnf/localaccount"
)

const localLoginTokenTTL = 15 * time.Minute

type localLoginResponse struct {
	AccountID string    `json:"account_id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// LocalAccountLoginHandler is mounted behind the existing platform admin policy.
// It additionally rejects non-loopback peers and never accepts PID/accountID or
// a historical auth receipt in place of the current password.
func (s *Service) LocalAccountLoginHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		http.Error(w, "local login requires loopback", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		http.Error(w, "invalid login request", http.StatusBadRequest)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "invalid login request", http.StatusBadRequest)
		return
	}
	repos, available := s.repositoryGroup()
	if !available || repos.LocalLogin == nil {
		http.Error(w, "local login unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), accountAPIWriteTimeout)
	defer cancel()
	result, err := s.issueLocalLoginToken(ctx, repos.LocalLogin, request.Username, request.Password, newLoginToken)
	if err != nil {
		status := http.StatusUnauthorized
		if !errors.Is(err, localaccount.ErrCredentials) {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, "local login failed", status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (s *Service) issueLocalLoginToken(ctx context.Context, repo localaccount.Repository, username, password string, generate func() (string, error)) (localLoginResponse, error) {
	s.localLoginMu.Lock()
	epoch, stopped := s.localLoginEpoch, s.localLoginStopped
	s.localLoginMu.Unlock()
	if stopped {
		return localLoginResponse{}, localaccount.ErrUnavailable
	}
	account, err := localaccount.Authenticate(ctx, repo, username, password)
	if err != nil {
		return localLoginResponse{}, err
	}
	token, err := generate()
	if err != nil || normalizeLoginToken(token) == "" {
		return localLoginResponse{}, localaccount.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return localLoginResponse{}, err
	}
	expires := time.Now().Add(localLoginTokenTTL)
	// Only this successful password authentication remembers an issued mapping.
	// No database token row is created; a new Service cannot recognise this token.
	s.localLoginMu.Lock()
	defer s.localLoginMu.Unlock()
	if s.localLoginStopped || s.localLoginEpoch != epoch {
		return localLoginResponse{}, localaccount.ErrUnavailable
	}
	s.loginTokens.remember(token, account, expires)
	if s.localLoginTokens == nil {
		s.localLoginTokens = make(map[string]struct{})
	}
	s.localLoginTokens[token] = struct{}{}
	return localLoginResponse{AccountID: account, Token: token, ExpiresAt: expires}, nil
}

func (s *Service) stopLocalLoginTokens() {
	s.localLoginMu.Lock()
	defer s.localLoginMu.Unlock()
	s.localLoginEpoch++
	s.localLoginStopped = true
	s.loginTokens.mu.Lock()
	defer s.loginTokens.mu.Unlock()
	for token := range s.localLoginTokens {
		delete(s.loginTokens.tokens, token)
		delete(s.loginTokens.misses, token)
	}
	s.localLoginTokens = nil
}

func (s *Service) startLocalLoginTokens() {
	s.localLoginMu.Lock()
	defer s.localLoginMu.Unlock()
	s.localLoginEpoch++
	s.localLoginStopped = false
}
