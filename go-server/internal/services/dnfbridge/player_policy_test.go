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
	if accepted.Load() != 2 {
		t.Fatalf("admitted %d", accepted.Load())
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
