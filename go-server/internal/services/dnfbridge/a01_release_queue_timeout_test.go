package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// Loading queue regression: an admitted release must survive a finite queue
// stall. PostFinish is deliberately pre-set by r195PrepareLoadingTail, so this
// proves real pending placement plus op37/op30 recovery, NOT the op19/peer-actor
// end-to-end lifecycle. No replacement op37 or manual second release repairs it.
func TestA01ReleaseQueueTimeoutMustRecoverAfterGate(t *testing.T) {
	// The R195 death fixture has no pending placement. Reuse the existing sync
	// entry fixture for that part, and R195's real-loop gate/tail setup below.
	s, blocked, healthy, blockedConn, healthyConn, _ := partyDungeonSyncFixture(t)
	gateStarted := make(chan struct{})
	gateRelease := make(chan struct{})
	gateDone := make(chan error, 1)
	releaseDone := make(chan struct{})
	var gateOnce sync.Once
	openGate := func() { gateOnce.Do(func() { close(gateRelease) }) }
	gateLaunched, releaseLaunched := false, false
	loopCtx, cancelLoops := context.WithCancel(context.Background())
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 20*time.Second)

	// Register before entry/startup: all fatal paths open the gate and stop the
	// loops before the fixture's packet logger cleanup. Every wait is bounded.
	t.Cleanup(func() {
		openGate()
		cancelWait()
		s.partyDungeonLoadingMu.Lock()
		for _, cycle := range s.partyDungeonLoadingCycles {
			if cycle != nil && cycle.Timer != nil {
				cycle.Timer.Stop()
				cycle.Timer = nil
			}
		}
		s.partyDungeonLoadingMu.Unlock()
		for _, session := range []*gameSession{blocked, healthy} {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := s.stopGameSessionEvents(ctx, session); err != nil {
				t.Errorf("stop session %d loop: %v", session.selectedCharacterID, err)
			}
			cancel()
		}
		cancelLoops()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if gateLaunched {
			select {
			case <-gateDone:
			case <-ctx.Done():
				t.Error("gate caller did not exit during bounded cleanup")
			}
		}
		if releaseLaunched {
			select {
			case <-releaseDone:
			case <-ctx.Done():
				t.Error("release workers did not exit during bounded cleanup")
			}
		}
	})

	runPartyDungeonSyncEntry(t, s, blocked)
	r195PrepareLoadingTail(blocked, healthy)
	for _, session := range []*gameSession{blocked, healthy} {
		if !s.partyDungeonPostStartMapPlacementPending(session) || session.postStartMapPlayerStateSent {
			t.Fatalf("fixture char=%d must have real, unsent pending placement", session.selectedCharacterID)
		}
		if gates := session.finishLoadingGateSnapshot(); gates.State || gates.Completion || !gates.PostFinish {
			t.Fatalf("unexpected R195 tail fixture gates for char=%d: %+v", session.selectedCharacterID, gates)
		}
		if err := s.startGameSessionEvents(loopCtx, session); err != nil {
			t.Fatal(err)
		}
		if session.events == nil || session.events.loop == nil {
			t.Fatal("test requires real session loops, not callGameSession's inline fallback")
		}
	}
	blockedStart, healthyStart := len(blockedConn.frames()), len(healthyConn.frames())

	// Exercise the release boundary only. Actual arrival logic freezes the
	// existing owners, marks Released and stops the entry watchdog. No dispatcher
	// override, synthetic timeout, new run, disconnect or queue saturation.
	partyID := uint16(runtimePartyStateSnapshot(blocked).PartyID)
	members := []uint16{blocked.selectedCharacterID, healthy.selectedCharacterID}
	if release, _, _, _, _ := s.partyDungeonLoadingBarrierArrive(blocked, partyID, members); release {
		t.Fatal("first arrival unexpectedly released the fixture")
	}
	release, _, _, _, cycle := s.partyDungeonLoadingBarrierArrive(healthy, partyID, members)
	if !release || cycle == nil {
		t.Fatal("second arrival did not create the release boundary")
	}
	s.partyDungeonLoadingMu.Lock()
	released, armed := cycle.Released, cycle.Timer != nil
	s.partyDungeonLoadingMu.Unlock()
	if !released || armed {
		t.Fatal("release precondition requires Released=true and stopped watchdog")
	}

	gateLaunched = true
	go func() {
		gateDone <- s.callGameSession(waitCtx, blocked, "a01-release-queue-gate", func() error {
			close(gateStarted)
			select {
			case <-gateRelease:
				return nil
			case <-waitCtx.Done():
				return waitCtx.Err()
			}
		})
	}()
	select {
	case <-gateStarted:
	case <-waitCtx.Done():
		t.Fatal("gate did not start: ", waitCtx.Err())
	}
	if queued := blocked.events.loop.QueueLengths().Medium; queued != 0 {
		t.Fatalf("gate must start with an otherwise empty medium queue, got %d", queued)
	}

	releaseLaunched = true
	go func() {
		s.releasePartyDungeonLoadingCycle(partyID, cycle, members, members, "a01_queue_timeout")
		close(releaseDone)
	}()
	// The gate owns the processor. The only producer into its otherwise empty
	// medium queue is the real release worker. This observes admission, not a
	// failed SubmitBlocking attempt on a full queue.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for blocked.events.loop.QueueLengths().Medium == 0 {
		select {
		case <-ticker.C:
		case <-waitCtx.Done():
			t.Fatal("release never entered the blocked loop queue: ", waitCtx.Err())
		}
	}

	// Start this deadline AFTER observing admission. The production release
	// context was created BEFORE admission, so its full 3s budget expires while
	// our gate remains closed. This timer creates the input condition; completion
	// is decided by same-loop sentinel callbacks, never by sleeping and assuming
	// a worker ran. A repair may finish dispatch early or wait for delivery.
	holdCtx, cancelHold := context.WithTimeout(waitCtx, createWriteTimeout)
	defer cancelHold()
	healthyState, err := a01ReleaseQueueAwaitComplete(waitCtx, s, healthy)
	if err != nil {
		t.Fatalf("healthy member must finish while peer gate is closed: state=%+v err=%v", healthyState, err)
	}
	a01ReleaseQueueAssertWire(t, healthyConn, healthyStart, "healthy before gate opens")
	if !s.partyDungeonPostStartMapPlacementPending(blocked) {
		t.Fatal("blocked member's placement was consumed outside its gated loop")
	}
	select {
	case <-holdCtx.Done():
	case <-waitCtx.Done():
		t.Fatal("test watchdog expired before gate release")
	}
	if err := waitCtx.Err(); err != nil {
		t.Fatal("test watchdog expired before gate release: ", err)
	}
	select {
	case <-releaseDone:
		t.Log("release workers returned while gate was closed (old code: queue deadline)")
	default:
		t.Log("release workers still await delivery; opening finite gate")
	}
	openGate()

	// Each observation is itself a medium-priority callGameSession event, behind
	// the admitted release. The first successful sentinel proves that original
	// event was drained. Repeated bounded sentinels also allow an implementation
	// that schedules recovery after the first sentinel. They do not run release.
	recoveryCtx, cancelRecovery := context.WithTimeout(waitCtx, 5*time.Second)
	defer cancelRecovery()
	blockedState, recoveryErr := a01ReleaseQueueAwaitComplete(recoveryCtx, s, blocked)
	if !blockedState.Observed {
		t.Fatalf("recovery sentinel never completed; this is not the expected loss-after-drain red result: %v", recoveryErr)
	}
	if recoveryErr != nil {
		t.Fatalf("admitted release was lost after finite gate opened: state=%+v err=%v; want pending=false placement=true state=true completion=true (PostFinish is fixture)\n%s",
			blockedState, recoveryErr, testPacketLogText(t, s))
	}
	a01ReleaseQueueAssertWire(t, blockedConn, blockedStart, "blocked member after recovery")
	select {
	case <-releaseDone:
	case <-waitCtx.Done():
		t.Fatal("release workers did not finish after both members recovered")
	}
}

type a01ReleaseQueueState struct {
	Observed   bool
	Pending    bool
	Placement  bool
	State      bool
	Completion bool
	PostFinish bool
}

func a01ReleaseQueueAwaitComplete(ctx context.Context, s *Service, session *gameSession) (a01ReleaseQueueState, error) {
	var last a01ReleaseQueueState
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		// Buffered result prevents a late callback from writing caller-owned state
		// after callGameSession returns because ctx expired.
		snapshot := make(chan a01ReleaseQueueState, 1)
		err := s.callGameSession(ctx, session, "a01-release-queue-sentinel", func() error {
			gates := session.finishLoadingGateSnapshot()
			snapshot <- a01ReleaseQueueState{
				Observed: true, Pending: s.partyDungeonPostStartMapPlacementPending(session),
				Placement: session.postStartMapPlayerStateSent,
				State:     gates.State, Completion: gates.Completion, PostFinish: gates.PostFinish,
			}
			return nil
		})
		if err != nil {
			return last, fmt.Errorf("same-loop sentinel: %w", err)
		}
		select {
		case last = <-snapshot:
		case <-ctx.Done():
			return last, fmt.Errorf("sentinel snapshot unavailable: %w", ctx.Err())
		}
		if !last.Pending && last.Placement && last.State && last.Completion && last.PostFinish {
			return last, nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return last, fmt.Errorf("placement/release did not recover: %w", ctx.Err())
		}
	}
}

func a01ReleaseQueueAssertWire(t *testing.T, conn *partyDungeonSyncConn, start int, phase string) {
	t.Helper()
	stream := conn.frames()
	if start < 0 || start > len(stream) {
		t.Fatalf("%s: invalid frame cursor %d/%d", phase, start, len(stream))
	}
	placement, state, completion := -1, -1, -1
	for index, rest := 0, stream[start:]; len(rest) > 0; index++ {
		packet, next := splitCurrentGameServerUpperPacketAuto(t, rest)
		if len(next) >= len(rest) {
			t.Fatalf("%s: packet parser made no progress", phase)
		}
		rest = next
		if packet.Header.Classification != 0 {
			continue
		}
		switch packet.Header.MsgID {
		case uint16(dnfenum.CmdPacketRequestBlacklist):
			if placement < 0 && bytes.Equal(packet.Body, buildCurrentSceneActorPlacementBody()) {
				placement = index
			}
		case uint16(dnfenum.CmdPacketFinishLoading):
			if state < 0 && len(packet.Body) == currentFinishLoadingCharacterStateBodySize {
				state = index
			}
		case currentIncreaseStatusResultMsgID:
			if completion < 0 && bytes.Equal(packet.Body, buildCurrentIncreaseStatusResultBody()) {
				completion = index
			}
		}
	}
	if placement < 0 || state <= placement || completion <= state {
		t.Fatalf("%s: want actual op120 -> class0/op37 -> op30; got indexes %d/%d/%d", phase, placement, state, completion)
	}
}
