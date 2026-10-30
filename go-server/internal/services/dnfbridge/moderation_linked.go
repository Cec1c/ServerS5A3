package dnfbridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"longheng.io/server/internal/modules/dnf/moderation"
	"longheng.io/server/internal/modules/dnf/qqbinding"
)

type banTargetReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (a *accountAPIServer) banTargets(ctx context.Context, q banTargetReader, id string) (*moderation.BanTargets, error) {
	if id == "" || len(id) > 64 {
		return nil, errors.New("invalid account")
	}
	out := &moderation.BanTargets{}
	err := q.QueryRowContext(ctx, "SELECT COALESCE(a.last_login_ip,''),COALESCE(r.ip,''),COALESCE(b.qq,'') FROM "+a.table+" a LEFT JOIN "+a.table+"_registration_sources r ON BINARY r.account_id=BINARY a.account_id LEFT JOIN "+a.table+"_qq_bindings b ON BINARY b.account_id=BINARY a.account_id WHERE BINARY a.account_id=BINARY ?", id).Scan(&out.LastLoginIP, &out.RegistrationIP, &out.QQ)
	if err != nil {
		return nil, err
	}
	out.LastLoginIP, _ = moderation.CanonicalIP(out.LastLoginIP)
	out.RegistrationIP, _ = moderation.CanonicalIP(out.RegistrationIP)
	var groups string
	if err = q.QueryRowContext(ctx, "SELECT groups_json,revision FROM "+a.table+"_qq_settings WHERE id=1 LOCK IN SHARE MODE").Scan(&groups, &out.SettingsRevision); err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(groups), &out.Groups); err != nil {
		return nil, err
	}
	return out, nil
}

// Account, selected IPs, blacklist and durable group tasks share one receipt and
// transaction. The client must confirm the exact targets read from the server.
func (a *accountAPIServer) commitLinkedBans(ctx context.Context, tx *sql.Tx, c moderation.BanChange, b *moderation.Ban) error {
	current, err := a.banTargets(ctx, tx, c.Target)
	if err != nil {
		return err
	}
	selected := c.Linked
	if selected.LastLoginIP != "" && selected.LastLoginIP != current.LastLoginIP || selected.RegistrationIP != "" && selected.RegistrationIP != current.RegistrationIP {
		return errBanConflict
	}
	if selected.QQ != "" && (selected.QQ != current.QQ || selected.SettingsRevision != current.SettingsRevision || !slices.Equal(selected.Groups, current.Groups)) {
		return errBanConflict
	}
	seen := map[string]bool{}
	for _, ip := range []string{selected.LastLoginIP, selected.RegistrationIP} {
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		prior, e := readBan(tx.QueryRowContext(ctx, "SELECT "+banColumns+" FROM "+a.table+"_bans WHERE kind='ip' AND target=? FOR UPDATE", ip))
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		expires := c.ExpiresAt
		// Linking an IP never shortens a stronger existing ban.
		if prior.Active && (prior.ExpiresAt == nil || expires != nil && prior.ExpiresAt.After(*expires)) {
			expires = prior.ExpiresAt
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO "+a.table+"_bans ("+banColumns+") VALUES('ip',?,?,?,?,FALSE,?,?) ON DUPLICATE KEY UPDATE reason=VALUES(reason),expires_at=VALUES(expires_at),revoked=FALSE,revision=VALUES(revision),updated_at=VALUES(updated_at)", ip, ip, c.Reason, expires, prior.Revision+1, b.UpdatedAt); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "DELETE t FROM "+a.loginTokenTable()+" t JOIN "+a.table+" a ON BINARY a.account_id=BINARY t.account_id WHERE a.last_login_ip=? OR t.last_used_ip=?", ip, ip); e != nil {
			return e
		}
		b.LinkedIPs = append(b.LinkedIPs, ip)
	}
	if selected.QQ == "" {
		return nil
	}
	if !qqbinding.Number.MatchString(selected.QQ) || len(selected.Groups) == 0 {
		return errors.New("invalid QQ")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+a.table+"_qq_blacklist(qq,account_id,reason,created_at) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE reason=VALUES(reason)", selected.QQ, c.Target, c.Reason, b.UpdatedAt.Unix()); err != nil {
		return err
	}
	for _, group := range selected.Groups {
		if !qqbinding.Number.MatchString(group) {
			return errors.New("invalid group")
		}
		digest := sha256.Sum256([]byte(c.RequestID + ":" + group))
		id := hex.EncodeToString(digest[:])
		if _, err = tx.ExecContext(ctx, "INSERT INTO "+a.table+"_qq_actions(id,account_id,qq,group_id,created_at) VALUES(?,?,?,?,?)", id, c.Target, selected.QQ, group, b.UpdatedAt.Unix()); err != nil {
			return err
		}
		b.QQActions = append(b.QQActions, moderation.QQAction{ID: id, AccountID: c.Target, QQ: selected.QQ, Group: group, State: "pending"})
	}
	return nil
}

func (a *accountAPIServer) qqBlacklisted(ctx context.Context, qq string) (bool, error) {
	if !qqbinding.Number.MatchString(qq) {
		return false, errors.New("invalid QQ")
	}
	var n int
	err := a.db.QueryRowContext(ctx, "SELECT 1 FROM "+a.table+"_qq_blacklist WHERE qq=?", qq).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (a *accountAPIServer) qqActionsForAccount(ctx context.Context, account string) ([]moderation.QQAction, error) {
	rows, err := a.db.QueryContext(ctx, "SELECT id,account_id,qq,group_id,state,attempts,cause FROM "+a.table+"_qq_actions WHERE account_id=? ORDER BY created_at DESC,id LIMIT 32", account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []moderation.QQAction{}
	for rows.Next() {
		var item moderation.QQAction
		if err = rows.Scan(&item.ID, &item.AccountID, &item.QQ, &item.Group, &item.State, &item.Attempts, &item.Cause); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (a *accountAPIServer) claimQQActions(ctx context.Context) ([]moderation.QQAction, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	rows, err := tx.QueryContext(ctx, "SELECT id,account_id,qq,group_id,state,attempts,cause FROM "+a.table+"_qq_actions WHERE state<>'succeeded' AND next_at<=? ORDER BY next_at,id LIMIT 4 FOR UPDATE", now)
	if err != nil {
		return nil, err
	}
	out := []moderation.QQAction{}
	for rows.Next() {
		var item moderation.QQAction
		if err = rows.Scan(&item.ID, &item.AccountID, &item.QQ, &item.Group, &item.State, &item.Attempts, &item.Cause); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		var raw [16]byte
		if _, err = rand.Read(raw[:]); err != nil {
			return nil, err
		}
		out[i].Lease = hex.EncodeToString(raw[:])
		out[i].Attempts++
		out[i].State = "running"
		if _, err = tx.ExecContext(ctx, "UPDATE "+a.table+"_qq_actions SET state='running',lease=?,attempts=attempts+1,next_at=? WHERE id=?", out[i].Lease, now+120, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, tx.Commit()
}

func (a *accountAPIServer) ackQQAction(ctx context.Context, ack moderation.QQAck) error {
	if len(ack.ID) != 64 || len(ack.Lease) != 32 {
		return errors.New("invalid acknowledgement")
	}
	cause := "qq_api_failed"
	switch ack.Cause {
	case "not_in_group", "permission_denied", "kicked", "qq_api_failed":
		cause = ack.Cause
	}
	state := "retry"
	if ack.Success {
		state = "succeeded"
	}
	// A late worker cannot acknowledge a task leased by a newer worker.
	_, err := a.db.ExecContext(ctx, "UPDATE "+a.table+"_qq_actions SET state=?,cause=?,next_at=? WHERE id=? AND lease=? AND state='running'", state, cause, time.Now().Add(5*time.Minute).Unix(), ack.ID, ack.Lease)
	return err
}
