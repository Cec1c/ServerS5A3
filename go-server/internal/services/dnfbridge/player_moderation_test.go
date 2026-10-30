package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"longheng.io/server/internal/modules/dnf/moderation"
	"net"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestModerationMySQLBanReplayExpiryAndUnban(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx := context.Background()
	_, login := f.login("main", "fixture-password")
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	session := &gameSession{conn: left, clientIP: "192.0.2.20"}
	if _, err := f.service.authenticateRemoteLogin(session, login.Token); err != nil {
		t.Fatal(err)
	}
	c := moderation.BanChange{RequestID: "fixture-account-ban-0001", Kind: "account", Target: "fixture:main", Reason: "fixture"}
	ban, err := f.api.changeBan(ctx, c)
	if err != nil || !ban.Active || ban.Revision != 1 {
		t.Fatalf("ban: %+v %v", ban, err)
	}
	if !session.loginRevoked.Load() {
		t.Fatal("live session not revoked")
	}
	f.service.accountLogins.finished(session)
	if _, err := f.service.authenticateRemoteLogin(&gameSession{}, login.Token); err == nil {
		t.Fatal("revoked token admitted")
	}
	if w, _ := f.login("main", "fixture-password"); w.Code != 403 {
		t.Fatal("banned login", w.Code)
	}
	replay, err := f.api.changeBan(ctx, c)
	if err != nil || replay.Revision != 1 {
		t.Fatal("replay", replay, err)
	}
	changed := c
	changed.Reason = "different"
	if _, err := f.api.changeBan(ctx, changed); !errors.Is(err, errBanConflict) {
		t.Fatal("payload collision", err)
	}
	unban := c
	unban.RequestID = "fixture-account-unban-1"
	unban.Revoke = true
	unban.Revision = 1
	if _, err := f.api.changeBan(ctx, unban); err != nil {
		t.Fatal(err)
	}
	if _, err := f.api.changeBan(ctx, c); err != nil {
		t.Fatal(err)
	} // A stale retry must never re-ban.
	if w, _ := f.login("main", "fixture-password"); w.Code != 200 {
		t.Fatal("unban", w.Code, w.Body.String())
	}
	expiry := time.Now().Add(time.Minute)
	c.RequestID = "fixture-expiring-ban-01"
	c.ExpiresAt = &expiry
	c.Revision = 2
	if _, err := f.api.changeBan(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE "+f.api.table+"_bans SET expires_at=?", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if w, _ := f.login("main", "fixture-password"); w.Code != 200 {
		t.Fatal("expiry did not restore login", w.Code)
	}
	page, err := f.api.moderationBans(ctx, moderation.Query{})
	if err != nil || len(page.Rows) != 1 || page.Rows[0].Active {
		t.Fatal(page, err)
	}
	var receipts int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM " + f.api.table + "_ban_receipts").Scan(&receipts); err != nil || receipts != 3 {
		t.Fatal(receipts, err)
	}
}

func TestModerationMySQLIPRegistrationNativeTokenAndConcurrency(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx := context.Background()
	_, login := f.login("main", "fixture-password")
	c := moderation.BanChange{RequestID: "fixture-ip-ban-000001", Kind: "ip", Target: "::ffff:192.0.2.99", Reason: "fixture"}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := f.api.changeBan(ctx, c); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.service.authenticateRemoteLogin(&gameSession{clientIP: "192.0.2.99"}, login.Token); err == nil {
		t.Fatal("old token bypassed IP ban")
	}
	allowed := &gameSession{clientIP: "192.0.2.100"}
	if _, err := f.service.authenticateRemoteLogin(allowed, login.Token); err != nil {
		t.Fatal("other IP rejected", err)
	}
	f.service.accountLogins.finished(allowed)
	f.api.allowRegister = true
	f.api.minPassword = 6
	raw, _ := json.Marshal(accountAPIRequest{Account: "new_fixture", Password: "fixture-password"})
	r := httptest.NewRequest("POST", "/api/account/register", bytes.NewReader(raw))
	r.RemoteAddr = "192.0.2.99:4321"
	w := httptest.NewRecorder()
	f.api.handleRegister(w, r)
	if w.Code != 403 {
		t.Fatal("registration bypass", w.Code, w.Body.String())
	}
	c.Kind = "ip"
	c.Target = "192.0.2.99/24"
	c.RequestID = "fixture-invalid-subnet"
	if _, err := f.api.changeBan(ctx, c); err == nil {
		t.Fatal("invalid IP accepted")
	}
}
