package dnfbridge

import (
	"fmt"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPlayerAdmissionConcurrentDeviceQuota(t *testing.T) {
	var p playerAdmission
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p.mu.Lock()
			defer p.mu.Unlock()
			account := fmt.Sprint(i)
			if p.available(account, "device", 0, time.Now()) == nil {
				p.reserve(account, "device", account, time.Now())
				accepted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("admitted %d", accepted.Load())
	}
}

func TestPlayerAdmissionReplacementCountsOnlyOtherAccounts(t *testing.T) {
	var p playerAdmission
	now := time.Now()
	p.prune(now)
	p.reserve("a", "device", "old", now)
	p.reserve("b", "other-device", "other", now)
	if err := p.availableReplacement("a", "device", 2, now); err != nil {
		t.Fatal("same launch counted twice at full capacity", err)
	}
	if err := p.availableReplacement("c", "device", 0, now); err == nil {
		t.Fatal("second device account accepted")
	}
	if err := p.availableReplacement("c", "third-device", 2, now); err == nil {
		t.Fatal("third server account accepted")
	}
	if err := p.availableReplacement("a", "other-device", 0, now); err == nil {
		t.Fatal("replacement evaded destination device quota")
	}
	if p.leases["a"].token != "old" || len(p.leases) != 2 {
		t.Fatal("capacity check mutated reservations")
	}
}

func TestPlayerAdmissionHandoffAndForeignToken(t *testing.T) {
	var p playerAdmission
	now := time.Now()
	a, b, c := &gameSession{}, &gameSession{}, &gameSession{}
	if err := p.attach("a", "device", "token", a, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := p.attach("a", "device", "token", b, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := p.attach("a", "device", "foreign", c, 1, now); err == nil {
		t.Fatal("foreign token accepted")
	}
	p.detach(a, now)
	if err := p.available("other", "other", 1, now.Add(time.Minute)); err == nil {
		t.Fatal("auxiliary close released live game")
	}
	p.detach(b, now)
	if err := p.available("a", "device", 1, now.Add(time.Second)); err == nil {
		t.Fatal("no reconnect grace")
	}
	if err := p.attach("a", "device", "token", c, 1, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	p.detach(c, now)
	if err := p.available("a", "device", 1, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestPlayerTrustedIPAndStrictFallback(t *testing.T) {
	s := &Service{}
	s.options.playerStrict = true
	s.options.playerProxySecret = "secret"
	a := &accountAPIServer{service: s}
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "127.0.0.1:9999"
	r.Header.Set("X-Forwarded-For", "192.0.2.3")
	r.Header.Set("X-Player-IP", "192.0.2.4")
	if a.clientIP(r) != "" {
		t.Fatal("spoofed source accepted")
	}
	r.Header.Set("X-Player-Proxy-Key", "secret")
	if a.clientIP(r) != "192.0.2.4" {
		t.Fatal("trusted source lost")
	}
	r.Header.Set("X-Player-IP", "::ffff:192.0.2.4")
	if a.clientIP(r) != "192.0.2.4" {
		t.Fatal("mapped address evades quota")
	}
	if s.accountIDForSession(&gameSession{accountID: "stale"}) != "" {
		t.Fatal("strict fallback")
	}
	if s.handleGameCommand(&gameSession{}, 1, 13, nil) == nil {
		t.Fatal("unauthenticated legacy dispatch")
	}
	if s.authenticatePlayer(&gameSession{}, "") == nil {
		t.Fatal("missing token accepted")
	}
}

func TestPlayerAdmissionTwentySecondExpiry(t *testing.T) {
	now := time.Now()
	var p playerAdmission
	if err := p.available("a", "device", 1, now); err != nil {
		t.Fatal(err)
	}
	p.reserve("a", "device", "token", now)
	if err := p.available("a", "device", 1, now.Add(20*time.Second-time.Nanosecond)); err == nil {
		t.Fatal("reservation expired early")
	}
	if err := p.available("a", "device", 1, now.Add(20*time.Second)); err != nil {
		t.Fatal("failed launch still reserved", err)
	}
	a, b := &gameSession{}, &gameSession{}
	if err := p.attach("a", "device", "token", a, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := p.attach("a", "device", "token", b, 1, now); err != nil {
		t.Fatal(err)
	}
	p.detach(a, now)
	if err := p.available("a", "device", 1, now.Add(time.Minute)); err == nil {
		t.Fatal("active auxiliary connection expired")
	}
	p.detach(b, now.Add(time.Minute))
	if err := p.available("a", "device", 1, now.Add(80*time.Second-time.Nanosecond)); err == nil {
		t.Fatal("disconnect grace expired early")
	}
	if err := p.available("a", "device", 1, now.Add(80*time.Second)); err != nil {
		t.Fatal("disconnect grace exceeded twenty seconds", err)
	}
}
