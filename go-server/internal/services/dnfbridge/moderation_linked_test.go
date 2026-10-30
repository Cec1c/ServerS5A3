package dnfbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/moderation"
	"longheng.io/server/internal/modules/dnf/qqbinding"
)

func TestModerationMySQLLinkedBanSnapshotReplayAndQueue(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx := context.Background()
	cfg, err := f.api.saveQQSettings(ctx, qqbinding.Settings{Enabled: true, Groups: []string{"123456789", "345678901"}})
	if err != nil {
		t.Fatal(err)
	}
	_, code := securityRequest(f, "binding-code", "main", "fixture-password", "")
	if _, err = f.api.completeQQBinding(ctx, qqbinding.Complete{Account: "main", Code: code.BindingCode, QQ: "234567890", Group: cfg.Groups[0], Revision: cfg.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("UPDATE " + f.api.table + " SET last_login_ip='::ffff:192.0.2.20' WHERE account='main'"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("INSERT INTO " + f.api.table + "_registration_sources(account_id,ip) VALUES('fixture:main','192.0.2.20')"); err != nil {
		t.Fatal(err)
	}
	// Existing account and registration tables may have different collations
	// after a database upgrade; joins must retain exact account identity.
	for suffix, collation := range map[string]string{"": "utf8mb4_unicode_ci", "_registration_sources": "utf8mb4_general_ci", "_qq_bindings": "utf8mb4_bin"} {
		if _, err = f.db.Exec("ALTER TABLE " + f.api.table + suffix + " MODIFY account_id VARCHAR(64) CHARACTER SET utf8mb4 COLLATE " + collation + " NOT NULL"); err != nil {
			t.Fatal(err)
		}
	}
	preview, err := f.api.banTargets(ctx, f.db, "fixture:main")
	if err != nil || preview.LastLoginIP != "192.0.2.20" {
		t.Fatal(preview, err)
	}
	bindings, err := f.api.queryQQBindings(ctx, qqbinding.Query{})
	if err != nil || len(bindings.Rows) != 1 || bindings.Rows[0].AccountID != "fixture:main" {
		t.Fatal(bindings, err)
	}
	expiry := time.Now().Add(time.Hour)
	c := moderation.BanChange{RequestID: "fixture-linked-ban-0001", Kind: "account", Target: "fixture:main", Reason: "fixture", ExpiresAt: &expiry, Linked: preview}
	b, err := f.api.changeBan(ctx, c)
	if err != nil || len(b.LinkedIPs) != 1 || len(b.QQActions) != 2 {
		t.Fatal(b, err)
	}
	replay, err := f.api.changeBan(ctx, c)
	if err != nil || replay.Revision != b.Revision {
		t.Fatal(replay, err)
	}
	var n int
	f.db.QueryRow("SELECT COUNT(*) FROM " + f.api.table + "_qq_actions").Scan(&n)
	if n != 2 {
		t.Fatal("duplicate tasks", n)
	}
	blocked, err := f.api.qqBlacklisted(ctx, "234567890")
	if err != nil || !blocked {
		t.Fatal(blocked, err)
	}
	claimed, err := f.api.claimQQActions(ctx)
	if err != nil || len(claimed) != 2 {
		t.Fatal(claimed, err)
	}
	if tasks, err := f.api.claimQQActions(ctx); err != nil || len(tasks) != 0 {
		t.Fatal("leased twice", tasks, err)
	}
	// A stale worker cannot overwrite the current claim.
	if err = f.api.ackQQAction(ctx, moderation.QQAck{ID: claimed[0].ID, Lease: "00000000000000000000000000000000", Success: true}); err != nil {
		t.Fatal(err)
	}
	page, err := f.api.moderationBans(ctx, moderation.Query{Search: "fixture:main"})
	if err != nil || page.Rows[0].QQActions[0].State != "running" {
		t.Fatal(page, err)
	}
	if err = f.api.ackQQAction(ctx, moderation.QQAck{ID: claimed[0].ID, Lease: claimed[0].Lease, Cause: "permission_denied"}); err != nil {
		t.Fatal(err)
	}
	if err = f.api.ackQQAction(ctx, moderation.QQAck{ID: claimed[1].ID, Lease: claimed[1].Lease, Success: true, Cause: "kicked"}); err != nil {
		t.Fatal(err)
	}
	f.db.Exec("UPDATE " + f.api.table + "_qq_actions SET next_at=0 WHERE state='retry'")
	tasks, err := f.api.claimQQActions(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].Attempts != 2 {
		t.Fatal(tasks, err)
	}
	// A permanent IP ban is not shortened by a later timed linked ban.
	f.db.Exec("UPDATE " + f.api.table + "_bans SET expires_at=NULL WHERE kind='ip'")
	c.RequestID = "fixture-linked-ban-0002"
	c.Revision = 1
	if _, err = f.api.changeBan(ctx, c); err != nil {
		t.Fatal(err)
	}
	ip, err := readBan(f.db.QueryRow("SELECT " + banColumns + " FROM " + f.api.table + "_bans WHERE kind='ip'"))
	if err != nil || ip.ExpiresAt != nil {
		t.Fatal(ip, err)
	}
	// Changing the confirmed account IP rolls back the whole account mutation.
	f.db.Exec("UPDATE " + f.api.table + " SET last_login_ip='192.0.2.21' WHERE account='main'")
	c.RequestID = "fixture-linked-ban-0003"
	c.Revision = 2
	if _, err = f.api.changeBan(ctx, c); !errors.Is(err, errBanConflict) {
		t.Fatal("stale target", err)
	}
	current, err := readBan(f.db.QueryRow("SELECT " + banColumns + " FROM " + f.api.table + "_bans WHERE kind='account'"))
	if err != nil || current.Revision != 2 {
		t.Fatal(current, err)
	}
	if _, err = f.api.completeQQBinding(ctx, qqbinding.Complete{Account: "main", Code: code.BindingCode, QQ: "234567890", Group: cfg.Groups[0], Revision: cfg.Revision}); err == nil {
		t.Fatal("blacklisted binding replay")
	}
}
