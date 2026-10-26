package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/platform/eventloop"
)

// Capacity lifecycle regression. The companion capacity test provides
// a01AdmissionLogCount; the other A01/R195 helpers are already in S.
// These are safety controls for recovery after FAILED admission, not proof that
// a retry was ever scheduled. Pair them with the positive capacity recovery test.
// PostFinish is preset: no op19, complete peer-actor, resource or GUI claim.
func TestA01ReleaseAdmissionCapacityGuard(t *testing.T) {
	for _, kind := range []string{"run_token", "cycle", "generation", "begin_close"} {
		t.Run(kind, func(t *testing.T) {
			a01ReleaseAdmissionGuardCase(t, kind)
		})
	}
}

func a01ReleaseAdmissionGuardCase(t *testing.T, kind string) {
	t.Helper()
	if gameSessionEventQueueSize != 512 || createWriteTimeout != 3*time.Second {
		t.Fatal("requires unchanged production capacity=512 and initial admission budget=3s")
	}
	s, session, peer, conn, peerConn, _ := partyDungeonSyncFixture(t)
	gateEntered := make(chan struct{})
	gateAction := make(chan bool, 1)
	mutated := make(chan struct{})
	gateDone := make(chan error, 1)
	releaseDone := make(chan struct{})
	var gateOnce sync.Once
	finishGate := func(applyMutation bool) {
		gateOnce.Do(func() { gateAction <- applyMutation })
	}
	gateLaunched, gateCollected, releaseLaunched := false, false, false
	loopCtx, cancelLoop := context.WithCancel(context.Background())
	controlCtx, cancelControl := context.WithTimeout(context.Background(), 30*time.Second)
	var fillersRun atomic.Int64
	var cycle *partyDungeonLoadingCycle

	// Register after the fixture, so this cleanup precedes its logger Close.
	// An early fatal path releases the gate WITHOUT pretending a mutation ran.
	t.Cleanup(func() {
		finishGate(false)
		if gateLaunched && !gateCollected {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			select {
			case err := <-gateDone:
				if err != nil {
					t.Errorf("guard gate caller failed during cleanup: %v", err)
				}
			case <-ctx.Done():
				t.Error("guard gate caller did not exit during bounded cleanup")
			}
			cancel()
		}
		cancelControl()
		// This cancellation happens only after the assertion window. In the
		// begin_close case the session context remains live throughout the test.
		cancelLoop()
		s.partyDungeonLoadingMu.Lock()
		for _, current := range s.partyDungeonLoadingCycles {
			if current != nil && current.Timer != nil {
				current.Timer.Stop()
				current.Timer = nil
			}
		}
		// The cycle case removes this pointer from the current-cycle map.
		if cycle != nil && cycle.Timer != nil {
			cycle.Timer.Stop()
			cycle.Timer = nil
		}
		s.partyDungeonLoadingMu.Unlock()
		if releaseLaunched {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			select {
			case <-releaseDone:
			case <-ctx.Done():
				t.Error("initial release caller did not exit during bounded cleanup")
			}
			cancel()
		}
		if session.events != nil && session.events.loop != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := s.stopGameSessionEvents(ctx, session); err != nil {
				t.Errorf("stop admission guard loop: %v", err)
			}
			cancel()
		}
	})

	runPartyDungeonSyncEntry(t, s, session)
	r195PrepareLoadingTail(session, peer)
	session.pendingPostStartMapMu.Lock()
	pending := session.pendingPostStartMapPlacement
	session.pendingPostStartMapMu.Unlock()
	beforeOwner := snapshotPartyDungeonLoadingOwner(session)
	beforeGates := session.finishLoadingGateSnapshot()
	beforePlacement := session.postStartMapPlayerStateSent
	if pending == nil || pending.Runtime != beforeOwner.runtime || beforePlacement ||
		beforeGates.State || beforeGates.Completion || !beforeGates.PostFinish {
		t.Fatal("requires actual entry-owned pending placement and the PostFinish-only preset")
	}
	beforeWire, beforePeerWire := conn.frames(), peerConn.frames()
	partyID := uint16(runtimePartyStateSnapshot(session).PartyID)
	allMembers := []uint16{session.selectedCharacterID, peer.selectedCharacterID}
	if release, _, _, _, _ := s.partyDungeonLoadingBarrierArrive(session, partyID, allMembers); release {
		t.Fatal("first arrival unexpectedly released")
	}
	release, _, _, _, arrived := s.partyDungeonLoadingBarrierArrive(peer, partyID, allMembers)
	cycle = arrived
	if !release || cycle == nil {
		t.Fatal("second arrival did not establish the Released cycle")
	}
	s.partyDungeonLoadingMu.Lock()
	released, armed := cycle.Released, cycle.Timer != nil
	// Use only the existing cycle fields; do not copy or invent retry state.
	replacement := &partyDungeonLoadingCycle{
		PartyID: partyID, Cycle: cycle.Cycle + 1, Released: true,
		Ready: make(map[uint16]bool), owners: make(map[uint16]partyDungeonLoadingOwner),
		membersAt: append([]uint16(nil), cycle.membersAt...),
	}
	for id, ready := range cycle.Ready {
		replacement.Ready[id] = ready
	}
	for id, owner := range cycle.owners {
		replacement.owners[id] = owner
	}
	s.partyDungeonLoadingMu.Unlock()
	if !released || armed {
		t.Fatal("requires actual Released transition and stopped initial watchdog")
	}
	session.ctx = loopCtx
	if err := s.startGameSessionEvents(loopCtx, session); err != nil {
		t.Fatal(err)
	}
	if session.events == nil || session.events.loop == nil {
		t.Fatal("real session event loop required")
	}
	identity, current := s.boundGameSessionCharacterSnapshot(session)
	if !current || identity.generation == 0 {
		t.Fatal("fixture requires a current nonzero binding generation")
	}

	gateLaunched = true
	go func() {
		gateDone <- s.callGameSession(controlCtx, session, "a01-admission-guard-gate", func() error {
			close(gateEntered)
			select {
			case applyMutation := <-gateAction:
				if !applyMutation {
					return nil // bounded cleanup only; no fake lifecycle transition
				}
			case <-controlCtx.Done():
				return controlCtx.Err()
			}
			// The main test authorizes this only AFTER initial admission failed.
			// No filler or retry callback can run until this callback returns.
			switch kind {
			case "run_token":
				session.dungeon.mu.Lock()
				session.dungeon.runToken++
				session.dungeon.mu.Unlock()
			case "cycle":
				s.partyDungeonLoadingMu.Lock()
				s.partyDungeonLoadingCycles[partyID] = replacement
				s.partyDungeonLoadingMu.Unlock()
			case "generation":
				s.mu.Lock()
				advanceGameSessionCharacterGeneration(session)
				s.mu.Unlock()
			case "begin_close":
				s.beginGameSessionClose(session)
			default:
				return fmt.Errorf("unknown admission guard case %q", kind)
			}
			close(mutated)
			return nil
		})
	}()
	select {
	case <-gateEntered:
	case <-controlCtx.Done():
		t.Fatal("guard gate did not enter: ", controlCtx.Err())
	}
	loop := session.events.loop
	if q := loop.QueueLengths(); q.High != 0 || q.Medium != 0 || q.Low != 0 {
		t.Fatalf("guard gate did not start with empty queues: %+v", q)
	}
	// Synchronous task data is deliberate: generation/active changes must not
	// skip the fillers and accidentally make a missing drain look successful.
	filler := eventloop.Event{
		Type: "a01-admission-guard-filler", Priority: eventloop.PriorityMedium, Context: loopCtx,
		Data: gameSessionEventTask{source: "a01-admission-guard-filler", run: func() error {
			fillersRun.Add(1)
			return nil
		}},
	}
	for i := 0; i < 512; i++ {
		if err := loop.Submit(filler); err != nil {
			t.Fatalf("medium filler %d/512: %v", i+1, err)
		}
	}
	if err := loop.Submit(filler); !errors.Is(err, eventloop.ErrQueueFull) {
		t.Fatalf("513th filler must prove actual full capacity, got %v", err)
	}
	full := loop.MetricsSnapshot()
	if loop.QueueLengths().Medium != 512 || fillersRun.Load() != 0 ||
		full.Submitted[eventloop.PriorityMedium] != 513 || full.Processed[eventloop.PriorityMedium] != 0 {
		t.Fatalf("invalid full-queue precondition: queues=%+v metrics=%+v fillers=%d", loop.QueueLengths(), full, fillersRun.Load())
	}

	trigger := "a01_admission_guard_" + kind
	started := time.Now()
	releaseLaunched = true
	go func() {
		// Only the guarded receiver is released. The untouched peer's wire must
		// stay unchanged; healthy-peer recovery is covered by the capacity test.
		members := []uint16{identity.character}
		s.releasePartyDungeonLoadingCycle(partyID, cycle, members, members, trigger)
		close(releaseDone)
	}()
	select {
	case <-releaseDone:
	case <-controlCtx.Done():
		t.Fatal("initial release worker did not finish within the watchdog")
	}
	if time.Since(started) < createWriteTimeout || controlCtx.Err() != nil || loopCtx.Err() != nil {
		t.Fatal("initial failure was not the live-session 3s capacity boundary")
	}
	failed := loop.MetricsSnapshot()
	if loop.QueueLengths().Medium != 512 || fillersRun.Load() != 0 ||
		failed.Submitted[eventloop.PriorityMedium] != full.Submitted[eventloop.PriorityMedium] ||
		failed.Processed[eventloop.PriorityMedium] != full.Processed[eventloop.PriorityMedium] {
		t.Fatalf("initial release may have entered; invalid guard setup: before=%+v after=%+v fillers=%d", full, failed, fillersRun.Load())
	}
	charField := fmt.Sprintf("char_id=%d", identity.character)
	logText := testPacketLogText(t, s)
	if n := a01AdmissionLogCount(logText, "kind=game-party-dungeon-loading-release-queue-failed", charField,
		fmt.Sprintf("party_id=%d", partyID), "trigger="+trigger); n < 1 {
		t.Fatalf("initial target queue-failed record missing; cannot prove admission failure:\n%s", logText)
	}
	// There is exactly one initial release call above. Do not constrain whether
	// a candidate also emits this existing diagnostic for later full-queue retries.
	if a01AdmissionLogCount(logText, "kind=game-party-dungeon-loading-released", charField) != 0 ||
		a01AdmissionLogCount(logText, "kind=game-party-dungeon-loading-release-wait-ended", charField) != 0 {
		t.Fatalf("release was admitted/executed before mutation; invalid setup:\n%s", logText)
	}
	if !s.boundGameSessionCharacterCurrent(identity) || !session.events.active.Load() ||
		snapshotPartyDungeonLoadingOwner(session) != beforeOwner || s.currentPartyDungeonLoadingCycle(partyID) != cycle {
		t.Fatal("identity/owner/cycle/active was invalid before the gate mutation")
	}
	session.pendingPostStartMapMu.Lock()
	retained := session.pendingPostStartMapPlacement
	session.pendingPostStartMapMu.Unlock()
	if retained != pending || session.finishLoadingGateSnapshot() != beforeGates ||
		session.postStartMapPlayerStateSent != beforePlacement ||
		!bytes.Equal(conn.frames(), beforeWire) || !bytes.Equal(peerConn.frames(), beforePeerWire) {
		t.Fatal("pending/gates/wire changed while no release could be admitted")
	}

	finishGate(true)
	select {
	case <-mutated:
	case <-controlCtx.Done():
		t.Fatal("gate did not apply the lifecycle mutation")
	}
	select {
	case err := <-gateDone:
		gateCollected = true
		if err != nil {
			t.Fatal("guard gate failed: ", err)
		}
	case <-controlCtx.Done():
		t.Fatal("guard gate caller did not finish")
	}

	observations := 0
	observe := func() {
		got, err := a01AdmissionGuardObserve(controlCtx, s, session, partyID)
		if err != nil {
			t.Fatal("same-medium sentinel failed; not proof of suppression: ", err)
		}
		observations++
		if fillersRun.Load() != 512 {
			t.Fatalf("sentinel completed without all 512 fillers: %d", fillersRun.Load())
		}
		wantOwner, wantCycle, wantActive := beforeOwner, cycle, true
		if kind == "run_token" {
			wantOwner.token++
		}
		if kind == "cycle" {
			wantCycle = replacement
		}
		if kind == "begin_close" {
			wantActive = false
		}
		if got.owner != wantOwner || got.cycle != wantCycle || got.active != wantActive {
			t.Fatalf("gate did not isolate %s: owner=%+v cycle=%p active=%t", kind, got.owner, got.cycle, got.active)
		}
		if kind == "generation" {
			if got.generation == 0 || got.generation == identity.generation || s.boundGameSessionCharacterCurrent(identity) {
				t.Fatal("old binding generation was not invalidated")
			}
		} else if got.generation != identity.generation || !s.boundGameSessionCharacterCurrent(identity) {
			t.Fatal("binding changed in a non-generation case")
		}
		if got.pending != pending || got.gates != beforeGates || got.placement != beforePlacement {
			t.Fatalf("invalidated admission recovery consumed old state: pending_same=%t gates=%+v want=%+v placement=%t want=%t",
				got.pending == pending, got.gates, beforeGates, got.placement, beforePlacement)
		}
		if !bytes.Equal(conn.frames(), beforeWire) || !bytes.Equal(peerConn.frames(), beforePeerWire) {
			t.Fatal("invalidated admission recovery wrote guarded/peer wire")
		}
		metrics := loop.MetricsSnapshot()
		if metrics.Failed[eventloop.PriorityMedium] != 0 || metrics.Panicked[eventloop.PriorityMedium] != 0 || metrics.CallbackDropped != 0 {
			t.Fatalf("processor/context/callback failure cannot count as guard success: %+v\n%s", metrics, testPacketLogText(t, s))
		}
		if controlCtx.Err() != nil || loopCtx.Err() != nil {
			t.Fatal("context cancellation cannot count as lifecycle guard success")
		}
	}
	observe() // First FIFO sentinel establishes drain after the gate mutation.
	first := loop.MetricsSnapshot()
	firstObservations := observations
	// Explicit bounded observation, not an invented retry-completion field/log.
	// 2s spans 20 nominal 100ms retry periods; scheduling is not guaranteed.
	window := time.NewTimer(2 * time.Second)
	defer window.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-window.C:
			observe() // Include a final successful sentinel at/after the boundary.
			logText = testPacketLogText(t, s)
			if a01AdmissionLogCount(logText, "kind=game-party-dungeon-loading-released", charField) != 0 {
				t.Fatalf("old release callback reported execution after %s invalidation:\n%s", kind, logText)
			}
			last := loop.MetricsSnapshot()
			t.Logf("%s: 512 fillers drained, %d successful sentinels; 2s window medium admissions delta=%d (own sentinels=%d); no old pending/gate/wire change; retry goroutine termination is not proven",
				kind, observations, last.Submitted[eventloop.PriorityMedium]-first.Submitted[eventloop.PriorityMedium], observations-firstObservations)
			return
		case <-ticker.C:
			observe()
		case <-controlCtx.Done():
			t.Fatal("guard observation window exceeded outer watchdog")
		}
	}
}

func a01AdmissionGuardObserve(ctx context.Context, s *Service, session *gameSession, partyID uint16) (a01QueuedLoadingGuardState, error) {
	snapshots := make(chan a01QueuedLoadingGuardState, 1)
	// callGameSession creates a synchronous medium task. It still executes when
	// begin_close has disabled async tasks, without canceling the live session ctx.
	if err := s.callGameSession(ctx, session, "a01-admission-guard-sentinel", func() error {
		session.pendingPostStartMapMu.Lock()
		pending := session.pendingPostStartMapPlacement
		session.pendingPostStartMapMu.Unlock()
		binding, _ := s.boundGameSessionCharacterSnapshot(session)
		snapshots <- a01QueuedLoadingGuardState{
			pending: pending, gates: session.finishLoadingGateSnapshot(),
			placement:  session.postStartMapPlayerStateSent,
			owner:      snapshotPartyDungeonLoadingOwner(session),
			cycle:      s.currentPartyDungeonLoadingCycle(partyID),
			generation: binding.generation, active: session.events.active.Load(),
		}
		return nil
	}); err != nil {
		return a01QueuedLoadingGuardState{}, err
	}
	select {
	case got := <-snapshots:
		return got, nil
	case <-ctx.Done():
		return a01QueuedLoadingGuardState{}, ctx.Err()
	}
}
