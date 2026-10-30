package dnfbridge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"longheng.io/server/internal/modules/dnf/adminapi"
)

// resetAccountPassword shares the player credential lock order and changes
// only credentials. The durable receipt contains no recoverable password.
func (a *accountAPIServer) resetAccountPassword(ctx context.Context, accountID string, in adminapi.PasswordReset) (*adminapi.PasswordResetResult, error) {
	if in.Validate() != nil || accountID == "" || len(accountID) > 64 {
		return nil, securityError(400, "invalid_reset", "重置参数无效")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	a.moderationMu.RLock()
	defer a.moderationMu.RUnlock()
	a.service.accountLogins.mu.Lock()
	defer a.service.accountLogins.mu.Unlock()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	subject := sha256.Sum256([]byte(strings.ToLower(in.Account)))
	key := hex.EncodeToString(subject[:])
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+a.table+"_auth_failures(subject,updated_at) VALUES(?,?) ON DUPLICATE KEY UPDATE subject=VALUES(subject)", key, now); err != nil {
		return nil, err
	}
	var id, account, currentHash string
	err = tx.QueryRowContext(ctx, "SELECT account_id,account,password_hash FROM "+a.table+" WHERE account_id=? FOR UPDATE", accountID).Scan(&id, &account, &currentHash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (id != accountID || account != in.Account)) {
		return nil, securityError(409, "account_changed", "账号信息已变化，请刷新后重新选择")
	}
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256([]byte(accountID + "\x00" + in.Account + "\x00" + in.Password))
	digest := hex.EncodeToString(fingerprint[:])
	var previousID, previousDigest string
	var resetAt int64
	err = tx.QueryRowContext(ctx, "SELECT account_id,fingerprint,created_at FROM "+a.table+"_password_resets WHERE request_id=? FOR UPDATE", in.RequestID).Scan(&previousID, &previousDigest, &resetAt)
	if err == nil {
		if previousID != id || previousDigest != digest {
			return nil, securityError(409, "reset_conflict", "请求编号已用于其他重置操作，请关闭后重新操作")
		}
		if bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(in.Password)) != nil {
			return nil, securityError(409, "reset_superseded", "该次重置后密码已再次修改，请关闭后重新操作")
		}
		// A replay never disconnects sessions created after the original reset.
		return &adminapi.PasswordResetResult{RequestID: in.RequestID, AccountID: id, Account: account, ResetAt: resetAt}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE "+a.table+" SET password_hash=? WHERE account_id=?", string(hash), id); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM "+a.loginTokenTable()+" WHERE account_id=?", id); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM "+a.table+"_qq_codes WHERE account_id=?", id); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE "+a.table+"_auth_failures SET failures=0,blocked_until=0,updated_at=? WHERE subject=?", now, key); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+a.table+"_password_resets(request_id,account_id,fingerprint,created_at) VALUES(?,?,?,?)", in.RequestID, id, digest, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	a.revokeAccountCredentialsLocked(id)
	return &adminapi.PasswordResetResult{RequestID: in.RequestID, AccountID: id, Account: account, ResetAt: now}, nil
}

// Caller holds accountLogins.mu, after SQL commit.
func (a *accountAPIServer) revokeAccountCredentialsLocked(id string) {
	a.service.loginTokens.forgetAccount(id)
	for _, session := range a.service.accountLogins.replace(id) {
		if session.conn != nil {
			session.conn.Close()
		}
	}
}
