package dnfbridge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/platform/eventloop"
)

func TestTryPostGameSessionCharacterEventReturnsQueueFullWithoutRun(t *testing.T) {
	eventsCtx, cancelEvents := context.WithCancel(context.Background())
	service := &Service{}
	session := &gameSession{
		connID:              "party-owner-queue-capacity",
		ctx:                 eventsCtx,
		selectedCharacterID: 19,
		characterGeneration: 1,
	}
	if err := service.startGameSessionEvents(eventsCtx, session); err != nil {
		cancelEvents()
		t.Fatalf("start real game-session event loop: %v", err)
	}
	post := func(source string, run func() error) error {
		return service.tryPostGameSessionCharacterEvent(
			session, source, session.selectedCharacterID, session.characterGeneration, run,
		)
	}

	gateStarted := make(chan struct{})
	releaseGate := make(chan struct{})
	gateDone := make(chan error, 1)
	t.Cleanup(func() {
		close(releaseGate)
		select {
		case err := <-gateDone:
			if err != nil {
				t.Errorf("release blocked gate: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("blocked gate did not exit during cleanup")
		}

		cancelEvents()
		stopTestGameSessionEvents(t, service, session)

		var runAfterClose atomic.Bool
		err := post("party-owner-queue-after-close", func() error { runAfterClose.Store(true); return nil })
		if !errors.Is(err, eventloop.ErrClosed) {
			t.Errorf("post after close error=%v, want=%v", err, eventloop.ErrClosed)
		}
		if runAfterClose.Load() {
			t.Error("run executed after the event loop was stopped")
		}
	})

	go func() {
		gateDone <- service.callGameSession(eventsCtx, session, "party-owner-queue-capacity-gate", func() error {
			close(gateStarted)
			select {
			case <-releaseGate:
				return nil
			case <-eventsCtx.Done():
				return eventsCtx.Err()
			}
		})
	}()
	waitTestSignal(t, gateStarted, "real session event-loop gate did not start")

	for i := 0; i < gameSessionEventQueueSize; i++ {
		if err := post("party-owner-queue-capacity-filler", func() error { return nil }); err != nil {
			t.Fatalf("fill real medium queue %d/%d: %v", i+1, gameSessionEventQueueSize, err)
		}
	}

	var overflowRan atomic.Bool
	overflowReturned := make(chan error, 1)
	go func() {
		overflowReturned <- post("party-owner-queue-overflow", func() error { overflowRan.Store(true); return nil })
	}()
	select {
	case err := <-overflowReturned:
		if !errors.Is(err, eventloop.ErrQueueFull) {
			t.Fatalf("overflow post error=%v, want=%v", err, eventloop.ErrQueueFull)
		}
	case <-time.After(time.Second):
		t.Fatal("queue-full post did not return while the gate was held")
	}
	if overflowRan.Load() {
		t.Fatal("overflow run executed despite eventloop.ErrQueueFull")
	}
}
