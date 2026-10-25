package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/platform/eventloop"
)

// Full production queue capacity, real event loops and synthetic resources.
// PostFinish is preset: these regressions do not prove op19/peer-actor/GUI.
func TestA01ReleaseAdmissionCapacityMustRecoverAfterGate(t *testing.T) {
	a01AdmissionCapacityCase(t, false, false)
}

func TestA01ReleaseAdmissionPendingPhases(t *testing.T) {
	t.Run("duplicate_full_tail", func(t *testing.T) { a01AdmissionCapacityCase(t, true, false) })
	t.Run("placement_then_tail", func(t *testing.T) { a01AdmissionCapacityCase(t, true, true) })
}

func a01AdmissionCapacityCase(t *testing.T, duplicates, upgrade bool) {
	if gameSessionEventQueueSize != 512 || createWriteTimeout != 3*time.Second {
		t.Fatal("fixture requires unchanged production capacity=512 and release budget=3s")
	}
	s, blocked, healthy, blockedConn, healthyConn, _ := partyDungeonSyncFixture(t)
	gateStarted := make(chan struct{})
	gateRelease := make(chan struct{})
	gateDone := make(chan error, 1)
	releaseDone := make(chan struct{})
	var gateOnce sync.Once
	openGate := func() { gateOnce.Do(func() { close(gateRelease) }) }
	gateLaunched, gateCollected, releaseLaunched := false, false, false
	loopCtx, cancelLoops := context.WithCancel(context.Background())
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 30*time.Second)
	var fillersRun atomic.Int64

	// Register before entry and loop startup. Open the finite gate before waiting
	// for its caller; do not cancel its context first and manufacture a gate error.
	// This cleanup runs before the fixture's packet logger cleanup (LIFO).
	t.Cleanup(func() {
		openGate()
		if gateLaunched && !gateCollected {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			select {
			case err := <-gateDone:
				if err != nil {
					t.Errorf("gate caller failed during cleanup: %v", err)
				}
			case <-ctx.Done():
				t.Error("gate caller did not exit during bounded cleanup")
			}
			cancel()
		}
		cancelWait()
		s.partyDungeonLoadingMu.Lock()
		for _, cycle := range s.partyDungeonLoadingCycles {
			if cycle != nil && cycle.Timer != nil {
				cycle.Timer.Stop()
				cycle.Timer = nil
			}
		}
		s.partyDungeonLoadingMu.Unlock()
		if releaseLaunched {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			select {
			case <-releaseDone:
			case <-ctx.Done():
				t.Error("release workers did not exit during bounded cleanup")
			}
			cancel()
		}
		for _, session := range []*gameSession{blocked, healthy} {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := s.stopGameSessionEvents(ctx, session); err != nil {
				t.Errorf("stop session %d loop: %v", session.selectedCharacterID, err)
			}
			cancel()
		}
		cancelLoops()
	})

	runPartyDungeonSyncEntry(t, s, blocked)
	r195PrepareLoadingTail(blocked, healthy)
	for _, session := range []*gameSession{blocked, healthy} {
		if !s.partyDungeonPostStartMapPlacementPending(session) || session.postStartMapPlayerStateSent {
			t.Fatalf("char=%d requires actual unsent pending placement", session.selectedCharacterID)
		}
		gates := session.finishLoadingGateSnapshot()
		if gates.State || gates.Completion || !gates.PostFinish {
			t.Fatalf("unexpected tail fixture for char=%d: %+v", session.selectedCharacterID, gates)
		}
		if err := s.startGameSessionEvents(loopCtx, session); err != nil {
			t.Fatal(err)
		}
		if session.events == nil || session.events.loop == nil {
			t.Fatal("real session event loops are required")
		}
	}
	blockedStart, healthyStart := len(blockedConn.frames()), len(healthyConn.frames())
	partyID := uint16(runtimePartyStateSnapshot(blocked).PartyID)
	members := []uint16{blocked.selectedCharacterID, healthy.selectedCharacterID}
	if release, _, _, _, _ := s.partyDungeonLoadingBarrierArrive(blocked, partyID, members); release {
		t.Fatal("first arrival unexpectedly released the cycle")
	}
	release, _, _, _, cycle := s.partyDungeonLoadingBarrierArrive(healthy, partyID, members)
	if !release || cycle == nil {
		t.Fatal("second arrival did not establish the real release boundary")
	}
	s.partyDungeonLoadingMu.Lock()
	released, armed := cycle.Released, cycle.Timer != nil
	s.partyDungeonLoadingMu.Unlock()
	if !released || armed {
		t.Fatal("requires Released=true and the original loading watchdog stopped")
	}

	gateLaunched = true
	go func() {
		gateDone <- s.callGameSession(waitCtx, blocked, "a01-admission-capacity-gate", func() error {
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
	loop := blocked.events.loop
	if q := loop.QueueLengths(); q.High != 0 || q.Medium != 0 || q.Low != 0 {
		t.Fatalf("gate requires otherwise empty queues: %+v", q)
	}
	filler := eventloop.Event{
		Type: "a01-admission-capacity-filler", Priority: eventloop.PriorityMedium, Context: loopCtx,
		Data: gameSessionEventTask{source: "a01-admission-capacity-filler", run: func() error {
			fillersRun.Add(1)
			return nil
		}},
	}
	for i := 0; i < 512; i++ {
		if err := loop.Submit(filler); err != nil {
			t.Fatalf("real medium filler %d/512 failed: %v", i+1, err)
		}
	}
	// The probe must NOT enter. Count its expected fast rejection separately from
	// the production SubmitBlocking attempt; Dropped is not the admission oracle.
	if err := loop.Submit(filler); !errors.Is(err, eventloop.ErrQueueFull) {
		t.Fatalf("513th medium filler must be rejected as full, got %v", err)
	}
	full := loop.MetricsSnapshot()
	if q := loop.QueueLengths(); q.Medium != 512 || fillersRun.Load() != 0 ||
		full.Submitted[eventloop.PriorityMedium] != 513 || full.Processed[eventloop.PriorityMedium] != 0 {
		t.Fatalf("invalid saturation proof: queues=%+v submitted=%v processed=%v fillers=%d", q, full.Submitted, full.Processed, fillersRun.Load())
	}

	const trigger = "a01_admission_phases"
	initialTail := members
	if upgrade {
		initialTail = []uint16{healthy.selectedCharacterID}
	}
	started := time.Now()
	releaseLaunched = true
	go func() {
		s.releasePartyDungeonLoadingCycle(partyID, cycle, members, initialTail, trigger)
		close(releaseDone)
	}()
	// Only this healthy loop receives observation events while the peer is full.
	healthyCtx, cancelHealthy := context.WithTimeout(waitCtx, 5*time.Second)
	healthyState, healthyErr := a01ReleaseQueueAwaitComplete(healthyCtx, s, healthy)
	cancelHealthy()
	if healthyErr != nil {
		t.Fatalf("healthy member failed while peer queue was full: state=%+v err=%v", healthyState, healthyErr)
	}
	a01ReleaseQueueAssertWire(t, healthyConn, healthyStart, "healthy while peer queue full")
	if got := a01AdmissionTailCounts(t, healthyConn, healthyStart); got != [3]int{1, 1, 1} {
		t.Fatalf("healthy member must receive exactly one placement/op37/op30 before gate opens, got %v", got)
	}
	select {
	case <-releaseDone:
	case <-waitCtx.Done():
		t.Fatal("release workers did not return within the finite test watchdog")
	}
	if elapsed := time.Since(started); elapsed < createWriteTimeout {
		t.Fatalf("release returned before its admission budget: %s", elapsed)
	}
	if waitCtx.Err() != nil || loopCtx.Err() != nil || !blocked.events.active.Load() {
		t.Fatal("test/session cancellation contaminated the full-queue boundary")
	}
	select {
	case <-gateRelease:
		t.Fatal("gate opened before admission failure was established")
	default:
	}
	failed := loop.MetricsSnapshot()
	if q := loop.QueueLengths(); q.Medium != 512 || fillersRun.Load() != 0 ||
		failed.Submitted[eventloop.PriorityMedium] != full.Submitted[eventloop.PriorityMedium] ||
		failed.Processed[eventloop.PriorityMedium] != full.Processed[eventloop.PriorityMedium] {
		t.Fatalf("release may have entered or queue drained; not a valid admission red: queues=%+v before=%+v after=%+v fillers=%d", q, full, failed, fillersRun.Load())
	}
	logText := testPacketLogText(t, s)
	blockedID := fmt.Sprintf("char_id=%d", blocked.selectedCharacterID)
	partyField := fmt.Sprintf("party_id=%d", partyID)
	triggerField := "trigger=" + trigger
	if n := a01AdmissionLogCount(logText, "kind=game-party-dungeon-loading-release-queue-failed", blockedID, partyField, triggerField); n != 1 {
		t.Fatalf("want one target queue-failed record, got %d; cannot establish rejection:\n%s", n, logText)
	}
	if a01AdmissionLogCount(logText, "kind=game-party-dungeon-loading-released", blockedID) != 0 ||
		a01AdmissionLogCount(logText, "kind=game-party-dungeon-loading-release-wait-ended", blockedID) != 0 {
		t.Fatalf("target had an accepted/executed release; wrong boundary:\n%s", logText)
	}
	if !s.partyDungeonPostStartMapPlacementPending(blocked) ||
		a01AdmissionTailCounts(t, blockedConn, blockedStart) != [3]int{} {
		t.Fatal("blocked placement/tail changed before its queue could admit release")
	}
	s.partyDungeonLoadingMu.Lock()
	sameCycle := s.partyDungeonLoadingCycles[partyID] == cycle
	released, armed = cycle.Released, cycle.Timer != nil
	s.partyDungeonLoadingMu.Unlock()
	if !sameCycle || !released {
		t.Fatal("lost the original Released cycle before recovery")
	}
	// The original watchdog was stopped above. A future repair may install a new
	// retry timer; observe it without prescribing how automatic recovery works.
	t.Logf("proved no release admission: full medium=512, submitted unchanged=%d, target queue-failed=1, timer_now_armed=%t, elapsed=%s", failed.Submitted[eventloop.PriorityMedium], armed, time.Since(started))

	if duplicates {
		// While the initial capacity recovery is pending, repeat exactly that
		// member/phase concurrently. It must coalesce instead of producing another
		// timed-out worker per duplicate. The healthy member is deliberately absent.
		var duplicateWorkers sync.WaitGroup
		for i := 0; i < 12; i++ {
			duplicateWorkers.Add(1)
			go func() {
				defer duplicateWorkers.Done()
				tail := []uint16(nil)
				if !upgrade {
					tail = []uint16{blocked.selectedCharacterID}
				}
				s.releasePartyDungeonLoadingCycle(partyID, cycle, []uint16{blocked.selectedCharacterID}, tail, trigger)
			}()
		}
		duplicatesDone := make(chan struct{})
		go func() { duplicateWorkers.Wait(); close(duplicatesDone) }()
		select {
		case <-duplicatesDone:
		case <-waitCtx.Done():
			t.Fatal("duplicate releases failed to finish within the test watchdog")
		}
		if n := a01AdmissionLogCount(testPacketLogText(t, s), "kind=game-party-dungeon-loading-release-queue-failed", blockedID, partyField, triggerField); n != 1 {
			t.Fatalf("duplicate pending requests generated extra admission failures: %d", n)
		}
		if upgrade {
			// The earlier timeout requested placement only. A later ready arrival
			// requires its tail too, even though placement is still awaiting capacity.
			upgradeDone := make(chan struct{})
			go func() {
				s.releasePartyDungeonLoadingCycle(partyID, cycle, []uint16{blocked.selectedCharacterID}, []uint16{blocked.selectedCharacterID}, trigger)
				close(upgradeDone)
			}()
			select {
			case <-upgradeDone:
			case <-waitCtx.Done():
				t.Fatal("tail upgrade did not return")
			}
			if n := a01AdmissionLogCount(testPacketLogText(t, s), "kind=game-party-dungeon-loading-release-queue-failed", blockedID, partyField, triggerField); n != 2 {
				t.Fatalf("tail must retain a separate admission after placement-only request: failures=%d", n)
			}
		}
		if loop.QueueLengths().Medium != 512 || fillersRun.Load() != 0 || loop.MetricsSnapshot().Submitted[eventloop.PriorityMedium] != full.Submitted[eventloop.PriorityMedium] {
			t.Fatal("duplicate/phase proof lost full-queue precondition")
		}

	}

	openGate()
	select {
	case err := <-gateDone:
		gateCollected = true
		if err != nil {
			t.Fatal("finite gate failed: ", err)
		}
	case <-waitCtx.Done():
		t.Fatal("finite gate caller did not complete")
	}
	// FIFO within medium: this successful sentinel is behind all 512 fillers.
	// It reads only; it does not call arrival, op37, release or a retry hook.
	state, err := a01AdmissionObserve(waitCtx, s, blocked)
	if err != nil || !state.Observed || fillersRun.Load() != 512 {
		t.Fatalf("drain sentinel/fillers incomplete; not the expected red: state=%+v err=%v fillers=%d", state, err, fillersRun.Load())
	}
	drained := loop.MetricsSnapshot()
	if drained.Failed[eventloop.PriorityMedium] != 0 || drained.Panicked[eventloop.PriorityMedium] != 0 || drained.CallbackDropped != 0 {
		t.Fatalf("gate/filler/sentinel processing failed: %+v\n%s", drained, testPacketLogText(t, s))
	}

	// Recovery has a separate observation window. Sentinel contexts use the outer
	// watchdog, not the recovery timer, so normal expected-red expiry does not
	// manufacture expired queued events. Every blocking call remains bounded.
	recoveryTimer := time.NewTimer(5 * time.Second)
	defer recoveryTimer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for state.Pending || !state.Placement || !state.State || !state.Completion || !state.PostFinish {
		select {
		case <-recoveryTimer.C:
			t.Fatalf("release rejected by capacity did not recover after queue drain: state=%+v; want pending=false placement=true op37=true op30=true; PostFinish is preset fixture\n%s", state, testPacketLogText(t, s))
		case <-waitCtx.Done():
			t.Fatal("outer watchdog expired during recovery; not the intended red")
		case <-ticker.C:
			state, err = a01AdmissionObserve(waitCtx, s, blocked)
			if err != nil {
				t.Fatal("recovery sentinel failed; not the intended red: ", err)
			}
		}
	}
	a01ReleaseQueueAssertWire(t, blockedConn, blockedStart, "capacity recovery")
	for _, receiver := range []struct {
		name  string
		conn  *partyDungeonSyncConn
		start int
	}{{"blocked", blockedConn, blockedStart}, {"healthy", healthyConn, healthyStart}} {
		if got := a01AdmissionTailCounts(t, receiver.conn, receiver.start); got != [3]int{1, 1, 1} {
			t.Errorf("%s: expected one actual placement/op37/op30 without replay, got %v", receiver.name, got)
		}
	}
}

func a01AdmissionObserve(ctx context.Context, s *Service, session *gameSession) (a01ReleaseQueueState, error) {
	snapshot := make(chan a01ReleaseQueueState, 1)
	if err := s.callGameSession(ctx, session, "a01-admission-capacity-sentinel", func() error {
		gates := session.finishLoadingGateSnapshot()
		snapshot <- a01ReleaseQueueState{
			Observed: true, Pending: s.partyDungeonPostStartMapPlacementPending(session),
			Placement: session.postStartMapPlayerStateSent,
			State:     gates.State, Completion: gates.Completion, PostFinish: gates.PostFinish,
		}
		return nil
	}); err != nil {
		return a01ReleaseQueueState{}, err
	}
	select {
	case state := <-snapshot:
		return state, nil
	case <-ctx.Done():
		return a01ReleaseQueueState{}, ctx.Err()
	}
}

func a01AdmissionLogCount(text string, want ...string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		fields := make(map[string]bool)
		for _, field := range strings.Fields(line) {
			fields[field] = true
		}
		matched := true
		for _, field := range want {
			matched = matched && fields[field]
		}
		if matched {
			count++
		}
	}
	return count
}

func a01AdmissionTailCounts(t *testing.T, conn *partyDungeonSyncConn, start int) [3]int {
	t.Helper()
	stream := conn.frames()
	if start < 0 || start > len(stream) {
		t.Fatalf("invalid frame cursor %d/%d", start, len(stream))
	}
	var counts [3]int
	for rest := stream[start:]; len(rest) > 0; {
		packet, next := splitCurrentGameServerUpperPacketAuto(t, rest)
		if len(next) >= len(rest) {
			t.Fatal("packet parser made no progress")
		}
		rest = next
		if packet.Header.Classification != 0 {
			continue
		}
		switch packet.Header.MsgID {
		case uint16(dnfenum.CmdPacketRequestBlacklist):
			if bytes.Equal(packet.Body, buildCurrentSceneActorPlacementBody()) {
				counts[0]++
			}
		case uint16(dnfenum.CmdPacketFinishLoading):
			if len(packet.Body) == currentFinishLoadingCharacterStateBodySize {
				counts[1]++
			}
		case currentIncreaseStatusResultMsgID:
			if bytes.Equal(packet.Body, buildCurrentIncreaseStatusResultBody()) {
				counts[2]++
			}
		}
	}
	return counts
}
