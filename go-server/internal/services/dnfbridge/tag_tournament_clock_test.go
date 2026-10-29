package dnfbridge

import (
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/platform/eventloop"
)

func TestTagTournamentClockUsesSharedQueueAndStopsWithService(t *testing.T) {
	q := newFakeCurrentDungeonDeathTimerQueue()
	s := &Service{options: options{currentClientVersion: "official-24.9"}, gameplayTimers: q}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.startCurrentTagTournamentClock(ctx)
	first := q.task(t, 0)
	if first.name != currentTagTournamentClockName || first.delay != time.Minute {
		t.Fatal(first)
	}
	q.fire(first, false)
	if count, _, _ := q.counts(); count != 2 {
		t.Fatal("没有续调")
	}
	cancel()
	q.fire(q.task(t, 1), false)
	if count, _, _ := q.counts(); count != 2 {
		t.Fatal("停服后仍续调")
	}
	legacy := &Service{gameplayTimers: q}
	legacy.startCurrentTagTournamentClock(context.Background())
	if count, _, _ := q.counts(); count != 2 {
		t.Fatal("旧配套启动帝国时钟")
	}
}

func TestTagTournamentClockFullSessionQueueCannotBlockSharedClock(t *testing.T) {
	s, p, _ := newTagRosterBridgeTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.startGameSessionEvents(ctx, p); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	defer func() {
		close(release)
		stop, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		if err := s.stopGameSessionEvents(stop, p); err != nil {
			t.Error(err)
		}
	}()
	if err := s.postGameSessionCharacterEvent(p, "隔离阻塞夹具", 29, p.characterGeneration, func() error { close(started); <-release; return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("夹具未启动")
	}
	for i := 0; i < gameSessionEventQueueSize; i++ {
		if err := p.events.loop.Submit(eventloop.Event{Type: "隔离满队列夹具", Priority: eventloop.PriorityLow, Data: gameSessionEventTask{run: func() error { return nil }}}); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() { s.queueCurrentTagTournamentLobbyRefresh(time.Now()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("满队列阻塞了共享调度")
	}
	if p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("队列满时绕过会话循环发包")
	}
}

func TestTagTournamentQueuedRefreshCannotReachNewCharacterGeneration(t *testing.T) {
	s, p, _ := newTagRosterBridgeTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.startGameSessionEvents(ctx, p); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		if err := s.stopGameSessionEvents(stop, p); err != nil {
			t.Error(err)
		}
	}()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	if err := s.postGameSessionCharacterEvent(p, "隔离世代夹具", 29, p.characterGeneration, func() error { close(started); <-release; return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("夹具未启动")
	}
	s.queueCurrentTagTournamentLobbyRefresh(time.Now())
	s.mu.Lock()
	p.characterGeneration++
	s.mu.Unlock()
	if err := p.events.loop.Submit(eventloop.Event{Type: "隔离末尾夹具", Priority: eventloop.PriorityLow, Data: gameSessionEventTask{run: func() error { close(done); return nil }}}); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("旧世代回调未结束")
	}
	if p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("旧世代刷新写入了新角色")
	}
}
