package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestDirectLoginGMProtectionWaitsAndTracksOccupancy(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s := admissionService(admissionFenceFixture{wait: func(ctx context.Context, account string) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	s.options.playerStrict = false
	s.options.gmEditProtection = true
	session := &gameSession{ctx: context.Background()}
	finished := make(chan error, 1)
	go func() { finished <- s.admitAccountForEdits(session, "a") }()
	<-entered
	assertAccountBusy(t, s, "a", true)
	select {
	case err := <-finished:
		t.Fatalf("login passed the pending GM transaction: %v", err)
	default:
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	assertAccountBusy(t, s, "a", true)
	s.releaseAccountEditAdmission(session)
	assertAccountBusy(t, s, "a", false)
}

func TestDirectLoginGMCharacterCancellationRemainsOccupied(t *testing.T) {
	s := admissionService(admissionFenceFixture{wait: func(context.Context, string) error { return context.Canceled }})
	s.options.playerStrict = false
	s.options.gmEditProtection = true
	session := &gameSession{}
	if err := s.admitCharacterForEdits(context.Background(), session, "a", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected rejected character admission, got %v", err)
	}
	state, err := s.administrativeCharacterEditStatus("a", 10)
	if err != nil || !state.Busy {
		t.Fatalf("failed admission lost occupancy: %v %v", state, err)
	}
	s.releaseCharacterEditAdmission(session)
	state, err = s.administrativeCharacterEditStatus("a", 10)
	if err != nil || state.Busy {
		t.Fatalf("explicit cleanup did not release occupancy: %v %v", state, err)
	}
	s.repositoryProvider = nil
	if err := s.admitAccountForEdits(session, "a"); err == nil {
		t.Fatal("missing repository fence admitted a login")
	}
}

func TestDirectLoginGMProtectionDefaultsOff(t *testing.T) {
	s := &Service{}
	if s.gmEditProtectionEnabled() {
		t.Fatal("direct-login behavior changed without opt-in")
	}
	if err := s.admitAccountForEdits(&gameSession{}, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.administrativeAccountEditStatus("a"); err == nil {
		t.Fatal("disabled protection advertised safe GM editing")
	}
}

func TestDirectLoginGMProtectionRejectsSharedFallback(t *testing.T) {
	s := &Service{options: options{gmEditProtection: true, accountID: "shared"}}
	if got := s.accountIDForSession(&gameSession{accountID: "unverified"}); got != "" {
		t.Fatalf("unverified account escaped edit admission: %q", got)
	}
	if got := s.accountIDForSession(&gameSession{accountID: "verified", accountFromToken: true}); got != "verified" {
		t.Fatalf("authenticated direct login lost account: %q", got)
	}
	if err := s.handleGameCommandRequest(&gameSession{}, gameplayRequest{}); !errors.Is(err, errPlayerAuthentication) {
		t.Fatalf("unbound gameplay was not rejected: %v", err)
	}
	if err := s.startAccountAPI(context.Background()); err == nil {
		t.Fatal("GM protection started with login tokens disabled")
	}
}

func TestDirectLoginGMTokenBindingRequiresCompletedFence(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		g := memory.NewMemoryGroup()
		s := admissionService(admissionFenceFixture{AccountRepository: g.Account, wait: func(context.Context, string) error {
			if blocked {
				return context.DeadlineExceeded
			}
			return nil
		}})
		s.options.playerStrict = false
		s.options.gmEditProtection = true
		const token = "de509f65000000000000000000000000"
		s.loginTokens.remember(token, "a", time.Now().Add(time.Hour))
		body := make([]byte, 594)
		binary.LittleEndian.PutUint32(body, 1)
		body[4] = 0x30
		binary.LittleEndian.PutUint32(body[5:9], 32)
		copy(body[9:], token)
		session := &gameSession{conn: &bufferConn{}}
		s.bindGameSessionAccountFromLoginToken(session, 1, body)
		if session.accountFromToken == blocked {
			t.Fatalf("direct login bound=%v with blocked=%v", session.accountFromToken, blocked)
		}
		assertAccountBusy(t, s, "a", true)
		s.shutdownGameSessionEvents(session, false)
		assertAccountBusy(t, s, "a", false)
	}
}
