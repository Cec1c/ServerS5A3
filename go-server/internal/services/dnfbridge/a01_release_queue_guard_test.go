package dnfbridge

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/platform/eventloop"
)

// Independent of a01_release_queue_timeout_test.go and any
// old-dungeon overlay. Dependencies already in S: partyDungeonDeathFixture,
// r195PrepareLoadingTail, r195RegisterLoadingCycle, newPeerEndpointTestLogger,
// testPacketLogText, bufferConn. No product stubs or global dispatcher changes.
//
// Run: -run '^TestA01QueuedLoadingReleaseGuard$' -count=1 -timeout 60s
// Four subcases invalidate an already-admitted event INSIDE its real gate
// callback. PostFinish is fixture state: these are suppression/ownership tests,
// not proof of an actual op19/peer-actor/GUI lifecycle.
func TestA01QueuedLoadingReleaseGuard(t *testing.T) {
	for _, kind := range []string{"run_token", "cycle", "generation", "begin_close"} {
		t.Run(kind, func(t *testing.T) {
			a01QueuedLoadingReleaseGuardCase(t, kind)
		})
	}
}

func a01QueuedLoadingReleaseGuardCase(t *testing.T, kind string) {
	t.Helper()
	s, session, peer, runtime, _ := partyDungeonDeathFixture(t)
	logger, _ := newPeerEndpointTestLogger(t)
	s.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	r195PrepareLoadingTail(session)
	conn, peerConn := session.conn.(*bufferConn), peer.conn.(*bufferConn)
	beforeBytes, beforePeerBytes := conn.write.Len(), peerConn.write.Len()
	beforeGates := session.finishLoadingGateSnapshot()
	beforePlacement := session.postStartMapPlayerStateSent

	// R195's fixture has a real runtime but no pending placement. Plant a pending
	// record for that exact runtime, without invoking placement: any consumption
	// by the invalidated task is observable even if a later packet send fails.
	pending := &pendingPostStartMapPlacement{Runtime: runtime, Source: "a01_guard_fixture"}
	session.pendingPostStartMapMu.Lock()
	session.pendingPostStartMapPlacement = pending
	session.pendingPostStartMapMu.Unlock()
	partyID := uint16(runtimePartyStateSnapshot(session).PartyID)
	cycle := r195RegisterLoadingCycle(t, s, partyID, session)
	s.partyDungeonLoadingMu.Lock()
	cycle.Released = true
	cycle.released = time.Now()
	s.partyDungeonLoadingMu.Unlock()
	beforeOwner := snapshotPartyDungeonLoadingOwner(session)

	gateEntered := make(chan struct{})
	gateRelease := make(chan struct{})
	mutated := make(chan struct{})
	gateDone := make(chan error, 1)
	releaseDone := make(chan struct{})
	var once sync.Once
	openGate := func() { once.Do(func() { close(gateRelease) }) }
	gateLaunched, releaseLaunched := false, false
	loopCtx, cancelLoop := context.WithCancel(context.Background())
	controlCtx, cancelControl := context.WithTimeout(context.Background(), 10*time.Second)
	// Keep the session context alive even in begin_close: that case must be
	// suppressed by the active guard, not by event.Context cancellation.
	session.ctx = loopCtx
	t.Cleanup(func() {
		openGate()
		cancelControl()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.stopGameSessionEvents(ctx, session); err != nil {
			t.Errorf("stop guard loop: %v", err)
		}
		cancelLoop()
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
				t.Error("release worker did not exit during bounded cleanup")
			}
		}
	})
	if err := s.startGameSessionEvents(loopCtx, session); err != nil {
		t.Fatal(err)
	}
	if session.events == nil || session.events.loop == nil {
		t.Fatal("guard regression requires a real loop")
	}
	identity, current := s.boundGameSessionCharacterSnapshot(session)
	if !current || identity.generation == 0 {
		t.Fatal("fixture must have a current nonzero binding generation")
	}

	gateLaunched = true
	go func() {
		gateDone <- s.callGameSession(controlCtx, session, "a01-queued-guard-gate", func() error {
			close(gateEntered)
			select {
			case <-gateRelease:
			case <-controlCtx.Done():
				return controlCtx.Err()
			}
			// All lifecycle mutations execute on this session's event loop, after
			// release admission, and use the same writer locks as the v2 guards.
			switch kind {
			case "run_token":
				session.dungeon.mu.Lock()
				session.dungeon.runToken++
				session.dungeon.mu.Unlock()
			case "cycle":
				s.partyDungeonLoadingMu.Lock()
				s.partyDungeonLoadingCycles[partyID] = &partyDungeonLoadingCycle{
					PartyID: partyID, Cycle: cycle.Cycle + 1, Released: true,
					Ready: cycle.Ready, owners: cycle.owners, membersAt: cycle.membersAt,
				}
				s.partyDungeonLoadingMu.Unlock()
			case "generation":
				s.mu.Lock()
				advanceGameSessionCharacterGeneration(session)
				s.mu.Unlock()
			case "begin_close":
				s.beginGameSessionClose(session)
			}
			close(mutated)
			return nil
		})
	}()
	select {
	case <-gateEntered:
	case <-controlCtx.Done():
		t.Fatal("gate never entered: ", controlCtx.Err())
	}
	// The gate is the only submitted event so far. Wait for SubmitBlocking's
	// post-admission atomic counter before starting the next producer.
	a01QueuedLoadingGuardWait(t, controlCtx, "gate admission", func() bool {
		return session.events.loop.MetricsSnapshot().Submitted[eventloop.PriorityMedium] == 1
	})
	if session.events.loop.QueueLengths().Medium != 0 {
		t.Fatal("gate did not start with an otherwise empty queue")
	}
	releaseLaunched = true
	go func() {
		members := []uint16{identity.character}
		s.releasePartyDungeonLoadingCycle(partyID, cycle, members, members, "a01_queued_guard_"+kind)
		close(releaseDone)
	}()
	a01QueuedLoadingGuardWait(t, controlCtx, "release admission", func() bool {
		// Submitted is incremented after the task (including binding identity)
		// is constructed and enqueued. Its atomic observation plus gateRelease
		// orders the mutation after admission; this is not an unguarded write
		// racing the worker's generation snapshot.
		return session.events.loop.MetricsSnapshot().Submitted[eventloop.PriorityMedium] == 2 &&
			session.events.loop.QueueLengths().Medium == 1
	})
	openGate()
	select {
	case <-mutated:
	case <-controlCtx.Done():
		t.Fatal("gate did not apply lifecycle mutation: ", controlCtx.Err())
	}

	// Synchronous tasks intentionally still execute after beginGameSessionClose;
	// async tasks must not. The same-medium FIFO sentinel follows the admitted
	// release and proves its turn was processed, including when its guard skips.
	snapshots := make(chan a01QueuedLoadingGuardState, 1)
	if err := s.callGameSession(controlCtx, session, "a01-queued-guard-sentinel", func() error {
		session.pendingPostStartMapMu.Lock()
		retained := session.pendingPostStartMapPlacement
		session.pendingPostStartMapMu.Unlock()
		binding, _ := s.boundGameSessionCharacterSnapshot(session)
		snapshots <- a01QueuedLoadingGuardState{
			pending: retained, gates: session.finishLoadingGateSnapshot(),
			placement:  session.postStartMapPlayerStateSent,
			owner:      snapshotPartyDungeonLoadingOwner(session),
			cycle:      s.currentPartyDungeonLoadingCycle(partyID),
			generation: binding.generation, active: session.events.active.Load(),
		}
		return nil
	}); err != nil {
		t.Fatal("sentinel did not complete; not a valid guard result: ", err)
	}
	var got a01QueuedLoadingGuardState
	select {
	case got = <-snapshots:
	case <-controlCtx.Done():
		t.Fatal("sentinel snapshot missing")
	}
	select {
	case <-releaseDone:
	case <-controlCtx.Done():
		t.Fatal("release caller did not finish after sentinel")
	}
	logText := testPacketLogText(t, s)
	// These cases do not deliberately wait out 3s. An overloaded-run deadline
	// must not produce a false green by dropping the event before its guard.
	// processEvent records an expired context in Failed before delivering its
	// callback. The same-medium sentinel has now completed, so that counter is
	// authoritative; packet-log error text may be redacted and is not evidence.
	metrics := session.events.loop.MetricsSnapshot()
	if failed := metrics.Failed[eventloop.PriorityMedium]; failed != 0 {
		t.Fatalf("medium-priority event failures=%d after sentinel; want 0 to prove guard suppression, not context expiry or processor failure:\n%s", failed, logText)
	}
	if strings.Contains(logText, "kind=game-party-dungeon-loading-release-queue-failed") ||
		strings.Contains(logText, "kind=game-party-dungeon-loading-release-wait-ended") {
		t.Fatalf("guard precondition contaminated by admission/wait failure, not a guard result:\n%s", logText)
	}
	if loopCtx.Err() != nil {
		t.Fatal("session context was canceled; active/generation guard was not isolated")
	}
	switch kind {
	case "run_token":
		if got.owner.runtime != beforeOwner.runtime || got.owner.token != beforeOwner.token+1 {
			t.Fatal("gate did not change only the intended run token")
		}
	case "cycle":
		if got.cycle == nil || got.cycle == cycle || got.cycle.Cycle != cycle.Cycle+1 {
			t.Fatal("gate did not replace the live loading cycle")
		}
	case "generation":
		if got.generation == identity.generation {
			t.Fatal("gate did not advance the binding generation")
		}
	case "begin_close":
		if got.active {
			t.Fatal("gate did not clear session active state")
		}
	}
	if got.pending != pending || got.gates != beforeGates || got.placement != beforePlacement {
		t.Errorf("invalidated queued release executed: pending retained=%t gates=%+v want=%+v placement=%t want=%t",
			got.pending == pending, got.gates, beforeGates, got.placement, beforePlacement)
	}
	// Only this session was released. Its callback and worker have both finished,
	// so neither buffer has a concurrent producer at these reads.
	if conn.write.Len() != beforeBytes || peerConn.write.Len() != beforePeerBytes {
		t.Errorf("invalidated release wrote packets: local delta=%d peer delta=%d",
			conn.write.Len()-beforeBytes, peerConn.write.Len()-beforePeerBytes)
	}
	if strings.Contains(logText, "kind=game-party-dungeon-loading-released") {
		t.Errorf("invalidated task reported a release after the sentinel:\n%s", logText)
	}
}

type a01QueuedLoadingGuardState struct {
	pending    *pendingPostStartMapPlacement
	gates      finishLoadingGates
	placement  bool
	owner      partyDungeonLoadingOwner
	cycle      *partyDungeonLoadingCycle
	generation uint64
	active     bool
}

func a01QueuedLoadingGuardWait(t *testing.T, ctx context.Context, label string, ready func() bool) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("%s was not observed: %v", label, ctx.Err())
		}
	}
}
