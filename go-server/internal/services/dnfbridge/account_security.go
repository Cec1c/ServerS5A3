package dnfbridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
	"longheng.io/server/internal/modules/dnf/qqbinding"
)

type accountSecurityError struct {
	status        int
	code, message string
	retry         int64
}

func (e *accountSecurityError) Error() string { return e.message }
func securityError(status int, code, message string) error {
	return &accountSecurityError{status: status, code: code, message: message}
}
func (a *accountAPIServer) securityFailure(w http.ResponseWriter, err error) {
	var e *accountSecurityError
	if !errors.As(err, &e) {
		e = &accountSecurityError{status: 503, code: "account_unavailable", message: "账号服务暂不可用，请重试"}
	}
	if e.retry > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(e.retry, 10))
	}
	writeAccountAPIJSON(w, e.status, accountAPIResponse{Code: e.code, Message: e.message, RetryAfter: e.retry})
}
func (a *accountAPIServer) ensureAccountSecuritySchema(ctx context.Context) error {
	for _, ddl := range []string{
		"_password_resets (request_id VARCHAR(96) COLLATE utf8mb4_bin NOT NULL PRIMARY KEY, account_id VARCHAR(64) NOT NULL, fingerprint CHAR(64) NOT NULL, created_at BIGINT NOT NULL, KEY by_account(account_id))",
		"_qq_blacklist (qq VARCHAR(16) NOT NULL PRIMARY KEY, account_id VARCHAR(64) NOT NULL, reason VARCHAR(512) NOT NULL, created_at BIGINT NOT NULL)",
		"_qq_actions (id CHAR(64) NOT NULL PRIMARY KEY, account_id VARCHAR(64) NOT NULL, qq VARCHAR(16) NOT NULL, group_id VARCHAR(16) NOT NULL, state VARCHAR(16) NOT NULL DEFAULT 'pending', attempts INT NOT NULL DEFAULT 0, cause VARCHAR(128) NOT NULL DEFAULT '', lease CHAR(32) NOT NULL DEFAULT '', next_at BIGINT NOT NULL DEFAULT 0, created_at BIGINT NOT NULL, KEY due(state,next_at), KEY by_account(account_id))",
		"_auth_failures (subject CHAR(64) NOT NULL PRIMARY KEY, failures INT NOT NULL DEFAULT 0, blocked_until BIGINT NOT NULL DEFAULT 0, updated_at BIGINT NOT NULL)",
		"_qq_settings (id INT NOT NULL PRIMARY KEY, enabled BOOLEAN NOT NULL DEFAULT FALSE, allow_binding BOOLEAN NOT NULL DEFAULT FALSE, groups_json TEXT NOT NULL, revision BIGINT UNSIGNED NOT NULL DEFAULT 0)",
		"_qq_codes (account_id VARCHAR(64) NOT NULL PRIMARY KEY, code_hash CHAR(64) NOT NULL, expires_at BIGINT NOT NULL, failures INT NOT NULL DEFAULT 0)",
		"_qq_bindings (account_id VARCHAR(64) NOT NULL PRIMARY KEY, qq VARCHAR(16) NOT NULL, group_id VARCHAR(16) NOT NULL, bound_at BIGINT NOT NULL, code_hash CHAR(64) NOT NULL, UNIQUE KEY uniq_qq(qq))",
	} {
		if _, err := a.db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+a.table+ddl+" ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"); err != nil {
			return err
		}
	}
	// Additive migration: existing enabled rules keep requiring binding, while
	// disabled installations remain closed until an administrator opts in.
	var columns int
	if err := a.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME='allow_binding'", a.table+"_qq_settings").Scan(&columns); err != nil {
		return err
	}
	if columns == 0 {
		if _, err := a.db.ExecContext(ctx, "ALTER TABLE "+a.table+"_qq_settings ADD COLUMN allow_binding BOOLEAN NOT NULL DEFAULT FALSE"); err != nil {
			var mysqlError *mysql.MySQLError
			if !errors.As(err, &mysqlError) || mysqlError.Number != 1060 {
				return err
			}
		}
	}
	_, err := a.db.ExecContext(ctx, "INSERT IGNORE INTO "+a.table+"_qq_settings(id,enabled,groups_json,revision) VALUES(1,FALSE,'[]',0)")
	return err
}

// Login, binding-code issuance and password changes share an account-wide,
// persistent failure counter. A row lock makes concurrent guesses count once.
// Successful credentials reset consecutive failures; cooldown attempts do not
// extend the one-minute deadline. Passwords and raw binding codes are not stored.
func (a *accountAPIServer) credentialTransaction(ctx context.Context, account, password string) (*sql.Tx, string, string, error) {
	if !accountAPIUsernamePattern.MatchString(account) || len(password) > 64 {
		return nil, "", "", securityError(400, "invalid_credentials", "请填写有效的账号和密码")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", "", err
	}
	success := false
	defer func() {
		if !success {
			tx.Rollback()
		}
	}()
	subject := sha256.Sum256([]byte(strings.ToLower(account)))
	key := hex.EncodeToString(subject[:])
	now := time.Now().Unix()
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+a.table+"_auth_failures(subject,updated_at) VALUES(?,?) ON DUPLICATE KEY UPDATE subject=VALUES(subject)", key, now); err != nil {
		return nil, "", "", err
	}
	var failures int
	var until, updatedAt int64
	if err = tx.QueryRowContext(ctx, "SELECT failures,blocked_until,updated_at FROM "+a.table+"_auth_failures WHERE subject=? FOR UPDATE", key).Scan(&failures, &until, &updatedAt); err != nil {
		return nil, "", "", err
	}
	// Apply the current duration to older persisted ten-minute cooldowns as well.
	if until > updatedAt+60 {
		until = updatedAt + 60
	}
	if until > now {
		return nil, "", "", &accountSecurityError{429, "account_cooldown", "连续验证失败，请在冷却结束后重试", until - now}
	}
	if until != 0 {
		failures = 0
	}
	var hash, id string
	err = tx.QueryRowContext(ctx, "SELECT password_hash,account_id FROM "+a.table+" WHERE account=? FOR UPDATE", account).Scan(&hash, &id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, "", "", err
	}
	if errors.Is(err, sql.ErrNoRows) || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		failures++
		until = 0
		if failures >= 3 {
			until = now + 60
		}
		if _, err = tx.ExecContext(ctx, "UPDATE "+a.table+"_auth_failures SET failures=?,blocked_until=?,updated_at=? WHERE subject=?", failures, until, now, key); err != nil {
			return nil, "", "", err
		}
		if err = tx.Commit(); err != nil {
			return nil, "", "", err
		}
		if until > now {
			return nil, "", "", &accountSecurityError{429, "account_cooldown", "已连续失败 3 次，请等待 1 分钟", 60}
		}
		return nil, "", "", securityError(401, "invalid_credentials", "账号不存在或密码错误")
	}
	if _, err = tx.ExecContext(ctx, "UPDATE "+a.table+"_auth_failures SET failures=0,blocked_until=0,updated_at=? WHERE subject=?", now, key); err != nil {
		return nil, "", "", err
	}
	success = true
	return tx, id, hash, nil
}

func (a *accountAPIServer) qqSettings(ctx context.Context) (*qqbinding.Settings, error) {
	var out qqbinding.Settings
	var raw string
	err := a.db.QueryRowContext(ctx, "SELECT enabled,allow_binding,groups_json,revision FROM "+a.table+"_qq_settings WHERE id=1").Scan(&out.Enabled, &out.AllowBinding, &raw, &out.Revision)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &out.Groups)
	}
	return &out, err
}
func (a *accountAPIServer) saveQQSettings(ctx context.Context, in qqbinding.Settings) (*qqbinding.Settings, error) {
	if err := in.Normalize(); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(in.Groups)
	result, err := a.db.ExecContext(ctx, "UPDATE "+a.table+"_qq_settings SET enabled=?,allow_binding=?,groups_json=?,revision=revision+1 WHERE id=1 AND revision=?", in.Enabled, in.AllowBinding, string(raw), in.Revision)
	if err != nil {
		return nil, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return nil, errors.New("群配置已变化，请刷新后保存")
	}
	in.Revision++
	return &in, nil
}
func (a *accountAPIServer) requireQQBinding(ctx context.Context, accountID string, clientRequired bool) error {
	cfg, err := a.qqSettings(ctx)
	if err != nil {
		return err
	}
	if !cfg.Enabled && !clientRequired {
		return nil
	}
	var qq string
	err = a.db.QueryRowContext(ctx, "SELECT qq FROM "+a.table+"_qq_bindings WHERE account_id=?", accountID).Scan(&qq)
	if errors.Is(err, sql.ErrNoRows) {
		return securityError(403, "qq_binding_required", "请先在登录器获取绑定码，并向 QQ 机器人发送绑定指令")
	}
	return err
}
func (a *accountAPIServer) handleAccountSecurity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	req, ok := a.decodeRequest(w, r)
	if !ok {
		return
	}
	ip := accountAPIClientIP(r)
	if !a.allowRate(ip) {
		a.securityFailure(w, securityError(429, "rate_limited", "尝试过于频繁，请稍后重试"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), accountAPIWriteTimeout)
	defer cancel()
	a.moderationMu.RLock()
	defer a.moderationMu.RUnlock()
	changing := r.URL.Path == "/api/account/password"
	if changing && (len(req.NewPassword) < max(6, a.minPassword) || len(req.NewPassword) > 64) {
		a.securityFailure(w, securityError(400, "invalid_password", "新密码需要 6 至 64 字节"))
		return
	}
	// Match login issuance lock order before changing credentials/tokens.
	if changing {
		a.service.accountLogins.mu.Lock()
		defer a.service.accountLogins.mu.Unlock()
	}
	tx, id, _, err := a.credentialTransaction(ctx, req.Account, req.Password)
	if err != nil {
		a.securityFailure(w, err)
		return
	}
	defer tx.Rollback()
	if err = a.checkModeration(ctx, id, ip); err != nil {
		a.securityFailure(w, securityError(403, "account_disabled", "该账号或 IP 暂不可操作，请联系管理员"))
		return
	}
	if changing {
		hash, e := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
		if e != nil {
			a.securityFailure(w, e)
			return
		}
		if _, err = tx.ExecContext(ctx, "UPDATE "+a.table+" SET password_hash=? WHERE account_id=?", string(hash), id); err == nil {
			_, err = tx.ExecContext(ctx, "DELETE FROM "+a.loginTokenTable()+" WHERE account_id=?", id)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, "DELETE FROM "+a.table+"_qq_codes WHERE account_id=?", id)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			a.securityFailure(w, err)
			return
		}
		a.revokeAccountCredentialsLocked(id)
		writeAccountAPIJSON(w, 200, accountAPIResponse{OK: true, Message: "密码已修改，请使用新密码登录"})
		return
	}
	cfg, err := a.qqSettings(ctx)
	if err != nil {
		a.securityFailure(w, err)
		return
	}
	if !cfg.BindingAllowed() {
		a.securityFailure(w, securityError(409, "binding_disabled", "当前服务器未启用 QQ 绑定"))
		return
	}
	var qq string
	err = tx.QueryRowContext(ctx, "SELECT qq FROM "+a.table+"_qq_bindings WHERE account_id=?", id).Scan(&qq)
	if err == nil {
		if err = tx.Commit(); err != nil {
			a.securityFailure(w, err)
			return
		}
		writeAccountAPIJSON(w, 200, accountAPIResponse{OK: true, Message: "账号已绑定 QQ，可直接登录", BoundQQ: qq})
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		a.securityFailure(w, err)
		return
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		a.securityFailure(w, err)
		return
	}
	code := hex.EncodeToString(token[:])
	digest := sha256.Sum256([]byte(code))
	expiry := time.Now().Add(10 * time.Minute).Unix()
	_, err = tx.ExecContext(ctx, "INSERT INTO "+a.table+"_qq_codes(account_id,code_hash,expires_at,failures) VALUES(?,?,?,0) ON DUPLICATE KEY UPDATE code_hash=VALUES(code_hash),expires_at=VALUES(expires_at),failures=0", id, hex.EncodeToString(digest[:]), expiry)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		a.securityFailure(w, err)
		return
	}
	writeAccountAPIJSON(w, 200, accountAPIResponse{OK: true, Message: "绑定码 10 分钟内有效", BindingCode: code, ExpiresAt: expiry, Groups: cfg.Groups})
}

func (a *accountAPIServer) completeQQBinding(ctx context.Context, in qqbinding.Complete) (*qqbinding.Binding, error) {
	a.moderationMu.RLock()
	defer a.moderationMu.RUnlock()
	if blocked, err := a.qqBlacklisted(ctx, in.QQ); err != nil {
		return nil, err
	} else if blocked {
		return nil, errors.New("该 QQ 已在黑名单中")
	}
	if !accountAPIUsernamePattern.MatchString(in.Account) || !qqbinding.Number.MatchString(in.QQ) || !qqbinding.Number.MatchString(in.Group) || len(in.Code) != 32 {
		return nil, errors.New("绑定参数无效")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var cfg qqbinding.Settings
	var raw string
	if err = tx.QueryRowContext(ctx, "SELECT enabled,allow_binding,groups_json,revision FROM "+a.table+"_qq_settings WHERE id=1 LOCK IN SHARE MODE").Scan(&cfg.Enabled, &cfg.AllowBinding, &raw, &cfg.Revision); err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(raw), &cfg.Groups); err != nil {
		return nil, err
	}
	allowed := false
	for _, g := range cfg.Groups {
		if g == in.Group {
			allowed = true
		}
	}
	if !cfg.BindingAllowed() || !allowed || cfg.Revision != in.Revision {
		return nil, errors.New("绑定未启用或允许群已变化，请重新发送指令")
	}
	var id, account string
	if err = tx.QueryRowContext(ctx, "SELECT account_id,account FROM "+a.table+" WHERE account=?", in.Account).Scan(&id, &account); errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("绑定码无效或已过期，请重新获取")
	}
	if err != nil {
		return nil, err
	}
	var hash string
	var expires int64
	var failures int
	if err = tx.QueryRowContext(ctx, "SELECT code_hash,expires_at,failures FROM "+a.table+"_qq_codes WHERE account_id=? FOR UPDATE", id).Scan(&hash, &expires, &failures); errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("绑定码无效或已过期，请重新获取")
	}
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(in.Code))
	candidate := hex.EncodeToString(digest[:])
	if expires <= time.Now().Unix() || failures >= 5 {
		return nil, errors.New("绑定码无效或已过期，请重新获取")
	}
	if subtle.ConstantTimeCompare([]byte(hash), []byte(candidate)) != 1 {
		if _, err = tx.ExecContext(ctx, "UPDATE "+a.table+"_qq_codes SET failures=failures+1 WHERE account_id=?", id); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, errors.New("绑定码无效或已过期，请重新获取")
	}
	out := qqbinding.Binding{AccountID: id, Account: account, QQ: in.QQ, Group: in.Group, BoundAt: time.Now().Unix()}
	var previousQQ, previousHash string
	err = tx.QueryRowContext(ctx, "SELECT qq,code_hash,group_id,bound_at FROM "+a.table+"_qq_bindings WHERE account_id=?", id).Scan(&previousQQ, &previousHash, &out.Group, &out.BoundAt)
	if err == nil {
		if previousQQ == in.QQ && previousHash == hash {
			return &out, tx.Commit()
		}
		return nil, errors.New("该账号已绑定 QQ")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+a.table+"_qq_bindings(account_id,qq,group_id,bound_at,code_hash) VALUES(?,?,?,?,?)", id, in.QQ, in.Group, out.BoundAt, hash); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return nil, errors.New("该 QQ 或账号已有绑定，每个 QQ 只能绑定一个账号")
		}
		return nil, err
	}
	return &out, tx.Commit()
}
func (a *accountAPIServer) queryQQBindings(ctx context.Context, q qqbinding.Query) (*qqbinding.Page, error) {
	if len(q.Search) > 64 || q.Offset < 0 || q.Offset > 1000000 || q.Limit < 0 || q.Limit > 100 {
		return nil, errors.New("查询参数无效")
	}
	if q.Limit == 0 {
		q.Limit = 50
	}
	filter := " WHERE (?='' OR a.account=? OR b.account_id=? OR b.qq=?)"
	args := []any{q.Search, q.Search, q.Search, q.Search}
	from := " FROM " + a.table + "_qq_bindings b JOIN " + a.table + " a ON BINARY a.account_id=BINARY b.account_id"
	out := &qqbinding.Page{Rows: []qqbinding.Binding{}}
	if err := a.db.QueryRowContext(ctx, "SELECT COUNT(*)"+from+filter, args...).Scan(&out.Total); err != nil {
		return nil, err
	}
	rows, err := a.db.QueryContext(ctx, "SELECT b.account_id,a.account,b.qq,b.group_id,b.bound_at"+from+filter+" ORDER BY b.bound_at DESC,b.account_id LIMIT ? OFFSET ?", append(args, q.Limit+1, q.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var row qqbinding.Binding
		if err = rows.Scan(&row.AccountID, &row.Account, &row.QQ, &row.Group, &row.BoundAt); err != nil {
			return nil, err
		}
		out.Rows = append(out.Rows, row)
	}
	if len(out.Rows) > q.Limit {
		out.HasMore = true
		out.Rows = out.Rows[:q.Limit]
	}
	return out, rows.Err()
}
