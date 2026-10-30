package dnfbridge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/moderation"
)

var errPlayerRestricted = errors.New("account or IP restricted")
var errBanConflict = errors.New("ban revision conflict")

func (a *accountAPIServer) ensureModerationSchema(ctx context.Context) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS ` + a.table + `_registration_sources (account_id VARCHAR(64) PRIMARY KEY, ip VARCHAR(45) NOT NULL, KEY(ip)) ENGINE=InnoDB`,
		`CREATE TABLE IF NOT EXISTS ` + a.table + `_player_accounts (account_id VARCHAR(64) PRIMARY KEY, disabled BOOLEAN NOT NULL DEFAULT FALSE) ENGINE=InnoDB`,
		`CREATE TABLE IF NOT EXISTS ` + a.table + `_bans (kind VARCHAR(16) NOT NULL, target VARCHAR(128) COLLATE utf8mb4_bin NOT NULL, label VARCHAR(128) NOT NULL, reason VARCHAR(512) NOT NULL, expires_at DATETIME(6) NULL, revoked BOOLEAN NOT NULL, revision BIGINT UNSIGNED NOT NULL, updated_at DATETIME(6) NOT NULL, PRIMARY KEY(kind,target), KEY ban_updated(updated_at)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS ` + a.table + `_ban_receipts (request_id VARCHAR(80) COLLATE utf8mb4_bin PRIMARY KEY, payload_hash CHAR(64) NOT NULL, result_json TEXT NOT NULL, created_at DATETIME(6) NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	} {
		if _, err := a.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// Callers hold moderationMu through admission/registration. Ban changes take
// its write lock, followed by accountLogins.mu.
func (a *accountAPIServer) checkModeration(ctx context.Context, account, ip string) error {
	if canonical, err := moderation.CanonicalIP(ip); err == nil {
		ip = canonical
	}
	var found int
	err := a.db.QueryRowContext(ctx, "SELECT 1 FROM "+a.table+"_bans WHERE ((kind='account' AND target=?) OR (kind='ip' AND target=?)) AND revoked=0 AND (expires_at IS NULL OR expires_at>?) LIMIT 1", account, ip, time.Now().UTC()).Scan(&found)
	if err == nil {
		return errPlayerRestricted
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func (a *accountAPIServer) moderationAccounts(ctx context.Context, q moderation.Query) (*moderation.AccountPage, error) {
	if err := q.Normalize(time.Now()); err != nil {
		return nil, err
	}
	if err := q.NormalizeSort("accounts"); err != nil {
		return nil, err
	}
	order := map[string]string{"id": "a.account_id", "name": "a.account", "lastLoginIp": "COALESCE(a.last_login_ip,'')", "registrationIp": "COALESCE(r.ip,'')", "disabled": "COALESCE(p.disabled,0)"}[q.SortBy] + " " + q.SortOrder + ",a.id"
	out := &moderation.AccountPage{Rows: []moderation.Account{}}
	rows, err := a.db.QueryContext(ctx, "SELECT a.account_id,a.account,COALESCE(a.last_login_ip,''),COALESCE(r.ip,''),COALESCE(p.disabled,0) FROM "+a.table+" a LEFT JOIN "+a.table+"_registration_sources r ON BINARY r.account_id=BINARY a.account_id LEFT JOIN "+a.table+"_player_accounts p ON BINARY p.account_id=BINARY a.account_id WHERE (?='' OR LOCATE(?,a.account)>0 OR a.account_id=? OR a.last_login_ip=? OR r.ip=?) ORDER BY "+order+" LIMIT ? OFFSET ?", q.Search, q.Search, q.Search, q.Search, q.Search, q.Limit+1, q.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c moderation.Account
		if err := rows.Scan(&c.ID, &c.Name, &c.LastLoginIP, &c.RegistrationIP, &c.Disabled); err != nil {
			return nil, err
		}
		out.Rows = append(out.Rows, c)
	}
	if len(out.Rows) > q.Limit {
		out.HasMore = true
		out.Rows = out.Rows[:q.Limit]
	}
	return out, rows.Err()
}

type banScanner interface{ Scan(...any) error }

func readBan(row banScanner) (moderation.Ban, error) {
	var b moderation.Ban
	var expires sql.NullTime
	err := row.Scan(&b.Kind, &b.Target, &b.Label, &b.Reason, &expires, &b.Revoked, &b.Revision, &b.UpdatedAt)
	if expires.Valid {
		t := expires.Time.UTC()
		b.ExpiresAt = &t
	}
	b.Active = !b.Revoked && (!expires.Valid || expires.Time.After(time.Now()))
	return b, err
}

const banColumns = "kind,target,label,reason,expires_at,revoked,revision,updated_at"

func (a *accountAPIServer) moderationBans(ctx context.Context, q moderation.Query) (*moderation.BanPage, error) {
	if err := q.Normalize(time.Now()); err != nil {
		return nil, err
	}
	if err := q.NormalizeSort("bans"); err != nil {
		return nil, err
	}
	column := map[string]string{"kind": "kind", "target": "target", "label": "label", "state": "CASE WHEN revoked=1 THEN 2 WHEN expires_at IS NOT NULL AND expires_at<=UTC_TIMESTAMP(6) THEN 1 ELSE 0 END", "reason": "reason", "expiresAt": "expires_at", "updatedAt": "updated_at"}[q.SortBy]
	order := "(" + column + " IS NULL) ASC," + column + " " + q.SortOrder + ",kind,target"
	out := &moderation.BanPage{Rows: []moderation.Ban{}}
	where := "(?='' OR LOCATE(?,target)>0 OR LOCATE(?,label)>0)"
	args := []any{q.Search, q.Search, q.Search}
	if ip, err := moderation.CanonicalIP(q.Search); err == nil {
		where = "kind='ip' AND target=?"
		args = []any{ip}
	}
	args = append(args, q.Limit+1, q.Offset)
	rows, err := a.db.QueryContext(ctx, "SELECT "+banColumns+" FROM "+a.table+"_bans WHERE "+where+" ORDER BY "+order+" LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		b, err := readBan(rows)
		if err != nil {
			return nil, err
		}
		out.Rows = append(out.Rows, b)
	}
	if len(out.Rows) > q.Limit {
		out.HasMore = true
		out.Rows = out.Rows[:q.Limit]
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out.Rows {
		if out.Rows[i].Kind == "account" {
			out.Rows[i].QQActions, err = a.qqActionsForAccount(ctx, out.Rows[i].Target)
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func (a *accountAPIServer) changeBan(ctx context.Context, c moderation.BanChange) (*moderation.Ban, error) {
	// Validate structural fields first, permitting expired retries to recover a
	// previous success. Expiry is checked for new commands inside the transaction.
	expires := c.ExpiresAt
	c.ExpiresAt = nil
	if err := c.Normalize(time.Now()); err != nil {
		return nil, err
	}
	if !c.Revoke {
		c.ExpiresAt = expires
	}
	raw, _ := json.Marshal(c)
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	a.moderationMu.Lock()
	defer a.moderationMu.Unlock()
	logins := &a.service.accountLogins
	logins.mu.Lock()
	result, _, err := a.commitBan(ctx, c, hash)
	var connections []net.Conn
	activeCurrent := false
	if err == nil && result.Active {
		latest, readErr := readBan(a.db.QueryRowContext(ctx, "SELECT "+banColumns+" FROM "+a.table+"_bans WHERE kind=? AND target=?", c.Kind, c.Target))
		if readErr != nil {
			err = readErr
		} else {
			activeCurrent = latest.Active && latest.Revision == result.Revision
		}
	}
	if err == nil && activeCurrent {
		targets := map[string]bool{}
		ips := map[string]bool{}
		if c.Kind == "ip" {
			ips[c.Target] = true
		}
		for _, ip := range result.LinkedIPs {
			latest, e := readBan(a.db.QueryRowContext(ctx, "SELECT "+banColumns+" FROM "+a.table+"_bans WHERE kind='ip' AND target=?", ip))
			if e == nil && latest.Active {
				ips[ip] = true
			}
		}
		if c.Kind == "account" {
			targets[c.Target] = true
		}
		for session, account := range logins.sessions {
			ip, _ := moderation.CanonicalIP(session.clientIP)
			if targets[account] || ips[ip] {
				targets[account] = true
			}
		}
		for account := range targets {
			for _, session := range logins.replace(account) {
				if session.conn != nil {
					connections = append(connections, session.conn)
				}
			}
			a.service.loginTokens.forgetAccount(account)
		}
	}
	logins.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
	return result, err
}
func (a *accountAPIServer) commitBan(ctx context.Context, c moderation.BanChange, hash string) (*moderation.Ban, bool, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var priorHash, priorJSON string
	err = tx.QueryRowContext(ctx, "SELECT payload_hash,result_json FROM "+a.table+"_ban_receipts WHERE request_id=?", c.RequestID).Scan(&priorHash, &priorJSON)
	if err == nil {
		if priorHash != hash {
			return nil, false, errBanConflict
		}
		var prior moderation.Ban
		if err := json.Unmarshal([]byte(priorJSON), &prior); err != nil {
			return nil, false, err
		}
		return &prior, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	now := time.Now().UTC()
	if err := c.Normalize(now); err != nil {
		return nil, false, err
	}
	label := c.Target
	if c.Kind == "account" {
		if err := tx.QueryRowContext(ctx, "SELECT account FROM "+a.table+" WHERE BINARY account_id=BINARY ? FOR UPDATE", c.Target).Scan(&label); err != nil {
			return nil, false, errors.New("account not found")
		}
	}
	current, err := readBan(tx.QueryRowContext(ctx, "SELECT "+banColumns+" FROM "+a.table+"_bans WHERE kind=? AND target=? FOR UPDATE", c.Kind, c.Target))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	if current.Revision != c.Revision {
		return nil, false, errBanConflict
	}
	b := moderation.Ban{Kind: c.Kind, Target: c.Target, Label: label, Reason: c.Reason, ExpiresAt: c.ExpiresAt, Revoked: c.Revoke, Active: !c.Revoke, Revision: current.Revision + 1, UpdatedAt: now}
	_, err = tx.ExecContext(ctx, "INSERT INTO "+a.table+"_bans ("+banColumns+") VALUES (?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE label=VALUES(label),reason=VALUES(reason),expires_at=VALUES(expires_at),revoked=VALUES(revoked),revision=VALUES(revision),updated_at=VALUES(updated_at)", b.Kind, b.Target, b.Label, b.Reason, b.ExpiresAt, b.Revoked, b.Revision, b.UpdatedAt)
	if err != nil {
		return nil, false, err
	}
	if !c.Revoke {
		if c.Kind == "account" {
			_, err = tx.ExecContext(ctx, "DELETE FROM "+a.loginTokenTable()+" WHERE BINARY account_id=BINARY ?", c.Target)
		} else {
			_, err = tx.ExecContext(ctx, "DELETE t FROM "+a.loginTokenTable()+" t JOIN "+a.table+" a ON BINARY a.account_id=BINARY t.account_id WHERE a.last_login_ip=? OR t.last_used_ip=?", c.Target, c.Target)
		}
		if err != nil {
			return nil, false, err
		}
	}
	if c.Linked != nil {
		if err = a.commitLinkedBans(ctx, tx, c, &b); err != nil {
			return nil, false, err
		}
	}
	encoded, _ := json.Marshal(b)
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+a.table+"_ban_receipts (request_id,payload_hash,result_json,created_at) VALUES (?,?,?,?)", c.RequestID, hash, string(encoded), now); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	return &b, false, nil
}

func moderationError(err error) string {
	if errors.Is(err, errBanConflict) {
		return "封禁状态已变化，请刷新后重新确认"
	}
	if err != nil && (strings.HasPrefix(err.Error(), "invalid") || err.Error() == "account not found") {
		return fmt.Sprint("参数无效或账号不存在")
	}
	return "反作弊服务暂不可用"
}
