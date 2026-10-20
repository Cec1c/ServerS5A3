package dnfbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/localaccount"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type localCredentialFixture42 struct {
	hash            []byte
	state, username string
	found           bool
	recordErr       error
	records         int
	onRecord        func()
}

func (f *localCredentialFixture42) LoadCredential(context.Context, string) (localaccount.Credential, bool, error) {
	return localaccount.Credential{AccountID: "dnf:10029", Username: f.username, PasswordHash: append([]byte(nil), f.hash...), State: f.state}, f.found, nil
}
func (f *localCredentialFixture42) RecordLogin(context.Context, localaccount.Credential) error {
	f.records++
	if f.onRecord != nil {
		f.onRecord()
	}
	return f.recordErr
}

func localAuthFixture42(t *testing.T) (*Service, *localCredentialFixture42, repository.Group) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("offline-fixture-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal("fixture hash failed")
	}
	f := &localCredentialFixture42{hash: hash, state: "active", username: "q7a", found: true}
	group := memory.NewMemoryGroup()
	group.LocalLogin = f
	if err := group.Character.Save(context.Background(), repository.CharacterRecord{CharacterID: "31", AccountID: "dnf:10029", Name: "q7a"}); err != nil {
		t.Fatal(err)
	}
	s := &Service{repositoryProvider: func() (repository.Group, bool) { return group, true }, clientAccounts: newClientAccountRegistry()}
	s.options.currentClientVersion = "official-24.9"
	s.startLocalLoginTokens()
	return s, f, group
}
func localAuthRequest42(s *Service, payload, remote, method string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1/local/account/login", strings.NewReader(payload))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	s.LocalAccountLoginHandler(rec, req)
	return rec
}
func localAuthIssue42(t *testing.T, s *Service) localLoginResponse {
	t.Helper()
	rec := localAuthRequest42(s, `{"username":"q7a","password":"offline-fixture-password"}`, "127.0.0.1:1234", http.MethodPost)
	if rec.Code != http.StatusOK {
		t.Fatalf("offline handler status=%d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential response cacheable")
	}
	var result localLoginResponse
	if json.Unmarshal(rec.Body.Bytes(), &result) != nil {
		t.Fatal("invalid credential response")
	}
	return result
}

func TestNativeAuth42RealHandlerIssuerLoginBinderAndRaidOwner(t *testing.T) {
	s, f, group := localAuthFixture42(t)
	session := &gameSession{conn: &bufferConn{}, connID: "offline-native42", clientIP: "127.0.0.1", clientPID: 42, accountID: "dnf:1", accountFromPID: true, channel: channelcatalog.Channel{ID: 23, Type: dnfenum.RaidChannelType}, loginPacketSeen: true}
	if err := s.RegisterClientAccount(42, "dnf:1"); err != nil {
		t.Fatal(err)
	}
	if s.currentRaidAuthenticated(session) {
		t.Fatal("PID authenticated raid before token")
	}
	first := localAuthIssue42(t, s)
	second := localAuthIssue42(t, s)
	if normalizeLoginToken(first.Token) == "" || first.Token == second.Token || f.records != 2 {
		t.Fatal("issuer did not use fresh random credential tokens")
	}
	if !first.ExpiresAt.After(time.Now()) || first.ExpiresAt.After(time.Now().Add(localLoginTokenTTL+time.Second)) {
		t.Fatal("issuer expiry incorrect")
	}
	if session.accountFromToken {
		t.Fatal("issuing token alone changed game authentication")
	}
	if !s.bindGameSessionAccountFromLoginToken(session, proto.DefaultChannelClassification, buildClientLoginBody(first.Token)) {
		t.Fatal("real issued token did not bind")
	}
	if !session.accountFromToken || session.accountID != "dnf:10029" {
		t.Fatal("binder did not authenticate canonical owner")
	}
	s.bindGameSessionCharacter(session, 31)
	if !s.currentRaidAuthenticated(session) {
		t.Fatal("issued credential chain did not satisfy current raid owner gate")
	}
	c, _, _ := group.Character.Load(context.Background(), "31")
	c.AccountID = "dnf:other-owner"
	if err := group.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if s.currentRaidAuthenticated(session) {
		t.Fatal("issued token bypassed character ownership")
	}
}

func TestNativeAuth42HandlerRejectsCredentialShortcutsAndErrors(t *testing.T) {
	for _, kind := range []string{"wrong_password", "disabled", "missing", "alias", "record_failure", "receipt", "pid", "account_id", "trailing_json", "oversize", "remote", "method", "stopped"} {
		t.Run(kind, func(t *testing.T) {
			s, f, _ := localAuthFixture42(t)
			payload := `{"username":"q7a","password":"offline-fixture-password"}`
			remote, method := "127.0.0.1:1234", http.MethodPost
			switch kind {
			case "wrong_password":
				payload = `{"username":"q7a","password":"different-fixture-password"}`
			case "disabled":
				f.state = "disabled"
			case "missing":
				f.found = false
			case "alias":
				f.username = "q7á"
			case "record_failure":
				f.recordErr = errors.New("offline error")
			case "receipt":
				payload = `{"authenticated":true}`
			case "pid":
				payload = `{"pid":42,"account_id":"dnf:10029"}`
			case "account_id":
				payload = `{"username":"q7a","password":"offline-fixture-password","account_id":"dnf:10029"}`
			case "trailing_json":
				payload += "{}"
			case "oversize":
				payload = strings.Repeat(" ", 4097) + payload
			case "remote":
				remote = "192.0.2.1:1234"
			case "method":
				method = http.MethodGet
			case "stopped":
				s.stopLocalLoginTokens()
			}
			rec := localAuthRequest42(s, payload, remote, method)
			if rec.Code == http.StatusOK || len(s.localLoginTokens) != 0 {
				t.Fatal("rejected request issued token")
			}
			if strings.Contains(rec.Body.String(), "offline-fixture-password") || strings.Contains(rec.Body.String(), `"token"`) {
				t.Fatal("failure leaked credential data")
			}
		})
	}
}

func TestNativeAuth42ExpiryRestartAndConcurrentStopInvalidateTokens(t *testing.T) {
	s, f, _ := localAuthFixture42(t)
	issued := localAuthIssue42(t, s)
	if _, ok := s.loginTokens.recall(issued.Token, issued.ExpiresAt); ok {
		t.Fatal("expired token recognised")
	}
	if s.bindGameSessionAccountFromLoginToken(&gameSession{accountID: "dnf:10029", clientIP: "127.0.0.1"}, 1, buildClientLoginBody(issued.Token)) {
		t.Fatal("expired credential bound")
	}
	issued = localAuthIssue42(t, s)
	fresh, _, _ := localAuthFixture42(t)
	if fresh.bindGameSessionAccountFromLoginToken(&gameSession{accountID: "dnf:10029", clientIP: "127.0.0.1"}, 1, buildClientLoginBody(issued.Token)) {
		t.Fatal("new service recognised old in-memory token")
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.startLocalLoginTokens()
	if s.bindGameSessionAccountFromLoginToken(&gameSession{accountID: "dnf:10029", clientIP: "127.0.0.1"}, 1, buildClientLoginBody(issued.Token)) {
		t.Fatal("same service restart retained old token")
	}
	entered, released := make(chan struct{}), make(chan struct{})
	f.onRecord = func() { close(entered); <-released }
	result := make(chan error, 1)
	go func() {
		_, err := s.issueLocalLoginToken(context.Background(), f, "q7a", "offline-fixture-password", newLoginToken)
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(released)
		t.Fatal("credential verification did not reach conditional update")
	}
	s.stopLocalLoginTokens()
	s.startLocalLoginTokens()
	close(released)
	if err := <-result; err == nil {
		t.Fatal("pre-restart authentication issued into new epoch")
	}
	f.onRecord = nil
	if _, err := s.issueLocalLoginToken(context.Background(), f, "q7a", "offline-fixture-password", func() (string, error) { return "", errors.New("entropy unavailable") }); err == nil {
		t.Fatal("entropy failure issued token")
	}
	if len(s.localLoginTokens) != 0 {
		t.Fatal("failed issuer retained mapping")
	}
}
