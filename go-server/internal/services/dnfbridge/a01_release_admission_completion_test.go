package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/platform/eventloop"
)

// An accepted event may be skipped before task.run, after its original worker
// has already returned. Its coalescing record must then retire as well. Both
// initial admission and a successful capacity retry use the real session loop.
// PostFinish remains preset; no op19, full peer actor or GUI claim.
func TestA01ReleaseAdmissionSkippedCompletionRetires(t *testing.T) {
	for _, retry := range []bool{false, true} {
		name := "initial_admission_after_wait"
		if retry {
			name = "retry_admission_without_waiter"
		}
		t.Run(name, func(t *testing.T) { a01AdmissionSkippedCompletion(t, retry) })
	}
}

func a01AdmissionSkippedCompletion(t *testing.T, retry bool) {
	s, session, peer, conn, peerConn, _ := partyDungeonSyncFixture(t)
	loopCtx, cancelLoop := context.WithCancel(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	gateOpen, gateEntered, gateDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	secondOpen, secondEntered := make(chan struct{}), make(chan struct{})
	var firstOnce, secondOnce sync.Once
	openFirst := func() { firstOnce.Do(func() { close(gateOpen) }) }
	openSecond := func() { secondOnce.Do(func() { close(secondOpen) }) }
	releaseDone := make(chan struct{})
	gateStarted, releaseStarted := false, false
	var fillers atomic.Int64
	t.Cleanup(func() {
		openFirst()
		openSecond()
		cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if gateStarted {
			select {
			case err := <-gateDone:
				if err != nil {
					t.Errorf("gate: %v", err)
				}
			case <-cleanupCtx.Done():
				t.Error("gate cleanup timed out")
			}
		}
		if releaseStarted {
			select {
			case <-releaseDone:
			case <-cleanupCtx.Done():
				t.Error("release cleanup timed out")
			}
		}
		if err := s.stopGameSessionEvents(cleanupCtx, session); err != nil {
			t.Errorf("stop loop: %v", err)
		}
		cancelLoop()
		cancel()
		s.partyDungeonLoadingMu.Lock()
		for _, cycle := range s.partyDungeonLoadingCycles {
			if cycle.Timer != nil {
				cycle.Timer.Stop()
				cycle.Timer = nil
			}
		}
		s.partyDungeonLoadingMu.Unlock()
	})
	runPartyDungeonSyncEntry(t, s, session)
	r195PrepareLoadingTail(session, peer)
	before, peerBefore := conn.frames(), peerConn.frames()
	beforeGates := session.finishLoadingGateSnapshot()
	session.pendingPostStartMapMu.Lock()
	pending := session.pendingPostStartMapPlacement
	session.pendingPostStartMapMu.Unlock()
	if pending == nil || session.postStartMapPlayerStateSent {
		t.Fatal("missing entry-owned pending placement")
	}
	partyID := uint16(runtimePartyStateSnapshot(session).PartyID)
	members := []uint16{session.selectedCharacterID, peer.selectedCharacterID}
	s.partyDungeonLoadingBarrierArrive(session, partyID, members)
	released, _, _, _, cycle := s.partyDungeonLoadingBarrierArrive(peer, partyID, members)
	if !released || cycle == nil {
		t.Fatal("cycle was not released")
	}
	session.ctx = loopCtx
	if err := s.startGameSessionEvents(loopCtx, session); err != nil {
		t.Fatal(err)
	}
	gateStarted = true
	go func() {
		gateDone <- s.callGameSession(ctx, session, "a01-completion-first-gate", func() error {
			close(gateEntered)
			select {
			case <-gateOpen:
			case <-ctx.Done():
				return ctx.Err()
			}
			if !retry {
				s.beginGameSessionClose(session)
			}
			return nil
		})
	}()
	select {
	case <-gateEntered:
	case <-ctx.Done():
		t.Fatal("first gate did not enter")
	}
	loop := session.events.loop
	if retry {
		for i := 0; i < gameSessionEventQueueSize; i++ {
			first := i == 0
			err := loop.Submit(eventloop.Event{Type: "a01-completion-filler", Priority: eventloop.PriorityMedium, Context: loopCtx,
				Data: gameSessionEventTask{source: "a01-completion-filler", run: func() error {
					if first {
						// Drain exactly one queue slot, then hold processing again.
						// The retry can enter that slot but cannot execute yet.
						close(secondEntered)
						select {
						case <-secondOpen:
						case <-ctx.Done():
							return ctx.Err()
						}
						s.beginGameSessionClose(session)
					}
					fillers.Add(1)
					return nil
				}}})
			if err != nil {
				t.Fatal(err)
			}
		}
		if loop.QueueLengths().Medium != 512 {
			t.Fatal("real queue was not filled")
		}
	}
	const trigger = "a01_skipped_completion"
	releaseStarted = true
	go func() {
		one := []uint16{session.selectedCharacterID}
		s.releasePartyDungeonLoadingCycle(partyID, cycle, one, one, trigger)
		close(releaseDone)
	}()
	select {
	case <-releaseDone:
	case <-ctx.Done():
		t.Fatal("initial worker did not return")
	}
	charField := fmt.Sprintf("char_id=%d", session.selectedCharacterID)
	log := testPacketLogText(t, s)
	if retry {
		if a01AdmissionLogCount(log, "kind=game-party-dungeon-loading-release-queue-failed", charField, "trigger="+trigger) != 1 || loop.MetricsSnapshot().Submitted[eventloop.PriorityMedium] != 513 {
			t.Fatal("first attempt was not rejected by full capacity")
		}
		openFirst()
		select {
		case <-secondEntered:
		case <-ctx.Done():
			t.Fatal("second gate did not enter")
		}
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for loop.MetricsSnapshot().Submitted[eventloop.PriorityMedium] != 514 {
			select {
			case <-tick.C:
			case <-ctx.Done():
				t.Fatal("capacity retry was not accepted while second gate held")
			}
		}
		if fillers.Load() != 0 || loop.QueueLengths().Medium != 512 {
			t.Fatal("retry did not occupy the single freed slot")
		}
		openSecond()
	} else {
		if a01AdmissionLogCount(log, "kind=game-party-dungeon-loading-release-wait-ended", charField, "trigger="+trigger) != 1 || loop.MetricsSnapshot().Submitted[eventloop.PriorityMedium] != 2 {
			t.Fatal("expected accepted release with expired worker wait")
		}
		openFirst()
	}
	// This synchronous sentinel still runs after begin_close. All earlier medium
	// work, including the deliberately skipped async release, is before it.
	state, err := a01AdmissionObserve(ctx, s, session)
	if err != nil || !state.Observed {
		t.Fatalf("drain sentinel: %+v %v", state, err)
	}
	if retry && fillers.Load() != 512 {
		t.Fatalf("filler drain=%d", fillers.Load())
	}
	if session.events.active.Load() || loopCtx.Err() != nil {
		t.Fatal("close must disable async work without canceling event context")
	}
	window := time.NewTimer(time.Second)
	defer window.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		s.partyDungeonLoadingMu.Lock()
		remaining := len(cycle.admissions)
		s.partyDungeonLoadingMu.Unlock()
		if remaining == 0 {
			break
		}
		select {
		case <-tick.C:
		case <-window.C:
			t.Fatalf("completed/discarded release retained admission: %d", remaining)
		case <-ctx.Done():
			t.Fatal("completion observation watchdog")
		}
	}
	session.pendingPostStartMapMu.Lock()
	retained := session.pendingPostStartMapPlacement
	session.pendingPostStartMapMu.Unlock()
	if retained != pending || session.finishLoadingGateSnapshot() != beforeGates || session.postStartMapPlayerStateSent || !bytes.Equal(conn.frames(), before) || !bytes.Equal(peerConn.frames(), peerBefore) {
		t.Fatal("skipped release consumed pending state or wrote packets")
	}
	metrics := loop.MetricsSnapshot()
	if metrics.Failed[eventloop.PriorityMedium] != 0 || metrics.Panicked[eventloop.PriorityMedium] != 0 || metrics.CallbackDropped != 0 {
		t.Fatalf("processing error: %+v", metrics)
	}
}
