package dnfbridge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestAccountLoginReplacementDrainsInflightWorkAndDropsQueuedWork(t *testing.T) {
	s := &Service{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	old := &gameSession{connID: "replacement-old", ctx: ctx}
	s.registerLiveGameSession(old)
	if err := s.startGameSessionEvents(ctx, old); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, old)
	s.accountLogins.sessions = map[*gameSession]string{old: "a"}
	started, release := make(chan struct{}), make(chan struct{})
	packetDone := make(chan error, 1)
	go func() {
		packetDone <- s.callGameSession(ctx, old, "game-stream-packet", func() error { close(started); <-release; return nil })
	}()
	waitTestSignal(t, started, "inflight packet not started")
	var queuedRan atomic.Bool
	if err := s.postGameSessionCharacterEvent(old, "old-timer", 0, 0, func() error { queuedRan.Store(true); return nil }); err != nil {
		t.Fatal(err)
	}
	s.accountLogins.mu.Lock()
	s.accountLogins.replace("a")
	retired := s.accountLogins.retiring["a"].done
	s.accountLogins.mu.Unlock()
	closed := make(chan struct{})
	go func() { s.shutdownGameSessionEvents(old, false); close(closed) }()
	select {
	case <-retired:
		t.Fatal("new login released during old mutation")
	default:
	}
	close(release)
	if err := <-packetDone; err != nil {
		t.Fatal(err)
	}
	waitTestSignal(t, closed, "old shutdown did not finish")
	select {
	case <-retired:
	default:
		t.Fatal("shutdown did not release new login")
	}
	if queuedRan.Load() {
		t.Fatal("retired timer mutated state")
	}
	if s.accountLogins.tracked(old) {
		t.Fatal("finished session retained")
	}
}

func TestAccountLoginReplacementRejectsQueuedSynchronousRequest(t *testing.T) {
	s := &Service{}
	ctx := context.Background()
	old := &gameSession{connID: "replacement-fenced", ctx: ctx}
	if err := s.startGameSessionEvents(ctx, old); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, old)
	started, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	inflight := make(chan error, 1)
	go func() {
		inflight <- s.callGameSession(ctx, old, "inflight", func() error { close(started); <-release; return nil })
	}()
	waitTestSignal(t, started, "owner not started")
	var ran atomic.Bool
	queued := make(chan error, 1)
	go func() {
		queued <- s.callGameSession(ctx, old, "queued", func() error { ran.Store(true); return nil })
	}()
	deadline := time.Now().Add(time.Second)
	for old.events.loop.QueueLengths().Medium == 0 {
		if time.Now().After(deadline) {
			t.Fatal("request did not queue")
		}
		time.Sleep(time.Millisecond)
	}
	old.loginRevoked.Store(true)
	close(release)
	if err := <-inflight; err != nil {
		t.Fatal(err)
	}
	if err := <-queued; !errors.Is(err, errLoginReplaced) || ran.Load() {
		t.Fatalf("retired queued dispatch err=%v ran=%v", err, ran.Load())
	}
	if err := s.callGameSession(ctx, old, "new-request", func() error { ran.Store(true); return nil }); !errors.Is(err, errLoginReplaced) || ran.Load() {
		t.Fatalf("retired new dispatch err=%v ran=%v", err, ran.Load())
	}
}

func TestAccountLoginReplacementEventuallyFinishesSlowShutdown(t *testing.T) {
	s := &Service{}
	ctx := context.Background()
	old := &gameSession{connID: "replacement-slow", ctx: ctx}
	s.registerLiveGameSession(old)
	if err := s.startGameSessionEvents(ctx, old); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, old)
	s.accountLogins.sessions = map[*gameSession]string{old: "a"}
	started, release := make(chan struct{}), make(chan struct{})
	packetDone := make(chan error, 1)
	go func() {
		packetDone <- s.callGameSession(ctx, old, "game-stream-packet", func() error { close(started); <-release; return nil })
	}()
	waitTestSignal(t, started, "old mutation not started")
	s.accountLogins.mu.Lock()
	s.accountLogins.replace("a")
	done := s.accountLogins.retiring["a"].done
	s.accountLogins.mu.Unlock()
	s.shutdownGameSessionEvents(old, false)
	select {
	case <-done:
		t.Fatal("timeout released an in-flight mutation")
	default:
	}
	close(release)
	<-packetDone
	waitTestSignal(t, done, "completed slow cleanup stayed fenced")
	s.wg.Wait()
	if s.accountLogins.tracked(old) {
		t.Fatal("finished session retained")
	}
}

func TestAccountLoginReplacementTracksAllSocketsUntilCleanup(t *testing.T) {
	main, aux, unrelated := &gameSession{}, &gameSession{}, &gameSession{}
	p := accountLoginSessions{sessions: map[*gameSession]string{main: "a", aux: "a", unrelated: "b"}}
	if got := p.replace("a"); len(got) != 2 || unrelated.loginRevoked.Load() {
		t.Fatal("wrong sessions replaced")
	}
	done := p.retiring["a"].done
	p.finished(main)
	if len(p.replace("a")) != 1 || p.retiring["a"].done != done {
		t.Fatal("successive replacement lost pending cleanup")
	}
	select {
	case <-done:
		t.Fatal("released before auxiliary cleanup")
	default:
	}
	p.finished(aux)
	select {
	case <-done:
	default:
		t.Fatal("cleanup did not release fence")
	}
	next := &gameSession{}
	p.sessions[next] = "a"
	p.finished(main) // Late duplicate cleanup must not remove the new session.
	if !p.tracked(next) {
		t.Fatal("late cleanup removed new session")
	}
}
