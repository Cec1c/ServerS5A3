package dnfbridge

import (
	"context"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/party"
	"longheng.io/server/internal/platform/eventloop"
)

func TestPartySelectionNoticeWaitsForOccupiedReceiverQueue(t *testing.T) {
	h := newPartyLeaveHarness(t)
	snapshot, found := h.service.runtimePartyManagerForService().SnapshotByUser(1002, 0)
	if !found {
		t.Fatal("party fixture lost member 1002")
	}
	result := party.RuntimePartyResult{OK: true, Previous: &snapshot, TargetUser: 1002}

	ctx, cancel := context.WithCancel(context.Background())
	h.leader.ctx = ctx
	if err := h.service.startGameSessionEvents(ctx, h.leader); err != nil {
		cancel()
		t.Fatalf("start leader event loop: %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
	gateDone := make(chan struct{})
	t.Cleanup(func() {
		releaseGate()
		select {
		case <-gateDone:
		case <-time.After(time.Second):
			t.Error("occupied receiver did not exit during cleanup")
		}
		cancel()
		stopTestGameSessionEvents(t, h.service, h.leader)
	})
	if err := h.service.postGameSessionCharacterEvent(h.leader, "test-selection-notice-hold", 1001, h.leader.characterGeneration, func() error {
		defer close(gateDone)
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}); err != nil {
		releaseGate()
		t.Fatalf("occupy leader event loop: %v", err)
	}
	waitTestSignal(t, entered, "leader event-loop gate did not start")
	if got := h.leaderConn.write.Len(); got != 0 {
		t.Fatalf("leader connection had %d pre-notice bytes", got)
	}

	returned := make(chan struct{})
	go func() {
		h.service.notifyRuntimePartySelectionDeparture(h.member, result, "return_select_character")
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		releaseGate()
		t.Fatal("selection notice waited for an occupied receiver event loop")
	}
	if got := h.leaderConn.write.Len(); got != 0 {
		releaseGate()
		t.Fatalf("selection notice bypassed the receiver loop and wrote %d bytes", got)
	}

	releaseGate()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), time.Second)
	defer drainCancel()
	if err := h.service.callGameSession(drainCtx, h.leader, "test-selection-notice-drained", func() error { return nil }); err != nil {
		t.Fatalf("drain queued selection notice: %v", err)
	}
	frames := splitUpperFrames(t, h.leaderConn.write.Bytes())
	if len(frames) != 1 || frames[0].msgID != currentAreaChatNotifyOpcode {
		t.Fatalf("queued selection notice frames=%d want exactly one opcode %d", len(frames), currentAreaChatNotifyOpcode)
	}
}

func TestPartySelectionNoticeQueuedIdentityChangeDropsOldNotice(t *testing.T) {
	changes := []struct {
		name   string
		mutate func(*partyLeaveHarness)
	}{
		{
			name: "character",
			mutate: func(h *partyLeaveHarness) {
				h.service.mu.Lock()
				h.leader.selectedCharacterID = 1003
				h.service.mu.Unlock()
			},
		},
		{
			name: "generation",
			mutate: func(h *partyLeaveHarness) {
				advanceGameSessionCharacterGeneration(h.leader)
			},
		},
	}
	for _, change := range changes {
		change := change
		t.Run(change.name, func(t *testing.T) {
			h := newPartyLeaveHarness(t)
			snapshot, found := h.service.runtimePartyManagerForService().SnapshotByUser(1002, 0)
			if !found {
				t.Fatal("party fixture lost member 1002")
			}
			result := party.RuntimePartyResult{OK: true, Previous: &snapshot, TargetUser: 1002}

			ctx, cancel := context.WithCancel(context.Background())
			h.leader.ctx = ctx
			if err := h.service.startGameSessionEvents(ctx, h.leader); err != nil {
				cancel()
				t.Fatalf("start leader event loop: %v", err)
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
			gateDone := make(chan struct{})
			t.Cleanup(func() {
				releaseGate()
				select {
				case <-gateDone:
				case <-time.After(time.Second):
					t.Error("identity-change receiver did not exit during cleanup")
				}
				cancel()
				stopTestGameSessionEvents(t, h.service, h.leader)
			})
			if err := h.service.postGameSessionCharacterEvent(h.leader, "test-selection-identity-hold", 1001, h.leader.characterGeneration, func() error {
				defer close(gateDone)
				close(entered)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}); err != nil {
				releaseGate()
				t.Fatalf("occupy leader event loop: %v", err)
			}
			waitTestSignal(t, entered, "leader event-loop gate did not start")

			before := h.leader.events.loop.MetricsSnapshot().Submitted[eventloop.PriorityMedium]
			h.service.notifyRuntimePartySelectionDeparture(h.member, result, "return_select_character")
			after := h.leader.events.loop.MetricsSnapshot().Submitted[eventloop.PriorityMedium]
			if after != before+1 {
				t.Fatalf("selection notice submissions=%d want=1", after-before)
			}
			if got := h.leaderConn.write.Len(); got != 0 {
				t.Fatalf("selection notice wrote directly before identity change: %d bytes", got)
			}

			change.mutate(h)
			releaseGate()
			drainCtx, drainCancel := context.WithTimeout(context.Background(), time.Second)
			defer drainCancel()
			if err := h.service.callGameSession(drainCtx, h.leader, "test-selection-identity-drained", func() error { return nil }); err != nil {
				t.Fatalf("drain stale selection notice: %v", err)
			}
			if got := h.leaderConn.write.Len(); got != 0 {
				t.Fatalf("stale selection notice wrote %d bytes after identity change", got)
			}
		})
	}
}

func TestPartySelectionNoticeFullReceiverQueueDoesNotCloseSession(t *testing.T) {
	h := newPartyLeaveHarness(t)
	snapshot, found := h.service.runtimePartyManagerForService().SnapshotByUser(1002, 0)
	if !found {
		t.Fatal("party fixture lost member 1002")
	}
	result := party.RuntimePartyResult{OK: true, Previous: &snapshot, TargetUser: 1002}
	trackedConn := &guildPresenceQueueConn{bufferConn: h.leaderConn}
	h.leader.conn = trackedConn

	ctx, cancel := context.WithCancel(context.Background())
	h.leader.ctx = ctx
	if err := h.service.startGameSessionEvents(ctx, h.leader); err != nil {
		cancel()
		t.Fatalf("start leader event loop: %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
	gateDone := make(chan struct{})
	t.Cleanup(func() {
		releaseGate()
		select {
		case <-gateDone:
		case <-time.After(time.Second):
			t.Error("full-queue receiver did not exit during cleanup")
		}
		cancel()
		stopTestGameSessionEvents(t, h.service, h.leader)
	})
	if err := h.service.postGameSessionCharacterEvent(h.leader, "test-selection-full-queue-hold", 1001, h.leader.characterGeneration, func() error {
		defer close(gateDone)
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}); err != nil {
		releaseGate()
		t.Fatalf("occupy leader event loop: %v", err)
	}
	waitTestSignal(t, entered, "leader event-loop gate did not start")
	for i := 0; i < gameSessionEventQueueSize; i++ {
		if err := h.leader.events.loop.Submit(eventloop.Event{
			Type:     "test-selection-full-queue",
			Priority: eventloop.PriorityMedium,
			Context:  ctx,
			Data: gameSessionEventTask{
				source: "test-selection-full-queue",
				run:    func() error { return nil },
			},
		}); err != nil {
			t.Fatalf("fill leader queue %d/%d: %v", i+1, gameSessionEventQueueSize, err)
		}
	}
	if got := h.leader.events.loop.QueueLengths().Medium; got != gameSessionEventQueueSize {
		t.Fatalf("leader queue length=%d want=%d", got, gameSessionEventQueueSize)
	}

	returned := make(chan struct{})
	go func() {
		h.service.notifyRuntimePartySelectionDeparture(h.member, result, "return_select_character")
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		releaseGate()
		t.Fatal("selection notice blocked on a full receiver queue")
	}
	if trackedConn.closed.Load() {
		t.Fatal("full receiver queue closed the normal leader connection")
	}
	if !h.leader.events.active.Load() {
		t.Fatal("full receiver queue deactivated the leader session")
	}
	if got := h.leaderConn.write.Len(); got != 0 {
		t.Fatalf("full receiver queue bypassed the event loop and wrote %d bytes", got)
	}
	releaseGate()
}
