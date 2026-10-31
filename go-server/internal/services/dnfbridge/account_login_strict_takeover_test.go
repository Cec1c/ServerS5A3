package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const takeoverDevice = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newStrictTakeoverFixture(t *testing.T) *replacementMySQLFixture {
	t.Helper()
	f := newReplacementMySQLFixture(t)
	f.service.options.playerStrict = true
	f.service.options.playerProxySecret = "fixture-proxy"
	if err := f.api.ensurePlayerSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}

func strictTakeoverLogin(f *replacementMySQLFixture, account, password, device string) (*httptest.ResponseRecorder, accountAPIResponse) {
	body, _ := json.Marshal(accountAPIRequest{Account: account, Password: password, Device: device})
	r := httptest.NewRequest("POST", "/api/account/login", bytes.NewReader(body))
	r.Header.Set("X-Player-Proxy-Key", f.service.options.playerProxySecret)
	r.Header.Set("X-Player-IP", "192.0.2.44")
	w := httptest.NewRecorder()
	f.api.handleLogin(w, r)
	var result accountAPIResponse
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	return w, result
}

func strictTakeoverSocket(t *testing.T, f *replacementMySQLFixture, token string, bind bool) (*gameSession, net.Conn) {
	t.Helper()
	server, peer := net.Pipe()
	t.Cleanup(func() { server.Close(); peer.Close() })
	session := &gameSession{conn: server}
	if err := f.service.authenticatePlayer(session, token); err != nil {
		t.Fatal("strict admission", err)
	}
	if bind {
		if _, err := f.service.authenticateRemoteLogin(session, token); err != nil {
			t.Fatal("account binding", err)
		}
	}
	return session, peer
}

func TestStrictTakeoverClosesOldLaunchWaitsForEveryCleanup(t *testing.T) {
	f := newStrictTakeoverFixture(t)
	f.service.options.playerMaxOnline = 1
	w, first := strictTakeoverLogin(f, "main", "fixture-password", takeoverDevice)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	main, mainPeer := strictTakeoverSocket(t, f, first.Token, true)
	aux, auxPeer := strictTakeoverSocket(t, f, first.Token, true)
	// A socket may have passed device admission but not bound account state yet.
	prebind, prebindPeer := strictTakeoverSocket(t, f, first.Token, false)
	w, second := strictTakeoverLogin(f, "main", "fixture-password", takeoverDevice)
	if w.Code != 200 || second.Token == "" || second.Token == first.Token {
		t.Fatalf("strict same-account login did not replace: %d %s", w.Code, w.Body.String())
	}
	for _, peer := range []net.Conn{mainPeer, auxPeer, prebindPeer} {
		_ = peer.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
			t.Fatal("old connection remains open", err)
		}
	}
	for _, old := range []*gameSession{main, aux, prebind} {
		if !old.loginRevoked.Load() {
			t.Fatal("old connection can still authenticate")
		}
	}
	if _, found := f.service.loginTokens.recall(first.Token, time.Now()); found {
		t.Fatal("old cached token retained")
	}
	if err := f.service.authenticatePlayer(&gameSession{}, first.Token); err == nil {
		t.Fatal("old strict token accepted")
	}
	if _, err := f.service.authenticateRemoteLogin(prebind, first.Token); err == nil {
		t.Fatal("old prebind connection entered after replacement")
	}
	assertWaiting := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		next := &gameSession{ctx: ctx}
		if err := f.service.authenticatePlayer(next, second.Token); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.authenticateRemoteLogin(next, second.Token); err == nil {
			t.Fatal("new launch entered before every old save/cleanup completed")
		}
		f.service.playerAdmission.detach(next, time.Now())
	}
	assertWaiting()
	f.service.accountLogins.finished(main)
	f.service.playerAdmission.detach(main, time.Now())
	assertWaiting() // Main has saved; auxiliary still owns its cleanup fence.
	f.service.accountLogins.finished(aux)
	f.service.playerAdmission.detach(aux, time.Now())
	f.service.playerAdmission.detach(prebind, time.Now())
	for i := 0; i < 2; i++ {
		strictTakeoverSocket(t, f, second.Token, true)
	}
	lease := f.service.playerAdmission.leases[first.AccountID]
	if lease == nil || lease.token != second.Token || len(lease.connections) != 2 {
		t.Fatal("late old cleanup changed replacement lease")
	}
}

func TestStrictTakeoverDeniedLoginAndRollbackPreserveOldLaunch(t *testing.T) {
	for _, reason := range []string{"password", "device_full", "server_full", "token_rollback", "disabled", "qq_required"} {
		t.Run(reason, func(t *testing.T) {
			f := newStrictTakeoverFixture(t)
			_, first := strictTakeoverLogin(f, "main", "fixture-password", takeoverDevice)
			old, peer := strictTakeoverSocket(t, f, first.Token, true)
			device, password, want := takeoverDevice, "fixture-password", 409
			switch reason {
			case "password":
				password, want = "wrong-password", 401
			case "device_full":
				device = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
				for _, account := range []string{"other"} {
					if w, _ := strictTakeoverLogin(f, account, password, device); w.Code != 200 {
						t.Fatal(w.Code)
					}
				}
			case "server_full":
				if w, _ := strictTakeoverLogin(f, "other", password, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); w.Code != 200 {
					t.Fatal(w.Code)
				}
				f.service.options.playerMaxOnline = 1
			case "token_rollback":
				_, err := f.db.Exec("CREATE TRIGGER " + f.api.table + "_device_fault BEFORE INSERT ON " + f.api.table + "_player_tokens FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture failure'")
				if err != nil {
					t.Fatal(err)
				}
				want = 503
			case "disabled":
				if _, err := f.db.Exec("INSERT INTO " + f.api.table + "_player_accounts(account_id,disabled) VALUES('fixture:main',1)"); err != nil {
					t.Fatal(err)
				}
				want = 403
			case "qq_required":
				if _, err := f.db.Exec("UPDATE " + f.api.table + "_qq_settings SET enabled=1,allow_binding=1"); err != nil {
					t.Fatal(err)
				}
				want = 403
			}
			w, _ := strictTakeoverLogin(f, "main", password, device)
			if w.Code != want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
			}
			if old.loginRevoked.Load() || f.service.playerAdmission.leases[first.AccountID].token != first.Token {
				t.Fatal("rejected login revoked current launch")
			}
			var count int
			if err := f.db.QueryRow("SELECT COUNT(*) FROM "+f.api.loginTokenTable()+" WHERE token=?", first.Token).Scan(&count); err != nil || count != 1 {
				t.Fatal("old persisted token lost", count, err)
			}
			_ = peer.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
			_, err := peer.Read(make([]byte, 1))
			if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
				t.Fatal("rejected login closed old connection", err)
			}
		})
	}
}

func TestStrictTakeoverConcurrentLoginsKeepLatestAndCleanupFence(t *testing.T) {
	f := newStrictTakeoverFixture(t)
	_, initial := strictTakeoverLogin(f, "main", "fixture-password", takeoverDevice)
	old, _ := strictTakeoverSocket(t, f, initial.Token, true)
	_, other := strictTakeoverLogin(f, "other", "fixture-password", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	otherSession, _ := strictTakeoverSocket(t, f, other.Token, true)
	f.service.options.playerMaxOnline = 2
	results := make(chan accountAPIResponse, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, result := strictTakeoverLogin(f, "main", "fixture-password", takeoverDevice)
			if w.Code != 200 {
				t.Errorf("concurrent takeover status=%d", w.Code)
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	if otherSession.loginRevoked.Load() {
		t.Fatal("other account was retired")
	}
	strictTakeoverSocket(t, f, other.Token, true)
	f.service.accountLogins.mu.Lock()
	retirement := f.service.accountLogins.retiring[initial.AccountID]
	f.service.accountLogins.mu.Unlock()
	if retirement == nil || len(retirement.sessions) != 1 {
		t.Fatal("successive logins lost old cleanup fence")
	}
	f.service.accountLogins.finished(old)
	f.service.playerAdmission.detach(old, time.Now())
	winners := 0
	for result := range results {
		session := &gameSession{}
		if err := f.service.authenticatePlayer(session, result.Token); err != nil {
			continue
		}
		if _, err := f.service.authenticateRemoteLogin(session, result.Token); err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatal("usable strict tokens", winners)
	}
	var count int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM "+f.api.loginTokenTable()+" WHERE account_id=?", initial.AccountID).Scan(&count); err != nil || count != 1 {
		t.Fatal("persisted winners", count, err)
	}
}

func TestStrictTakeoverWaitingTokenIsSuperseded(t *testing.T) {
	f := newStrictTakeoverFixture(t)
	_, first := strictTakeoverLogin(f, "main", "fixture-password", takeoverDevice)
	old, _ := strictTakeoverSocket(t, f, first.Token, true)
	_, second := strictTakeoverLogin(f, "main", "fixture-password", takeoverDevice)
	waitingSession, waitingPeer := strictTakeoverSocket(t, f, second.Token, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	waitingSession.ctx = ctx
	done := make(chan error, 1)
	go func() { _, err := f.service.authenticateRemoteLogin(waitingSession, second.Token); done <- err }()
	w, third := strictTakeoverLogin(f, "main", "fixture-password", fmt.Sprintf("%032x", 3))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	_ = waitingPeer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := waitingPeer.Read(make([]byte, 1)); err != io.EOF {
		t.Fatal("intermediate launch connection stayed open", err)
	}
	f.service.accountLogins.finished(old)
	if err := <-done; err == nil {
		t.Fatal("superseded waiting launch entered")
	}
	strictTakeoverSocket(t, f, third.Token, true)
}
