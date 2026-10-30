package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"longheng.io/server/internal/modules/dnf/qqbinding"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func securityRequest(f *replacementMySQLFixture, path, account, password, newPassword string) (*httptest.ResponseRecorder, accountAPIResponse) {
	raw, _ := json.Marshal(accountAPIRequest{Account: account, Password: password, NewPassword: newPassword})
	w := httptest.NewRecorder()
	f.api.handleAccountSecurity(w, httptest.NewRequest("POST", "/api/account/"+path, bytes.NewReader(raw)))
	var out accountAPIResponse
	json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}
func TestAccountSecurityMySQLBindingLifecycle(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx := context.Background()
	cfg, err := f.api.qqSettings(ctx)
	if err != nil || cfg.Enabled || len(cfg.Groups) != 0 {
		t.Fatal(cfg, err)
	}
	if w, _ := f.login("main", "fixture-password"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cfg, err = f.api.saveQQSettings(ctx, qqbinding.Settings{Enabled: true, Groups: []string{"123456789"}})
	if err != nil {
		t.Fatal(err)
	}
	if w, out := f.login("main", "fixture-password"); w.Code != 403 || out.Code != "qq_binding_required" {
		t.Fatal(w.Body.String())
	}
	w, out := securityRequest(f, "binding-code", "main", "fixture-password", "")
	if w.Code != 200 || len(out.BindingCode) != 32 {
		t.Fatal(w.Body.String())
	}
	c := qqbinding.Complete{Account: "main", Code: out.BindingCode, QQ: "234567890", Group: "123456789", Revision: cfg.Revision}
	bad := c
	bad.Group = "345678901"
	if _, err = f.api.completeQQBinding(ctx, bad); err == nil {
		t.Fatal("unlisted group")
	}
	bad = c
	bad.Revision = 0
	if _, err = f.api.completeQQBinding(ctx, bad); err == nil {
		t.Fatal("stale policy")
	}
	b, err := f.api.completeQQBinding(ctx, c)
	if err != nil || b.QQ != c.QQ {
		t.Fatal(b, err)
	}
	b2, err := f.api.completeQQBinding(ctx, c)
	if err != nil || *b2 != *b {
		t.Fatal("lost response replay", b2, err)
	}
	bad = c
	bad.QQ = "456789012"
	if _, err = f.api.completeQQBinding(ctx, bad); err == nil {
		t.Fatal("code reused by another QQ")
	}
	if w, _ = f.login("main", "fixture-password"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	_, other := securityRequest(f, "binding-code", "other", "fixture-password", "")
	bad = c
	bad.Account = "other"
	bad.Code = other.BindingCode
	if _, err = f.api.completeQQBinding(ctx, bad); err == nil {
		t.Fatal("QQ bound to two accounts")
	}
	page, err := f.api.queryQQBindings(ctx, qqbinding.Query{Search: c.QQ, Limit: 1})
	if err != nil || page.Total != 1 || len(page.Rows) != 1 {
		t.Fatal(page, err)
	}
	if _, err = f.api.saveQQSettings(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	if _, err = f.api.saveQQSettings(ctx, *cfg); err == nil {
		t.Fatal("stale configuration accepted")
	}
}
func TestAccountSecurityMySQLSharedCooldownAndPassword(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	f.api.minPassword = 6
	_, old := f.login("main", "fixture-password")
	if w, _ := f.login("MAIN", "incorrect"); w.Code != 401 {
		t.Fatal(w.Body.String())
	}
	if w, _ := securityRequest(f, "password", "main", "incorrect", "new-password"); w.Code != 401 {
		t.Fatal(w.Body.String())
	}
	if w, out := securityRequest(f, "binding-code", "main", "incorrect", ""); w.Code != 429 || out.RetryAfter != 60 {
		t.Fatal(w.Body.String())
	}
	// New API instance reads the same deadline: process restarts cannot reset it.
	saved := f.api
	copyAPI := accountAPIServer{db: saved.db, table: saved.table, service: saved.service, rateHits: make(map[string][]time.Time), minPassword: saved.minPassword}
	f.api = &copyAPI
	if w, out := f.login("main", "fixture-password"); w.Code != 429 || out.RetryAfter < 50 || out.RetryAfter > 60 {
		t.Fatal(w.Body.String())
	}
	f.api = saved
	if _, err := f.db.Exec("UPDATE "+f.api.table+"_auth_failures SET blocked_until=?", time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if w, _ := securityRequest(f, "password", "main", "fixture-password", "new-password"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, ok := f.service.loginTokens.recall(old.Token, time.Now()); ok {
		t.Fatal("password change left token cache valid")
	}
	var n int
	f.db.QueryRow("SELECT COUNT(*) FROM " + f.api.loginTokenTable()).Scan(&n)
	if n != 0 {
		t.Fatal("old tokens persisted")
	}
	if w, _ := f.login("main", "fixture-password"); w.Code != 401 {
		t.Fatal(w.Body.String())
	}
	if w, _ := f.login("main", "new-password"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var failures int
	f.db.QueryRow("SELECT failures FROM " + f.api.table + "_auth_failures LIMIT 1").Scan(&failures)
	if failures != 0 {
		t.Fatal("success did not reset failures")
	}
}
func TestAccountSecurityMySQLConcurrentGuessesAndBinding(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	statuses := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); w, _ := f.login("main", "incorrect"); statuses <- w.Code }()
	}
	wg.Wait()
	close(statuses)
	denied, cool := 0, 0
	for s := range statuses {
		if s == 401 {
			denied++
		} else if s == 429 {
			cool++
		} else {
			t.Fatal(s)
		}
	}
	if denied != 2 || cool != 6 {
		t.Fatal(denied, cool)
	}
	cfg, err := f.api.saveQQSettings(ctx, qqbinding.Settings{Enabled: true, Groups: []string{"123456789"}})
	if err != nil {
		t.Fatal(err)
	}
	_, out := securityRequest(f, "binding-code", "other", "fixture-password", "")
	results := make(chan error, 2)
	for _, qq := range []string{"234567890", "345678901"} {
		wg.Add(1)
		go func(qq string) {
			defer wg.Done()
			_, e := f.api.completeQQBinding(ctx, qqbinding.Complete{Account: "other", Code: out.BindingCode, QQ: qq, Group: cfg.Groups[0], Revision: cfg.Revision})
			results <- e
		}(qq)
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatal(winners)
	}
}

func TestAccountSecurityMySQLShortenedPersistedCooldown(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	for i := 0; i < 3; i++ {
		f.login("main", "incorrect")
	}
	now := time.Now().Unix()
	if _, err := f.db.Exec("UPDATE "+f.api.table+"_auth_failures SET blocked_until=?,updated_at=?", now+570, now-30); err != nil {
		t.Fatal(err)
	}
	if w, out := f.login("main", "fixture-password"); w.Code != 429 || out.RetryAfter < 20 || out.RetryAfter > 30 || w.Header().Get("Retry-After") == "" {
		t.Fatal(w.Body.String())
	}
	var until, updated int64
	if err := f.db.QueryRow("SELECT blocked_until,updated_at FROM "+f.api.table+"_auth_failures").Scan(&until, &updated); err != nil {
		t.Fatal(err)
	}
	if until != now+570 || updated != now-30 {
		t.Fatal("blocked attempt extended or reset original deadline")
	}
	if _, err := f.db.Exec("UPDATE "+f.api.table+"_auth_failures SET blocked_until=?,updated_at=?", now+530, now-70); err != nil {
		t.Fatal(err)
	}
	if w, _ := f.login("main", "fixture-password"); w.Code != 200 {
		t.Fatal("legacy ten-minute cooldown survived sixty seconds", w.Body.String())
	}
}
func TestAccountSecurityMySQLCodeRotationExpiryAndPasswordInvalidation(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx := context.Background()
	cfg, _ := f.api.saveQQSettings(ctx, qqbinding.Settings{Enabled: true, Groups: []string{"123456789"}})
	_, old := securityRequest(f, "binding-code", "main", "fixture-password", "")
	_, fresh := securityRequest(f, "binding-code", "main", "fixture-password", "")
	c := qqbinding.Complete{Account: "main", Code: old.BindingCode, QQ: "234567890", Group: cfg.Groups[0], Revision: cfg.Revision}
	if _, err := f.api.completeQQBinding(ctx, c); err == nil {
		t.Fatal("superseded code accepted")
	}
	c.Code = fresh.BindingCode
	f.db.Exec("UPDATE " + f.api.table + "_qq_codes SET expires_at=0")
	if _, err := f.api.completeQQBinding(ctx, c); err == nil {
		t.Fatal("expired code accepted")
	}
	_, fresh = securityRequest(f, "binding-code", "main", "fixture-password", "")
	c.Code = fresh.BindingCode
	if w, _ := securityRequest(f, "password", "main", "fixture-password", "changed-password"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, err := f.api.completeQQBinding(ctx, c); err == nil {
		t.Fatal("password change left code valid")
	}
}
