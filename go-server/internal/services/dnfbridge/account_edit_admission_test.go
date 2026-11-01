package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adminapi"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type admissionFenceFixture struct {
	repository.AccountRepository
	wait func(context.Context, string) error
}

func TestGameAdminAccountEditStatus(t *testing.T) {
	s := admissionService(admissionFenceFixture{wait: func(context.Context, string) error { return nil }})
	s.options.playerAdminSecret = strings.Repeat("x", 32)
	api := &accountAPIServer{service: s}
	call := func(body, token string) (int, adminapi.Response) {
		r := httptest.NewRequest("POST", adminapi.Path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.handleGameAdmin(w, r)
		var out adminapi.Response
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	body := `{"version":1,"action":"account_edit_status","accountId":"a"}`
	if code, _ := call(body, ""); code != 404 {
		t.Fatal("missing credentials accepted", code)
	}
	for _, invalid := range []string{
		`{"version":1,"action":"account_edit_status"}`,
		`{"version":1,"action":"account_edit_status","accountId":" a "}`,
		`{"version":1,"action":"account_edit_status","accountId":"a","characterId":10}`,
	} {
		if _, out := call(invalid, s.options.playerAdminSecret); out.OK || out.AccountEdit != nil {
			t.Fatal("invalid request accepted", out)
		}
	}
	_, out := call(body, s.options.playerAdminSecret)
	if !out.OK || out.AccountEdit == nil || out.AccountEdit.Protocol != adminapi.AccountEditProtocol || out.AccountEdit.AccountID != "a" || out.AccountEdit.Busy {
		t.Fatal(out)
	}
	if err := s.admitAccountForEdits(&gameSession{}, "a"); err != nil {
		t.Fatal(err)
	}
	_, out = call(body, s.options.playerAdminSecret)
	if !out.OK || out.AccountEdit == nil || !out.AccountEdit.Busy {
		t.Fatal(out)
	}
	s.options.playerStrict = false
	_, out = call(body, s.options.playerAdminSecret)
	if out.OK || out.AccountEdit != nil {
		t.Fatal("unsupported profile accepted", out)
	}
}

func (f admissionFenceFixture) WaitForAccountEdits(ctx context.Context, account string) error {
	return f.wait(ctx, account)
}

func admissionService(f admissionFenceFixture) *Service {
	g := memory.NewMemoryGroup()
	g.Account = f
	return &Service{options: options{playerStrict: true}, repositoryProvider: func() (repository.Group, bool) { return g, true }}
}

func assertAccountBusy(t *testing.T, s *Service, account string, busy bool) {
	t.Helper()
	v, err := s.administrativeAccountEditStatus(account)
	if err != nil || v.Busy != busy {
		t.Fatalf("account busy=%v expected=%v error=%v", v, busy, err)
	}
}

func TestAccountEditAdmissionWaitAndCleanup(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s := admissionService(admissionFenceFixture{wait: func(ctx context.Context, a string) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	session := &gameSession{ctx: context.Background(), connID: "admission"}
	done := make(chan error, 1)
	go func() { done <- s.admitAccountForEdits(session, "a") }()
	<-entered
	assertAccountBusy(t, s, "a", true)
	assertAccountBusy(t, s, "b", false)
	select {
	case <-done:
		t.Fatal("login crossed uncommitted edit")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// A completed DB wait leaves the active session registered; character
	// unbinding / auxiliary transport removal must not remove that marker.
	s.unregisterLiveGameSession(session)
	assertAccountBusy(t, s, "a", true)
	s.shutdownGameSessionEvents(session, false)
	assertAccountBusy(t, s, "a", false)
}

func TestAccountEditAdmissionMultipleTransports(t *testing.T) {
	s := admissionService(admissionFenceFixture{wait: func(context.Context, string) error { return nil }})
	a, b := &gameSession{}, &gameSession{}
	if err := s.admitAccountForEdits(a, "account"); err != nil {
		t.Fatal(err)
	}
	if err := s.admitAccountForEdits(b, "account"); err != nil {
		t.Fatal(err)
	}
	s.shutdownGameSessionEvents(a, false)
	assertAccountBusy(t, s, "account", true)
	s.shutdownGameSessionEvents(b, false)
	assertAccountBusy(t, s, "account", false)
}

func TestAccountEditAdmissionFailureDoesNotBindToken(t *testing.T) {
	g := memory.NewMemoryGroup()
	s := admissionService(admissionFenceFixture{AccountRepository: g.Account, wait: func(context.Context, string) error { return context.DeadlineExceeded }})
	const token = "de509f65000000000000000000000000"
	s.loginTokens.remember(token, "a", time.Now().Add(time.Hour))
	body := make([]byte, 594)
	binary.LittleEndian.PutUint32(body, 1)
	body[4] = 0x30
	binary.LittleEndian.PutUint32(body[5:9], 32)
	copy(body[9:], token)
	session := &gameSession{conn: &bufferConn{}}
	if s.bindGameSessionAccountFromLoginToken(session, 1, body) || session.accountFromToken || session.accountID != "" {
		t.Fatal("failed fence authenticated player")
	}
	if _, found, err := g.Account.Load(context.Background(), "a"); err != nil || found {
		t.Fatal("account bootstrap ran before fence", err)
	}
	assertAccountBusy(t, s, "a", true)
	s.shutdownGameSessionEvents(session, false)
	assertAccountBusy(t, s, "a", false)
}

func TestAccountEditAdmissionCancellationAndCapability(t *testing.T) {
	s := admissionService(admissionFenceFixture{wait: func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() }})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	session := &gameSession{ctx: ctx}
	if err := s.admitAccountForEdits(session, "a"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	assertAccountBusy(t, s, "a", true)
	s.options.playerStrict = false
	if _, err := s.administrativeAccountEditStatus("a"); err == nil {
		t.Fatal("legacy profile advertised admission protection")
	}
	s.options.playerStrict = true
	s.repositoryProvider = nil
	if _, err := s.administrativeAccountEditStatus("a"); err == nil {
		t.Fatal("missing database fence advertised protection")
	}
}

func TestAccountEditAdmissionRetainedDuringSlowShutdown(t *testing.T) {
	s := admissionService(admissionFenceFixture{wait: func(context.Context, string) error { return nil }})
	session := &gameSession{connID: "slow-close"}
	if err := s.startGameSessionEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if err := s.admitAccountForEdits(session, "a"); err != nil {
		t.Fatal(err)
	}
	entered, release, completed := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		completed <- s.callGameSession(context.Background(), session, "held-save", func() error { close(entered); <-release; return nil })
	}()
	<-entered
	s.shutdownGameSessionEvents(session, false)
	assertAccountBusy(t, s, "a", true)
	close(release)
	<-completed
	// A subsequent confirmed stop/cleanup can retire the held admission.
	s.shutdownGameSessionEvents(session, false)
	assertAccountBusy(t, s, "a", false)
}
