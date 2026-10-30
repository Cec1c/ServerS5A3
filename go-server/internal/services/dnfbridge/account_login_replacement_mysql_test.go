package dnfbridge

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

type replacementMySQLFixture struct {
	service *Service
	api     *accountAPIServer
	db      *sql.DB
}

func newReplacementMySQLFixture(t *testing.T) *replacementMySQLFixture {
	t.Helper()
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{}
	s.options.accountAPILoginToken = true
	a := &accountAPIServer{db: db, table: fmt.Sprintf("replace_%d", time.Now().UnixNano()), service: s, rateHits: make(map[string][]time.Time)}
	s.accountAPI = a
	if err := a.ensureSchema(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, suffix := range []string{"_benefit_policy", "_benefit_signup", "_benefit_jobs", "_tokens", ""} {
			_, _ = db.Exec("DROP TABLE " + a.table + suffix)
		}
		db.Close()
	})
	f := &replacementMySQLFixture{service: s, api: a, db: db}
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main", "other", "third"} {
		if _, err := db.Exec("INSERT INTO "+a.table+" (account,password_hash,account_id,created_at) VALUES (?,?,?,?)", name, string(hash), "fixture:"+name, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *replacementMySQLFixture) login(account, password string) (*httptest.ResponseRecorder, accountAPIResponse) {
	body, _ := json.Marshal(accountAPIRequest{Account: account, Password: password})
	r := httptest.NewRequest("POST", "/api/account/login", bytes.NewReader(body))
	w := httptest.NewRecorder()
	f.api.handleLogin(w, r)
	var result accountAPIResponse
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	return w, result
}

func TestAccountLoginReplacementMySQLClosesAllSocketsRevokesAndWaits(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	w, oldLogin := f.login("main", "fixture-password")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var oldSessions []*gameSession
	var peers []net.Conn
	for i := 0; i < 2; i++ {
		server, client := net.Pipe()
		t.Cleanup(func() { server.Close(); client.Close() })
		session := &gameSession{connID: fmt.Sprint("old-", i), conn: server}
		if _, err := f.service.authenticateRemoteLogin(session, oldLogin.Token); err != nil {
			t.Fatal(err)
		}
		oldSessions = append(oldSessions, session)
		peers = append(peers, client)
	}
	w, newLogin := f.login("main", "fixture-password")
	if w.Code != 200 || newLogin.Token == oldLogin.Token {
		t.Fatal(w.Code, "replacement not issued")
	}
	for _, peer := range peers {
		_ = peer.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
			t.Fatalf("old socket not closed: %v", err)
		}
	}
	if _, found := f.service.loginTokens.recall(oldLogin.Token, time.Now()); found {
		t.Fatal("old token cached")
	}
	if _, err := f.service.authenticateRemoteLogin(&gameSession{}, oldLogin.Token); err == nil {
		t.Fatal("revoked token accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := f.service.authenticateRemoteLogin(&gameSession{ctx: ctx}, newLogin.Token); err == nil {
		t.Fatal("authenticated before old cleanup")
	}
	for _, session := range oldSessions {
		f.service.accountLogins.finished(session)
	}
	next := &gameSession{}
	if _, err := f.service.authenticateRemoteLogin(next, newLogin.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.authenticateRemoteLogin(&gameSession{}, newLogin.Token); err != nil {
		t.Fatal("auxiliary rejected", err)
	}
	for _, table := range []string{f.api.loginTokenTable()} {
		var count int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("token table %s count=%d err=%v", table, count, err)
		}
	}
}

func TestAccountLoginReplacementMySQLRejectedPasswordPreservesOld(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	_, oldLogin := f.login("main", "fixture-password")
	old := &gameSession{}
	if _, err := f.service.authenticateRemoteLogin(old, oldLogin.Token); err != nil {
		t.Fatal(err)
	}
	if w, _ := f.login("main", "wrong-password"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	// The hash may change after bcrypt but before the transaction obtains its row lock.
	f.service.accountLogins.mu.Lock()
	_, _, err := f.api.replaceLoginLocked(context.Background(), "main", oldLogin.AccountID, "outdated-hash", "192.0.2.44", "")
	f.service.accountLogins.mu.Unlock()
	if err == nil || old.loginRevoked.Load() {
		t.Fatal("changed password retired old session")
	}
	if _, err := f.service.authenticateRemoteLogin(&gameSession{}, oldLogin.Token); err != nil {
		t.Fatal("old token revoked", err)
	}
}

func TestAccountLoginReplacementMySQLTransactionRollback(t *testing.T) {
	for _, phase := range []string{"token_insert", "old_token_delete"} {
		t.Run(phase, func(t *testing.T) {
			f := newReplacementMySQLFixture(t)
			_, oldLogin := f.login("main", "fixture-password")
			old := &gameSession{}
			if _, err := f.service.authenticateRemoteLogin(old, oldLogin.Token); err != nil {
				t.Fatal(err)
			}
			verb := "INSERT"
			if phase == "old_token_delete" {
				verb = "DELETE"
			}
			statement := "CREATE TRIGGER " + f.api.table + "_fault BEFORE " + verb + " ON " + f.api.loginTokenTable() + " FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture failure'"
			if _, err := f.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			if w, _ := f.login("main", "fixture-password"); w.Code != 503 {
				t.Fatal(w.Code)
			}
			if old.loginRevoked.Load() {
				t.Fatal("failed transaction retired live session")
			}
			if _, err := f.service.authenticateRemoteLogin(&gameSession{}, oldLogin.Token); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := f.db.QueryRow("SELECT COUNT(*) FROM " + f.api.loginTokenTable()).Scan(&count); err != nil || count != 1 {
				t.Fatalf("tokens=%d err=%v", count, err)
			}
		})
	}
}

func TestAccountLoginReplacementMySQLConcurrentLoginsHaveOneWinner(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	_, initial := f.login("main", "fixture-password")
	old := &gameSession{}
	if _, err := f.service.authenticateRemoteLogin(old, initial.Token); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan accountAPIResponse, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, result := f.login("main", "fixture-password")
			if w.Code != 200 {
				t.Errorf("login status %d", w.Code)
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	f.service.accountLogins.finished(old)
	winners := 0
	for result := range results {
		if _, err := f.service.authenticateRemoteLogin(&gameSession{}, result.Token); err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("usable credentials=%d", winners)
	}
	var count int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM "+f.api.loginTokenTable()+" WHERE account_id=?", initial.AccountID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("tokens=%d err=%v", count, err)
	}
}

func TestAccountLoginReplacementMySQLWireRejectsOldAndMissingToken(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	_, old := f.login("main", "fixture-password")
	_, next := f.login("main", "fixture-password")
	// Even a stale cache entry cannot authenticate after SQL revocation.
	f.service.loginTokens.remember(old.Token, old.AccountID, time.Now().Add(time.Hour))
	for _, token := range []string{old.Token, ""} {
		session := &gameSession{accountID: "dnf:1"}
		raw, err := dnfproto.BuildChannelPacket(1, buildClientLoginBody(token), 0, dnfproto.DefaultChannelClassification)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.service.handleGameUpper(session, raw); err != errLoginReplaced || !session.loginRevoked.Load() || session.accountFromToken {
			t.Fatalf("invalid login fell through: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		session := &gameSession{}
		if !f.service.bindGameSessionAccountFromLoginToken(session, dnfproto.DefaultChannelClassification, buildClientLoginBody(next.Token)) || session.accountID != next.AccountID || session.loginRevoked.Load() {
			t.Fatal("current launch/auxiliary could not bind")
		}
	}
}

func TestAccountLoginReplacementMySQLWaitRechecksLatestToken(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	_, first := f.login("main", "fixture-password")
	old := &gameSession{}
	if _, err := f.service.authenticateRemoteLogin(old, first.Token); err != nil {
		t.Fatal(err)
	}
	_, second := f.login("main", "fixture-password")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	waiting := make(chan error, 1)
	go func() {
		_, err := f.service.authenticateRemoteLogin(&gameSession{ctx: ctx}, second.Token)
		waiting <- err
	}()
	// This third login and an unrelated account must proceed while cleanup waits.
	_, third := f.login("main", "fixture-password")
	_, other := f.login("other", "fixture-password")
	if _, err := f.service.authenticateRemoteLogin(&gameSession{}, other.Token); err != nil {
		t.Fatal(err)
	}
	f.service.accountLogins.finished(old)
	if err := <-waiting; err == nil {
		t.Fatal("superseded waiting token accepted")
	}
	if _, err := f.service.authenticateRemoteLogin(&gameSession{}, third.Token); err != nil {
		t.Fatal(err)
	}
}

func TestAccountLoginReplacementMySQLKeepsLocalAndTokenDisabledLogin(t *testing.T) {
	f := newReplacementMySQLFixture(t)
	local, err := newLoginToken()
	if err != nil {
		t.Fatal(err)
	}
	f.service.localLoginTokens = map[string]struct{}{local: {}}
	f.service.loginTokens.remember(local, "local:fixture", time.Now().Add(time.Minute))
	session := &gameSession{}
	if !f.service.bindGameSessionAccountFromLoginToken(session, dnfproto.DefaultChannelClassification, buildClientLoginBody(local)) || session.accountID != "local:fixture" || f.service.accountLogins.tracked(session) {
		t.Fatal("local token changed")
	}
	f.service.options.accountAPILoginToken = false
	if w, result := f.login("main", "fixture-password"); w.Code != 200 || result.Token != "" {
		t.Fatal("token-disabled login changed", w.Code)
	}
}
