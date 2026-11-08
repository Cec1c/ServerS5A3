package dnfbridge

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// A cross-session timeout does not stop a callback already executing. Recovery
// must wait behind that callback before deciding whether it actually retired.
func TestRetryReturnOwnerRecoveryReadsPermitOnPeerQueue(t *testing.T) {
	s, leader, peer := reviewExitV2Party(t)
	old := peer.dungeon.runtime
	owners, err := s.captureCurrentRaidRetryReturnOwners(leader, leader.dungeon.runtime)
	if err != nil {
		t.Fatal(err)
	}
	owner := currentRaidRetryOwnerForSession(owners, peer)
	if owner == nil {
		t.Fatal("peer return owner absent")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.startGameSessionEvents(ctx, peer); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() {
		unblock()
		stop, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		if err := s.stopGameSessionEvents(stop, peer); err != nil {
			t.Error(err)
		}
	}()
	retireDone := make(chan error, 1)
	go func() {
		retireDone <- s.callGameSession(ctx, peer, "test-running-retry-retirement", func() error {
			close(started)
			<-release
			retired, err := s.retireCompletedDungeonForTownAction(peer, true, old)
			if err != nil {
				return err
			}
			if !retired {
				return fmt.Errorf("peer retirement declined")
			}
			owner.retired = true
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("peer callback did not start")
	}
	restoreDone := make(chan struct{})
	go func() {
		s.restoreCurrentRaidRetryReturnOwners(leader, owners)
		close(restoreDone)
	}()
	// The old code returns immediately from the stale permit=false snapshot.
	// The fixed code submits recovery behind the held retirement callback.
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
waitQueued:
	for peer.events.loop.QueueLengths().Medium == 0 {
		select {
		case <-restoreDone:
			break waitQueued
		case <-deadline.C:
			t.Fatal("recovery neither queued nor completed")
		case <-tick.C:
		}
	}
	unblock()
	select {
	case err := <-retireDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("retirement did not finish")
	}
	select {
	case <-restoreDone:
	case <-time.After(time.Second):
		t.Fatal("recovery did not finish")
	}
	peer.dungeon.mu.Lock()
	restored := peer.dungeon.runtime == old
	peer.dungeon.mu.Unlock()
	if !restored {
		t.Fatal("stale pre-queue retirement permit lost the completed return owner")
	}
}
