package dnfbridge

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

var errPlayerAuthentication = errors.New("player authentication required")
var playerDevicePattern = regexp.MustCompile(`^[a-f0-9]{32,64}$`)

// One lease represents a launched client, not a socket: the native client uses
// auxiliary connections and overlapping connections during channel changes.
type playerLease struct {
	account, device, token string
	until                  time.Time
	connections            map[*gameSession]struct{}
}

type playerAdmission struct {
	mu     sync.Mutex
	leases map[string]*playerLease
}

func (p *playerAdmission) prune(now time.Time) {
	if p.leases == nil {
		p.leases = make(map[string]*playerLease)
	}
	for account, lease := range p.leases {
		if len(lease.connections) == 0 && !now.Before(lease.until) {
			delete(p.leases, account)
		}
	}
}

// Caller holds mu across token issuance and reservation.
func (p *playerAdmission) available(account, device string, limit int, now time.Time) error {
	p.prune(now)
	if p.leases[account] != nil {
		return errors.New("该账号已在线或正在连接，请退出后稍等再试")
	}
	count := 0
	for _, lease := range p.leases {
		if lease.device == device {
			count++
		}
	}
	if count >= 2 {
		return errors.New("每台设备最多同时登录两个账号")
	}
	if limit > 0 && len(p.leases) >= limit {
		return errors.New("服务器人数已满，请稍后再试")
	}
	return nil
}

func (p *playerAdmission) reserve(account, device, token string, now time.Time) {
	p.leases[account] = &playerLease{account: account, device: device, token: token,
		until: now.Add(90 * time.Second), connections: make(map[*gameSession]struct{})}
}

func (p *playerAdmission) attach(account, device, token string, session *gameSession, limit int, now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.attachLocked(account, device, token, session, limit, now)
}

func (p *playerAdmission) attachLocked(account, device, token string, session *gameSession, limit int, now time.Time) error {
	p.prune(now)
	lease := p.leases[account]
	if lease == nil {
		if err := p.available(account, device, limit, now); err != nil {
			return err
		}
		p.reserve(account, device, token, now)
		lease = p.leases[account]
	}
	if lease.token != token || lease.device != device {
		return errPlayerAuthentication
	}
	lease.connections[session] = struct{}{}
	return nil
}

func (p *playerAdmission) detach(session *gameSession, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, lease := range p.leases {
		if _, found := lease.connections[session]; found {
			delete(lease.connections, session)
			if len(lease.connections) == 0 {
				lease.until = now.Add(30 * time.Second)
			}
		}
	}
}

func (a *accountAPIServer) clientIP(r *http.Request) string {
	secret := a.service.options.playerProxySecret
	if secret != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Player-Proxy-Key")), []byte(secret)) == 1 {
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Player-IP"))); ip != nil {
			return ip.String()
		}
	}
	// Never trust arbitrary X-Forwarded-For supplied by a player.
	if a.service.options.playerStrict {
		return ""
	}
	return accountAPIClientIP(r)
}

func (a *accountAPIServer) ensurePlayerSchema(ctx context.Context) error {
	for _, statement := range []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s_registration_ips (ip VARCHAR(45) PRIMARY KEY, used INT NOT NULL DEFAULT 0) ENGINE=InnoDB`, a.table),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s_registration_sources (account_id VARCHAR(64) PRIMARY KEY, ip VARCHAR(45) NOT NULL, KEY(ip)) ENGINE=InnoDB`, a.table),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s_player_tokens (token VARCHAR(64) PRIMARY KEY, device VARCHAR(64) NOT NULL) ENGINE=InnoDB`, a.table),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s_player_accounts (account_id VARCHAR(64) PRIMARY KEY, disabled BOOLEAN NOT NULL DEFAULT FALSE) ENGINE=InnoDB`, a.table),
	} {
		if _, err := a.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (a *accountAPIServer) takeRegistrationSlot(ctx context.Context, tx *sql.Tx, ip string) error {
	limit := a.service.options.playerRegistrationLimit
	if limit <= 0 {
		return nil
	}
	if ip == "" {
		return errors.New("请从新版登录器注册账号")
	}
	table := a.table + "_registration_ips"
	if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+" (ip, used) VALUES (?, 0) ON DUPLICATE KEY UPDATE ip=VALUES(ip)", ip); err != nil {
		return err
	}
	var used int
	if err := tx.QueryRowContext(ctx, "SELECT used FROM "+table+" WHERE ip=? FOR UPDATE", ip).Scan(&used); err != nil {
		return err
	}
	if used >= limit {
		return errors.New("该 IP 已达到注册上限（两个账号）")
	}
	_, err := tx.ExecContext(ctx, "UPDATE "+table+" SET used=used+1 WHERE ip=?", ip)
	return err
}

func (s *Service) authenticatePlayer(session *gameSession, token string) error {
	if token == "" || s.accountAPI == nil {
		return errPlayerAuthentication
	}
	a := s.accountAPI
	s.playerAdmission.mu.Lock()
	defer s.playerAdmission.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), accountAPIWriteTimeout)
	defer cancel()
	var account, device string
	query := "SELECT t.account_id, p.device FROM " + a.loginTokenTable() + " t JOIN " + a.table + "_player_tokens p ON BINARY p.token=BINARY t.token LEFT JOIN " + a.table + "_player_accounts a ON BINARY a.account_id=BINARY t.account_id WHERE t.token=? AND t.expires_at > ? AND COALESCE(a.disabled,0)=0"
	if err := a.db.QueryRowContext(ctx, query, token, time.Now()).Scan(&account, &device); err != nil {
		return errPlayerAuthentication
	}
	if err := s.playerAdmission.attachLocked(account, device, token, session, s.options.playerMaxOnline, time.Now()); err != nil {
		return err
	}
	// Bind through the existing authoritative token path; strict mode always
	// revalidates SQL before adding this short-lived cache entry.
	s.loginTokens.remember(token, account, time.Now().Add(time.Minute))
	return nil
}
