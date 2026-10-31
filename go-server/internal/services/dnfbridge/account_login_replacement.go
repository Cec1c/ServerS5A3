package dnfbridge

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

var errLoginReplaced = errors.New("game login is no longer valid")

// Tracks the entire authenticated lifetime, including logout after the socket
// closes. Replacement and token admission share mu; gameplay never holds it.
type accountLoginSessions struct {
	mu       sync.Mutex
	sessions map[*gameSession]string
	retiring map[string]*accountLoginRetirement
}

type accountLoginRetirement struct {
	sessions map[*gameSession]struct{}
	done     chan struct{}
}

// Caller holds mu and has committed the token transaction.
func (p *accountLoginSessions) replace(account string) []*gameSession {
	var displaced []*gameSession
	for session, id := range p.sessions {
		if id != account {
			continue
		}
		if p.retiring == nil {
			p.retiring = make(map[string]*accountLoginRetirement)
		}
		retirement := p.retiring[account]
		if retirement == nil {
			retirement = &accountLoginRetirement{sessions: make(map[*gameSession]struct{}), done: make(chan struct{})}
			p.retiring[account] = retirement
		}
		retirement.sessions[session] = struct{}{}
		session.loginRevoked.Store(true)
		displaced = append(displaced, session)
	}
	return displaced
}

func (p *accountLoginSessions) tracked(session *gameSession) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[session] != ""
}

// Only actual event-loop termination and logout completion release this fence.
func (p *accountLoginSessions) finished(session *gameSession) {
	p.mu.Lock()
	defer p.mu.Unlock()
	account := p.sessions[session]
	delete(p.sessions, session)
	if retirement := p.retiring[account]; retirement != nil {
		delete(retirement.sessions, session)
		if len(retirement.sessions) == 0 {
			close(retirement.done)
			delete(p.retiring, account)
		}
	}
}

func (a *accountAPIServer) handleLoginReplacement(ctx context.Context, w http.ResponseWriter, request accountAPIRequest, accountID, verifiedHash, ip string) {
	p := &a.service.accountLogins
	p.mu.Lock()
	token, displaced, err := a.replaceLoginLocked(ctx, request.Account, accountID, verifiedHash, ip, request.Device)
	p.mu.Unlock()
	if err != nil {
		writeAccountAPIError(w, http.StatusServiceUnavailable, "登录失败，请重试")
		return
	}
	// Strict admission is held by handleLogin across issuance and reservation.
	// A failed transaction never reserves a slot or retires a live session.
	if a.service.options.playerStrict {
		a.service.playerAdmission.reserve(accountID, request.Device, token, time.Now())
	}
	for _, session := range displaced {
		if session.conn != nil {
			_ = session.conn.Close()
		}
	}
	a.service.logInfo("dnfbridge account login replaced previous launch", "account_id", accountID, "replaced_connections", len(displaced))
	writeAccountAPIJSON(w, http.StatusOK, accountAPIResponse{OK: true, AccountID: accountID, Token: token, BoundQQ: request.boundQQ, Message: "登录成功"})
}

// Caller holds accountLogins.mu. Password verification is performed before the
// lock, then rechecked under a row lock before changing any login state.
func (a *accountAPIServer) replaceLoginLocked(ctx context.Context, account, accountID, verifiedHash, ip, device string) (string, []*gameSession, error) {
	if a.service.options.playerStrict && !playerDevicePattern.MatchString(device) {
		return "", nil, errPlayerAuthentication
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	var currentHash string
	if err := tx.QueryRowContext(ctx, "SELECT password_hash FROM "+a.table+" WHERE BINARY account_id=BINARY ? FOR UPDATE", accountID).Scan(&currentHash); err != nil {
		return "", nil, err
	}
	if currentHash != verifiedHash {
		return "", nil, errLoginReplaced
	}
	token, err := newLoginToken()
	if err != nil {
		return "", nil, err
	}
	now := time.Now()
	if _, err := tx.ExecContext(ctx, "UPDATE "+a.table+" SET last_login_at=?, last_login_ip=? WHERE BINARY account_id=BINARY ?", now, ip, accountID); err != nil {
		return "", nil, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO "+a.loginTokenTable()+" (token, account, account_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?)", token, account, accountID, now, now.Add(loginTokenTTL)); err != nil {
		return "", nil, err
	}
	if a.service.options.playerStrict {
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+a.table+"_player_tokens (token, device) VALUES (?, ?)", token, device); err != nil {
			return "", nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+a.loginTokenTable()+" WHERE BINARY account_id=BINARY ? AND BINARY token<>BINARY ?", accountID, token); err != nil {
		return "", nil, err
	}
	if err := tx.Commit(); err != nil {
		return "", nil, err
	}
	displaced := a.service.accountLogins.replace(accountID)
	if a.service.options.playerStrict {
		// Admission is also held by handleLogin. Include sockets between strict
		// token admission and authoritative account binding: they have no game
		// state to save yet, but must not survive as part of the old launch.
		if lease := a.service.playerAdmission.leases[accountID]; lease != nil {
			seen := make(map[*gameSession]bool, len(displaced))
			for _, session := range displaced {
				seen[session] = true
			}
			for session := range lease.connections {
				session.loginRevoked.Store(true)
				if !seen[session] {
					displaced = append(displaced, session)
				}
			}
		}
	}
	a.service.loginTokens.forgetAccount(accountID)
	a.service.loginTokens.remember(token, accountID, now.Add(loginTokenTTL))
	return token, displaced, nil
}

// A remote token must be revalidated in SQL under the replacement lock; a cached
// mapping cannot revive a revoked launch. Waiting for old cleanup releases mu.
func (s *Service) authenticateRemoteLogin(session *gameSession, token string) (string, error) {
	token = normalizeLoginToken(token)
	if session == nil || token == "" {
		return "", errLoginReplaced
	}
	ctx := session.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, accountAPIWriteTimeout)
	defer cancel()
	api := s.accountAPI
	if api == nil || api.db == nil {
		return "", errLoginReplaced
	}
	api.moderationMu.RLock()
	defer api.moderationMu.RUnlock()
	p := &s.accountLogins
	for {
		p.mu.Lock()
		if session.loginRevoked.Load() {
			p.mu.Unlock()
			return "", errLoginReplaced
		}
		var account string
		api := s.accountAPI
		if api == nil || api.db == nil {
			p.mu.Unlock()
			return "", errLoginReplaced
		}
		err := api.db.QueryRowContext(ctx, "SELECT account_id FROM "+api.loginTokenTable()+" WHERE BINARY token=BINARY ? AND expires_at>?", token, time.Now()).Scan(&account)
		if err != nil || account == "" {
			p.mu.Unlock()
			return "", errLoginReplaced
		}
		if err := api.checkModeration(ctx, account, session.clientIP); err != nil {
			p.mu.Unlock()
			return "", errPlayerRestricted
		}
		if err := api.requireQQBinding(ctx, account, false); err != nil {
			p.mu.Unlock()
			return "", errPlayerRestricted
		}
		if retirement := p.retiring[account]; retirement != nil {
			done := retirement.done
			p.mu.Unlock()
			select {
			case <-done:
				continue // Another login may have superseded this token while waiting.
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		if previous := p.sessions[session]; previous != "" && previous != account {
			p.mu.Unlock()
			return "", errLoginReplaced
		}
		if _, err := api.db.ExecContext(ctx, "UPDATE "+api.loginTokenTable()+" SET last_used_at=?, last_used_ip=?, use_count=use_count+1 WHERE BINARY token=BINARY ?", time.Now(), nullableClientIP(session.clientIP), token); err != nil {
			p.mu.Unlock()
			return "", err
		}
		if p.sessions == nil {
			p.sessions = make(map[*gameSession]string)
		}
		p.sessions[session] = account
		p.mu.Unlock()
		return account, nil
	}
}

// Local loopback password tokens have their own existing in-memory lifetime.
func (s *Service) isLocalLoginToken(token string) bool {
	s.localLoginMu.Lock()
	defer s.localLoginMu.Unlock()
	_, issued := s.localLoginTokens[token]
	_, valid := s.loginTokens.recall(token, time.Now())
	return issued && valid
}
