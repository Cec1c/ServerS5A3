package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adminapi"
)

func resetFixtureRequest() adminapi.PasswordReset {
	return adminapi.PasswordReset{RequestID: "fixture-reset-00000001", Account: "main", Password: "fixture_password_12345"}
}

func TestAccountPasswordResetMySQLLifecycleAndReplay(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	ctx := context.Background()
	in := resetFixtureRequest()
	w, old := f.login("main", "fixture-password")
	if w.Code != 200 {
		t.Fatal("initial login", w.Code)
	}
	server, peer := net.Pipe()
	t.Cleanup(func() { server.Close(); peer.Close() })
	session := &gameSession{connID: "reset-fixture", conn: server}
	if _, err := f.service.authenticateRemoteLogin(session, old.Token); err != nil {
		t.Fatal(err)
	}
	_, other := f.login("other", "fixture-password")
	if _, err := f.db.Exec("INSERT INTO "+f.api.table+"_qq_bindings(account_id,qq,group_id,bound_at,code_hash) VALUES(?,?,?,?,?)", old.AccountID, "234567890", "123456789", 1, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("INSERT INTO "+f.api.table+"_qq_codes(account_id,code_hash,expires_at) VALUES(?,?,?)", old.AccountID, strings.Repeat("b", 64), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		f.login("main", "wrong")
	}
	out, err := f.api.resetAccountPassword(ctx, old.AccountID, in)
	if err != nil || out.AccountID != old.AccountID || out.RequestID != in.RequestID {
		t.Fatal("reset result", err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = peer.Read(make([]byte, 1)); err != io.EOF || !session.loginRevoked.Load() {
		t.Fatal("old connection retained", err)
	}
	f.service.accountLogins.finished(session)
	if _, ok := f.service.loginTokens.recall(old.Token, time.Now()); ok {
		t.Fatal("old token cached")
	}
	if _, err = f.service.authenticateRemoteLogin(&gameSession{}, old.Token); err == nil {
		t.Fatal("old token accepted")
	}
	if _, ok := f.service.loginTokens.recall(other.Token, time.Now()); !ok {
		t.Fatal("other account token revoked")
	}
	for _, suffix := range []string{"_tokens", "_qq_codes"} {
		var n int
		if err = f.db.QueryRow("SELECT COUNT(*) FROM "+f.api.table+suffix+" WHERE account_id=?", old.AccountID).Scan(&n); err != nil || n != 0 {
			t.Fatal("old credentials persisted", suffix, n, err)
		}
	}
	var qq string
	if err = f.db.QueryRow("SELECT qq FROM "+f.api.table+"_qq_bindings WHERE account_id=?", old.AccountID).Scan(&qq); err != nil || qq != "234567890" {
		t.Fatal("binding changed", err)
	}
	if w, _ = f.login("main", "fixture-password"); w.Code != 401 {
		t.Fatal("old password accepted", w.Code)
	}
	w, next := f.login("main", in.Password)
	if w.Code != 200 {
		t.Fatal("new password rejected / cooldown retained", w.Code)
	}
	// Reconstruct the API to prove the receipt survives process state loss.
	restarted := &accountAPIServer{db: f.db, table: f.api.table, service: f.service}
	replayed, err := restarted.resetAccountPassword(ctx, old.AccountID, in)
	if err != nil || *out != *replayed {
		t.Fatal("replay result", err)
	}
	if _, ok := f.service.loginTokens.recall(next.Token, time.Now()); !ok {
		t.Fatal("replay revoked a new login")
	}
	var fingerprint string
	var n int
	if err = f.db.QueryRow("SELECT fingerprint FROM "+f.api.table+"_password_resets WHERE request_id=?", in.RequestID).Scan(&fingerprint); err != nil || len(fingerprint) != 64 || strings.Contains(fingerprint, in.Password) {
		t.Fatal("invalid receipt", err)
	}
	if err = f.db.QueryRow("SELECT COUNT(*) FROM " + f.api.table + "_password_resets").Scan(&n); err != nil || n != 1 {
		t.Fatal("duplicate reset", n, err)
	}
	if w, _ := securityRequest(f, "password", "main", in.Password, "player-changed-password"); w.Code != 200 {
		t.Fatal("player password change", w.Code)
	}
	if _, err = f.api.resetAccountPassword(ctx, old.AccountID, in); err == nil {
		t.Fatal("replay reverted later password")
	}
	if w, _ := f.login("main", "player-changed-password"); w.Code != 200 {
		t.Fatal("later password changed", w.Code)
	}
}

func TestAccountPasswordResetMySQLConcurrentAndConflicting(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	in := resetFixtureRequest()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.api.resetAccountPassword(context.Background(), "fixture:main", in); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var n int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM " + f.api.table + "_password_resets").Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	for _, change := range []func(*adminapi.PasswordReset){func(r *adminapi.PasswordReset) { r.Password = "different_password_123" }, func(r *adminapi.PasswordReset) { r.Account = "other" }} {
		bad := in
		change(&bad)
		id := "fixture:" + bad.Account
		if _, err := f.api.resetAccountPassword(context.Background(), id, bad); err == nil {
			t.Fatal("conflicting request accepted")
		}
	}
	if w, _ := f.login("other", "fixture-password"); w.Code != 200 {
		t.Fatal("other password changed", w.Code)
	}
}

func TestAccountPasswordResetMySQLRollbackAndIdentity(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	in := resetFixtureRequest()
	_, old := f.login("main", "fixture-password")
	for _, id := range []string{"missing", "fixture:other", "FIXTURE:MAIN"} {
		if _, err := f.api.resetAccountPassword(context.Background(), id, in); err == nil {
			t.Fatal("wrong account accepted")
		}
	}
	if _, err := f.db.Exec("CREATE TRIGGER " + f.api.table + "_reset_fault BEFORE INSERT ON " + f.api.table + "_password_resets FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture failure'"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.api.resetAccountPassword(context.Background(), old.AccountID, in); err == nil {
		t.Fatal("injected failure did not fail")
	}
	if _, ok := f.service.loginTokens.recall(old.Token, time.Now()); !ok {
		t.Fatal("rollback revoked cached token")
	}
	if _, err := f.service.authenticateRemoteLogin(&gameSession{}, old.Token); err != nil {
		t.Fatal("rollback revoked stored token", err)
	}
	if w, _ := f.login("main", "fixture-password"); w.Code != 200 {
		t.Fatal("rollback changed password", w.Code)
	}
}

func TestAccountPasswordResetMySQLPrivateHTTP(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	f.service.options.playerAdminSecret = strings.Repeat("x", 32)
	in := resetFixtureRequest()
	raw, _ := json.Marshal(adminapi.Request{Version: adminapi.Version, Action: "reset_account_password", AccountID: "fixture:main", PasswordReset: &in})
	for _, authorized := range []bool{false, true} {
		r := httptest.NewRequest("POST", adminapi.Path, bytes.NewReader(raw))
		if authorized {
			r.Header.Set("Authorization", "Bearer "+f.service.options.playerAdminSecret)
		}
		w := httptest.NewRecorder()
		f.api.handleGameAdmin(w, r)
		if !authorized {
			if w.Code != 404 {
				t.Fatal(w.Code)
			}
			continue
		}
		var out adminapi.Response
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || !out.OK || out.PasswordReset == nil {
			t.Fatal("reset HTTP failed")
		}
		if strings.Contains(w.Body.String(), in.Password) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("credential leaked/cached")
		}
	}
	// Persisted cool-down was cleared for the canonical account.
	subject := sha256.Sum256([]byte(in.Account))
	var blocked int64
	if err := f.db.QueryRow("SELECT blocked_until FROM "+f.api.table+"_auth_failures WHERE subject=?", hex.EncodeToString(subject[:])).Scan(&blocked); err != nil || blocked != 0 {
		t.Fatal(blocked, err)
	}
}

func TestAccountPasswordResetRejectsInvalidBeforeDatabase(t *testing.T) {
	a := &accountAPIServer{}
	in := resetFixtureRequest()
	in.Password = "short"
	_, err := a.resetAccountPassword(context.Background(), "fixture:main", in)
	var failure *accountSecurityError
	if !errors.As(err, &failure) || failure.code != "invalid_reset" {
		t.Fatal("invalid credential reached storage")
	}
}

func TestAccountPasswordResetRetiresOnlyTargetSession(t *testing.T) {
	s := &Service{}
	a := &accountAPIServer{service: s}
	target, other := &gameSession{}, &gameSession{}
	s.accountLogins.sessions = map[*gameSession]string{target: "main", other: "other"}
	s.accountLogins.mu.Lock()
	a.revokeAccountCredentialsLocked("main")
	s.accountLogins.mu.Unlock()
	if !target.loginRevoked.Load() || other.loginRevoked.Load() {
		t.Fatal("reset did not isolate target sessions")
	}
}
