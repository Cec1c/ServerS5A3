package dnfbridge

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

func awaitRoomFanoutLoading(t *testing.T, s *Service, ctx context.Context, peers []*gameSession) {
	t.Helper()
	for _, p := range peers {
		deadline := time.Now().Add(3 * time.Second)
		for {
			ready := false
			if err := s.callGameSession(ctx, p, "room-fanout-loading-observe", func() error {
				g := p.finishLoadingGateSnapshot()
				ready = g.State && g.Completion
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if ready {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("member%d did not finish shared loading", p.selectedCharacterID)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func TestRoomFanoutContinuesAfterFirstFollowerConfirmsPersonalReturn(t *testing.T) {
	for _, leavingIndex := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(leavingIndex), func(t *testing.T) { testRoomFanoutPersonalReturn(t, leavingIndex) })
	}
}

func testRoomFanoutPersonalReturn(t *testing.T, leavingIndex int) {
	s, peers := fourMemberReturnLoadingFixture(t)
	leader, leaving := peers[0], peers[leavingIndex]
	instance := leader.dungeon.runtime.partyInstance
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
		if err := s.callGameSession(ctx, p, "initial-room-op37", func() error {
			return s.sendFinishLoadingStatus(p, make([]byte, currentFinishLoadingLegacyRequestBodySize))
		}); err != nil {
			t.Fatal(err)
		}
	}
	awaitRoomFanoutLoading(t, s, ctx, peers)
	go func() {
		_ = s.callGameSession(ctx, leaving, "first-follower-owner-gate", func() error {
			close(started)
			<-gate
			return nil
		})
		close(gateDone)
	}()
	waitTestSignal(t, started, "first follower owner did not block")
	returned := make(chan error, 1)
	if err := s.postGameSessionCharacterEvent(leaving, "personal-return-before-queued-room-move", leaving.selectedCharacterID, leaving.characterGeneration, func() error {
		if err := s.handleDungeonGiveupGame(leaving, nil); err != nil {
			returned <- err
			return err
		}
		err := s.commitPendingDungeonReturnForSceneRequest(leaving, "room_fanout_personal_return")
		returned <- err
		return err
	}); err != nil {
		t.Fatal(err)
	}
	move := make([]byte, dungeoncmd.MoveMapRequestSize)
	move[0] = 1
	moved := make(chan error, 1)
	go func() {
		moved <- s.callGameSession(ctx, leader, "leader-room-move-during-return", func() error { return s.handleDungeonMoveMap(leader, move) })
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		leader.dungeon.mu.Lock()
		scene, ok := leader.dungeon.runtime.Session.Scene()
		committed := ok && scene.Coordinate.X == 1
		leader.dungeon.mu.Unlock()
		if committed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("leader did not commit its room before blocked follower returned")
		}
		time.Sleep(time.Millisecond)
	}
	// Observe actual admission, not only the leader's scene commit. Otherwise
	// opening the gate could let return finish before fanout captured its old
	// roster, accidentally exercising a different already-correct ordering.
	deadline = time.Now().Add(2 * time.Second)
	for leaving.events.loop.QueueLengths().Medium < 2 {
		if time.Now().After(deadline) {
			t.Fatal("follow move was not queued behind the personal return")
		}
		time.Sleep(time.Millisecond)
	}
	openGate()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follower did not confirm personal return")
	}
	<-gateDone
	select {
	case err := <-moved:
		if err != nil {
			t.Errorf("committed departure aborted survivor room fanout: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("leader move did not finish")
	}
	if leaving.dungeon.runtime != nil {
		t.Fatal("queued room move revived returned follower dungeon")
	}
	if _, found := s.runtimePartyManagerForService().SnapshotByUser(leaving.selectedCharacterID, 0); found {
		t.Fatal("returned follower retained authoritative party membership")
	}
	var survivors []*gameSession
	for _, p := range peers {
		if p == leaving {
			continue
		}
		survivors = append(survivors, p)
		if err := s.callGameSession(ctx, p, "retained-member-room-observe", func() error {
			scene, ok := p.dungeon.runtime.Session.Scene()
			if !ok || scene.Coordinate.X != 1 || p.dungeon.runtime.partyInstance != instance {
				t.Errorf("retained member%d remained in previous room or lost instance", p.selectedCharacterID)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if t.Failed() {
		return
	}
	for _, p := range survivors {
		if err := s.callGameSession(ctx, p, "survivor-room-op37", func() error {
			return s.sendFinishLoadingStatus(p, make([]byte, currentFinishLoadingLegacyRequestBodySize))
		}); err != nil {
			t.Fatal(err)
		}
	}
	awaitRoomFanoutLoading(t, s, ctx, survivors)
}

func TestRoomFanoutMissingRuntimeWithoutAuthoritativeDepartureStillFails(t *testing.T) {
	s, peers := fourMemberReturnLoadingFixture(t)
	leader, missing := peers[0], peers[1]
	move := make([]byte, dungeoncmd.MoveMapRequestSize)
	move[0] = 1
	if err := s.handleDungeonMoveMapPlanned(leader, move, nil, false); err != nil {
		t.Fatal(err)
	}
	r := leader.dungeon.runtime
	scene, ok := r.Session.Scene()
	if !ok || scene.Coordinate.X != 1 {
		t.Fatal("leader did not establish an actual committed target plan")
	}
	plan := runtimePartyDungeonMovePlan{target: runtimeDungeonRoomKeyFromScene(scene), seed: r.Seed, instance: r.partyInstance, room: r.Room}
	missing.dungeon.runtime = nil
	if _, present := s.runtimePartyManagerForService().SnapshotByUser(missing.selectedCharacterID, 0); !present {
		t.Fatal("control unexpectedly lost authoritative membership")
	}
	if err := s.moveRuntimePartyFollowers(leader, move, plan); err == nil {
		t.Fatal("missing runtime alone was mistaken for a confirmed departure")
	}
}
