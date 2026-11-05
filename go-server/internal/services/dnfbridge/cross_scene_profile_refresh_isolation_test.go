package dnfbridge

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestCrossSceneAutomaticRefreshSlowReaderDoesNotBlockOtherRecipients(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	left, right := net.Pipe()
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	blocked := &crossSceneProfileObservedConn{Conn: left, writing: make(chan struct{})}
	f.target.conn = blocked
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- newCurrentCrossSceneProfileBatch(f.service, ctx).send(f.target, f.actor, currentConnectionTownActorOwnerContext(f.target))
	}()
	select {
	case <-blocked.writing:
	case <-time.After(3 * time.Second):
		t.Fatal("first recipient never reached its socket")
	}
	// The first write is still blocked. This exact actor must be available for
	// another recipient, and invalidation must not await the slow connection.
	if !f.actor.crossSceneAppearanceMu.TryLock() {
		t.Fatal("slow recipient retained the shared actor lock")
	}
	f.actor.crossSceneAppearanceMu.Unlock()
	invalidateCurrentCrossSceneAppearance(f.actor)
	healthy := make(chan error, 1)
	go func() {
		healthy <- newCurrentCrossSceneProfileBatch(f.service, context.Background()).send(f.viewer2, f.actor, currentConnectionTownActorOwnerContext(f.viewer2))
	}()
	select {
	case err := <-healthy:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("healthy recipient waited for another recipient's socket")
	}
	requireCurrentCrossSceneProfileBatchMode8(t, splitUpperFrames(t, f.viewer2Conn.write.Bytes()), f.actor.selectedCharacterID, currentConnectionTownActorOwnerContext(f.viewer2))
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked write cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled recipient did not finish")
	}
}
