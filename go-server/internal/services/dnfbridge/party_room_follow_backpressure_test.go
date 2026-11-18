package dnfbridge

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

func TestRoomFollowSurvivesBlockedOwnerBeyondCallerBudget(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprint(full), func(t *testing.T) { testRoomFollowBlockedOwner(t, full) })
	}
}

func testRoomFollowBlockedOwner(t *testing.T, full bool) {
	s, peers := fourMemberReturnLoadingFixture(t)
	leader, blocked := peers[0], peers[1]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, p := range peers {
		p.ctx = ctx
		if err := s.startGameSessionEvents(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	gate, started, gateDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	openGate := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(func() {
		openGate()
		for _, p := range peers {
			stopTestGameSessionEvents(t, s, p)
		}
	})
	for _, p := range peers {
		if err := s.callGameSession(ctx, p, "room-follow-initial-op37", func() error {
			return s.sendFinishLoadingStatus(p, make([]byte, currentFinishLoadingLegacyRequestBodySize))
		}); err != nil {
			t.Fatal(err)
		}
	}
	awaitRoomFanoutLoading(t, s, ctx, peers)
	go func() {
		_ = s.callGameSession(ctx, blocked, "room-follow-blocked-owner", func() error {
			close(started)
			<-gate
			return nil
		})
		close(gateDone)
	}()
	waitTestSignal(t, started, "recipient owner did not block")
	if full {
		for i := 0; i < gameSessionEventQueueSize; i++ {
			if err := s.tryPostGameSessionCharacterEvent(blocked, "room-follow-full-queue", 0, 0, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
		}
	}
	move := make([]byte, dungeoncmd.MoveMapRequestSize)
	move[0] = 1
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- s.callGameSession(ctx, leader, "room-follow-leader-move", func() error { return s.handleDungeonMoveMap(leader, move) })
	}()
	var moveErr error
	select {
	case moveErr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("leader stayed blocked on recipient owner beyond its existing budget")
	}
	if moveErr != nil {
		t.Errorf("recipient backlog failed the already-committed leader move: %v", moveErr)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("leader waited for another owner: %s", elapsed)
	}
	// Ordinary peers must progress independently, even while one recipient
	// cannot process anything. Read through each unblocked owner's actual loop.
	for _, p := range peers[2:] {
		if err := s.callGameSession(ctx, p, "observe-unblocked-followers", func() error {
			scene, _ := p.dungeon.runtime.Session.Scene()
			if scene.Coordinate.X != 1 {
				t.Errorf("healthy follower%d was delayed by blocked first follower", p.selectedCharacterID)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Keep the accepted follow blocked past the old caller's three-second
	// lifetime. Its later execution must still belong to the live recipient.
	if remaining := createWriteTimeout + 100*time.Millisecond - time.Since(start); remaining > 0 {
		time.Sleep(remaining)
	}
	openGate()
	<-gateDone
	deadline := time.Now().Add(2 * time.Second)
	for {
		moved := false
		if err := s.callGameSession(ctx, blocked, "observe-delayed-follow", func() error {
			scene, _ := blocked.dungeon.runtime.Session.Scene()
			moved = scene.Coordinate.X == 1
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if moved {
			break
		}
		if time.Now().After(deadline) {
			t.Error("accepted follow disappeared after caller timeout and owner recovery")
			return
		}
		time.Sleep(time.Millisecond)
	}
	for _, p := range peers {
		if err := s.callGameSession(ctx, p, "room-follow-next-op37", func() error {
			return s.sendFinishLoadingStatus(p, make([]byte, currentFinishLoadingLegacyRequestBodySize))
		}); err != nil {
			t.Fatal(err)
		}
	}
	awaitRoomFanoutLoading(t, s, ctx, peers)
}
