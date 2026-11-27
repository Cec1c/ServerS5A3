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
		session.stopGameTransport()
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

// forceReleaseRetirement 强制拆掉某个账号的 retiring 栅栏。
//
// 只在 authenticateRemoteLogin 等栅栏超时时调用。此时旧会话的事件循环几乎必然
// 已经卡死（processor 停在 database/sql 的 closingMutex 互等上），finished()
// 永远不会被调用；再等下去就是该账号被永久锁死，只有重启服务端才能恢复。
//
// 安全性：这些会话早已 loginRevoked=true —— callGameSession /
// postGameSessionCharacterEvent / tryPostGameSessionCharacterEvent 在入口处
// 直接返回 errLoginReplaced，transportCtx 也已 cancel、conn 已 close，
// 不可能再产生任何副作用。这里只是把「等它自己收尾」改成「不再等」。
//
// 返回是否真的拆掉了栅栏（便于调用方打点）。调用方不得持有 p.mu。
func (p *accountLoginSessions) forceReleaseRetirement(account string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	retirement := p.retiring[account]
	if retirement == nil {
		return false
	}
	// 会话已经从事件循环里消失了，别再让 p.sessions 挂着它 ——
	// 否则后续 replace() 会把它当成"在线"再处理一遍。
	for session := range retirement.sessions {
		delete(p.sessions, session)
	}
	close(retirement.done)
	delete(p.retiring, account)
	return true
}

// forceReleaseSessionFence 按会话拆栅栏：从 sessions 反查账号再释放。
// 供延期 stop 失败时兜底，避免卡死的会话把 retiring 栅栏永久挂住。
func (p *accountLoginSessions) forceReleaseSessionFence(session *gameSession) bool {
	p.mu.Lock()
	account := p.sessions[session]
	p.mu.Unlock()
	if account == "" {
		return false
	}
	return p.forceReleaseRetirement(account)
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
	writeAccountAPIJSON(w, http.StatusOK, accountAPIResponse{OK: true, AccountID: accountID, Token: token, BoundQQ: request.boundQQ, Message: "登录成功", Diagnostics: a.service.launcherDiagnostics.issue(token, request.DiagnosticCorrelationID, time.Now())})
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
			started := time.Now()
			select {
			case <-done:
				s.logInfo("dnfbridge login retirement completed", "conn_id", session.connID, "wait_ms", time.Since(started).Milliseconds())
				continue // Another login may have superseded this token while waiting.
			case <-ctx.Done():
				s.logWarn("dnfbridge login retirement wait failed", "conn_id", session.connID, "stage", "old_session_cleanup", "wait_ms", time.Since(started).Milliseconds())
				// ★ 兜底解卡：旧会话的 retiring 栅栏只有在 finished() 里才会 close，
				// 而 finished() 依赖事件循环停得下来。processor 一旦卡死在
				// database/sql 的 closingMutex 互等上，栅栏就永不释放，该账号
				// 此后每次登录都要等满 accountAPIWriteTimeout 再失败。
				// 这里强制拆掉栅栏：被拆的会话早已 loginRevoked、transport 已关闭，
				// 不可能再产生任何副作用，只是不再等它自己收尾。
				// 本次登录仍按原逻辑失败一次（不提前放行，避免旧会话的 cleanup
				// 还在落盘时新会话进来），下一次登录即可直接通过。
				if p.forceReleaseRetirement(account) {
					s.logWarn("dnfbridge login retirement fence force released",
						"conn_id", session.connID,
						"account", account,
						"wait_ms", time.Since(started).Milliseconds())
				}
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
