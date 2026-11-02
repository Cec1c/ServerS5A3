package dnfbridge

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/qqbinding"
	"strings"
	"time"
)

func (a *accountAPIServer) ensureQQBindingLifecycleSchema(ctx context.Context) error {
	for _, column := range []string{"state VARCHAR(16) NOT NULL DEFAULT 'active'", "revision BIGINT UNSIGNED NOT NULL DEFAULT 1", "updated_at BIGINT NOT NULL DEFAULT 0", "checked_at BIGINT NOT NULL DEFAULT 0", "check_cause VARCHAR(32) NOT NULL DEFAULT ''", "next_check_at BIGINT NOT NULL DEFAULT 0", "check_lease CHAR(32) NOT NULL DEFAULT ''", "check_until BIGINT NOT NULL DEFAULT 0"} {
		name := strings.Fields(column)[0]
		var count int
		if err := a.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME=?", a.table+"_qq_bindings", name).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			if _, err := a.db.ExecContext(ctx, "ALTER TABLE "+a.table+"_qq_bindings ADD COLUMN "+column); err != nil {
				var e *mysql.MySQLError
				if !errors.As(err, &e) || e.Number != 1060 {
					return err
				}
			}
		}
	}
	_, err := a.db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+a.table+"_qq_changes (request_id VARCHAR(80) COLLATE utf8mb4_bin PRIMARY KEY, fingerprint CHAR(64) NOT NULL, result_json TEXT NOT NULL, created_at BIGINT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4")
	return err
}

const qqBindingColumns = "account_id,qq,group_id,bound_at,state,revision,updated_at,checked_at,check_cause"

func readQQBinding(row banScanner) (*qqbinding.Binding, error) {
	b := new(qqbinding.Binding)
	err := row.Scan(&b.AccountID, &b.QQ, &b.Group, &b.BoundAt, &b.State, &b.Revision, &b.UpdatedAt, &b.CheckedAt, &b.CheckCause)
	return b, err
}
func bindingNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func bindingRevision() (uint64, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	var n uint64
	for _, v := range b {
		n = n<<8 | uint64(v)
	}
	return n + 1, nil
}

func (a *accountAPIServer) saveVerifiedQQBinding(ctx context.Context, tx *sql.Tx, id, account string, in qqbinding.Complete, hash string) (*qqbinding.Binding, error) {
	prior, err := readQQBinding(tx.QueryRowContext(ctx, "SELECT "+qqBindingColumns+" FROM "+a.table+"_qq_bindings WHERE account_id=? FOR UPDATE", id))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	now := time.Now().Unix()
	b := &qqbinding.Binding{AccountID: id, Account: account, QQ: in.QQ, Group: in.Group, BoundAt: now, State: "active", UpdatedAt: now, CheckedAt: now, CheckCause: "member"}
	if err == nil {
		if prior.QQ != in.QQ {
			return nil, errors.New("该账号已保留原 QQ 绑定，请使用原 QQ 重新验证")
		}
		if prior.State == "disabled" {
			return nil, errors.New("该绑定已由管理员禁用，请联系管理员")
		}
		b.BoundAt, b.Revision = prior.BoundAt, prior.Revision+1
		_, err = tx.ExecContext(ctx, "UPDATE "+a.table+"_qq_bindings SET group_id=?,state='active',revision=revision+1,updated_at=?,checked_at=?,check_cause='member',next_check_at=?,check_lease='',check_until=0,code_hash=? WHERE account_id=?", in.Group, now, now, now+qqbinding.MembershipIntervalSeconds, hash, id)
	} else {
		b.Revision, err = bindingRevision()
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO "+a.table+"_qq_bindings(account_id,qq,group_id,bound_at,code_hash,state,revision,updated_at,checked_at,check_cause,next_check_at) VALUES(?,?,?,?,?,'active',?,?,?,'member',?)", id, in.QQ, in.Group, now, hash, b.Revision, now, now, now+qqbinding.MembershipIntervalSeconds)
	}
	if err != nil {
		var duplicate *mysql.MySQLError
		if errors.As(err, &duplicate) && duplicate.Number == 1062 {
			return nil, errors.New("该 QQ 或账号已有绑定，每个 QQ 只能绑定一个账号")
		}
		return nil, err
	}
	return b, nil
}

func (a *accountAPIServer) changeQQBinding(ctx context.Context, c qqbinding.Change) (*qqbinding.Binding, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	a.moderationMu.RLock()
	defer a.moderationMu.RUnlock()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Account lock serializes code issuance, consumption and administrator changes.
	var name string
	if err = tx.QueryRowContext(ctx, "SELECT account FROM "+a.table+" WHERE account_id=? FOR UPDATE", c.AccountID).Scan(&name); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(c)
	fingerprint := bindingCodeHash(string(raw))
	var previous, payload string
	err = tx.QueryRowContext(ctx, "SELECT fingerprint,result_json FROM "+a.table+"_qq_changes WHERE request_id=?", c.RequestID).Scan(&previous, &payload)
	if err == nil {
		if previous != fingerprint {
			return nil, errors.New("请求编号已用于其他操作")
		}
		var b *qqbinding.Binding
		err = json.Unmarshal([]byte(payload), &b)
		return b, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	b, err := readQQBinding(tx.QueryRowContext(ctx, "SELECT "+qqBindingColumns+" FROM "+a.table+"_qq_bindings WHERE account_id=? FOR UPDATE", c.AccountID))
	if errors.Is(err, sql.ErrNoRows) || err == nil && (b.QQ != c.QQ || b.Revision != c.Revision) {
		return nil, errors.New("绑定记录已变化，请刷新后操作")
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if c.Action == "delete" {
		_, err = tx.ExecContext(ctx, "DELETE FROM "+a.table+"_qq_bindings WHERE account_id=?", c.AccountID)
		b = nil
	} else {
		state := "disabled"
		if c.Action == "enable" {
			var blocked int
			e := tx.QueryRowContext(ctx, "SELECT 1 FROM "+a.table+"_qq_blacklist WHERE qq=?", c.QQ).Scan(&blocked)
			if e == nil {
				return nil, errors.New("该 QQ 已在黑名单中")
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}
			state = "active"
		}
		_, err = tx.ExecContext(ctx, "UPDATE "+a.table+"_qq_bindings SET state=?,revision=revision+1,updated_at=?,next_check_at=0,check_lease='',check_until=0 WHERE account_id=?", state, now, c.AccountID)
		b.State, b.Revision, b.UpdatedAt, b.Account = state, b.Revision+1, now, name
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM "+a.table+"_qq_codes WHERE account_id=?", c.AccountID); err != nil {
		return nil, err
	}
	result, _ := json.Marshal(b)
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+a.table+"_qq_changes(request_id,fingerprint,result_json,created_at) VALUES(?,?,?,?)", c.RequestID, fingerprint, string(result), now); err != nil {
		return nil, err
	}
	return b, tx.Commit()
}

// The bot owns scheduling and membership I/O. This bounded lease persists only
// queue progress and protects new bindings from stale bot responses.
func (a *accountAPIServer) claimQQChecks(ctx context.Context) ([]qqbinding.Check, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var groups string
	var cfg qqbinding.Settings
	if err = tx.QueryRowContext(ctx, "SELECT groups_json,revision FROM "+a.table+"_qq_settings WHERE id=1 LOCK IN SHARE MODE").Scan(&groups, &cfg.Revision); err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(groups), &cfg.Groups); err != nil {
		return nil, err
	}
	out := []qqbinding.Check{}
	if len(cfg.Groups) == 0 {
		return out, nil
	}
	now := time.Now().Unix()
	rows, err := tx.QueryContext(ctx, "SELECT account_id,qq,revision FROM "+a.table+"_qq_bindings WHERE state='active' AND next_check_at<=? AND check_until<=? ORDER BY next_check_at,account_id LIMIT 10 FOR UPDATE", now, now)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c qqbinding.Check
		if err = rows.Scan(&c.AccountID, &c.QQ, &c.Revision); err != nil {
			rows.Close()
			return nil, err
		}
		c.Groups, c.SettingsRevision = cfg.Groups, cfg.Revision
		out = append(out, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		c := &out[i]
		c.Lease, err = bindingNonce()
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE "+a.table+"_qq_bindings SET check_lease=?,check_until=? WHERE account_id=?", c.Lease, now+300, c.AccountID); err != nil {
			return nil, err
		}
	}
	return out, tx.Commit()
}
func (a *accountAPIServer) queueQQCheck(ctx context.Context, qq string) error {
	if !qqbinding.Number.MatchString(qq) {
		return errors.New("QQ 参数无效")
	}
	_, err := a.db.ExecContext(ctx, "UPDATE "+a.table+"_qq_bindings SET next_check_at=0,check_until=0,check_lease='',revision=revision+1 WHERE qq=? AND state='active'", qq)
	return err
}
func (a *accountAPIServer) finishQQCheck(ctx context.Context, c qqbinding.CheckResult) (bool, error) {
	if c.AccountID == "" || !qqbinding.Number.MatchString(c.QQ) || len(c.Lease) != 32 || (c.Outcome != "member" && c.Outcome != "absent" && c.Outcome != "unknown") {
		return false, errors.New("群复核参数无效")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var raw string
	var revision uint64
	if err = tx.QueryRowContext(ctx, "SELECT groups_json,revision FROM "+a.table+"_qq_settings WHERE id=1 LOCK IN SHARE MODE").Scan(&raw, &revision); err != nil {
		return false, err
	}
	if revision != c.SettingsRevision {
		return false, nil
	}
	var groups []string
	if err = json.Unmarshal([]byte(raw), &groups); err != nil {
		return false, err
	}
	if len(groups) == 0 {
		return false, nil
	}
	if c.Outcome == "member" {
		allowed := false
		for _, g := range groups {
			allowed = allowed || g == c.Group
		}
		if !allowed {
			return false, errors.New("验证群已变化")
		}
	}
	now := time.Now().Unix()
	state := "active"
	next := now + qqbinding.MembershipIntervalSeconds
	set := "state=?,revision=revision+1,updated_at=?,checked_at=?,check_cause=?,next_check_at=?,check_lease='',check_until=0"
	args := []any{state, now, now, c.Outcome, next}
	if c.Outcome == "absent" {
		args[0] = "left"
	}
	if c.Outcome == "member" {
		set += ",group_id=?"
		args = append(args, c.Group)
	}
	if c.Outcome == "unknown" {
		set = "check_cause='unknown',next_check_at=?,check_lease='',check_until=0"
		args = []any{now + 300}
	}
	args = append(args, c.AccountID, c.QQ, c.Revision, c.Lease, now)
	result, err := tx.ExecContext(ctx, "UPDATE "+a.table+"_qq_bindings SET "+set+" WHERE account_id=? AND qq=? AND revision=? AND check_lease=? AND check_until>? AND state='active'", args...)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, tx.Commit()
}
