package dnfbridge

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func TestPlayerPolicyMySQL(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	table := fmt.Sprintf("player_test_%d", time.Now().UnixNano())
	s := &Service{}
	s.options.playerStrict = true
	s.options.accountAPILoginToken = true
	s.options.playerRegistrationLimit = 2
	s.options.playerProxySecret = "test-proxy-secret"
	s.options.playerAdminSecret = "test-admin-secret-more-than-32-characters"
	a := &accountAPIServer{db: db, table: table, service: s, allowRegister: true, minPassword: 6, rateHits: make(map[string][]time.Time)}
	s.accountAPI = a
	if err = a.ensureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	policy, err := s.signupBenefits.Policy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	policy.AccountCera = 2468
	if _, err = s.signupBenefits.SavePolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	// Imported installations can have a different database default than the
	// explicitly utf8mb4 account/token tables. Opaque identifiers compare bytes.
	for _, suffix := range []string{"_player_accounts", "_registration_sources", "_player_tokens"} {
		if _, err = db.Exec("ALTER TABLE " + table + suffix + " CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, suffix := range []string{"_benefit_policy", "_benefit_signup", "_benefit_jobs", "_tokens", "_player_tokens", "_player_accounts", "_registration_sources", "_registration_ips", ""} {
			db.Exec("DROP TABLE " + table + suffix)
		}
	}()
	call := func(action, account, device, ip string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(accountAPIRequest{Account: account, Password: "test-only-password", Device: device})
		r := httptest.NewRequest("POST", "/api/account/"+action, bytes.NewReader(body))
		r.RemoteAddr = "127.0.0.1:3333"
		r.Header.Set("X-Player-IP", ip)
		r.Header.Set("X-Player-Proxy-Key", s.options.playerProxySecret)
		w := httptest.NewRecorder()
		if action == "register" {
			a.handleRegister(w, r)
		} else {
			a.handleLogin(w, r)
		}
		return w
	}
	var wg sync.WaitGroup
	codes := make(chan int, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes <- call("register", fmt.Sprintf("race_%02d", i), "", "192.0.2.1").Code
		}(i)
	}
	wg.Wait()
	close(codes)
	var entitlementCount, entitlementTotal int64
	if err = db.QueryRow("SELECT COUNT(*), COALESCE(SUM(cera),0) FROM "+table+"_benefit_signup").Scan(&entitlementCount, &entitlementTotal); err != nil {
		t.Fatal(err)
	}
	if entitlementCount != 2 || entitlementTotal != 4936 {
		t.Fatalf("signup benefits not atomic: count=%d total=%d", entitlementCount, entitlementTotal)
	}
	created := 0
	for code := range codes {
		if code == 201 {
			created++
		} else if code != 403 {
			t.Fatalf("unexpected registration status %d", code)
		}
	}
	if created != 2 {
		t.Fatalf("concurrent quota created %d", created)
	}
	for i := 0; i < 3; i++ {
		if w := call("register", fmt.Sprintf("login_%d", i), "", fmt.Sprintf("192.0.2.%d", i+10)); w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	device := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	w := call("login", "login_0", device, "192.0.2.10")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var login accountAPIResponse
	json.Unmarshal(w.Body.Bytes(), &login)
	if w = call("login", "login_0", device, "192.0.2.10"); w.Code != 200 {
		t.Fatal("same account replacement rejected", w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &login)
	if w = call("login", "login_1", device, "192.0.2.11"); w.Code != 409 {
		t.Fatal("second device account accepted", w.Code)
	}
	if w = call("login", "login_2", device, "192.0.2.12"); w.Code != 409 {
		t.Fatal("third device session accepted", w.Code)
	}
	session := &gameSession{}
	if err = s.authenticatePlayer(session, login.Token); err != nil {
		t.Fatal(err)
	}
	if err = s.authenticatePlayer(&gameSession{}, "de509f65000000000000000000000000"); err == nil {
		t.Fatal("unknown token accepted")
	}
	// Revocation must win over a populated in-memory token cache.
	if err = func() error {
		_, err := db.Exec("INSERT INTO "+table+"_player_accounts(account_id,disabled) VALUES (?,TRUE)", login.AccountID)
		return err
	}(); err != nil {
		t.Fatal(err)
	}
	if err = s.authenticatePlayer(&gameSession{}, login.Token); err == nil {
		t.Fatal("revoked cached token accepted")
	}
	if w = call("login", "login_0", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "192.0.2.10"); w.Code != 403 {
		t.Fatal("disabled login accepted", w.Code)
	}
}
